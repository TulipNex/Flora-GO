package plugins

import (
	"bytes"
	"context"
	"fmt"
	"math"
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
	// diubah via .absen radius; default baris baru = 20m (skema DB).
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
//   - .absen patokan (reply lokasi / lat,long [nama]) — admin/owner
//   - .absen mulai [durasi_mnt] — admin/owner (default 60)
//   - .absen berhenti — admin/owner (+rekap)
//   - .absen perpanjang <mnt> — admin/owner
//   - .absen jadwal <hari> <mulai> <selesai> | off | (polos=lihat)
//   - .absen hasil — semua (rekap sesi)
//   - .absen rekap — admin/owner (.xlsx sesi terakhir)
//
// Aturan keras: live location SAJA (statis ditolak dengan hint), first-
// valid-wins per pengirim, tanpa status Telat (semua hadir = Tepat).
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
	case "patokan":
		return handleAbsenPatokan(ctx, mctx)
	case "radius", "jarak", "toleransi":
		return handleAbsenRadius(ctx, mctx)
	case "mulai", "start", "buka":
		return handleAbsenMulai(ctx, mctx)
	case "berhenti", "stop", "tutup":
		return handleAbsenBerhenti(ctx, mctx)
	case "extend", "perpanjang":
		return handleAbsenExtend(ctx, mctx)
	case "jadwal", "schedule":
		return handleAbsenJadwal(ctx, mctx)
	case "hasil", "result":
		return handleAbsenHasil(ctx, mctx)
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
.absen patokan <reply lokasi> atau <lat,long [nama]>
> menentukan titik patokan
.absen radius <meter>
> mengatur toleransi jarak kehadiran
.absen mulai <menit>
> membuka sesi absen
.absen berhenti
> menutup sesi
.absen perpanjang <menit>
> memperpanjang sesi berjalan
.absen jadwal <hari> <mulai> <selesai> atau off
> mengatur, melihat, mematikan jadwal harian
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

// absenFormRangeLines menyusun blok Hari/Tanggal/Jam untuk rentang sesi:
// satu hari → satu blok; lintas hari → blok Mulai:/Selesai:.
func absenFormRangeLines(startTS, endTS int64) []string {
	sh, sd := absenFormDay(startTS)
	eh, ed := absenFormDay(endTS)
	if sd == ed {
		return []string{
			"Hari : " + sh,
			"Tanggal : " + sd,
			fmt.Sprintf("Jam : %s – %s", absenFormClock(startTS), absenFormClock(endTS)),
		}
	}
	return []string{
		"Mulai:",
		"Hari : " + sh,
		"Tanggal : " + sd,
		"Jam : " + absenFormClock(startTS),
		"Selesai:",
		"Hari : " + eh,
		"Tanggal : " + ed,
		"Jam : " + absenFormClock(endTS),
	}
}

// ---- subcommand admin ----

func handleAbsenPatokan(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireModGate(ctx, mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	lat, long, name, ok := parseAbsenPatokan(mctx)
	if !ok {
		return absenReply(ctx, mctx, "Gagal: sertakan latitude,longitudinal [Nama] atau reply pesan lokasi.")
	}
	chat := mctx.Chat.String()
	if err := store.Absensi().SetConfig(chat, lat, long, name); err != nil {
		return absenReply(ctx, mctx, fmt.Sprintf("Gagal: simpan patokan gagal (%v).", err))
	}
	label := firstNonEmptyLoc(name, fmt.Sprintf("%.5f,%.5f", lat, long))
	return absenReply(ctx, mctx,
		fmt.Sprintf("Berhasil: patokan grup ini %s (radius %.0fm).", label, absensiRadiusOf(chat)),
		"Lanjut: .absen radius untuk toleransi, .absen mulai untuk buka sesi.")
}

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

func handleAbsenRadius(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireModGate(ctx, mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	chat := mctx.Chat.String()
	if len(mctx.Args) < 2 || strings.TrimSpace(mctx.Args[1]) == "" {
		return absenReply(ctx, mctx,
			fmt.Sprintf("Info: radius grup ini %.0fm.", absensiRadiusOf(chat)),
			fmt.Sprintf("Ubah: .absen radius <meter> (%.0f–%.0f)", absensiRadiusMin, absensiRadiusMax))
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(strings.Replace(mctx.Args[1], ",", ".", 1)), 64)
	if err != nil || v < absensiRadiusMin || v > absensiRadiusMax {
		return absenReply(ctx, mctx,
			fmt.Sprintf("Gagal: radius harus angka %.0f–%.0f (meter).", absensiRadiusMin, absensiRadiusMax),
			"Contoh: .absen radius 50")
	}
	if err := store.Absensi().SetRadius(chat, v); err != nil {
		if strings.Contains(err.Error(), "belum ada patokan") {
			return absenReply(ctx, mctx, "Gagal: belum ada patokan. Set dulu via .absen patokan.")
		}
		return absenReply(ctx, mctx, fmt.Sprintf("Gagal: simpan radius gagal (%v).", err))
	}
	return absenReply(ctx, mctx,
		fmt.Sprintf("Berhasil: radius grup ini %.0fm.", v),
		"Lanjut: .absen mulai untuk buka sesi.")
}

// parseAbsenPatokan mengambil koordinat dari reply pesan lokasi ATAU argumen
// "lat,long" + sisa teks sebagai nama. Reply didahulukan (paling akurat —
func parseAbsenPatokan(mctx *store.MessageContext) (lat, long float64, name string, ok bool) {
	// 1. Reply ke pesan lokasi (statis maupun live — untuk PATOKAN, titik
	//    statis justru ideal karena presisi).
	if quoted := store.UnwrapMessage(store.ExtractQuotedMessage(mctx.RawEvt)); quoted != nil {
		if qlat, qlong, qok := absenCoordsOf(quoted); qok {
			return qlat, qlong, "", true
		}
	}
	// 2. Argumen "lat,long [nama]".
	rest := strings.TrimSpace(mctx.RawArgs)
	if i := strings.IndexAny(rest, " \t\n\r"); i >= 0 && len(mctx.Args) > 1 {
		// RawArgs = "patokan ..." — buang subcommand pertama.
		rest = strings.TrimSpace(rest[len(mctx.Args[0]):])
	}
	m := sharelocRe.FindStringSubmatch(rest)
	if m == nil {
		return 0, 0, "", false
	}
	la, err1 := strconv.ParseFloat(m[1], 64)
	lo, err2 := strconv.ParseFloat(m[2], 64)
	if err1 != nil || err2 != nil || la < -90 || la > 90 || lo < -180 || lo > 180 {
		return 0, 0, "", false
	}
	return la, lo, strings.TrimSpace(m[3]), true
}

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
	if cfg, err := store.Absensi().GetConfig(chat); err != nil || cfg == nil {
		if err != nil {
			return absenReply(ctx, mctx, fmt.Sprintf("Gagal: baca patokan gagal (%v).", err))
		}
		return absenReply(ctx, mctx, "Gagal: belum ada patokan. Set dulu via .absen patokan.")
	}
	dur := absensiDefaultDurasi
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
	sess, _ := store.Absensi().GetSession(id)
	armAbsensiTimers(chat, id, sess.EndTS)
	lines := append([]string{"Berhasil: sesi #" + strconv.FormatInt(id, 10) + " dibuka."},
		append(absenFormRangeLines(sess.StartTS, sess.EndTS),
			fmt.Sprintf("Share live location untuk tercatat (radius %.0fm). Pantau via .absen hasil.", absensiRadiusOf(chat)))...)
	return absenReply(ctx, mctx, lines[0], lines[1:]...)
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
	lines := append([]string{"Berhasil: sesi diperpanjang."},
		append(absenFormRangeLines(sess.StartTS, newEnd), "Pantau via .absen hasil.")...)
	return absenReply(ctx, mctx, lines[0], lines[1:]...)
}

func handleAbsenJadwal(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireModGate(ctx, mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	chat := mctx.Chat.String()
	rest := mctx.Args[1:]
	if len(rest) == 0 {
		sched, err := store.Absensi().GetSchedule(chat)
		if err != nil {
			return absenReply(ctx, mctx, fmt.Sprintf("Gagal: baca jadwal gagal (%v).", err))
		}
		if sched == nil || !sched.Enabled {
			return absenReply(ctx, mctx,
				"Info: belum ada jadwal harian.",
				"Contoh: .absen jadwal senin-jumat 07.00 08.00")
		}
		return absenReply(ctx, mctx,
			"Info: jadwal harian aktif.",
			"Hari : "+formatAbsenDays(sched.Days),
			fmt.Sprintf("Jam : %s – %s (%s)", formatAbsenMin(sched.StartMin), formatAbsenMin(sched.EndMin), timex.Name()))
	}
	if strings.EqualFold(strings.TrimSpace(rest[0]), "off") {
		if err := store.Absensi().ClearSchedule(chat); err != nil {
			return absenReply(ctx, mctx, fmt.Sprintf("Gagal: hapus jadwal gagal (%v).", err))
		}
		return absenReply(ctx, mctx,
			"Berhasil: jadwal harian dimatikan.",
			"Sesi manual tetap via .absen mulai.")
	}
	if len(rest) < 3 {
		return absenReply(ctx, mctx,
			"Gagal: sertakan hari, jam mulai, jam selesai.",
			"Contoh: .absen jadwal senin-jumat 07.00 08.00")
	}
	days, ok := parseAbsenDays(rest[0])
	if !ok {
		return store.ReplyText(ctx, mctx, "Hari tidak valid. Contoh: senin-jumat | setiap-hari | senin,sabtu")
	}
	start, ok1 := parseAbsenClock(rest[1])
	end, ok2 := parseAbsenClock(rest[2])
	if !ok1 || !ok2 || start >= end {
		return absenReply(ctx, mctx,
			"Gagal: jam tidak valid (mulai < selesai).",
			"Contoh: .absen jadwal senin-jumat 07.00 08.00")
	}
	if err := store.Absensi().SetSchedule(chat, days, start, end); err != nil {
		return absenReply(ctx, mctx, fmt.Sprintf("Gagal: simpan jadwal gagal (%v).", err))
	}
	return absenReply(ctx, mctx,
		"Berhasil: jadwal harian aktif.",
		"Hari : "+formatAbsenDays(days),
		fmt.Sprintf("Jam : %s – %s (%s)", formatAbsenMin(start), formatAbsenMin(end), timex.Name()),
		"Bot otomatis buka+tutup absen.",
		"Cek via .absen status.")
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
// subcommand konfigurasi/ekspor.
func handleAbsenStatus(ctx context.Context, mctx *store.MessageContext) error {
	chat := mctx.Chat.String()
	var sb strings.Builder
	sb.WriteString("*Status Absen*\n")

	// Patokan.
	patokan := "belum diset"
	if cfg, err := store.Absensi().GetConfig(chat); err == nil && cfg != nil {
		label := firstNonEmptyLoc(cfg.Name, fmt.Sprintf("%.5f,%.5f", cfg.Lat, cfg.Long))
		patokan = fmt.Sprintf("%s (radius %.0fm)", label, cfg.RadiusM)
	}
	fmt.Fprintf(&sb, "\nPatokan: %s", patokan)

	// Sesi.
	sess, err := store.Absensi().ActiveSession(chat)
	if err != nil {
		return absenReply(ctx, mctx, fmt.Sprintf("Gagal: baca sesi gagal (%v).", err))
	}
	if sess == nil {
		sess, err = store.Absensi().LatestSession(chat)
		if err != nil {
			return absenReply(ctx, mctx, fmt.Sprintf("Gagal: baca sesi gagal (%v).", err))
		}
	}
	if sess == nil {
		sb.WriteString("\nSesi: belum pernah ada")
	} else if sess.Status == "open" {
		records, _ := store.Absensi().ListRecords(sess.ID)
		sisa := sess.EndTS - time.Now().Unix()
		sisaStr := "segera berakhir"
		if sisa > 60 {
			sisaStr = fmt.Sprintf("sisa %d mnt", sisa/60)
		}
		fmt.Fprintf(&sb, "\nSesi AKTIF #%d, hadir %d", sess.ID, len(records))
		for _, line := range absenFormRangeLines(sess.StartTS, sess.EndTS) {
			sb.WriteString("\n" + line)
		}
		sb.WriteString("\n(" + sisaStr + ")")
	} else {
		records, _ := store.Absensi().ListRecords(sess.ID)
		fmt.Fprintf(&sb, "\nSesi terakhir #%d, hadir %d", sess.ID, len(records))
		for _, line := range absenFormRangeLines(sess.StartTS, sess.EndTS) {
			sb.WriteString("\n" + line)
		}
	}

	// Jadwal (form Hari/Jam; tanpa Tanggal karena jadwal berulang).
	if sched, err := store.Absensi().GetSchedule(chat); err == nil && sched != nil && sched.Enabled {
		sb.WriteString("\nJadwal: aktif")
		sb.WriteString("\nHari : " + formatAbsenDays(sched.Days))
		fmt.Fprintf(&sb, "\nJam : %s – %s (%s)", formatAbsenMin(sched.StartMin), formatAbsenMin(sched.EndMin), timex.Name())
	} else {
		sb.WriteString("\nJadwal: mati")
	}
	return store.ReplyText(ctx, mctx, sb.String())
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
	return crmSendDoc(ctx, mctx, filename, string(data), caption)
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
	if loc := msg.GetLocationMessage(); loc != nil && loc.GetIsLive() {
		lat, long, acc = loc.GetDegreesLatitude(), loc.GetDegreesLongitude(), float64(loc.GetAccuracyInMeters())
		live = true
	}
	if ll := msg.GetLiveLocationMessage(); ll != nil {
		lat, long, acc = ll.GetDegreesLatitude(), ll.GetDegreesLongitude(), float64(ll.GetAccuracyInMeters())
		live = true
	}
	if !live {
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
	_ = store.ReplyText(ctx, mctx, fmt.Sprintf("Hadir tercatat, %s (%.0fm dari patokan).", name, dist))
}

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

// buildAbsensiRecap merangkai rekap sesi (dipakai berhenti/auto-tutup/hasil):
// header polos (tanpa status), blok form Hari/Tanggal/Jam, list nama + jam
// (tanpa jarak — keputusan tampilan), hint unduh bila ada data.
func buildAbsensiRecap(sess *store.AbsensiSession, records []store.AbsensiRecord) string {
	var sb strings.Builder
	sb.WriteString("*Rekap Absen*\n")
	sb.WriteString(strings.Join(absenFormRangeLines(sess.StartTS, sess.EndTS), "\n"))
	fmt.Fprintf(&sb, "\nHadir: %d", len(records))
	for i, r := range records {
		fmt.Fprintf(&sb, "\n%d. %s — %s", i+1, r.Name, absenFormClockS(r.TS))
	}
	if len(records) > 0 {
		sb.WriteString("\nUnduh: .absen rekap")
	}
	return strings.TrimRight(sb.String(), "\n")
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
				absenAnnounce(sched.Chat, "Absen OTOMATIS dibuka.\n"+
					strings.Join(absenFormRangeLines(nowUnix, endTS), "\n")+
					fmt.Sprintf("\nShare live location untuk tercatat (radius %.0fm).", absensiRadiusOf(sched.Chat)))
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

// parseAbsenDays menerima "senin-jumat" / "setiap-hari" / "senin,sabtu"
// (case-insensitive) → CSV "1,2,3,4,5" urut. false bila tak ada hari valid.
func parseAbsenDays(raw string) (string, bool) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "setiap-hari" || s == "tiap-hari" || s == "harian" || s == "semua" {
		return "0,1,2,3,4,5,6", true
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
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
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
