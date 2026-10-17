package plugins

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"wabot-go/internal/identity"
	"wabot-go/internal/logfmt"
	"wabot-go/internal/store"
	"wabot-go/internal/timex"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types"
	"github.com/polymorfa/hypermeow/types/events"
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
		v, err := strconv.Atoi(strings.TrimSpace(mctx.Args[1]))
		if err != nil || v < 1 || v > 1440 {
			return absenReply(ctx, mctx, "Gagal: menit harus angka 1–1440.", "Contoh: .absen mulai 60")
		}
		dur = v
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
	// Kunci identitas kanonis (bare, tanpa device) + semua bentuk alt
	// (SenderAlt event, resolve store LID↔PN): first-valid-wins berlaku
	// lintas format, flip format antar update live-location tidak mencatat
	// orang yang sama dua kali. Lihat internal/identity.
	bg := context.Background()
	canonical := evt.Info.Sender.ToNonAD().String()
	var altKeys []string
	for _, k := range identity.Strings(bg, client, evt.Info.Sender, evt.Info.SenderAlt) {
		if k != canonical {
			altKeys = append(altKeys, k)
		}
	}
	for _, k := range append([]string{canonical}, altKeys...) {
		if has, herr := store.Absensi().HasRecord(sess.ID, k); herr != nil {
			logfmt.Warn("absensi: gagal cek duplikat (%v)\n", herr)
			return
		} else if has {
			return // sudah tercatat (format mana pun) — diam
		}
	}
	inserted, err := store.Absensi().AddRecord(sess.ID, canonical, name,
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

// sendAbsensiTextRetry mengirim teks ke chat dengan retry singkat (3x,
// jeda 2s lalu 3s): pengumuman sesi (rekap/tutup) tidak boleh hilang karena
// satu kegagalan kirim sesaat. Mengembalikan ID pesan bila sukses.
func sendAbsensiTextRetry(chat, text string) (string, error) {
	if absensiWAClient == nil {
		return "", fmt.Errorf("absensi mati")
	}
	chatJID, err := types.ParseJID(chat)
	if err != nil {
		return "", err
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt+1) * time.Second)
		}
		resp, err := absensiWAClient.SendMessage(context.Background(), chatJID, &waE2E.Message{
			ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String(text)},
		})
		if err == nil {
			return string(resp.ID), nil
		}
		lastErr = err
	}
	return "", lastErr
}

// absensiCloseAndRecap menutup sesi + mengirim rekap ke chat. Dipakai
// `.absen berhenti`, timer tutup, dan ticker jadwal.
//
// Urutan: rekap disusun + dikirim DULU (dengan retry singkat), sesi ditutup
// SETELAHNYA — pengumuman tidak hilang bila kirim pertama gagal. Retry
// dibatasi ±5 detik total supaya penutupan tetap praktis tepat jadwal
// (tidak molor bermenit-menit). Bila kirim tetap gagal setelah retry, sesi
// TETAP ditutup tepat jadwal + kabar singkat dikirim (rekap lengkap bisa
// dibaca via `.absen hasil` — data aman).
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
	records, err := store.Absensi().ListRecords(sessionID)
	if err != nil {
		return err
	}
	text := buildAbsensiRecap(sess, records)
	msgID, sendErr := sendAbsensiTextRetry(chat, text)
	if cerr := store.Absensi().CloseSession(sessionID); cerr != nil {
		return cerr
	}
	if sendErr != nil {
		logfmt.Warn("absensi: rekap sesi #%d gagal terkirim setelah retry (%v), sesi tetap ditutup.\n", sessionID, sendErr)
		sendAbsensiTextOnce(chat, "Sesi absen ditutup. Rekap gagal terkirim — lihat via .absen hasil.")
		return nil
	}
	chatJID, err := types.ParseJID(chat)
	if err != nil {
		return err
	}
	mctx := &store.MessageContext{Client: absensiWAClient, Chat: chatJID}
	store.LogOutgoing(mctx, "text", text)
	store.TrackBotMessageID(msgID, text)
	return nil
}

// sendAbsensiTextOnce mengirim teks sekali tanpa retry (untuk kabar
// fallback) — hasilnya diabaikan pemanggil.
func sendAbsensiTextOnce(chat, text string) {
	if absensiWAClient == nil {
		return
	}
	chatJID, err := types.ParseJID(chat)
	if err != nil {
		return
	}
	_, _ = absensiWAClient.SendMessage(context.Background(), chatJID, &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String(text)},
	})
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
