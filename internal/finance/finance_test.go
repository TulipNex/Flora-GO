package finance

import (
	"bytes"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
)

func TestPeriodeHelpers(t *testing.T) {
	if got := Periode(time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC)); got != "2026-02" {
		t.Errorf("Periode = %q, mau 2026-02", got)
	}
	if got := PrevPeriode("2026-01"); got != "2025-12" {
		t.Errorf("PrevPeriode = %q, mau 2025-12", got)
	}
	if got := NextPeriode("2025-12"); got != "2026-01" {
		t.Errorf("NextPeriode = %q, mau 2026-01", got)
	}
	if got := PrevPeriode("bukan"); got != "" {
		t.Errorf("PrevPeriode invalid = %q, mau kosong", got)
	}
}

func TestPeriodeBounds(t *testing.T) {
	d, s, ok := PeriodeBounds("2026-02")
	if !ok || d != "2026-02-01" || s != "2026-02-28" {
		t.Errorf("2026-02 → %q..%q ok=%v", d, s, ok)
	}
	_, s, ok = PeriodeBounds("2024-02")
	if !ok || s != "2024-02-29" {
		t.Errorf("2024-02 akhir = %q (kabisat harus 29)", s)
	}
	if _, _, ok := PeriodeBounds("2026-13"); ok {
		t.Error("bulan 13 harus invalid")
	}
}

func TestNextMonthStart(t *testing.T) {
	loc := time.FixedZone("WITA", 8*3600)
	got := NextMonthStart(time.Date(2026, 1, 31, 23, 59, 0, 0, loc))
	want := time.Date(2026, 2, 1, 0, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Errorf("NextMonthStart = %v, mau %v", got, want)
	}
}

func TestRingkas(t *testing.T) {
	i := func(n int64) *int64 { return &n }
	masuk, keluar, akhir := Ringkas(Saldo{Kas: 100000, Rekening: 500000}, []Transaksi{
		{Sumber: "kas", Masuk: i(50000)},
		{Sumber: "kas", Keluar: i(20000)},
		{Sumber: "rekening", Keluar: i(100000)},
	})
	if masuk != 50000 || keluar != 120000 {
		t.Errorf("masuk=%d keluar=%d, mau 50000/120000", masuk, keluar)
	}
	if akhir.Kas != 130000 || akhir.Rekening != 400000 || akhir.Total() != 530000 {
		t.Errorf("akhir=%+v, mau kas 130000 rek 400000", akhir)
	}
}

func TestBuildLaporan(t *testing.T) {
	i := func(n int64) *int64 { return &n }
	data, err := BuildLaporan(Laporan{
		Owner:   "628123",
		Periode: "2026-01",
		Awal:    Saldo{Kas: 1000},
		Transaksi: []Transaksi{
			{Tanggal: "2026-01-05", InputAt: "2026-01-05 10:00:00", Teks: "gaji", Sumber: "kas", Masuk: i(500)},
		},
	})
	if err != nil {
		t.Fatalf("BuildLaporan: %v", err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("buka hasil: %v", err)
	}
	defer f.Close()
	sheets := f.GetSheetList()
	if !contains(sheets, "Ringkasan") || !contains(sheets, "Transaksi") {
		t.Fatalf("sheet kurang: %v", sheets)
	}
	if v, _ := f.GetCellValue("Transaksi", "A1"); v != "Nomor" {
		t.Errorf("Transaksi!A1 = %q", v)
	}
	if v, _ := f.GetCellValue("Transaksi", "C2"); v != "2026-01-05 10:00:00" {
		t.Errorf("Waktu Input = %q", v)
	}
	if v, _ := f.GetCellValue("Transaksi", "F2"); v != "500" {
		t.Errorf("Masuk = %q", v)
	}
	if v, _ := f.GetCellValue("Ringkasan", "B7"); v != "1,000" {
		t.Errorf("saldo awal kas = %q, mau 1,000", v)
	}
	if v, _ := f.GetCellValue("Ringkasan", "B18"); v != "1,500" {
		t.Errorf("saldo akhir total = %q, mau 1,500", v)
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
