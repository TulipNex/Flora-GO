package plugins

// finance_saldo_test.go — tes murni command keuangan (.saldo/.transaksi):
// parsing subcommand, validasi tanggal/nominal, dan format baris. Tanpa DB.

import (
	"strings"
	"testing"
	"time"

	"wabot-go/internal/store"
)

func financePtr(v int64) *int64 { return &v }

func TestParseFinanceLast(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"5", 5, true},
		{"1", 1, true},
		{"50", 50, true},
		{" 10 ", 10, true},
		{"0", 0, false},
		{"51", 0, false},
		{"-3", 0, false},
		{"abc", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		got, err := parseFinanceLast(c.in)
		if c.ok && (err != nil || got != c.want) {
			t.Errorf("parseFinanceLast(%q) = %d, err=%v; mau %d", c.in, got, err, c.want)
		}
		if !c.ok && err == nil {
			t.Errorf("parseFinanceLast(%q) harus error", c.in)
		}
	}
}

func TestParseFinanceDay(t *testing.T) {
	nov := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	feb := time.Date(2026, 2, 15, 10, 0, 0, 0, time.UTC)

	cases := []struct {
		raw  string
		now  time.Time
		want int
		ok   bool
	}{
		{"25", nov, 25, true},
		{"01", nov, 1, true},
		{"30", nov, 30, true},
		{"31", nov, 0, false}, // November hanya 30 hari
		{"32", nov, 0, false},
		{"0", nov, 0, false},
		{"-1", nov, 0, false},
		{"tanggal", nov, 0, false},
	}
	// 2026 bukan tahun kabisat: 29 Februari harus ditolak, 28 diterima.
	if got, err := parseFinanceDay("29", feb); err == nil {
		t.Errorf("parseFinanceDay(29, Februari 2026) = %d, mau error (bukan kabisat)", got)
	}
	if got, err := parseFinanceDay("28", feb); err != nil || got != 28 {
		t.Errorf("parseFinanceDay(28, Februari 2026) = %d, err=%v; mau 28", got, err)
	}
	for _, c := range cases {
		got, err := parseFinanceDay(c.raw, c.now)
		if c.ok && (err != nil || got != c.want) {
			t.Errorf("parseFinanceDay(%q) = %d, err=%v; mau %d", c.raw, got, err, c.want)
		}
		if !c.ok && err == nil {
			t.Errorf("parseFinanceDay(%q) harus error", c.raw)
		}
	}
}

func TestParseFinanceNominal(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"20000", 20000, true},
		{"20.000", 20000, true},
		{"20,000", 20000, true},
		{"Rp20.000", 20000, true},
		{"rp 20.000", 20000, true},
		{" 50000 ", 50000, true},
		{"0", 0, false},
		{"-500", 0, false},
		{"abc", 0, false},
		{"", 0, false},
		{"2000000000000000", 0, false}, // 16 digit > batas
	}
	for _, c := range cases {
		got, err := parseFinanceNominal(c.in)
		if c.ok && (err != nil || got != c.want) {
			t.Errorf("parseFinanceNominal(%q) = %d, err=%v; mau %d", c.in, got, err, c.want)
		}
		if !c.ok && err == nil {
			t.Errorf("parseFinanceNominal(%q) harus error", c.in)
		}
	}
}

func TestFinanceRupiah(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{500, "500"},
		{20000, "20.000"},
		{1250000, "1.250.000"},
		{-2500, "-2.500"},
		{999999999999999, "999.999.999.999.999"},
	}
	for _, c := range cases {
		if got := financeRupiah(c.in); got != c.want {
			t.Errorf("financeRupiah(%d) = %q, mau %q", c.in, got, c.want)
		}
	}
	if got := financeRupiahFull(1250000); got != "Rp1.250.000" {
		t.Errorf("financeRupiahFull = %q, mau Rp1.250.000", got)
	}
	if got := financeRupiahFull(-2500); got != "-Rp2.500" {
		t.Errorf("financeRupiahFull(-2500) = %q, mau -Rp2.500", got)
	}
}

func TestFinanceDateLabel(t *testing.T) {
	if got := financeDateLabel("2026-11-05"); got != "05 Nov 2026" {
		t.Errorf("financeDateLabel = %q, mau 05 Nov 2026", got)
	}
	if got := financeDayMonthLabel("2026-11-08"); got != "08 Nov" {
		t.Errorf("financeDayMonthLabel = %q, mau 08 Nov", got)
	}
	// Nilai rusak dikembalikan apa adanya supaya baris tidak hilang.
	if got := financeDateLabel("bukan-tanggal"); got != "bukan-tanggal" {
		t.Errorf("financeDateLabel(rusak) = %q", got)
	}
	if got := financeDayMonthLabel("bukan-tanggal"); got != "bukan-tanggal" {
		t.Errorf("financeDayMonthLabel(rusak) = %q", got)
	}
}

// TestFinanceNominalLine mengunci bentuk baris .transaksi last/date:
// "- *-Rp5.000* • [Kas] Minuman" (nominal bertanda + sumber Title-Case).
func TestFinanceNominalLine(t *testing.T) {
	masuk := store.FinanceRow{Transaksi: "gaji", Sumber: "rekening", Tanggal: "2026-11-05", Masuk: financePtr(500000)}
	keluar := store.FinanceRow{Transaksi: "bensin", Sumber: "kas", Tanggal: "2026-11-12", Keluar: financePtr(20000)}
	kecil := store.FinanceRow{Transaksi: "Bunga simpanan bank", Sumber: "rekening", Tanggal: "2026-11-11", Masuk: financePtr(7)}

	if got := financeNominalLine(masuk); got != "- *+Rp500.000* • [Rekening] gaji" {
		t.Errorf("baris masuk = %q", got)
	}
	if got := financeNominalLine(keluar); got != "- *-Rp20.000* • [Kas] bensin" {
		t.Errorf("baris keluar = %q", got)
	}
	if got := financeNominalLine(kecil); got != "- *+Rp7* • [Rekening] Bunga simpanan bank" {
		t.Errorf("baris nominal kecil = %q", got)
	}
	// Baris tanpa nominal tetap tampil (bukan disembunyikan).
	kosong := store.FinanceRow{Transaksi: "lama", Sumber: "kas", Tanggal: "2026-11-01"}
	if got := financeNominalLine(kosong); got != "- *tanpa nominal* • [Kas] lama" {
		t.Errorf("baris tanpa nominal = %q", got)
	}
}

// TestFinanceAmountLine mengunci bentuk baris .transaksi amount:
// "- *08 Nov* • [-Kas] Minuman" (nominal sudah jadi judul, tanda masuk/keluar
// pindah ke dalam bracket sumber).
func TestFinanceAmountLine(t *testing.T) {
	masuk := store.FinanceRow{Transaksi: "Transfer", Sumber: "rekening", Tanggal: "2026-11-09", Masuk: financePtr(20000)}
	keluar := store.FinanceRow{Transaksi: "Minuman", Sumber: "kas", Tanggal: "2026-11-08", Keluar: financePtr(5000)}

	if got := financeAmountLine(keluar); got != "- *08 Nov* • [-Kas] Minuman" {
		t.Errorf("baris amount keluar = %q", got)
	}
	if got := financeAmountLine(masuk); got != "- *09 Nov* • [+Rekening] Transfer" {
		t.Errorf("baris amount masuk = %q", got)
	}
}

func TestFinanceSumberLabel(t *testing.T) {
	cases := []struct{ in, want string }{
		{"kas", "Kas"},
		{"Kas", "Kas"},
		{"rekening", "Rekening"},
		{" lain ", " lain "},
		{"", ""},
	}
	for _, c := range cases {
		if got := financeSumberLabel(c.in); got != c.want {
			t.Errorf("financeSumberLabel(%q) = %q, mau %q", c.in, got, c.want)
		}
	}
}

// TestFinanceGroupByDate memastikan baris urut tanggal dikelompokkan
// berurutan (header per tanggal) dan tanggal yang sama tidak dipecah.
func TestFinanceGroupByDate(t *testing.T) {
	rows := []store.FinanceRow{
		{Transaksi: "a", Tanggal: "2026-11-11"},
		{Transaksi: "b", Tanggal: "2026-11-11"},
		{Transaksi: "c", Tanggal: "2026-11-12"},
		{Transaksi: "d", Tanggal: "2026-11-13"},
		{Transaksi: "e", Tanggal: "2026-11-13"},
	}
	groups := financeGroupByDate(rows)
	if len(groups) != 3 {
		t.Fatalf("jumlah kelompok = %d, mau 3", len(groups))
	}
	want := []struct {
		tanggal string
		n       int
	}{
		{"2026-11-11", 2},
		{"2026-11-12", 1},
		{"2026-11-13", 2},
	}
	for i, w := range want {
		if groups[i].Tanggal != w.tanggal || len(groups[i].Rows) != w.n {
			t.Errorf("kelompok %d = %s/%d, mau %s/%d", i, groups[i].Tanggal, len(groups[i].Rows), w.tanggal, w.n)
		}
	}
}

// TestFinanceReverseRows memastikan hasil "terbaru dulu" dibalik jadi urut
// tanggal naik (opsi yang dipilih owner untuk .transaksi last).
func TestFinanceReverseRows(t *testing.T) {
	rows := []store.FinanceRow{
		{Transaksi: "c", Tanggal: "2026-11-12"},
		{Transaksi: "b", Tanggal: "2026-11-11"},
		{Transaksi: "a", Tanggal: "2026-11-11"},
	}
	financeReverseRows(rows)
	got := []string{rows[0].Transaksi, rows[1].Transaksi, rows[2].Transaksi}
	want := []string{"a", "b", "c"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("urutan setelah dibalik = %v, mau %v", got, want)
		}
	}
}

// TestFinanceListBlock mengunci kerangka balasan: judul, baris kosong, isi,
// baris kosong + footer `> *N Transaksi*`, dan peringatan terpotong.
func TestFinanceListBlock(t *testing.T) {
	got := financeListBlock("Transaksi 09 Nov 2026",
		[]string{"- *-Rp5.000* • [Kas] Minuman"}, financeCountLabel(1))
	want := "*Transaksi 09 Nov 2026*\n\n- *-Rp5.000* • [Kas] Minuman\n\n> *1 Transaksi*"
	if got != want {
		t.Errorf("financeListBlock = %q, mau %q", got, want)
	}

	// Tanpa footer (dipakai .transaksi last) & dengan peringatan terpotong.
	got = financeListBlock("5 Transaksi terakhir", []string{"- baris"}, "", true)
	want = "*5 Transaksi terakhir*\n\n- baris\n> Dibatasi 500 baris — masih ada transaksi lain."
	if got != want {
		t.Errorf("financeListBlock(last) = %q, mau %q", got, want)
	}
}

func TestFinanceMonthBounds(t *testing.T) {
	now := time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)
	dari, sampai := financeMonthBounds(now)
	if dari != "2026-11-01" || sampai != "2026-11-30" {
		t.Errorf("financeMonthBounds = %q..%q, mau 2026-11-01..2026-11-30", dari, sampai)
	}
}

func TestFinanceSaldoText(t *testing.T) {
	got := financeSaldoText(store.FinanceSaldo{
		Periode: "2026-11", Kas: 1250000, Rekening: 3400000, Total: 4650000,
		Masuk: 500000, Keluar: 50000, Count: 3,
	})
	want := "*Saldo Keuangan*\n" +
		"• Kas : Rp1.250.000\n" +
		"• Rekening : Rp3.400.000\n" +
		"• Total : Rp4.650.000\n" +
		"\n" +
		"*Bulan Ini (Nov 2026)*\n" +
		"• Masuk : Rp500.000\n" +
		"• Keluar : Rp50.000\n" +
		"• Transaksi : 3"
	if got != want {
		t.Errorf("financeSaldoText = %q, mau %q", got, want)
	}
}

func TestFinancePeriodeLabel(t *testing.T) {
	cases := []struct{ in, want string }{
		{"2026-11", "Nov 2026"},
		{"2026-01", "Jan 2026"},
		{"2026-12", "Dec 2026"},
		{"bukan-periode", "bukan-periode"},
		{"", ""},
	}
	for _, c := range cases {
		if got := financePeriodeLabel(c.in); got != c.want {
			t.Errorf("financePeriodeLabel(%q) = %q, mau %q", c.in, got, c.want)
		}
	}
}

func TestFinanceCountLabel(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{1, "1 Transaksi"},
		{0, "0 Transaksi"},
		{12, "12 Transaksi"},
	}
	for _, c := range cases {
		if got := financeCountLabel(c.in); got != c.want {
			t.Errorf("financeCountLabel(%d) = %q, mau %q", c.in, got, c.want)
		}
	}
}

// TestFinanceTransaksiUsage mengunci bentuk blok bantuan: gaya referensi
// (.videocall) — seksi monospace, baris Format :, blok kode berisi tipe.
func TestFinanceTransaksiUsage(t *testing.T) {
	got := financeTransaksiUsage()
	for _, wajib := range []string{
		"*Transaksi Keuangan*",
		"`Saldo`",
		"Format : .saldo",
		"`Daftar`",
		"Format : .transaksi <type> <nilai>",
		"type : last • date • amount",
		"last   : 1–50",
		"date   : tanggal",
		"amount : nominal",
	} {
		if !strings.Contains(got, wajib) {
			t.Errorf("financeTransaksiUsage tidak memuat %q:\n%s", wajib, got)
		}
	}
	// Blok kode harus dibuka & ditutup (klien WhatsApp butuh pasangan).
	if strings.Count(got, "```") != 2 {
		t.Errorf("blok kode tidak seimbang: %q", got)
	}
}

// TestFinanceTransaksiAlias memastikan alias .trx/.trans terdaftar pada
// command transaksi (tanpa tabrakan — dikunci TestNoCommandAliasCollision).
func TestFinanceTransaksiAlias(t *testing.T) {
	r := NewRegistry()
	RegisterAll(r)
	found, ok := r.Get("transaksi")
	if !ok {
		t.Fatal("command transaksi tidak terdaftar")
	}
	aliases := map[string]bool{}
	for _, a := range found.Aliases {
		aliases[a] = true
	}
	for _, want := range []string{"trx", "trans"} {
		if !aliases[want] {
			t.Errorf("alias %q tidak ada di %v", want, found.Aliases)
		}
	}
	// Alias harus menunjuk handler yang sama dengan command utama.
	for _, a := range []string{"trx", "trans"} {
		alias, ok := r.Get(a)
		if !ok {
			t.Fatalf("alias %q tidak bisa di-resolve", a)
		}
		if alias.Name != found.Name {
			t.Errorf("alias %q menunjuk %q, mau %q", a, alias.Name, found.Name)
		}
	}
}
