package plugins

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"wabot-go/internal/logfmt"
	"wabot-go/internal/store"
	"wabot-go/internal/timex"

	"github.com/xuri/excelize/v2"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func init() {
	registerPlugin(RegisterAbsensi)
}

// Konstanta perilaku absensi (keputusan owner, terpusat di sini):
const (
	// absensiRadiusM adalah fallback bila radius patokan tak terbaca
	// (DB mati/row korup). Nilai per-chat tinggal di absensi_config dan
	// diubah via wizard .absen setup bagian [3]; default baris baru = 20m
	// (skema DB).
	absensiRadiusM = 20.0
	// absensiDefaultDurasi adalah durasi sesi `.absen mulai` tanpa argumen.
	absensiDefaultDurasi = 60
	// absensiReminderBefore adalah H-berapa reminder "segera berakhir".
	absensiReminderBefore = 5 * time.Minute
	// absensiTickInterval adalah interval ticker jadwal harian.
	absensiTickInterval = 30 * time.Second
)

// RegisterAbsensi mendaftarkan command `.absen` kategori Absensi: presensi
// via share LIVE location. Check-in OTOMATIS saat member share live-loc
// di grup bersesi aktif (tanpa command khusus); command ini untuk kelola.
//
//   - .absen setup — admin/owner (wizard patokan/durasi/radius/jadwal)
//   - .absen mulai [durasi_mnt] — admin/owner (default dari setup)
//   - .absen berhenti — admin/owner (+rekap)
//   - .absen perpanjang <mnt> — admin/owner
//   - .absen hasil — semua (rekap sesi)
//   - .absen status — semua (setelan + status)
//   - .absen rekap — admin/owner (.xlsx sesi terakhir)
//
// Aturan keras: live location SAJA (statis/luar radius = diam + log),
// first-valid-wins per pengirim, tanpa status Telat (semua hadir = Tepat).
func RegisterAbsensi(r *Registry) {
	r.Register(Command{
		Name:        "absen",
		Description: "Absensi live-location (.absen mulai/berhenti/hasil/...)",
		Category:    CategoryAbsensi,
		Handler:     handleAbsen,
	})
}

// absensiWAClient adalah client untuk kirim timer (reminder/tutup/rekap).
// Dipasang sekali via InitAbsensiScheduler sebelum Connect (pola
// InitCallClient/InitReminderClient).
var absensiWAClient *whatsmeow.Client

// InitAbsensiScheduler menyimpan client pengirim timer absensi.
func InitAbsensiScheduler(client *whatsmeow.Client) {
	absensiWAClient = client
}

// absensiTimers menyimpan timer per chat aktif (reminder + tutup).
// Timer lama dibuang tiap re-arm; kecocokan end_ts di DB adalah token
// (pola reminder.go) sehingga timer basi jadi no-op.
var absensiTimers = struct {
	sync.Mutex
	m map[string]*absensiTimerSet
}{m: make(map[string]*absensiTimerSet)}

type absensiTimerSet struct {
	reminder *time.Timer
	close    *time.Timer
}

func handleAbsen(ctx context.Context, mctx *store.MessageContext) error {
	if !mctx.IsGroup() {
		return absenReply(ctx, mctx, "Info: Absensi hanya berjalan di grup.")
	}
	if store.Absensi() == nil {
		return absenReply(ctx, mctx, "Gagal: fitur absensi mati (absensi.db tidak terbuka).")
	}
	sub := ""
	if len(mctx.Args) > 0 {
		sub = strings.ToLower(strings.TrimSpace(mctx.Args[0]))
	}
	switch sub {
	case "mulai", "start", "buka":
		return handleAbsenMulai(ctx, mctx)
	case "berhenti", "stop", "tutup":
		return handleAbsenBerhenti(ctx, mctx)
	case "extend", "perpanjang":
		return handleAbsenExtend(ctx, mctx)
	case "hasil", "result":
		return handleAbsenHasil(ctx, mctx)
	case "setup":
		return handleAbsenSetup(ctx, mctx)
	case "status", "info", "setelan":
		return handleAbsenStatus(ctx, mctx)
	case "export", "ekspor", "rekap":
		return handleAbsenExport(ctx, mctx)
	default:
		return store.ReplyText(ctx, mctx, absenUsage())
	}
}

func absenUsage() string {
	return `*📋 ABSEN — Presensi Live Location*

*Admin*
.absen setup
> mengatur patokan, durasi, radius, jadwal
.absen mulai <menit>
> membuka sesi absen
.absen berhenti
> menutup sesi
.absen perpanjang <menit>
> memperpanjang sesi berjalan
.absen rekap
> mengunduh rekap .xlsx sesi terakhir

*Semua*
.absen hasil
> menampilkan rekap sesi
.absen status
> menampilkan setelan + status berlaku`
}

// absenEcho menggema perintah yang diketik user ("[COMMAND]") untuk baris
// pertama balasan .absen. RawArgs tak memuat nama command (dipotong
// handler), jadi prefix ".absen " ditambah di sini.
func absenEcho(mctx *store.MessageContext) string {
	if raw := strings.TrimSpace(mctx.RawArgs); raw != "" {
		if i := strings.IndexAny(raw, "\n\r"); i >= 0 {
			raw = strings.TrimSpace(raw[:i])
		}
		if raw != "" {
			return ".absen " + raw
		}
	}
	return ".absen"
}

// absenReply mengirim balasan gaya .absen: gema perintah + MAKSIMAL satu
// quote headline (Berhasil:/Gagal:/Info:) + baris detail normal. Tanpa
// emoji, tanpa quote berlapis.
func absenReply(ctx context.Context, mctx *store.MessageContext, headline string, details ...string) error {
	var sb strings.Builder
	sb.WriteString(absenEcho(mctx))
	sb.WriteString("\n> " + strings.TrimSpace(headline))
	for _, d := range details {
		if strings.TrimSpace(d) == "" {
			continue
		}
		sb.WriteString("\n" + d)
	}
	return store.ReplyText(ctx, mctx, sb.String())
}

// Nama hari & bulan Indonesia untuk format tanggal form (tanpa tergantung
// locale server). Index hari mengikuti time.Weekday (0=Minggu).
var (
	absenDayLong  = []string{"Minggu", "Senin", "Selasa", "Rabu", "Kamis", "Jumat", "Sabtu"}
	absenMonShort = []string{"Jan", "Feb", "Mar", "Apr", "Mei", "Jun", "Jul", "Agu", "Sep", "Okt", "Nov", "Des"}
)

// absenFormDay memecah epoch jadi ("Senin", "12 Okt 2026") zona bot.
func absenFormDay(ts int64) (hari, tanggal string) {
	if ts <= 0 {
		return "-", "-"
	}
	t := time.Unix(ts, 0).In(timex.Zone())
	return absenDayLong[t.Weekday()], fmt.Sprintf("%d %s %d", t.Day(), absenMonShort[int(t.Month())-1], t.Year())
}

// absenFormClock "08.00", absenFormClockS "08.00.05" (zona bot).
func absenFormClock(ts int64) string {
	if ts <= 0 {
		return "-"
	}
	return time.Unix(ts, 0).In(timex.Zone()).Format("15.04")
}

func absenFormClockS(ts int64) string {
	if ts <= 0 {
		return "--:--:--"
	}
	return time.Unix(ts, 0).In(timex.Zone()).Format("15.04.05")
}

// ---- subcommand admin ----

// absensiRadiusOf mengembalikan radius toleransi chat (dari patokan,
// fallback default konstanta bila patokan/DB bermasalah).
func absensiRadiusOf(chat string) float64 {
	if store.Absensi() == nil {
		return absensiRadiusM
	}
	if cfg, err := store.Absensi().GetConfig(chat); err == nil && cfg != nil && cfg.RadiusM > 0 {
		return cfg.RadiusM
	}
	return absensiRadiusM
}

// Batas radius .absen radius: 1m (di bawah akurasi GPS, tak berguna) s/d
// 5km (di atas itu bukan presensi lokasi lagi).
const (
	absensiRadiusMin = 1.0
	absensiRadiusMax = 5000.0
)

// absenCoordsOf mengambil koordinat dari pesan lokasi APAPUN (statis/live/
// update-live) — dipakai patokan (butuh titik) dan check-in (difilter live).
func absenCoordsOf(msg *waE2E.Message) (lat, long float64, ok bool) {
	if msg == nil {
		return 0, 0, false
	}
	if loc := msg.GetLocationMessage(); loc != nil {
		return loc.GetDegreesLatitude(), loc.GetDegreesLongitude(), true
	}
	if ll := msg.GetLiveLocationMessage(); ll != nil {
		return ll.GetDegreesLatitude(), ll.GetDegreesLongitude(), true
	}
	return 0, 0, false
}

func handleAbsenMulai(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireModGate(ctx, mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	chat := mctx.Chat.String()
	cfg, err := store.Absensi().GetConfig(chat)
	if err != nil || cfg == nil {
		if err != nil {
			return absenReply(ctx, mctx, fmt.Sprintf("Gagal: baca patokan gagal (%v).", err))
		}
		return absenReply(ctx, mctx, "Gagal: belum ada patokan. Set dulu via .absen setup (bagian [1]).")
	}
	dur := absensiDefaultDurasi
	if cfg.DefaultDurasi > 0 {
		dur = cfg.DefaultDurasi
	}
	if len(mctx.Args) > 1 {
		if v, err := strconv.Atoi(strings.TrimSpace(mctx.Args[1])); err == nil && v > 0 {
			dur = v
		}
	}
	sender := absensiSenderLabel(mctx)
	id, err := store.Absensi().OpenSession(chat, dur, false, sender, time.Now().Unix())
	if err != nil {
		if strings.Contains(err.Error(), "sudah ada sesi aktif") {
			return absenReply(ctx, mctx, "Gagal: sudah ada sesi aktif. Pakai .absen perpanjang atau .absen berhenti.")
		}
		return absenReply(ctx, mctx, fmt.Sprintf("Gagal: buka absen gagal (%v).", err))
	}
	sess, serr := store.Absensi().GetSession(id)
	if serr != nil || sess == nil {
		return absenReply(ctx, mctx, "Gagal: baca sesi gagal.")
	}
	armAbsensiTimers(chat, id, sess.EndTS)
	text := buildAbsensiSessionText(
		fmt.Sprintf("Absen sesi #%d dibuka.", id),
		sess.StartTS, sess.EndTS, absensiRadiusOf(chat), true,
		"Share live location untuk tercatat.", "Pantau via .absen hasil.")
	return store.ReplyText(ctx, mctx, text)
}

// buildAbsensiSessionText merangkai teks pengumuman sesi: headline +
// blok form + baris kaki. Murni (tanpa I/O) supaya bisa diuji.
func buildAbsensiSessionText(headline string, startTS, endTS int64, radius float64, withRadius bool, footers ...string) string {
	var sb strings.Builder
	sb.WriteString(headline + "\n")
	sb.WriteString("\n" + absenSesiFormBlock(startTS, endTS, radius, withRadius))
	for _, f := range footers {
		if strings.TrimSpace(f) == "" {
			continue
		}
		sb.WriteString("\n\n" + f)
	}
	return sb.String()
}

// untuk pengumuman sesi (mulai/perpanjang/otomatis). Lintas tengah malam:
// Jam akhir ditandai "(+1 hari)".
func absenSesiFormBlock(startTS, endTS int64, radius float64, withRadius bool) string {
	const w = 7 // selebar "Tanggal"
	sh, sd := absenFormDay(startTS)
	_, ed := absenFormDay(endTS)
	jam := absenFormClock(startTS) + " – " + absenFormClock(endTS)
	if sd != ed {
		jam += " (+1 hari)"
	}
	lines := []string{
		absenPadColon("Hari", sh, w),
		absenPadColon("Tanggal", sd, w),
		absenPadColon("Jam", jam, w),
	}
	if withRadius {
		lines = append(lines, absenPadColon("Radius", fmt.Sprintf("%.0f meter", radius), w))
	}
	return "```" + strings.Join(lines, "\n") + "```"
}

func handleAbsenBerhenti(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireModGate(ctx, mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	chat := mctx.Chat.String()
	sess, err := store.Absensi().ActiveSession(chat)
	if err != nil {
		return absenReply(ctx, mctx, fmt.Sprintf("Gagal: baca sesi gagal (%v).", err))
	}
	if sess == nil {
		return absenReply(ctx, mctx, "Info: tidak ada sesi absen aktif.")
	}
	return absensiCloseAndRecap(chat, sess.ID)
}

func handleAbsenExtend(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireModGate(ctx, mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	if len(mctx.Args) < 2 {
		return absenReply(ctx, mctx, "Gagal: sertakan menit.", "Contoh: .absen perpanjang 30")
	}
	add, err := strconv.Atoi(strings.TrimSpace(mctx.Args[1]))
	if err != nil || add < 1 {
		return absenReply(ctx, mctx, "Gagal: menit harus angka ≥ 1.", "Contoh: .absen perpanjang 30")
	}
	chat := mctx.Chat.String()
	sess, err := store.Absensi().ActiveSession(chat)
	if err != nil {
		return absenReply(ctx, mctx, fmt.Sprintf("Gagal: baca sesi gagal (%v).", err))
	}
	if sess == nil {
		return absenReply(ctx, mctx, "Info: tidak ada sesi absen aktif.")
	}
	newEnd, err := store.Absensi().ExtendSession(sess.ID, add)
	if err != nil {
		return absenReply(ctx, mctx, fmt.Sprintf("Gagal: perpanjang gagal (%v).", err))
	}
	// Re-arm: timer lama (reminder/tutup) dibuang, yang baru ikut end baru.
	// Kecocokan end_ts di DB adalah token (pola reminder) — timer basi no-op.
	armAbsensiTimers(chat, sess.ID, newEnd)
	text := buildAbsensiSessionText(
		fmt.Sprintf("Durasi sesi absensi diperpanjang %d menit.", add),
		sess.StartTS, newEnd, 0, false,
		"Pantau via .absen hasil.")
	return store.ReplyText(ctx, mctx, text)
}

// ---- hasil & rekap (hasil: semua; rekap: admin) ----

func handleAbsenHasil(ctx context.Context, mctx *store.MessageContext) error {
	chat := mctx.Chat.String()
	sess, err := store.Absensi().ActiveSession(chat)
	if err != nil {
		return absenReply(ctx, mctx, fmt.Sprintf("Gagal: baca sesi gagal (%v).", err))
	}
	if sess == nil {
		sess, err = store.Absensi().LatestSession(chat)
		if err != nil {
			return absenReply(ctx, mctx, fmt.Sprintf("Gagal: baca sesi gagal (%v).", err))
		}
		if sess == nil {
			return absenReply(ctx, mctx, "Info: belum pernah ada sesi absen di grup ini.")
		}
	}
	records, err := store.Absensi().ListRecords(sess.ID)
	if err != nil {
		return absenReply(ctx, mctx, fmt.Sprintf("Gagal: baca hasil gagal (%v).", err))
	}
	return store.ReplyText(ctx, mctx, buildAbsensiRecap(sess, records))
}

// handleAbsenStatus menampilkan setelan + status berlaku: patokan,
// sesi (aktif dengan sisa waktu, atau terakhir), jadwal. Read-only untuk
// semua member grup (tanpa mod gate) — yang sensitif tetap terkunci di
// subcommand konfigurasi/ekspor. Tiga blok monospace bernomor ":" sejajar
// (lebar 7, pola rekap).
func handleAbsenStatus(ctx context.Context, mctx *store.MessageContext) error {
	body, err := buildAbsensiStatus(mctx.Chat.String())
	if err != nil {
		return absenReply(ctx, mctx, fmt.Sprintf("Gagal: baca status gagal (%v).", err))
	}
	return store.ReplyText(ctx, mctx, body)
}

// buildAbsensiStatus merangkai blok status (murni, tanpa I/O chat) supaya
// bisa diuji: tiga blok monospace Patokan/Sesi/Jadwal dengan ":" sejajar.
func buildAbsensiStatus(chat string) (string, error) {
	const w = 7 // selebar "Tanggal"
	var sb strings.Builder
	sb.WriteString("*Status Absen*\n")

	// Patokan: nama + lat + long + radius, masing-masing sebaris.
	sb.WriteString("\n```")
	if cfg, err := store.Absensi().GetConfig(chat); err != nil {
		return "", err
	} else if cfg != nil {
		name := strings.TrimSpace(cfg.Name)
		if name == "" {
			name = "-"
		}
		sb.WriteString(absenPadColon("Patokan", name, w))
		sb.WriteString("\n" + absenPadColon("Lat", fmt.Sprintf("%.5f", cfg.Lat), w))
		sb.WriteString("\n" + absenPadColon("Long", fmt.Sprintf("%.5f", cfg.Long), w))
		sb.WriteString("\n" + absenPadColon("Radius", fmt.Sprintf("%.0f meter", cfg.RadiusM), w))
	} else {
		sb.WriteString(absenPadColon("Patokan", "belum diset", w))
		sb.WriteString("\n" + absenPadColon("Radius", "-", w))
	}
	sb.WriteString("```\n")

	// Sesi.
	sb.WriteString("\n```")
	sess, err := store.Absensi().ActiveSession(chat)
	if err != nil {
		return "", err
	}
	if sess == nil {
		sess, err = store.Absensi().LatestSession(chat)
		if err != nil {
			return "", err
		}
	}
	if sess == nil {
		sb.WriteString(absenPadColon("Sesi", "belum pernah ada", w))
	} else {
		records, _ := store.Absensi().ListRecords(sess.ID)
		if sess.Status == "open" {
			sisa := sess.EndTS - time.Now().Unix()
			sisaStr := "segera berakhir"
			if sisa > 60 {
				sisaStr = fmt.Sprintf("%d menit", sisa/60)
			}
			sh, sd := absenFormDay(sess.StartTS)
			sb.WriteString(absenPadColon("Sesi", fmt.Sprintf("AKTIF #%d", sess.ID), w))
			sb.WriteString("\n" + absenPadColon("Hadir", strconv.Itoa(len(records)), w))
			sb.WriteString("\n" + absenPadColon("Hari", sh, w))
			sb.WriteString("\n" + absenPadColon("Tanggal", sd, w))
			sb.WriteString("\n" + absenPadColon("Jam", fmt.Sprintf("%s – %s",
				absenFormClock(sess.StartTS), absenFormClock(sess.EndTS)), w))
			sb.WriteString("\n" + absenPadColon("Sisa", sisaStr, w))
		} else {
			sb.WriteString(absenPadColon("Sesi", fmt.Sprintf("terakhir #%d", sess.ID), w))
			sb.WriteString("\n" + absenPadColon("Hadir", strconv.Itoa(len(records)), w))
			sh, sd := absenFormDay(sess.StartTS)
			sb.WriteString("\n" + absenPadColon("Hari", sh, w))
			sb.WriteString("\n" + absenPadColon("Tanggal", sd, w))
			sb.WriteString("\n" + absenPadColon("Jam", fmt.Sprintf("%s – %s",
				absenFormClock(sess.StartTS), absenFormClock(sess.EndTS)), w))
		}
	}
	sb.WriteString("```\n")

	// Jadwal (tanpa Tanggal karena berulang, tanpa zona dalam kurung).
	sb.WriteString("\n```")
	sched, err := store.Absensi().GetSchedule(chat)
	if err != nil {
		return "", err
	}
	if sched != nil && sched.Enabled {
		sb.WriteString(absenPadColon("Jadwal", "aktif", w))
		sb.WriteString("\n" + absenPadColon("Hari", formatAbsenDaysLong(sched.Days), w))
		sb.WriteString("\n" + absenPadColon("Jam", fmt.Sprintf("%s – %s",
			formatAbsenMin(sched.StartMin), formatAbsenMin(sched.EndMin)), w))
	} else {
		sb.WriteString(absenPadColon("Jadwal", "mati", w))
	}
	sb.WriteString("```")
	return strings.TrimRight(sb.String(), "\n"), nil
}

func handleAbsenExport(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireModGate(ctx, mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	chat := mctx.Chat.String()
	sess, err := store.Absensi().LatestSession(chat)
	if err != nil {
		return absenReply(ctx, mctx, fmt.Sprintf("Gagal: baca sesi gagal (%v).", err))
	}
	if sess == nil {
		return absenReply(ctx, mctx, "Info: belum pernah ada sesi absen di grup ini.")
	}
	records, err := store.Absensi().ListRecords(sess.ID)
	if err != nil {
		return absenReply(ctx, mctx, fmt.Sprintf("Gagal: baca hasil gagal (%v).", err))
	}
	data, err := buildAbsensiXLSX(records)
	if err != nil {
		return absenReply(ctx, mctx, fmt.Sprintf("Gagal: susun xlsx gagal (%v).", err))
	}
	filename := fmt.Sprintf("absen-%s.xlsx", time.Unix(sess.StartTS, 0).In(timex.Zone()).Format("20060102-1504"))
	hari, tanggal := absenFormDay(sess.StartTS)
	caption := fmt.Sprintf("Rekap absen %s, %s — %d hadir.", hari, tanggal, len(records))
	return crmSendDoc(ctx, mctx, filename,
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		string(data), caption)
}

// ---- check-in otomatis ----

// CheckAbsensiCheckin dipanggil handler untuk tiap pesan grup. Bila pesan
// adalah share LIVE location dan ada sesi aktif + patokan, jarak dihitung
// dan kehadiran dicatat (first-valid-wins). Void + fail-soft total: bukan
// interaksi yang boleh error ke user, kecuali hint statis (edukatif).
func CheckAbsensiCheckin(client *whatsmeow.Client, evt *events.Message) {
	if store.Absensi() == nil || !evt.Info.IsGroup {
		return
	}
	msg := store.UnwrapMessage(evt.Message)
	if msg == nil {
		return
	}
	var lat, long, acc float64
	live := false
	staticLoc := false
	if loc := msg.GetLocationMessage(); loc != nil {
		if loc.GetIsLive() {
			lat, long, acc = loc.GetDegreesLatitude(), loc.GetDegreesLongitude(), float64(loc.GetAccuracyInMeters())
			live = true
		} else {
			staticLoc = true
		}
	}
	if ll := msg.GetLiveLocationMessage(); ll != nil {
		lat, long, acc = ll.GetDegreesLatitude(), ll.GetDegreesLongitude(), float64(ll.GetAccuracyInMeters())
		live = true
	}
	if !live {
		// Lokasi statis: respons SETIAP KALI, tetapi hanya bila sesi
		// aktif (di luar sesi diam total). Bukan command → tanpa gema.
		if staticLoc {
			if sess, _ := store.Absensi().ActiveSession(evt.Info.Chat.String()); sess != nil {
				mctx := &store.MessageContext{Client: client, Chat: evt.Info.Chat, Sender: evt.Info.Sender, RawEvt: evt}
				_ = store.ReplyText(context.Background(), mctx, absenStaticHintText)
			}
		}
		return
	}
	ctx := context.Background()
	chat := evt.Info.Chat.String()
	sess, err := store.Absensi().ActiveSession(chat)
	if err != nil || sess == nil {
		return // tak ada sesi aktif: abaikan diam-diam
	}
	cfg, err := store.Absensi().GetConfig(chat)
	if err != nil || cfg == nil {
		return // sesi tanpa patokan (tak seharusnya terjadi — mulai menolaknya)
	}
	dist := haversineM(lat, long, cfg.Lat, cfg.Long)
	radius := absensiRadiusOf(chat)
	if dist > radius {
		logfmt.Warn("absensi: %s di luar radius %.0fm (jarak %.0fm)\n", evt.Info.Sender.String(), radius, dist)
		return // diam (anti-spam drift GPS) — user cek via .absen hasil
	}
	name := strings.TrimSpace(evt.Info.PushName)
	if name == "" {
		name = evt.Info.Sender.ToNonAD().User
	}
	inserted, err := store.Absensi().AddRecord(sess.ID, evt.Info.Sender.String(), name,
		evt.Info.Timestamp.Unix(), lat, long, dist, acc)
	if err != nil {
		logfmt.Warn("absensi: gagal catat (%v)\n", err)
		return
	}
	if !inserted {
		return // sudah tercatat sebelumnya — diam
	}
	mctx := &store.MessageContext{Client: client, Chat: evt.Info.Chat, Sender: evt.Info.Sender, RawEvt: evt}
	_ = store.ReplyText(ctx, mctx, buildAbsensiCheckinText(name, evt.Info.Timestamp.Unix(), dist))
}

// buildAbsensiCheckinText merangkai konfirmasi check-in (murni, bisa diuji).
// Lebar kolom disamakan dengan blok lain (7) agar ":" sejajar di semua pesan.
func buildAbsensiCheckinText(name string, ts int64, dist float64) string {
	const w = 7 // selebar "Tanggal"
	return "Hadir tercatat.\n\n```" + absenPadColon("Nama", name, w) + "\n" +
		absenPadColon("Waktu", absenFormClockS(ts), w) + "\n" +
		absenPadColon("Jarak", fmt.Sprintf("%.0f meter", dist), w) + "```"
}

// absenStaticHintText adalah hint lokasi statis (dikirim tiap share statis
// saat sesi aktif).
const absenStaticHintText = "Lokasi statis tidak tercatat.\nShare live location untuk absen kehadiran."

// haversineM mengembalikan jarak meter great-circle dua koordinat.
func haversineM(lat1, lon1, lat2, lon2 float64) float64 {
	const earthR = 6371000.0
	toRad := math.Pi / 180
	dLat := (lat2 - lat1) * toRad
	dLon := (lon2 - lon1) * toRad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*toRad)*math.Cos(lat2*toRad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * earthR * math.Asin(math.Sqrt(a))
}

// ---- timer sesi + scheduler ----

// armAbsensiTimers memasang ulang timer reminder + tutup untuk satu sesi.
// Timer lama dibuang; kecocokan end_ts DB adalah token anti-basi.
func armAbsensiTimers(chat string, sessionID, endTS int64) {
	absensiTimers.Lock()
	defer absensiTimers.Unlock()
	if old, ok := absensiTimers.m[chat]; ok {
		if old.reminder != nil {
			old.reminder.Stop()
		}
		if old.close != nil {
			old.close.Stop()
		}
	}
	set := &absensiTimerSet{}
	if remAt := endTS - int64(absensiReminderBefore/time.Second); remAt > time.Now().Unix() {
		sid := sessionID
		set.reminder = time.AfterFunc(time.Until(time.Unix(remAt, 0)), func() {
			fireAbsensiReminder(chat, sid, remAt+int64(absensiReminderBefore/time.Second))
		})
	}
	sid := sessionID
	set.close = time.AfterFunc(time.Until(time.Unix(endTS, 0)), func() {
		fireAbsensiClose(chat, sid, endTS)
	})
	absensiTimers.m[chat] = set
}

// fireAbsensiReminder mengirim "segera berakhir" bila sesi masih open dan
// end_ts-nya masih cocok (token). Dipanggil dari timer, fail-soft.
func fireAbsensiReminder(chat string, sessionID, expectEnd int64) {
	if absensiWAClient == nil || store.Absensi() == nil {
		return
	}
	sess, err := store.Absensi().GetSession(sessionID)
	if err != nil || sess == nil || sess.Status != "open" || sess.EndTS != expectEnd {
		return
	}
	chatJID, err := types.ParseJID(chat)
	if err != nil {
		return
	}
	ctx := context.Background()
	eh, ed := absenFormDay(sess.EndTS)
	text := "Absen berakhir 5 menit lagi.\n" +
		"Hari : " + eh + "\n" +
		"Tanggal : " + ed + "\n" +
		"Jam : " + absenFormClock(sess.EndTS) + "\n" +
		"Segera share live location bila belum tercatat."
	resp, err := absensiWAClient.SendMessage(ctx, chatJID, &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String(text)},
	})
	if err != nil {
		logfmt.Warn("absensi: reminder gagal (%v)\n", err)
		return
	}
	mctx := &store.MessageContext{Client: absensiWAClient, Chat: chatJID}
	store.LogOutgoing(mctx, "text", text)
	store.TrackBotMessageID(string(resp.ID), text)
}

// fireAbsensiClose menutup sesi + mengirim rekap (token end_ts sama).
func fireAbsensiClose(chat string, sessionID, expectEnd int64) {
	if absensiWAClient == nil || store.Absensi() == nil {
		return
	}
	sess, err := store.Absensi().GetSession(sessionID)
	if err != nil || sess == nil || sess.Status != "open" || sess.EndTS != expectEnd {
		return
	}
	if err := absensiCloseAndRecap(chat, sessionID); err != nil {
		logfmt.Warn("absensi: auto-tutup gagal (%v)\n", err)
	}
}

// absensiCloseAndRecap menutup sesi + mengirim rekap ke chat. Dipakai
// `.absen berhenti`, timer tutup, dan ticker jadwal.
func absensiCloseAndRecap(chat string, sessionID int64) error {
	if absensiWAClient == nil || store.Absensi() == nil {
		return fmt.Errorf("absensi mati")
	}
	sess, err := store.Absensi().GetSession(sessionID)
	if err != nil {
		return err
	}
	if sess == nil || sess.Status != "open" {
		return fmt.Errorf("tidak ada sesi aktif")
	}
	if err := store.Absensi().CloseSession(sessionID); err != nil {
		return err
	}
	records, err := store.Absensi().ListRecords(sessionID)
	if err != nil {
		return err
	}
	chatJID, err := types.ParseJID(chat)
	if err != nil {
		return err
	}
	text := buildAbsensiRecap(sess, records)
	resp, err := absensiWAClient.SendMessage(context.Background(), chatJID, &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String(text)},
	})
	if err != nil {
		return err
	}
	mctx := &store.MessageContext{Client: absensiWAClient, Chat: chatJID}
	store.LogOutgoing(mctx, "text", text)
	store.TrackBotMessageID(string(resp.ID), text)
	return nil
}

// absenPadColon meratakan label ke lebar kolom lalu ": " — dipakai di
// dalam blok monospace (```) supaya ":" sejajar vertikal (font monospace
// WhatsApp). Label ASCII sehingga padding byte = padding kolom.
func absenPadColon(label, value string, width int) string {
	for len(label) < width {
		label += " "
	}
	return label + ": " + value
}

// buildAbsensiRecap merangkai rekap sesi (dipakai berhenti/auto-tutup/hasil):
// header polos, blok form monospace (Hari/Tanggal/Jam/Hadir, ":" sejajar),
// list nama + jam (tanpa jarak — keputusan tampilan), hint unduh bila ada.
func buildAbsensiRecap(sess *store.AbsensiSession, records []store.AbsensiRecord) string {
	var sb strings.Builder
	sb.WriteString("*Rekap Absen*\n")
	sb.WriteString("\n```" + absenRecapFormBlock(sess, len(records)) + "```\n")
	for i, r := range records {
		fmt.Fprintf(&sb, "\n%d. %s — %s", i+1, r.Name, absenFormClockS(r.TS))
	}
	if len(records) > 0 {
		sb.WriteString("\n\nUnduh: .absen rekap")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// absenRecapFormBlock menyusun ISI blok monospace rekap (tanpa ```):
// satu hari → Hari/Tanggal/Jam/Hadir; lintas hari → sub-blok
// Mulai:/Selesai: + Hadir.
func absenRecapFormBlock(sess *store.AbsensiSession, hadir int) string {
	const w = 7 // selebar "Tanggal"
	sh, sd := absenFormDay(sess.StartTS)
	eh, ed := absenFormDay(sess.EndTS)
	if sd == ed {
		return strings.Join([]string{
			absenPadColon("Hari", sh, w),
			absenPadColon("Tanggal", sd, w),
			absenPadColon("Jam", absenFormClock(sess.StartTS)+" – "+absenFormClock(sess.EndTS), w),
			absenPadColon("Hadir", strconv.Itoa(hadir), w),
		}, "\n")
	}
	return strings.Join([]string{
		absenPadColon("Mulai", sh+", "+sd+" "+absenFormClock(sess.StartTS), w),
		absenPadColon("Selesai", eh+", "+ed+" "+absenFormClock(sess.EndTS), w),
		absenPadColon("Hadir", strconv.Itoa(hadir), w),
	}, "\n")
}

// absenAnnounce mengirim pengumuman teks ke chat (mulai/extend).
func absenAnnounce(chat, text string) {
	if absensiWAClient == nil {
		return
	}
	chatJID, err := types.ParseJID(chat)
	if err != nil {
		logfmt.Warn("absensi: chat JID invalid (%v)\n", err)
		return
	}
	ctx := context.Background()
	resp, err := absensiWAClient.SendMessage(ctx, chatJID, &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String(text)},
	})
	if err != nil {
		logfmt.Warn("absensi: umumkan gagal (%v)\n", err)
		return
	}
	mctx := &store.MessageContext{Client: absensiWAClient, Chat: chatJID}
	store.LogOutgoing(mctx, "text", text)
	store.TrackBotMessageID(string(resp.ID), text)
}

// RestoreAbsensiSessions memulihkan timer sesi aktif (tahan restart) +
// menyalakan ticker jadwal harian. Dipanggil sekali setelah Connect
// (pola RestoreReminders): sesi kedaluwarsa ditutup diam-diam.
func RestoreAbsensiSessions() {
	if store.Absensi() == nil {
		return
	}
	for _, s := range store.Absensi().ListOpenSessions() {
		if s.EndTS <= time.Now().Unix() {
			_ = store.Absensi().CloseSession(s.ID)
			continue
		}
		armAbsensiTimers(s.Chat, s.ID, s.EndTS)
	}
	go absensiScheduleLoop()
}

// absensiScheduleLoop memeriksa jadwal tiap tick: cocok → auto-mulai;
// sesi aktif yang end-nya lewat >60 dtk (backstop restart) → tutup+rekap.
func absensiScheduleLoop() {
	ticker := time.NewTicker(absensiTickInterval)
	defer ticker.Stop()
	for range ticker.C {
		absensiTick()
	}
}

func absensiTick() {
	if absensiWAClient == nil || store.Absensi() == nil {
		return
	}
	now := timex.Now()
	nowMin := now.Hour()*60 + now.Minute()
	nowUnix := now.Unix()
	scheds, err := store.Absensi().ListSchedules()
	if err != nil {
		return
	}
	for _, sched := range scheds {
		if !absensiDayMatch(sched.Days, int(now.Weekday())) {
			continue
		}
		active, err := store.Absensi().ActiveSession(sched.Chat)
		if err != nil {
			continue
		}
		if active == nil {
			if nowMin >= sched.StartMin && nowMin < sched.EndMin {
				dur := sched.EndMin - nowMin
				if dur < 1 {
					dur = 1
				}
				id, err := store.Absensi().OpenSession(sched.Chat, dur, true, "jadwal", nowUnix)
				if err != nil {
					continue
				}
				endTS := nowUnix + int64(dur)*60
				if sess, _ := store.Absensi().GetSession(id); sess != nil {
					endTS = sess.EndTS
				}
				armAbsensiTimers(sched.Chat, id, endTS)
				absenAnnounce(sched.Chat, buildAbsensiSessionText(
					"Absen OTOMATIS dibuka.",
					nowUnix, endTS, absensiRadiusOf(sched.Chat), true,
					"Share live location untuk tercatat."))
			}
			continue
		}
		// Backstop: sesi aktif yang end-nya lewat >60 dtk (timer hilang
		// saat restart). Grace 60 dtk menghindari dobel-rekap dengan timer.
		if active.EndTS <= nowUnix-60 {
			if err := absensiCloseAndRecap(sched.Chat, active.ID); err != nil {
				logfmt.Warn("absensi: backstop tutup gagal (%v)\n", err)
			}
		}
	}
}

// absensiDayMatch true bila weekday Go (0=Minggu) ada di CSV days.
func absensiDayMatch(daysCSV string, weekday int) bool {
	for _, part := range strings.Split(daysCSV, ",") {
		if v, err := strconv.Atoi(strings.TrimSpace(part)); err == nil && v == weekday {
			return true
		}
	}
	return false
}

// absensiSenderLabel untuk kolom created_by (owner/admin pembuka).
func absensiSenderLabel(mctx *store.MessageContext) string {
	s := mctx.Sender.ToNonAD().User
	if s == "" {
		return "admin"
	}
	return s
}

// ---- parser hari & jam ----

var absensiDayNames = map[string]int{
	"minggu": 0, "senin": 1, "selasa": 2, "rabu": 3,
	"kamis": 4, "jumat": 5, "sabtu": 6,
}

// parseAbsenDays menerima NAMA ("senin-jumat", "setiap-hari") MAUPUN
// ANGKA ("8", "1-3", "1,3,5") — case-insensitive → CSV "1,2,3,4,5" urut.
// Angka: 1–7 = Senin–Minggu, 8 = Senin–Jumat, 9 = Senin–Sabtu,
// 10 = Setiap hari. Preset 8/9/10 harus tunggal (tak bisa digabung).
// Nama dan angka tak boleh campur. false bila tak ada hari valid.
func parseAbsenDays(raw string) (string, bool) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "setiap-hari" || s == "tiap-hari" || s == "harian" || s == "semua" || s == "10" {
		return "0,1,2,3,4,5,6", true
	}
	if s == "8" {
		return "1,2,3,4,5", true
	}
	if s == "9" {
		return "1,2,3,4,5,6", true
	}
	got := map[int]bool{}
	addRange := func(a, b int) {
		if a > b {
			a, b = b, a
		}
		for d := a; d <= b; d++ {
			got[d] = true
		}
	}
	isNumericPart := func(p string) bool {
		if p == "" {
			return false
		}
		for _, r := range p {
			if (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
		return true
	}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		// Preset hanya sah tunggal (sudah ditangani di atas bila utuh).
		if part == "8" || part == "9" || part == "10" {
			return "", false
		}
		if isNumericPart(part) {
			if bounds := strings.SplitN(part, "-", 2); len(bounds) == 2 && bounds[0] != "" && bounds[1] != "" {
				a, err1 := strconv.Atoi(bounds[0])
				b, err2 := strconv.Atoi(bounds[1])
				na, ok1 := numDay(a)
				nb, ok2 := numDay(b)
				if err1 != nil || err2 != nil || !ok1 || !ok2 {
					return "", false
				}
				if na <= nb {
					addRange(na, nb)
				} else {
					// Rentang melingkar (mis. 5-7 = Jum–Min): pecah dua.
					addRange(na, 6)
					addRange(0, nb)
				}
				continue
			}
			if v, err := strconv.Atoi(part); err == nil {
				if nd, ok := numDay(v); ok {
					got[nd] = true
					continue
				}
			}
			return "", false
		}
		if bounds := strings.SplitN(part, "-", 2); len(bounds) == 2 {
			a, ok1 := absensiDayNames[strings.TrimSpace(bounds[0])]
			b, ok2 := absensiDayNames[strings.TrimSpace(bounds[1])]
			if !ok1 || !ok2 {
				return "", false
			}
			addRange(a, b)
			continue
		}
		if d, ok := absensiDayNames[part]; ok {
			got[d] = true
			continue
		}
		return "", false
	}
	if len(got) == 0 {
		return "", false
	}
	var days []int
	for d := 0; d <= 6; d++ {
		if got[d] {
			days = append(days, d)
		}
	}
	var sb strings.Builder
	for i, d := range days {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, "%d", d)
	}
	return sb.String(), true
}

// numDay memetakan angka user 1–7 (1=Senin..7=Minggu) ke weekday Go 0–6.
func numDay(v int) (int, bool) {
	if v >= 1 && v <= 6 {
		return v, true
	}
	if v == 7 {
		return 0, true
	}
	return 0, false
}

// addNumCheck true bila 1–7 (Senin–Minggu).
func addNumCheck(v int) bool {
	_, ok := numDay(v)
	return ok
}

// parseAbsenClock menerima "07.00" / "07:00" → menit sejak 00:00.
func parseAbsenClock(raw string) (int, bool) {
	s := strings.TrimSpace(strings.Replace(raw, ".", ":", 1))
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, false
	}
	h, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	m, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// formatAbsenDays "1,2,3,4,5" → "Senin–Jumat" (rentang kompak bila
// berurutan, daftar bila tidak).
func formatAbsenDays(daysCSV string) string {
	names := []string{"Min", "Sen", "Sel", "Rab", "Kam", "Jum", "Sab"}
	var days []int
	for _, part := range strings.Split(daysCSV, ",") {
		if v, err := strconv.Atoi(strings.TrimSpace(part)); err == nil && v >= 0 && v <= 6 {
			days = append(days, v)
		}
	}
	if len(days) == 0 {
		return "-"
	}
	sort.Ints(days)
	if len(days) == 7 {
		return "Setiap hari"
	}
	contiguous := true
	for i := 1; i < len(days); i++ {
		if days[i] != days[i-1]+1 {
			contiguous = false
			break
		}
	}
	if contiguous {
		return names[days[0]] + "–" + names[days[len(days)-1]]
	}
	var parts []string
	for _, d := range days {
		parts = append(parts, names[d])
	}
	return strings.Join(parts, ",")
}

// formatAbsenMin menit → "07.00".
func formatAbsenMin(min int) string {
	return fmt.Sprintf("%02d.%02d", min/60, min%60)
}

// ---- ekspor xlsx ----

// buildAbsensiXLSX menyusun workbook rekap: No, Nama, Nomor, Hari,
// Tanggal, Jam, Jarak(m), Akurasi(m), Koordinat. Pola buildGenericXLSX
// (bridge/excel.go) ditulis ulang lokal karena helper itu unexported
// (preseden finance.go).
func buildAbsensiXLSX(records []store.AbsensiRecord) ([]byte, error) {
	headers := []string{"No", "Nama", "Nomor", "Hari", "Tanggal", "Jam", "Jarak(m)", "Akurasi(m)", "Koordinat"}
	widths := []float64{6, 25, 18, 12, 16, 12, 10, 12, 30}
	f := excelize.NewFile()
	sheet := "Absensi"
	idx, err := f.NewSheet(sheet)
	if err != nil {
		return nil, err
	}
	headStyle, err := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{Bold: true},
		Border: []excelize.Border{
			{Type: "left", Color: "000000", Style: 1},
			{Type: "top", Color: "000000", Style: 1},
			{Type: "bottom", Color: "000000", Style: 1},
			{Type: "right", Color: "000000", Style: 1},
		},
	})
	if err != nil {
		return nil, err
	}
	cellStyle, err := f.NewStyle(&excelize.Style{
		Border: []excelize.Border{
			{Type: "left", Color: "000000", Style: 1},
			{Type: "top", Color: "000000", Style: 1},
			{Type: "bottom", Color: "000000", Style: 1},
			{Type: "right", Color: "000000", Style: 1},
		},
	})
	if err != nil {
		return nil, err
	}
	colName := func(i int) string {
		name, _ := excelize.ColumnNumberToName(i + 1)
		return name
	}
	for i, h := range headers {
		cell := fmt.Sprintf("%s1", colName(i))
		if err := f.SetCellValue(sheet, cell, h); err != nil {
			return nil, err
		}
		if err := f.SetColWidth(sheet, colName(i), colName(i), widths[i]); err != nil {
			return nil, err
		}
	}
	if err := f.SetCellStyle(sheet, "A1", fmt.Sprintf("%s1", colName(len(headers)-1)), headStyle); err != nil {
		return nil, err
	}
	for r, rec := range records {
		row := r + 2
		hari, tanggal := absenFormDay(rec.TS)
		vals := []any{
			r + 1,
			rec.Name,
			absensiPhone(rec.Sender),
			hari,
			tanggal,
			absenFormClockS(rec.TS),
			math.Round(rec.DistanceM),
			math.Round(rec.AccuracyM),
			fmt.Sprintf("%.6f,%.6f", rec.Lat, rec.Long),
		}
		for i, v := range vals {
			if err := f.SetCellValue(sheet, fmt.Sprintf("%s%d", colName(i), row), v); err != nil {
				return nil, err
			}
		}
		last := fmt.Sprintf("%s%d", colName(len(vals)-1), row)
		if err := f.SetCellStyle(sheet, fmt.Sprintf("%s%d", colName(0), row), last, cellStyle); err != nil {
			return nil, err
		}
	}
	f.SetActiveSheet(idx)
	_ = f.DeleteSheet("Sheet1")
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// absensiPhone memendekkan JID sender jadi nomor/ID tampilan.
func absensiPhone(sender string) string {
	if i := strings.Index(sender, "@s.whatsapp.net"); i >= 0 {
		return sender[:i]
	}
	if strings.TrimSpace(sender) == "" {
		return "-"
	}
	return sender
}

// ---- wizard .absen setup ----

// absenSetupTimeout adalah masa berlaku setup (diam = batal diam-diam,
// setting lama utuh).
const absenSetupTimeout = 5 * time.Minute

// absenSetup adalah state wizard setup per chat: stage 0 = menunggu nomor
// bagian (reply ke dashboard), 1..4 = menunggu nilai section, 5 = menunggu
// pilihan hari (daftar angka), 6 = menunggu jam mulai+selesai.
type absenSetup struct {
	dashID  string
	stage   int
	days    string
	expires time.Time
}

var absenSetups = struct {
	sync.Mutex
	m map[string]*absenSetup
}{m: make(map[string]*absenSetup)}

// absenSetupGet mengambil state aktif chat (nil bila tak ada/kedaluwarsa;
// kedaluwarsa dibersihkan diam-diam di sini).
func absenSetupGet(chat string) *absenSetup {
	absenSetups.Lock()
	defer absenSetups.Unlock()
	st, ok := absenSetups.m[chat]
	if !ok {
		return nil
	}
	if time.Now().After(st.expires) {
		delete(absenSetups.m, chat)
		return nil
	}
	return st
}

func absenSetupSet(chat string, st *absenSetup) {
	absenSetups.Lock()
	defer absenSetups.Unlock()
	st.expires = time.Now().Add(absenSetupTimeout)
	absenSetups.m[chat] = st
}

func absenSetupClear(chat string) {
	absenSetups.Lock()
	defer absenSetups.Unlock()
	delete(absenSetups.m, chat)
}

// handleAbsenSetup memulai wizard: kirim dashboard nilai saat ini (wajib
// via ReplyText supaya bisa di-reply angkanya) + catat state pilih-nomor.
func handleAbsenSetup(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireModGate(ctx, mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	chat := mctx.Chat.String()
	dashID, err := absenSendDashboard(ctx, mctx, chat)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal: tampilkan pengaturan gagal (%v).", err))
	}
	absenSetupSet(chat, &absenSetup{dashID: dashID})
	return nil
}

// absenSendDashboard mengirim dashboard + mengembalikan stanza ID-nya
// (untuk pencocokan reply angka).
func absenSendDashboard(ctx context.Context, mctx *store.MessageContext, chat string) (string, error) {
	text := buildAbsensiDashboard(chat)
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String(text)},
	})
	if err != nil {
		return "", err
	}
	store.LogOutgoing(mctx, "text", text)
	store.TrackBotMessageID(string(resp.ID), text)
	return string(resp.ID), nil
}

// absenPadEq meratakan label ke lebar 6 kolom lalu " = " sehingga tanda
// "=" sejajar vertikal dalam satu blok (Nama/Lat/Long, Status/Mulai/...).
func absenPadEq(label, value string) string {
	for len(label) < 6 {
		label += " "
	}
	return label + " = " + value
}

// Nama hari penuh untuk ringkasan (index weekday Go 0=Minggu).
// Ejaan mengikuti spec display yang disetujui ("Jum'at").
var absenDayFull = []string{"Minggu", "Senin", "Selasa", "Rabu", "Kamis", "Jum'at", "Sabtu"}

// buildAbsensiDashboard merangkai tampilan nilai berlaku PERSIS format yang
// disetujui: baris kosong pemisah antar section, "=" sejajar per blok,
// blok JADWAL hanya 3 elemen (Status/Hari/Jam).
func buildAbsensiDashboard(chat string) string {
	nama, lat, long := "-", "-", "-"
	durasi := 60
	jarak := "20 meter"
	if cfg, err := store.Absensi().GetConfig(chat); err == nil && cfg != nil {
		if strings.TrimSpace(cfg.Name) != "" {
			nama = strings.TrimSpace(cfg.Name)
		}
		lat = fmt.Sprintf("%v", cfg.Lat)
		long = fmt.Sprintf("%v", cfg.Long)
		if cfg.DefaultDurasi > 0 {
			durasi = cfg.DefaultDurasi
		}
		if cfg.RadiusM > 0 {
			jarak = fmt.Sprintf("%.0f meter", cfg.RadiusM)
		}
	}
	status, hari, jam := "Off", "-", "-"
	// Baris Hari/Jam hanya diisi bila jadwal AKTIF (off = "- semua",
	// nilai simpanan tetap untuk "on" kembali).
	if sched, err := store.Absensi().GetSchedule(chat); err == nil && sched != nil && sched.Enabled {
		status = "On"
		if h := formatAbsenDaysLong(sched.Days); h != "-" {
			hari = h
		}
		jam = fmt.Sprintf("%s-%s", formatAbsenMin(sched.StartMin), formatAbsenMin(sched.EndMin))
	}
	var sb strings.Builder
	sb.WriteString("*PENGATURAN ABSEN*\n")
	sb.WriteString("\n*[1] PATOKAN*\n")
	sb.WriteString("```" + absenPadEq("Nama", nama) + "\n" + absenPadEq("Lat", lat) + "\n" + absenPadEq("Long", long) + "```\n")
	sb.WriteString("\n*[2] DURASI*\n")
	fmt.Fprintf(&sb, "```%s```\n", absenPadEq("Durasi", fmt.Sprintf("%d menit", durasi)))
	sb.WriteString("\n*[3] RADIUS*\n")
	sb.WriteString("```" + absenPadEq("Jarak", jarak) + "```\n")
	sb.WriteString("\n*[4] JADWAL*\n")
	sb.WriteString("```" + absenPadEq("Status", status) + "\n" + absenPadEq("Hari", hari) + "\n" + absenPadEq("Jam", jam) + "```\n")
	sb.WriteString("\n_Reply dengan nomor bagian yang ingin disesuaikan_")
	return sb.String()
}

// absenSectionPrompt mengembalikan teks prompt tiap section (verbatim
// sesuai persetujuan).
func absenSectionPrompt(section int) string {
	switch section {
	case 1:
		return "PATOKAN\nKirim pesan lokasi (Shareloc) atau ketik *lat, long [nama]*\n\nContoh : -3.97617, 122.51786 Kantor\n\nTips :\nKetik *kembali* untuk batal"
	case 2:
		return "DURASI\nKetik angka dalam satuan menit\n\nContoh : 60\n\nTips :\nKetik *kembali* untuk batal"
	case 3:
		return "RADIUS\nKetik angka dalam satuan meter\n\nContoh : 20\n\nTips :\nKetik *kembali* untuk batal"
	default:
		return "JADWAL\nAtur jadwal absensi otomatis, ketik :\n- ON = untuk mengaktifkan\n- OFF = untuk menonaktifkan\n\nTips :\nKetik *kembali* untuk batal"
	}
}

// absenIsManager true bila pengirim adalah owner atau admin grup.
func absenIsManager(ctx context.Context, client *whatsmeow.Client, evt *events.Message) bool {
	mctx := &store.MessageContext{Client: client, Chat: evt.Info.Chat, Sender: evt.Info.Sender, RawEvt: evt}
	if IsOwner(mctx) {
		return true
	}
	ok, err := mctx.IsSenderAdmin(ctx)
	return err == nil && ok
}

// absenSetupText mengambil teks polos pesan (conversation / extended-text).
// "" bila bukan pesan teks (mis. lokasi — ditangani khusus section 1).
func absenSetupText(msg *waE2E.Message) string {
	if msg == nil {
		return ""
	}
	if s := msg.GetConversation(); s != "" {
		return s
	}
	if ext := msg.GetExtendedTextMessage(); ext != nil {
		return ext.GetText()
	}
	return ""
}

// absenIsCommand true bila teks adalah command bot (prefix ./$//) —
// command SELALU lolos wizard (diproses normal, setup tetap menunggu).
func absenIsCommand(text string) bool {
	t := strings.TrimSpace(text)
	return strings.HasPrefix(t, ".") || strings.HasPrefix(t, "$") || strings.HasPrefix(t, "\\")
}

// CheckAbsensiSetup menelan pesan yang merupakan bagian wizard setup yang
// sedang pending: reply angka ke dashboard, kata "kembali", atau nilai
// section. true = ditangani (pemanggil setop alur); false = bukan bagian
// setup (lanjut normal). Fail-soft total.
func CheckAbsensiSetup(client *whatsmeow.Client, evt *events.Message) bool {
	if store.Absensi() == nil || !evt.Info.IsGroup {
		return false
	}
	chat := evt.Info.Chat.String()
	st := absenSetupGet(chat)
	if st == nil {
		return false
	}
	ctx := context.Background()
	if !absenIsManager(ctx, client, evt) {
		return false
	}
	msg := store.UnwrapMessage(evt.Message)
	if msg == nil {
		return false
	}
	text := strings.TrimSpace(absenSetupText(msg))
	// Command lolos wizard.
	if text != "" && absenIsCommand(text) {
		return false
	}
	mctx := &store.MessageContext{Client: client, Chat: evt.Info.Chat, Sender: evt.Info.Sender, RawEvt: evt}
	replyTo := func(text string) {
		_ = store.ReplyText(ctx, mctx, text)
	}

	// Kata kembali: batalkan + kembali ke tampilan awal (dashboard dikirim
	// ulang agar bisa langsung pilih bagian lain).
	if st.stage >= 1 && strings.EqualFold(text, "kembali") {
		absenSetupDone(ctx, mctx, chat, "Setup dibatalkan, kembali ke tampilan awal.")
		return true
	}

	// Stage 0: wajib reply ke dashboard berisi SATU angka 1–4.
	if st.stage == 0 {
		if store.ExtractStanzaID(evt) == "" || store.ExtractStanzaID(evt) != st.dashID {
			return false
		}
		n, err := strconv.Atoi(text)
		if err != nil || n < 1 || n > 4 || strings.Contains(strings.TrimSpace(text), " ") {
			replyTo("Balas dengan angka 1–4 sesuai bagian yang ingin disesuaikan.")
			return true
		}
		st.stage = n
		absenSetupSet(chat, st)
		replyTo(absenSectionPrompt(n))
		return true
	}

	// Stage 1..4: nilai section. Lokasi (tanpa teks) hanya sah di section 1.
	if st.stage == 1 {
		if lat, long, name, ok := absenSetupPatokanValue(msg, text, chat); ok {
			if err := store.Absensi().SetConfig(chat, lat, long, name); err != nil {
				replyTo(fmt.Sprintf("Gagal: simpan patokan gagal (%v).", err))
				return true
			}
			if strings.TrimSpace(name) == "" {
				name = "-"
			}
			absenSetupDone(ctx, mctx, chat, fmt.Sprintf(
				"Patokan absensi ditetapkan.\nnama : %s\nlat : %v\nlong : %v", name, lat, long))
			return true
		}
		replyTo("Format salah atau tidak lengkap. Kirim pesan lokasi (Shareloc) atau ketik *lat, long [nama]*\n\n> lat : latitude (garis lintang)\n> long : longitudinal (garis bujur)\n> nama : nama lokasi (opsional)\n\nContoh : -3.97617, 122.51786 Kantor\n\nTips :\nKetik *kembali* untuk batal")
		return true
	}
	if st.stage == 2 {
		v, err := strconv.Atoi(text)
		if err != nil || v < 1 || v > 1440 {
			replyTo("Menit harus angka 1–1440. Ketik angka dalam satuan menit\n\nContoh : 60\n\nTips :\nKetik *kembali* untuk batal")
			return true
		}
		if err := store.Absensi().SetDefaultDurasi(chat, v); err != nil {
			if strings.Contains(err.Error(), "belum ada patokan") {
				replyTo("Gagal: belum ada patokan. Isi bagian [1] dulu, atau ketik *kembali* untuk batal.")
				return true
			}
			replyTo(fmt.Sprintf("Gagal: simpan durasi gagal (%v).", err))
			return true
		}
		// Sinkron: jam selesai jadwal = mulai + durasi baru (bila baris
		// jadwal ada; dijepit 24.00 agar tak lintas hari).
		if sched, _ := store.Absensi().GetSchedule(chat); sched != nil {
			if end := sched.StartMin + v; end > 0 {
				if end > 24*60 {
					end = 24 * 60
				}
				_ = store.Absensi().SetSchedule(chat, sched.Days, sched.StartMin, end)
			}
		}
		absenSetupDone(ctx, mctx, chat, fmt.Sprintf("Durasi %d menit untuk absensi berhasil ditetapkan.", v))
		return true
	}
	if st.stage == 3 {
		v, err := strconv.ParseFloat(strings.TrimSpace(strings.Replace(text, ",", ".", 1)), 64)
		if err != nil || v < absensiRadiusMin || v > absensiRadiusMax {
			replyTo("Jarak harus berada di antara 1 sampai 5000. Ketik angka dalam satuan meter\n\nContoh : 20\n\nTips :\nKetik *kembali* untuk batal")
			return true
		}
		if err := store.Absensi().SetRadius(chat, v); err != nil {
			if strings.Contains(err.Error(), "belum ada patokan") {
				replyTo("Gagal: belum ada patokan. Isi bagian [1] dulu, atau ketik *kembali* untuk batal.")
				return true
			}
			replyTo(fmt.Sprintf("Gagal: simpan radius gagal (%v).", err))
			return true
		}
		absenSetupDone(ctx, mctx, chat, fmt.Sprintf("Jarak maksimal absen sejauh %.0f dari patokan berhasil di tetapkan.", v))
		return true
	}
	if st.stage == 4 {
		low := strings.ToLower(text)
		if low == "off" {
			if err := store.Absensi().SetScheduleEnabled(chat, false); err != nil {
				if strings.Contains(err.Error(), "belum ada jadwal") {
					replyTo("Gagal: belum ada jadwal. Ketik *kembali* untuk batal.")
					return true
				}
				replyTo(fmt.Sprintf("Gagal: matikan jadwal gagal (%v).", err))
				return true
			}
			absenSetupDone(ctx, mctx, chat, "Berhasil: jadwal harian dimatikan.")
			return true
		}
		if low == "on" {
			// "on" SELALU lanjut setting dulu (daftar hari → jam), tak
			// peduli ada riwayat — "on" berarti konfigurasi, bukan
			// aktifkan-cepat jadwal lama.
			st.stage = 5
			absenSetupSet(chat, st)
			replyTo(absenDayListText())
			return true
		}
		replyTo("Argumen tidak valid. Untuk mengaktifkan ketik ON atau OFF untuk menonaktifkan absen terjadwal otomatis.\n\nTips :\nKetik *kembali* untuk batal")
		return true
	}
	// Stage 5: pilihan hari (angka 1–7, preset 8/9/10, rentang, daftar).
	if st.stage == 5 {
		if days, ok := parseAbsenDays(text); ok {
			st.days = days
			st.stage = 6
			absenSetupSet(chat, st)
			replyTo("Ketik jam absen dimulai\n\nContoh : 07.00\n\nTips :\nKetik *kembali* untuk batal")
			return true
		}
		replyTo("Hari tidak dikenali.\n\n" + absenDayListText())
		return true
	}
	// Stage 6: HANYA jam mulai — jam selesai = mulai + durasi default chat
	// (agar tak bentrok dengan durasi sesi).
	start, ok := -1, false
	if fields := strings.Fields(text); len(fields) >= 1 {
		start, ok = parseAbsenClock(fields[0])
	}
	if ok {
		dur := 60
		if cfg, _ := store.Absensi().GetConfig(chat); cfg != nil && cfg.DefaultDurasi > 0 {
			dur = cfg.DefaultDurasi
		}
		end := start + dur
		if end > 24*60 {
			end = 24 * 60
		}
		if err := store.Absensi().SetSchedule(chat, st.days, start, end); err != nil {
			replyTo(fmt.Sprintf("Gagal: simpan jadwal gagal (%v).", err))
			return true
		}
		absenSetupDone(ctx, mctx, chat, fmt.Sprintf(
			"Jadwal absen harian otomatis berhasil ditetapkan.\nHari : %s\nJam : %s-%s",
			formatAbsenDaysLong(st.days), formatAbsenMin(start), formatAbsenMin(end)))
		return true
	}
	replyTo("Jam tidak valid. Ketik jam mulai (contoh: 07.00)\n\nTips :\nKetik *kembali* untuk batal")
	return true
}

// formatAbsenDaysLong merangkai CSV days jadi ringkasan nama penuh:
// berurutan → "Senin-Jumat", tidak → "Senin, Selasa, dan Rabu".
func formatAbsenDaysLong(daysCSV string) string {
	var days []int
	for _, part := range strings.Split(daysCSV, ",") {
		if v, err := strconv.Atoi(strings.TrimSpace(part)); err == nil && v >= 0 && v <= 6 {
			days = append(days, v)
		}
	}
	if len(days) == 0 {
		return "-"
	}
	sort.Ints(days)
	if len(days) == 7 {
		return "Setiap hari"
	}
	if len(days) == 1 {
		return absenDayFull[days[0]]
	}
	contiguous := true
	for i := 1; i < len(days); i++ {
		if days[i] != days[i-1]+1 {
			contiguous = false
			break
		}
	}
	if contiguous {
		return absenDayFull[days[0]] + "-" + absenDayFull[days[len(days)-1]]
	}
	names := make([]string, 0, len(days))
	for _, d := range days {
		names = append(names, absenDayFull[d])
	}
	if len(names) == 1 {
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + ", dan " + names[len(names)-1]
}

// absenDayListText adalah daftar pilih hari section jadwal (angka 1–7,
// preset 8/9/10, rentang, daftar).
func absenDayListText() string {
	return "Pilih hari absen:\n[1] Senin\n[2] Selasa\n[3] Rabu\n[4] Kamis\n[5] Jumat\n[6] Sabtu\n[7] Minggu\n[8] Senin–Jumat\n[9] Senin–Sabtu\n[10] Setiap hari\n\nKetik angka, rentang (contoh: 1-3), atau daftar (contoh: 1,3,5)\n\nTips :\nKetik *kembali* untuk batal"
}

// absenCoordRe mengambil pasangan koordinat di AWAL argumen: desimal
// opsional + pemisah koma/spasi + sisa teks sebagai nama. Dipindah dari
// shareloc.go (plugin .shareloc dihapus; regex-nya tetap dipakai di sini).
var absenCoordRe = regexp.MustCompile(`^(-?\d+(?:\.\d+)?)\s*[, ]\s*(-?\d+(?:\.\d+)?)\s*(.*)$`)

// absenSetupPatokanValue mengekstrak koordinat section 1: dari pesan lokasi
// (nama ikut bila ada, kalau tidak pertahankan nama lama) atau teks
// "lat,long [nama]".
func absenSetupPatokanValue(msg *waE2E.Message, text, chat string) (lat, long float64, name string, ok bool) {
	if qlat, qlong, qok := absenCoordsOf(msg); qok {
		n := ""
		if loc := msg.GetLocationMessage(); loc != nil {
			n = strings.TrimSpace(loc.GetName())
		}
		if n == "" {
			if cfg, err := store.Absensi().GetConfig(chat); err == nil && cfg != nil {
				n = cfg.Name
			}
		}
		return qlat, qlong, n, true
	}
	m := absenCoordRe.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return 0, 0, "", false
	}
	la, err1 := strconv.ParseFloat(m[1], 64)
	lo, err2 := strconv.ParseFloat(m[2], 64)
	if err1 != nil || err2 != nil || la < -90 || la > 90 || lo < -180 || lo > 180 {
		return 0, 0, "", false
	}
	n := strings.TrimSpace(m[3])
	if n == "" {
		if cfg, err := store.Absensi().GetConfig(chat); err == nil && cfg != nil {
			n = cfg.Name
		}
	}
	return la, lo, n, true
}

// absenSetupDone menyelesaikan satu bagian: konfirmasi + dashboard terbaru
// (bisa lanjut ubah bagian lain; keluar via "kembali"/timeout).
func absenSetupDone(ctx context.Context, mctx *store.MessageContext, chat, confirm string) {
	_ = store.ReplyText(ctx, mctx, confirm)
	dashID, err := absenSendDashboard(ctx, mctx, chat)
	if err != nil {
		absenSetupClear(chat)
		return
	}
	absenSetupSet(chat, &absenSetup{dashID: dashID, stage: 0})
}
