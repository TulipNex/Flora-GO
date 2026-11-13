package handler

import (
	"context"
	"strings"
	"time"

	"wabot-go/internal/ai/tools/bridge"
	"wabot-go/internal/logfmt"
	"wabot-go/internal/metrics"
	"wabot-go/internal/store"
	"wabot-go/plugins"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/types/events"
)

// Prefix command bot, misal ".ping" atau "!ping". Ubah sesuai selera.
const CommandPrefix = "."

// init memasang factory handler clone ke plugins supaya .jadibot /
// RestoreClones bisa membuat event handler untuk client clone tanpa
// import cycle (plugins tidak boleh import handler balik).
func init() {
	plugins.NewCloneHandlerFactory = NewEventHandler
}

// staleGrace adalah toleransi umur pesan: pesan ber-timestamp server lebih
// tua dari (boot − grace) dianggap sisa antrean offline (dikirim saat bot
// mati) dan dibuang diam-diam — menoleransi skew jam + pesan yang transit
// tepat saat boot.
const staleGrace = 2 * time.Minute

// NewEventHandler mengembalikan fungsi handler yang didaftarkan ke whatsmeow.
// Ini dipisah dari main.go supaya main.go tetap ringkas (cuma bootstrap).
func NewEventHandler(client *whatsmeow.Client) func(interface{}) {
	// Registry semua plugin/command didaftarkan sekali di sini.
	registry := plugins.NewRegistry()
	plugins.RegisterAll(registry)
	bootTime := time.Now()

	return func(rawEvt interface{}) {
		switch evt := rawEvt.(type) {
		case *events.Message:
			handleMessage(client, registry, evt, bootTime)
		case *events.GroupInfo:
			// Perubahan info grup: sambutan (join) & perpisahan (leave)
			// ditangani di plugins/welcome.go.
			plugins.HandleGroupJoin(client, evt)
		case *events.Connected:
			logfmt.Plain("Terhubung ke server WhatsApp\n")
		case *events.Disconnected:
			logfmt.Warn("Terputus dari server WhatsApp\n")
		case *events.LoggedOut:
			logfmt.Plain("Bot logout dari WhatsApp. Hapus folder data/ lalu login ulang jika perlu.\n")
		}
	}
}

// handleMessage memproses tiap pesan masuk, mengekstrak teks & command,
// lalu memanggil plugin yang cocok. Pesan yang BUKAN command diteruskan
// ke pengecekan trigger AI (lihat handleAITrigger).
func handleMessage(client *whatsmeow.Client, registry *plugins.Registry, evt *events.Message, bootTime time.Time) {
	metrics.IncMessages()
	// Buang pesan basi: dikirim sebelum bot hidup (luapan antrean offline
	// server saat reconnect) — jangan eksekusi command/vote/AI susulan.
	// Drop diam-diam + satu baris log (tanpa balasan chat).
	if !evt.Info.Timestamp.IsZero() && evt.Info.Timestamp.Before(bootTime.Add(-staleGrace)) {
		logfmt.Plain("Abaikan pesan basi %s dari %s (umur %s)\n",
			evt.Info.ID, evt.Info.Sender.String(), time.Since(evt.Info.Timestamp).Round(time.Second))
		return
	}

	// Teks diekstrak duluan karena guard self-command butuh isinya.
	text := extractText(evt)

	// Pesan dari bot sendiri diabaikan untuk mencegah loop — kecuali saklar
	// .selfcmd mengizinkan (lihat plugins.ShouldProcessSelfMessage):
	// pesan self hanya dirutekan ke command (bukan observasi/moderasi/
	// staging/AI, lihat flag selfCmd di bawah), dan output bot sendiri
	// tetap diabaikan via IsBotMessageID.
	selfCmd := false
	if evt.Info.IsFromMe {
		if !plugins.ShouldProcessSelfMessage(text, evt.Info.ID, CommandPrefix) {
			return
		}
		selfCmd = true
	}
	store.LogIncoming(client, evt, classifyAim(client, evt, text))

	// Gerbang owner (ban user, ban grup, mode self/public) — dicek SEBELUM
	// routing command maupun trigger AI supaya user/grup yang di-ban benar-benar
	// diabaikan total. Owner SELALU lolos supaya bisa unban/ubah mode bahkan
	// dari dalam grup yang di-ban. Pesan self-command (selfCmd) diperlakukan
	// setara owner: pemegang akun bot adalah owner terkuat. Cek DB fail-open:
	// kalau DB error, pesan tetap diproses (log saja) supaya gangguan DB
	// tidak bikin bot diam total.
	if !selfCmd && !plugins.IsOwnerEvent(evt) {
		bg := context.Background()
		gdb := store.GroupsFor(client)
		if self, err := gdb.IsSelfMode(bg); err != nil {
			logfmt.Warn("Gagal cek mode bot: %v\n", err)
		} else if self {
			return
		}
		if banned, err := gdb.IsUserBanned(bg, evt.Info.Sender, evt.Info.SenderAlt); err != nil {
			logfmt.Warn("Gagal cek ban user: %v\n", err)
		} else if banned {
			return
		}
		if evt.Info.IsGroup {
			if gbanned, err := gdb.IsGroupBanned(bg, evt.Info.Chat.String()); err != nil {
				logfmt.Warn("Gagal cek ban grup: %v\n", err)
			} else if gbanned {
				return
			}
		}
	}

	// Cap-stempel aktivitas DM owner untuk eskalasi telepon reminder:
	// pesan DM apa pun dari owner menandakan "merespons" dan membatalkan
	// telepon susulan. Ditaruh di sini (setelah gerbang, sebelum routing)
	// supaya SEMUA pesan tercatat — termasuk command dan pesan yang nantinya
	// ditindak moderasi. Murah (map in-memory) dan tidak mengubah alur.
	if !evt.Info.IsGroup && plugins.IsOwnerEvent(evt) && store.IsMainClient(client) {
		bridge.NoteOwnerDMActivity(evt.Info.Sender, evt.Info.SenderAlt)
	}

	// Arsip pesan masuk ala FIORA/CRM (observasi murni: tak konsumsi atau
	// mutasi evt, tak ada early-return baru). Setelah gerbang ban supaya
	// pesan dari user/grup banned tetap tidak terarsip, konsisten dengan
	// "diabaikan total". Fail-soft di dalam (DB mati = no-op).
	// Dilewati untuk pesan self-command: itu perintah owner, bukan chat user.
	// Hanya bot utama: arsip CRM milik utama (keputusan jadibot).
	if !selfCmd && store.IsMainClient(client) {
		plugins.ArchiveIncoming(client, evt)
	}

	// Check-in absensi: share live location di grup bersesi aktif dicatat
	// otomatis (observasi murni, fail-soft di dalam). Setelah gerbang ban
	// supaya konsisten; sebelum AI (lokasi tak ber-teks, tak mengganggu AI).
	// Dilewati untuk pesan self-command (alasan sama dengan arsip).
	// Hanya bot utama: absensi milik utama (keputusan jadibot).
	if !selfCmd && store.IsMainClient(client) {
		plugins.CheckAbsensiCheckin(client, evt)
	}

	// Wizard .absen setup: menelan reply angka/nilai/kembali saat setup
	// pending (true = setop alur). Command (./$/\) selalu lolos. Setelah
	// gerbang ban; sebelum antilink/AI supaya input setup tak bocor.
	// Dilewati untuk pesan self-command: teks self selalu berprefix
	// command (jaminan ShouldProcessSelfMessage) sehingga selalu lolos.
	// Hanya bot utama: absensi milik utama (keputusan jadibot).
	if !selfCmd && store.IsMainClient(client) && plugins.CheckAbsensiSetup(client, evt) {
		return
	}

	// Moderasi antilink otomatis — hanya pesan grup NON-command. Kalau pesan
	// ditindak (link asing dihapus + warn), setop di sini supaya tidak
	// diteruskan ke trigger AI.
	if evt.Info.IsGroup && text != "" && !strings.HasPrefix(text, CommandPrefix) {
		if plugins.CheckAntilink(client, evt, text) {
			return
		}
	}

	// Antistatus otomatis — SWGC orang di grup yang toggle-nya aktif dicoba
	// revoke. Tanpa syarat teks (status media acap tanpa caption); fungsi
	// ini yang memutuskan false bila tak relevan.
	if evt.Info.IsGroup && !strings.HasPrefix(text, CommandPrefix) {
		if plugins.CheckAntistatus(client, evt) {
			return
		}
	}

	// Cek apakah pesan diawali prefix command
	if text != "" && strings.HasPrefix(text, CommandPrefix) {
		handleCommand(client, registry, evt, text)
		return
	}

	// Pengaman lapis kedua untuk pesan self-command: teks self dijamin
	// berprefix command oleh ShouldProcessSelfMessage, jadi routing di atas
	// pasti sudah return. Kalau suatu saat jaminan itu berubah, drop di sini
	// daripada bocor ke staging/autodownload/game/AI.
	if selfCmd {
		return
	}

	// Staging PDF (.pdfmerge up): pesan non-command berisi PDF atau
	// reply-angka reorder ditampung/diproses di sini lalu setop — supaya
	// tidak bocor ke autodownload/AI. Tanpa text pun dicek (PDF tanpa
	// caption): fungsi ini yang memutuskan false bila tak relevan. Kata
	// `mulai` hanya ditelan selama jendela 5 menit setelah `.pdfmerge`
	// (startarm.go), selebihnya jadi pesan biasa tanpa notifikasi.
	if plugins.CheckPDFStaging(client, evt) {
		return
	}

	// Staging JPG (.jpgpdf mulai): foto polos tanpa caption ditampung saat
	// sesi aktif (cermin CheckPDFStaging di atas). Foto bercaption .jpgpdf
	// lewat jalur command biasa; kata `mulai` ikut digerbangi jendela
	// `.jpgpdf` 5 menit.
	if plugins.CheckJpgStaging(client, evt) {
		return
	}

	// Sesi Gemini (mode tamu, tanpa login): pesan "gemini <teks>" atau reply
	// ke bubble bot diteruskan ke Gemini lalu setop di sini supaya tidak ikut
	// autodownload/AI. Berlaku di DM maupun grup, hanya untuk owner. Ditaruh
	// sebelum autodownload karena prompt boleh memuat tautan.
	if plugins.CheckGeminiSession(client, evt, text) {
		return
	}

	// Autodownload medsos — pesan NON-command (grup maupun private) yang
	// mengandung link didukung. Kalau link ditangani (return true), trigger
	// AI untuk pesan itu dilewati (download saja) — tapi jalur lain
	// (prefix spesial, game, Spotify/LRC/Shazam, tombol) tetap jalan normal.
	// Fungsi kembali seketika, download-nya async di dalam.
	dlHandled := false
	if text != "" {
		dlHandled = plugins.CheckAutodownload(client, evt, text)
	}

	// Prefix spesial karakter tunggal: "$" = shell server, "\" = eval JS.
	// Tidak lewat prefix "." supaya tetap satu karakter seperti aslinya,
	// tapi eksekusinya memakai jalur command yang sama (termasuk RawArgs
	// utuh — penting karena script shell/JS multi-baris).
	if rest, ok := cutSpecialPrefix(text, "$"); ok {
		executeNamedCommand(client, registry, evt, "$", strings.Fields(rest), strings.TrimSpace(rest))
		return
	}
	if rest, ok := cutSpecialPrefix(text, "\\"); ok {
		executeNamedCommand(client, registry, evt, "\\", strings.Fields(rest), strings.TrimSpace(rest))
		return
	}

	// Bukan command (termasuk pesan media tanpa caption) → cek input game
	// dulu (angka polos tictactoe): kalau dikonsumsi game, setop di sini
	// supaya tidak bocor ke trigger AI. Bukan input game = lanjut normal.
	if text != "" && plugins.CheckGameInput(client, evt, text) {
		return
	}

	// Pilihan angka hasil pencarian Spotify (.sp <judul> → balas 1-5).
	// Sejajar CheckGameInput: ditelan di sini supaya tidak bocor ke AI.
	if text != "" && plugins.CheckSpotifyPick(client, evt, text) {
		return
	}

	// Pilihan angka hasil pencarian lirik (.lrc <judul> → balas 1-5).
	// Setelah Spotify; semuanya punya guard resensi silang (terbaru menang).
	if text != "" && plugins.CheckLrcPick(client, evt, text) {
		return
	}

	// Tap tombol "Unduh" pada kartu hasil Shazam (id "shazam:dl:<trackID>").
	if plugins.CheckShazamButtonTap(client, evt) {
		return
	}

	// Tap tombol native uji (.btntest). Tanpa syarat teks (tap tak berteks).
	if plugins.CheckButtonTestTap(client, evt, text) {
		return
	}

	// Lokasi masuk saat sesi uji tombol aktif (verifikasi send_location).
	if plugins.CheckButtonTestLocation(client, evt) {
		return
	}

	// Kontak masuk saat sesi uji tombol aktif (verifikasi request nomor).
	if plugins.CheckButtonTestContact(client, evt) {
		return
	}

	// Tap tombol quick_reply kartu carousel (.carouseltest reply).
	if plugins.CheckCarouselTestTap(client, evt, text) {
		return
	}

	// Tap tombol playground (id berawalan "play:").
	if plugins.CheckPlayTap(client, evt, text) {
		return
	}

	// Tap tombol pdfmerge (Mulai/Daftar/Gabung/Batal/Nama). Tanpa syarat
	// teks (tap tak berteks); fungsi ini yang memutuskan false bila tak
	// relevan (bukan tap pdfmerge).
	if plugins.CheckPDFButtonTap(client, evt) {
		return
	}

	// Tap tombol jpgpdf (cermin pdfmerge di atas, prefix "jpgpdf:").
	if plugins.CheckJpgButtonTap(client, evt) {
		return
	}

	// Tap tombol convert (prefix "convert:").
	if plugins.CheckConvertButtonTap(client, evt) {
		return
	}

	// Bukan command (termasuk pesan media tanpa caption) → cek trigger AI.
	// Dilewati bila pesan sudah ditangani autodownload (download saja).
	if dlHandled {
		return
	}
	handleAITrigger(client, evt, text)
}
