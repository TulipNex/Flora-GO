// Package finance berisi helper periode bulanan + pembangun laporan Excel
// keuangan (dipakai penjadwal di plugins/finance_monthly.go).
//
// Murni (tanpa DB, tanpa WhatsApp) supaya bisa diuji sendiri: pemanggil
// menyiapkan saldo awal + daftar transaksi, paket ini merakit workbook.
package finance

import (
	"bytes"
	"fmt"
	"strconv"
	"time"

	"github.com/xuri/excelize/v2"
)

const timeLayoutLocal = "2006-01-02 15:04"

// Periode mengubah waktu jadi kunci bulan 'YYYY-MM'.
func Periode(t time.Time) string {
	return t.Format("2006-01")
}

// PrevPeriode mengembalikan bulan sebelum periode ('YYYY-MM'); "" bila
// periode tidak valid.
func PrevPeriode(periode string) string {
	y, m, ok := parsePeriode(periode)
	if !ok {
		return ""
	}
	t := time.Date(y, time.Month(m), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0)
	return t.Format("2006-01")
}

// PeriodeBounds mengembalikan tanggal awal & akhir (inklusif, 'YYYY-MM-DD')
// untuk periode bulan 'YYYY-MM'. ok=false bila periode tidak valid.
func PeriodeBounds(periode string) (dari, sampai string, ok bool) {
	y, m, ok := parsePeriode(periode)
	if !ok {
		return "", "", false
	}
	first := time.Date(y, time.Month(m), 1, 0, 0, 0, 0, time.UTC)
	last := first.AddDate(0, 1, -1)
	return first.Format("2006-01-02"), last.Format("2006-01-02"), true
}

// NextPeriode mengembalikan bulan setelah periode ('YYYY-MM'); "" bila
// periode tidak valid.
func NextPeriode(periode string) string {
	y, m, ok := parsePeriode(periode)
	if !ok {
		return ""
	}
	t := time.Date(y, time.Month(m), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
	return t.Format("2006-01")
}

// NextMonthStart mengembalikan awal bulan berikutnya setelah t (zona t).
func NextMonthStart(t time.Time) time.Time {
	y, m, _ := t.Date()
	return time.Date(y, m+1, 1, 0, 0, 0, 0, t.Location())
}

// parsePeriode mem-parse 'YYYY-MM' menjadi tahun & bulan.
func parsePeriode(periode string) (year, month int, ok bool) {
	if len(periode) != 7 || periode[4] != '-' {
		return 0, 0, false
	}
	y, err1 := strconv.Atoi(periode[:4])
	m, err2 := strconv.Atoi(periode[5:])
	if err1 != nil || err2 != nil || m < 1 || m > 12 {
		return 0, 0, false
	}
	return y, m, true
}

// Saldo menampung saldo per sumber dana.
type Saldo struct {
	Kas      int64
	Rekening int64
}

// Total = kas + rekening.
func (s Saldo) Total() int64 { return s.Kas + s.Rekening }

// Transaksi adalah satu baris untuk laporan (masuk/keluar nil = sisi kosong).
type Transaksi struct {
	Tanggal string // 'YYYY-MM-DD'
	InputAt string // 'YYYY-MM-DD HH:MM:SS' ("" = baris lama)
	Teks    string
	Sumber  string
	Masuk   *int64
	Keluar  *int64
}

// Laporan adalah bahan laporan bulanan.
type Laporan struct {
	Owner     string
	Periode   string
	Awal      Saldo
	Transaksi []Transaksi
	Dibuat    time.Time
}

// Ringkas menghitung total masuk/keluar + saldo akhir dari daftar transaksi.
func Ringkas(awal Saldo, tx []Transaksi) (masuk, keluar int64, akhir Saldo) {
	akhir = awal
	for _, t := range tx {
		if t.Masuk != nil {
			masuk += *t.Masuk
			if t.Sumber == "rekening" {
				akhir.Rekening += *t.Masuk
			} else {
				akhir.Kas += *t.Masuk
			}
		}
		if t.Keluar != nil {
			keluar += *t.Keluar
			if t.Sumber == "rekening" {
				akhir.Rekening -= *t.Keluar
			} else {
				akhir.Kas -= *t.Keluar
			}
		}
	}
	return masuk, keluar, akhir
}

// BuildLaporan merakit workbook .xlsx: sheet "Ringkasan" + "Transaksi".
func BuildLaporan(l Laporan) ([]byte, error) {
	f := excelize.NewFile()
	masuk, keluar, akhir := Ringkas(l.Awal, l.Transaksi)

	if err := tulisRingkasan(f, l, masuk, keluar, akhir); err != nil {
		return nil, err
	}
	if err := tulisTransaksi(f, l.Transaksi); err != nil {
		return nil, err
	}
	if err := f.DeleteSheet("Sheet1"); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		return nil, err
	}
	if buf.Len() == 0 {
		return nil, fmt.Errorf("file xlsx kosong")
	}
	return buf.Bytes(), nil
}

// tulisRingkasan menulis sheet ringkasan saldo awal/mutasi/saldo akhir.
func tulisRingkasan(f *excelize.File, l Laporan, masuk, keluar int64, akhir Saldo) error {
	sheet := "Ringkasan"
	if _, err := f.NewSheet(sheet); err != nil {
		return err
	}
	bold, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	title, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Size: 13}})

	dibuat := l.Dibuat
	if dibuat.IsZero() {
		dibuat = time.Now()
	}
	set := func(cell string, v interface{}, style int) {
		_ = f.SetCellValue(sheet, cell, v)
		if style != 0 {
			_ = f.SetCellStyle(sheet, cell, cell, style)
		}
	}
	set("A1", "Laporan Keuangan Bulanan", title)
	set("A2", "Owner", 0)
	set("B2", l.Owner, 0)
	set("A3", "Periode", 0)
	set("B3", l.Periode, 0)
	set("A4", "Dibuat", 0)
	set("B4", dibuat.Format(timeLayoutLocal), 0)

	set("A6", "SALDO AWAL", bold)
	set("A7", "Kas", 0)
	set("B7", l.Awal.Kas, 0)
	set("A8", "Rekening", 0)
	set("B8", l.Awal.Rekening, 0)
	set("A9", "Total", bold)
	set("B9", l.Awal.Total(), bold)

	set("A11", "MUTASI BULAN INI", bold)
	set("A12", "Total Masuk", 0)
	set("B12", masuk, 0)
	set("A13", "Total Keluar", 0)
	set("B13", keluar, 0)

	set("A15", "SALDO AKHIR", bold)
	set("A16", "Kas", 0)
	set("B16", akhir.Kas, 0)
	set("A17", "Rekening", 0)
	set("B17", akhir.Rekening, 0)
	set("A18", "Total", bold)
	set("B18", akhir.Total(), bold)

	_ = f.SetColWidth(sheet, "A", "A", 16)
	_ = f.SetColWidth(sheet, "B", "B", 24)

	// Nomor angka: tetap numerik supaya bisa dijumlah di Excel.
	numStyle, _ := f.NewStyle(&excelize.Style{NumFmt: 3})
	if numStyle != 0 {
		for _, r := range []string{"B7", "B8", "B9", "B12", "B13", "B16", "B17", "B18"} {
			_ = f.SetCellStyle(sheet, r, r, numStyle)
		}
	}
	return nil
}

// tulisTransaksi menulis sheet daftar transaksi (terlama→terbaru).
func tulisTransaksi(f *excelize.File, tx []Transaksi) error {
	sheet := "Transaksi"
	if _, err := f.NewSheet(sheet); err != nil {
		return err
	}
	headers := []string{"Nomor", "Tanggal", "Waktu Input", "Transaksi", "Sumber", "Masuk", "Keluar"}
	bold, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	border, _ := f.NewStyle(&excelize.Style{Border: []excelize.Border{
		{Type: "left", Style: 1}, {Type: "right", Style: 1},
		{Type: "top", Style: 1}, {Type: "bottom", Style: 1},
	}})
	caps := []float64{8, 13, 20, 42, 12, 15, 15}
	maxW := make([]int, len(headers))
	for c, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(c+1, 1)
		if err := f.SetCellValue(sheet, cell, h); err != nil {
			return err
		}
		maxW[c] = len([]rune(h))
	}
	lastHead, _ := excelize.CoordinatesToCellName(len(headers), 1)
	_ = f.SetCellStyle(sheet, "A1", lastHead, bold)

	for i, t := range tx {
		rn := i + 2
		row := []interface{}{
			i + 1,
			t.Tanggal,
			t.InputAt,
			t.Teks,
			t.Sumber,
			numPtrVal(t.Masuk),
			numPtrVal(t.Keluar),
		}
		texts := []string{
			strconv.Itoa(i + 1),
			t.Tanggal,
			t.InputAt,
			t.Teks,
			t.Sumber,
			numPtrText(t.Masuk),
			numPtrText(t.Keluar),
		}
		for c, v := range row {
			cell, _ := excelize.CoordinatesToCellName(c+1, rn)
			if err := f.SetCellValue(sheet, cell, v); err != nil {
				return err
			}
			if n := len([]rune(texts[c])); n > maxW[c] {
				maxW[c] = n
			}
		}
	}
	if len(tx) > 0 {
		lastCell, _ := excelize.CoordinatesToCellName(len(headers), len(tx)+1)
		_ = f.SetCellStyle(sheet, "A1", lastCell, border)
	}
	for c := range headers {
		w := float64(maxW[c] + 2)
		if w < 8 {
			w = 8
		}
		if c < len(caps) && w > caps[c] {
			w = caps[c]
		}
		col, err := excelize.ColumnNumberToName(c + 1)
		if err != nil {
			return err
		}
		_ = f.SetColWidth(sheet, col, col, w)
	}
	return nil
}

func numPtrVal(v *int64) interface{} {
	if v == nil {
		return ""
	}
	return *v
}

func numPtrText(v *int64) string {
	if v == nil {
		return ""
	}
	return strconv.FormatInt(*v, 10)
}
