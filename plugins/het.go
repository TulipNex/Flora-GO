package plugins

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"wabot-go/internal/external"
	"wabot-go/internal/store"
	"wabot-go/internal/timex"

	"github.com/xuri/excelize/v2"
)

func init() {
	registerPlugin(RegisterHET)
}

// RegisterHET mendaftarkan command `.het` kategori Alat (semua user):
// unduh Excel Harga Eceran (pasar tradisional) Sulawesi Tenggara 7 hari
// terakhir dari PIHPS BI. Async pola .bk (status → fetch → bangun → kirim)
// supaya chat lain tidak antre.
func RegisterHET(r *Registry) {
	r.Register(Command{
		Name:        "het",
		Description: "Harga pangan Sultra 7 hari (.het)",
		Category:    CategoryInternet,
		Handler:     handleHET,
	})
}

// hetProvinceName adalah provinsi tetap fitur ini (ID di-resolve via API
// tiap request — anti gagal diam-diam bila BI mengubah ID).
const hetProvinceName = "Sulawesi Tenggara"

// hetDays adalah rentang hari ke belakang (keputusan owner: 7 hari,
// ikut default situs).
const hetDays = 7

func handleHET(ctx context.Context, mctx *store.MessageContext) error {
	if len(mctx.Args) == 0 {
		return store.ReplyText(ctx, mctx,
			"*HET SULTRA*\n"+
				"\n"+
				"Ambil data harga pangan dari BI :\n"+
				"*.het*\n"+
				"> tampilkan menu ini\n"+
				"*.het -7*\n"+
				"> 7 hari terakhir\n"+
				"*.het -30*\n"+
				"> 30 hari terakhir\n"+
				"*.het 01/09/2026-30/09/2026*\n"+
				"> rentang tanggal tertentu (DD/MM/YYYY-DD/MM/YYYY)")
	}
	from, to, err := parseHETRange(mctx.Args)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("%v\n\nContoh benar:\n.het -7\n.het -30\n.het 01/09/2026-30/09/2026", err))
	}
	if _, err := sendDlStatus(ctx, mctx, fmt.Sprintf("Mengambil data harga Sultra %s s.d. %s dari BI...", hetShortDate(from), hetShortDate(to))); err != nil {
		return err
	}
	go func() {
		bg := context.Background()
		if rerr := resolveAndSendHET(bg, mctx, from, to); rerr != nil {
			_ = store.ReplyText(bg, mctx, rerr.Error())
		}
	}()
	return nil
}

// parseHETRange mengubah argumen .het jadi rentang YYYY-MM-DD:
// kosong → 7 hari terakhir; "-N" (1–31) → N hari terakhir;
// "DD/MM/YYYY-DD/MM/YYYY" → rentang eksplisit (maks 62 hari, cap hari ini).
// Acuan "hari ini" = timex.Today() (zona WIB/WITA/WIT aktif).
func parseHETRange(args []string) (from, to string, err error) {
	today := timex.Today()
	if len(args) == 0 {
		return shiftDays(today, -(hetDays - 1)), today, nil
	}
	// Gabung semua argumen tanpa spasi (antisipasi ".het 01/09/2026 - 30/09/2026").
	raw := strings.Join(strings.Fields(strings.Join(args, " ")), "")
	if strings.HasPrefix(raw, "-") {
		n, cerr := strconv.Atoi(strings.TrimPrefix(raw, "-"))
		if cerr != nil || n < 1 || n > 31 {
			return "", "", fmt.Errorf("jumlah hari harus 1..31 (dapat %q).", raw)
		}
		return shiftDays(today, -(n - 1)), today, nil
	}
	m := hetRangeRe.FindStringSubmatch(raw)
	if m == nil {
		return "", "", fmt.Errorf("format salah %q.", raw)
	}
	return parseHETExplicit(m[1], m[2], today)
}

// hetRangeRe mengenali DD/MM/YYYY-DD/MM/YYYY (1-2 digit longgar).
var hetRangeRe = regexp.MustCompile(`^(\d{1,2}/\d{1,2}/\d{4})-(\d{1,2}/\d{1,2}/\d{4})$`)

// parseHETExplicit memvalidasi dua tanggal manual → YYYY-MM-DD (kalender
// valid, dari ≤ sampai, maks 62 hari, cap hari ini).
func parseHETExplicit(dariRaw, sampaiRaw, today string) (string, string, error) {
	dari, ok := hetDMYtoISO(dariRaw)
	if !ok {
		return "", "", fmt.Errorf("tanggal mulai %q tidak valid.", dariRaw)
	}
	sampai, ok := hetDMYtoISO(sampaiRaw)
	if !ok {
		return "", "", fmt.Errorf("tanggal akhir %q tidak valid.", sampaiRaw)
	}
	if sampai > today {
		sampai = today
	}
	if dari > sampai {
		// Seluruh rentang di masa depan (sampai tercap hari ini): lipat
		// jadi 1 hari (hari ini) daripada error membingungkan.
		if dari > today {
			dari = today
		} else {
			return "", "", fmt.Errorf("tanggal mulai (%s) setelah tanggal akhir (%s).", dariRaw, sampaiRaw)
		}
	}
	if hetDaySpan(dari, sampai) > 62 {
		return "", "", fmt.Errorf("rentang maks 62 hari.")
	}
	return dari, sampai, nil
}

// hetDMYtoISO mengubah D/M/YYYY jadi YYYY-MM-DD bila kalender valid.
func hetDMYtoISO(s string) (string, bool) {
	p := strings.Split(s, "/")
	if len(p) != 3 {
		return "", false
	}
	d, m, y := padHET(p[0]), padHET(p[1]), p[2]
	for len(y) < 4 {
		y = "0" + y
	}
	iso := fmt.Sprintf("%s-%s-%s", y, m, d)
	t, err := time.Parse("2006-01-02", iso)
	if err != nil || t.Format("2006-01-02") != iso {
		return "", false
	}
	return iso, true
}

func padHET(s string) string {
	if len(s) < 2 {
		return "0" + s
	}
	return s
}

// shiftDays menggeser tanggal YYYY-MM-DD sejauh n hari.
func shiftDays(iso string, n int) string {
	t, err := time.Parse("2006-01-02", iso)
	if err != nil {
		return iso
	}
	return t.AddDate(0, 0, n).Format("2006-01-02")
}

// hetDaySpan menghitung selisih hari dari–sampai (asumsi valid & dari≤sampai).
func hetDaySpan(dari, sampai string) int {
	a, _ := time.Parse("2006-01-02", dari)
	b, _ := time.Parse("2006-01-02", sampai)
	return int(b.Sub(a).Hours()/24) + 1
}

// hetShortDate mengubah YYYY-MM-DD jadi "28 Sep" untuk status/caption.
func hetShortDate(iso string) string {
	t, err := time.Parse("2006-01-02", iso)
	if err != nil {
		return iso
	}
	return t.Format("02 Jan")
}

// resolveAndSendHET: resolve provinsi → fetch grid rentang → bangun xlsx →
// kirim dokumen. Error berupa pesan ramah siap-balas.
func resolveAndSendHET(ctx context.Context, mctx *store.MessageContext, from, to string) error {
	provID, provName, err := external.PihpsProvinceID(ctx, hetProvinceName)
	if err != nil {
		return fmt.Errorf("gagal baca daftar provinsi BI: %v", err)
	}
	rows, dates, err := external.PihpsGridDaerah(ctx, provID, from, to)
	if err != nil {
		return fmt.Errorf("gagal ambil data BI: %v", err)
	}

	xlsx, fname, err := buildHETXLSX(provName, from, to, rows, dates)
	if err != nil {
		return err
	}
	if err := sendGetDocument(ctx, mctx, xlsx, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", fname); err != nil {
		return fmt.Errorf("gagal kirim file: %v", err)
	}
	return nil
}

// buildHETXLSX merakit workbook: header (No, Komoditas, tanggal DD-MM) +
// baris kategori bold + harga sebagai angka asli + border + autofit.
func buildHETXLSX(provName, from, to string, rows []external.PihpsRow, dates []string) ([]byte, string, error) {
	f := excelize.NewFile()
	sheet := "HET " + shortProvName(provName)
	if _, err := f.NewSheet(sheet); err != nil {
		return nil, "", err
	}
	headers := append([]string{"No", "Komoditas"}, shortDates(dates)...)
	// maxW untuk autofit.
	maxW := make([]int, len(headers))
	for c, h := range headers {
		maxW[c] = len([]rune(h))
	}
	// Tulis header.
	for c, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(c+1, 1)
		if err := f.SetCellValue(sheet, cell, h); err != nil {
			return nil, "", err
		}
	}
	// Tulis baris: kategori (level 1) bold, harga angka asli.
	boldStyle, err := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	if err != nil {
		return nil, "", err
	}
	for i, r := range rows {
		rn := i + 2
		set := func(c int, v interface{}) error {
			cell, _ := excelize.CoordinatesToCellName(c+1, rn)
			return f.SetCellValue(sheet, cell, v)
		}
		dispNo := r.No
		dispName := r.Nama
		if err := set(0, dispNo); err != nil {
			return nil, "", err
		}
		if err := set(1, dispName); err != nil {
			return nil, "", err
		}
		for c, d := range dates {
			v := ""
			if p := r.Prices[d]; p != nil {
				if err := set(c+2, *p); err != nil {
					return nil, "", err
				}
			} else if err := set(c+2, v); err != nil {
				return nil, "", err
			}
		}
		// Ukur lebar dari tampilan.
		strs := append([]string{dispNo, dispName}, priceStrs(r, dates)...)
		for c, s := range strs {
			if n := len([]rune(s)); n > maxW[c] {
				maxW[c] = n
			}
		}
		if r.Level == 1 {
			first, _ := excelize.CoordinatesToCellName(1, rn)
			last, _ := excelize.CoordinatesToCellName(len(headers), rn)
			if err := f.SetCellStyle(sheet, first, last, boldStyle); err != nil {
				return nil, "", err
			}
		}
	}
	// Border seluruh tabel.
	borderStyle, err := f.NewStyle(&excelize.Style{
		Border: []excelize.Border{
			{Type: "left", Style: 1},
			{Type: "right", Style: 1},
			{Type: "top", Style: 1},
			{Type: "bottom", Style: 1},
		},
	})
	if err != nil {
		return nil, "", err
	}
	lastCell, _ := excelize.CoordinatesToCellName(len(headers), len(rows)+1)
	if err := f.SetCellStyle(sheet, "A1", lastCell, borderStyle); err != nil {
		return nil, "", err
	}
	// Autofit dengan batas.
	caps := append([]float64{8, 40}, dateCaps(len(dates))...)
	for c := range headers {
		w := float64(maxW[c] + 2)
		if w < 8 {
			w = 8
		}
		if c < len(caps) && w > caps[c] {
			w = caps[c]
		}
		colName, cerr := excelize.ColumnNumberToName(c + 1)
		if cerr != nil {
			return nil, "", cerr
		}
		if err := f.SetColWidth(sheet, colName, colName, w); err != nil {
			return nil, "", err
		}
	}
	if err := f.DeleteSheet("Sheet1"); err != nil {
		return nil, "", err
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		return nil, "", err
	}
	if buf.Len() == 0 {
		return nil, "", fmt.Errorf("file xlsx kosong")
	}
	fname := fmt.Sprintf("het-sultra-%s-%s.xlsx", strings.ReplaceAll(from, "-", ""), strings.ReplaceAll(to, "-", ""))
	return buf.Bytes(), fname, nil
}

// shortProvName memendekkan nama sheet ("Sulawesi Tenggara" → "Sultra").
func shortProvName(provName string) string {
	p := strings.ToLower(strings.TrimSpace(provName))
	if strings.Contains(p, "tenggara") && strings.Contains(p, "sulawesi") {
		return "Sultra"
	}
	if provName == "" {
		return "Daerah"
	}
	return provName
}

// shortDates mengubah DD/MM/YYYY jadi DD-MM untuk header kolom.
func shortDates(dates []string) []string {
	out := make([]string, len(dates))
	for i, d := range dates {
		p := strings.Split(d, "/")
		if len(p) == 3 {
			out[i] = p[0] + "-" + p[1]
		} else {
			out[i] = d
		}
	}
	return out
}

// dateCaps memberi batas lebar tiap kolom tanggal (13).
func dateCaps(n int) []float64 {
	caps := make([]float64, n)
	for i := range caps {
		caps[i] = 13
	}
	return caps
}

// priceStrs memformat harga untuk ukur lebar (ribuan tanpa pemisah).
func priceStrs(r external.PihpsRow, dates []string) []string {
	out := make([]string, len(dates))
	for i, d := range dates {
		if p := r.Prices[d]; p != nil {
			out[i] = strconv.FormatInt(*p, 10)
		}
	}
	return out
}
