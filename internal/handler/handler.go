package handler

import (
	"context"
	"fmt"
	"strings"
	"time"

	"wabot-go/internal/ai"
	"wabot-go/internal/ai/tools/bridge"
	"wabot-go/internal/logfmt"
	"wabot-go/internal/store"
	"wabot-go/plugins"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// Prefix command bot, misal ".ping" atau "!ping". Ubah sesuai selera.
const CommandPrefix = "."

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
	// Abaikan pesan dari bot sendiri untuk mencegah loop
	if evt.Info.IsFromMe {
		return
	}

	// Buang pesan basi: dikirim sebelum bot hidup (luapan antrean offline
	// server saat reconnect) — jangan eksekusi command/vote/AI susulan.
	// Drop diam-diam + satu baris log (tanpa balasan chat).
	if !evt.Info.Timestamp.IsZero() && evt.Info.Timestamp.Before(bootTime.Add(-staleGrace)) {
		logfmt.Plain("Abaikan pesan basi %s dari %s (umur %s)\n",
			evt.Info.ID, evt.Info.Sender.String(), time.Since(evt.Info.Timestamp).Round(time.Second))
		return
	}

	// Teks diekstrak duluan karena klasifikasi aim butuh isinya.
	text := extractText(evt)
	store.LogIncoming(client, evt, classifyAim(client, evt, text))

	// Gerbang owner (ban user, ban grup, mode self/public) — dicek SEBELUM
	// routing command maupun trigger AI supaya user/grup yang di-ban benar-benar
	// diabaikan total. Owner SELALU lolos supaya bisa unban/ubah mode bahkan
	// dari dalam grup yang di-ban. Cek DB fail-open: kalau DB error, pesan
	// tetap diproses (log saja) supaya gangguan DB tidak bikin bot diam total.
	if !plugins.IsOwnerEvent(evt) {
		bg := context.Background()
		if self, err := store.Groups().IsSelfMode(bg); err != nil {
			logfmt.Warn("Gagal cek mode bot: %v\n", err)
		} else if self {
			return
		}
		if banned, err := store.Groups().IsUserBanned(bg, evt.Info.Sender, evt.Info.SenderAlt); err != nil {
			logfmt.Warn("Gagal cek ban user: %v\n", err)
		} else if banned {
			return
		}
		if evt.Info.IsGroup {
			if gbanned, err := store.Groups().IsGroupBanned(bg, evt.Info.Chat.String()); err != nil {
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
	if !evt.Info.IsGroup && plugins.IsOwnerEvent(evt) {
		bridge.NoteOwnerDMActivity(evt.Info.Sender, evt.Info.SenderAlt)
	}

	// Arsip pesan masuk ala FIORA/CRM (observasi murni: tak konsumsi atau
	// mutasi evt, tak ada early-return baru). Setelah gerbang ban supaya
	// pesan dari user/grup banned tetap tidak terarsip, konsisten dengan
	// "diabaikan total". Fail-soft di dalam (DB mati = no-op).
	plugins.ArchiveIncoming(client, evt)

	// Check-in absensi: share live location di grup bersesi aktif dicatat
	// otomatis (observasi murni, fail-soft di dalam). Setelah gerbang ban
	// supaya konsisten; sebelum AI (lokasi tak ber-teks, tak mengganggu AI).
	plugins.CheckAbsensiCheckin(client, evt)

	// Wizard .absen setup: menelan reply angka/nilai/kembali saat setup
	// pending (true = setop alur). Command (./$/\) selalu lolos. Setelah
	// gerbang ban; sebelum antilink/AI supaya input setup tak bocor.
	if plugins.CheckAbsensiSetup(client, evt) {
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

	// Staging PDF (.pdfmerge up): pesan non-command berisi PDF atau
	// reply-angka reorder ditampung/diproses di sini lalu setop — supaya
	// tidak bocor ke autodownload/AI. Tanpa text pun dicek (PDF tanpa
	// caption): fungsi ini yang memutuskan false bila tak relevan.
	if plugins.CheckPDFStaging(client, evt) {
		return
	}

	// Autodownload medsos — pesan NON-command (grup maupun private) yang
	// mengandung link didukung. TIDAK menyetop alur (beda dari antilink):
	// trigger AI tetap diproses setelah ini. Fungsi kembali seketika,
	// download-nya async di dalam.
	if text != "" {
		plugins.CheckAutodownload(client, evt, text)
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

	// Pilihan angka hasil pencarian Shazam (.shazam search → balas 1-5).
	if text != "" && plugins.CheckShazamPick(client, evt, text) {
		return
	}

	// Kata "unduh" setelah kartu hasil Shazam (download lagu pending).
	if text != "" && plugins.CheckShazamDownload(client, evt, text) {
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

	// Bukan command (termasuk pesan media tanpa caption) → cek trigger AI.
	handleAITrigger(client, evt, text)
}

// classifyAim menentukan baris aim: untuk log pesan masuk (lihat
// store.LogIncoming). "command (.x)" bila teks berprefix command/spesial,
// "agent" bila pemicu AI cocok, "none" bila keduanya tidak.
// Catatan jujur: ini MAKSUD pesan, bukan hasilnya — gerbang ban/lock/trust
// bisa tetap menggugurkan pesan ber-aim agent/command setelahnya. Input
// game angka polos tidak diklasifikasikan di sini (CheckGameInput punya
// efek samping konsumsi, tak boleh dipanggil dua kali) — ia tercatat
// mengikuti hasil trigger seperti pesan biasa.
func classifyAim(client *whatsmeow.Client, evt *events.Message, text string) string {
	if text != "" {
		if strings.HasPrefix(text, CommandPrefix) {
			name := text
			if f := strings.Fields(strings.TrimPrefix(text, CommandPrefix)); len(f) > 0 {
				name = f[0]
			}
			return "command (." + strings.ToLower(name) + ")"
		}
		if _, ok := cutSpecialPrefix(text, "$"); ok {
			return "command ($)"
		}
		if _, ok := cutSpecialPrefix(text, "\\"); ok {
			return "command (\\)"
		}
	}
	sessionKey := ai.BuildSessionKey(evt.Info.IsGroup, evt.Info.Chat.String(), evt.Info.Sender.String())
	trigger := ai.DetectTrigger(ai.TriggerInput{
		Text:             text,
		IsReplyToBot:     store.IsReplyToBot(evt),
		IsStickerMsg:     store.IsStickerMessage(evt),
		IsMediaMsg:       store.IsMediaMessage(evt),
		HasActiveSession: ai.GlobalSession.HasActive(sessionKey),
	})
	if trigger.Matched {
		return "agent"
	}
	return "none"
}

// cutSpecialPrefix memotong prefix spesial di awal teks. Mengembalikan sisa
// teks (boleh kosong — pemanggil yang memutuskan) dan true kalau cocok.
func cutSpecialPrefix(text, prefix string) (string, bool) {
	if text == "" || !strings.HasPrefix(text, prefix) {
		return "", false
	}
	return strings.TrimPrefix(text, prefix), true
}

// handleCommand memproses pesan yang diawali prefix command seperti sebelumnya.
func handleCommand(client *whatsmeow.Client, registry *plugins.Registry, evt *events.Message, text string) {
	// Parse "command arg1 arg2" menjadi nama command + argumen
	withoutPrefix := strings.TrimPrefix(text, CommandPrefix)
	parts := strings.Fields(withoutPrefix)
	if len(parts) == 0 {
		return
	}

	cmdName := strings.ToLower(parts[0])
	args := parts[1:]

	// RawArgs: teks mentah setelah nama command dengan baris baru utuh.
	// Diambil langsung dari teks asli (tanpa lewat strings.Fields yang
	// menghancurkan whitespace) — potong di whitespace pertama setelah
	// nama command, lalu trim tepi saja supaya baris baru di TENGAH utuh.
	rawArgs := ""
	if i := strings.IndexAny(withoutPrefix, " \t\n\r"); i >= 0 {
		rawArgs = strings.TrimSpace(withoutPrefix[i+1:])
	}

	executeNamedCommand(client, registry, evt, cmdName, args, rawArgs)
}

// executeNamedCommand menjalankan command yang sudah diketahui namanya
// (dari prefix "." maupun prefix spesial "$"/"\"). Dipisah dari
// handleCommand supaya semua jalur routing berbagi penanganan error yang
// sama. args boleh kosong; rawArgs adalah sisa teks mentah (baris baru
// utuh) untuk command yang membutuhkannya (shell, eval, template).
func executeNamedCommand(client *whatsmeow.Client, registry *plugins.Registry, evt *events.Message, cmdName string, args []string, rawArgs string) {
	if rawArgs == "" && len(args) > 0 {
		rawArgs = strings.Join(args, " ")
	}

	cmd, ok := registry.Get(cmdName)
	if !ok {
		return // command tidak ditemukan, diamkan saja (bisa diubah untuk balas "command tidak dikenal")
	}

	ctx := &store.MessageContext{
		Client:  client,
		Chat:    evt.Info.Chat,
		Sender:  evt.Info.Sender,
		RawEvt:  evt,
		Args:    args,
		RawArgs: rawArgs,
	}

	if err := cmd.Handler(context.Background(), ctx); err != nil {
		logfmt.Error("Error menjalankan command '%s': %v\n", cmdName, err)
		_ = store.ReplyText(context.Background(), ctx, "Terjadi kesalahan saat menjalankan perintah.")
	}
}

// handleAITrigger mengecek apakah pesan non-command ini memicu AI
// (kata kunci nama bot, reply ke pesan bot, sticker-reply, atau
// media-reply tanpa caption), dan kalau ya, memprosesnya lewat
// plugins.HandleAIMessage (lihat plugins/ai.go).
//
// Kalau pesan ini adalah reply ke salah satu bubble bot (lihat
// store.GetQuotedBotText), teks bubble yang di-reply disertakan sebagai
// konteks ke AI — supaya user tidak perlu mengulang topik yang ingin
// ditanyakan lebih lanjut.
func handleAITrigger(client *whatsmeow.Client, evt *events.Message, text string) {
	// sessionKey dibangun lewat ai.BuildSessionKey yang sama dipakai
	// plugins/ai.go (HandleAIMessage/processAITurn) — WAJIB identik supaya
	// HasActiveSession di bawah mengecek kunci yang benar-benar dipakai
	// GlobalSession.Append untuk menyimpan histori sesi ini. Sebelum
	// perbaikan ini, di sini selalu memakai sender polos meski histori
	// sungguhan disimpan dengan kunci gabungan chatJID:senderJID untuk
	// pesan grup — akibatnya HasActive selalu salah (selalu dianggap
	// idle) untuk sesi di dalam grup.
	sessionKey := ai.BuildSessionKey(evt.Info.IsGroup, evt.Info.Chat.String(), evt.Info.Sender.String())

	// Perintah lock/unlock ("lock flo", "unlock flora", "lock @flora")
	// dicek PALING DULU — bahkan saat sedang terkunci — supaya unlock
	// selalu bisa. Hanya owner yang boleh; non-owner diabaikan diam-diam.
	if action := ai.ParseLockCommand(text, isBotMentioned(client, evt)); action != ai.LockNone {
		if !plugins.IsOwnerEvent(evt) {
			return
		}
		mctx := &store.MessageContext{
			Client: client, Chat: evt.Info.Chat, Sender: evt.Info.Sender, RawEvt: evt,
		}
		bg := context.Background()
		if action == ai.LockOn {
			if err := store.Groups().SetAILock(bg, true); err != nil {
				_ = store.ReplyText(bg, mctx, fmt.Sprintf("Gagal mengunci: %v", err))
				return
			}
			_ = store.ReplyText(bg, mctx, "Flora di-lock — trigger natural dimatikan di semua chat. Buka lagi dengan unlock flo/flora.")
		} else {
			if err := store.Groups().SetAILock(bg, false); err != nil {
				_ = store.ReplyText(bg, mctx, fmt.Sprintf("Gagal membuka kunci: %v", err))
				return
			}
			_ = store.ReplyText(bg, mctx, "Flora aktif lagi — trigger natural menyala.")
		}
		return
	}

	// Gerbang lock global: saat terkunci, semua trigger natural di semua
	// chat diabaikan diam-diam. Command admin .agent TIDAK lewat sini
	// sehingga tetap jalan.
	if locked, err := store.Groups().IsAILocked(context.Background()); err != nil {
		logfmt.Warn("Gagal cek lock AI: %v\n", err)
	} else if locked {
		return
	}

	// Gerbang trust Flora: trigger natural hanya melayani owner + user
	// terpercaya (lihat store.GroupDB.TrustUser). Non-trusted diabaikan
	// diam-diam — tanpa bocoran bahwa Flora terkunci untuk mereka.
	if !plugins.IsOwnerOrTrustedEvent(context.Background(), evt) {
		return
	}

	trigger := ai.DetectTrigger(ai.TriggerInput{
		Text:             text,
		IsReplyToBot:     store.IsReplyToBot(evt),
		IsStickerMsg:     store.IsStickerMessage(evt),
		IsMediaMsg:       store.IsMediaMessage(evt),
		HasActiveSession: ai.GlobalSession.HasActive(sessionKey),
	})

	if !trigger.Matched {
		return
	}

	quotedText := store.GetQuotedBotText(evt)
	finalText := ai.BuildTextWithQuotedContext(quotedText, text)

	ctx := &store.MessageContext{
		Client: client,
		Chat:   evt.Info.Chat,
		Sender: evt.Info.Sender,
		RawEvt: evt,
		Args:   nil, // trigger natural tidak berbasis command prefix, teks ada di 'text'
	}

	// Pemrosesan AI jalan di goroutine sendiri supaya tidak memblokir event
	// loop whatsmeow. HandleAIMessage butuh waktu lama (HTTP ke provider +
	// delay ketik antar-bubble, total bisa belasan detik) — kalau dijalankan
	// sinkron di sini, whatsmeow tidak bisa memproses stanza berikutnya dan
	// memunculkan warning "Node handling took Xs". Aman dari balapan karena
	// sesi yang sama sudah diserialkan via ai.WithSenderLock di dalamnya.
	go func() {
		if err := plugins.HandleAIMessage(context.Background(), ctx, finalText); err != nil {
			logfmt.Error("Error memproses trigger AI (alasan: %s): %v\n", trigger.Reason, err)
			_ = store.ReplyText(context.Background(), ctx, "Maaf, ada gangguan saat memproses ini.")
		}
	}()
}

// isBotMentioned true kalau pesan menandai bot sendiri — dibandingkan
// dengan ID maupun LID bot (lihat IsBotAdmin untuk dual-format yang sama),
// karena mention di grup mode LID datang sebagai @lid.
func isBotMentioned(client *whatsmeow.Client, evt *events.Message) bool {
	mentioned := store.ExtractMentionedJIDs(evt)
	if len(mentioned) == 0 {
		return false
	}
	matches := func(jid types.JID) bool {
		if jid.IsEmpty() {
			return false
		}
		bare := jid.ToNonAD().User
		for _, raw := range mentioned {
			if parsed, err := types.ParseJID(raw); err == nil && parsed.ToNonAD().User == bare {
				return true
			}
		}
		return false
	}
	if client.Store.ID != nil && matches(*client.Store.ID) {
		return true
	}
	if !client.Store.LID.IsEmpty() && matches(client.Store.LID) {
		return true
	}
	return false
}

// extractText mengambil teks dari berbagai tipe pesan (teks biasa, caption gambar, dll).
//
// Pesan di-unwrap dulu lewat store.UnwrapMessage — WhatsApp membungkus isi
// pesan asli satu-tiga layer di dalam EphemeralMessage ("Pesan Sementara" di
// grup) atau ViewOnceMessage (foto/video sekali lihat), sehingga tanpa
// unwrap ini semua getter di bawah akan selalu kosong untuk pesan bertipe
// tersebut dan bot terlihat "diam" di grup yang mengaktifkan fitur itu.
// Fungsi unwrap yang sama juga dipakai oleh ExtractStanzaID/
// IsStickerMessage/IsMediaMessage di internal/store/trigger_extract.go
// supaya perilakunya konsisten di semua jalur.
func extractText(evt *events.Message) string {
	msg := store.UnwrapMessage(evt.Message)
	if msg == nil {
		return ""
	}

	if msg.GetConversation() != "" {
		return msg.GetConversation()
	}
	if ext := msg.GetExtendedTextMessage(); ext != nil {
		return ext.GetText()
	}
	if img := msg.GetImageMessage(); img != nil {
		return img.GetCaption()
	}
	if vid := msg.GetVideoMessage(); vid != nil {
		return vid.GetCaption()
	}

	return ""
}
