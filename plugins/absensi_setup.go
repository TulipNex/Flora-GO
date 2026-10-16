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

	"wabot-go/internal/store"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types/events"
	"github.com/xuri/excelize/v2"
	"google.golang.org/protobuf/proto"
)

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
