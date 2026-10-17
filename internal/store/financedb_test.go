package store

import (
	"context"
	"path/filepath"
	"testing"
)

// TestFinanceInputAtAndRange memverifikasi kolom input_at terisi saat insert
// dan ListKeuanganRange memfilter per rentang tanggal. DB temporer.
func TestFinanceInputAtAndRange(t *testing.T) {
	dir := t.TempDir()
	if err := InitFinanceDB(filepath.Join(dir, "finance.db")); err != nil {
		t.Fatalf("InitFinanceDB: %v", err)
	}
	defer Finance().db.Close()
	ctx := context.Background()
	in := func(n int64) *int64 { return &n }

	if dup, err := Finance().InsertKeuangan(ctx, "ID1", "628", "gaji", in(500), nil, "kas", "2026-01-05", ""); err != nil || dup {
		t.Fatalf("InsertKeuangan: dup=%v err=%v", dup, err)
	}
	row, err := Finance().GetKeuangan(ctx, "628", "ID1")
	if err != nil || row == nil {
		t.Fatalf("GetKeuangan: row=%v err=%v", row, err)
	}
	if len(row.InputAt) != 19 {
		t.Errorf("input_at = %q (harus 'YYYY-MM-DD HH:MM:SS')", row.InputAt)
	}

	jan, err := Finance().ListKeuanganRange(ctx, "628", "2026-01-01", "2026-01-31")
	if err != nil || len(jan) != 1 {
		t.Fatalf("range Januari = %d baris (err=%v), mau 1", len(jan), err)
	}
	feb, err := Finance().ListKeuanganRange(ctx, "628", "2026-02-01", "2026-02-28")
	if err != nil || len(feb) != 0 {
		t.Fatalf("range Februari = %d baris (err=%v), mau 0", len(feb), err)
	}
}

// TestFinancePeriodeLifecycle menguji siklus baris periode: pastikan ada,
// set saldo awal, daftar yang belum dilaporkan, tandai dilaporkan, dan reset.
func TestFinancePeriodeLifecycle(t *testing.T) {
	dir := t.TempDir()
	if err := InitFinanceDB(filepath.Join(dir, "finance.db")); err != nil {
		t.Fatalf("InitFinanceDB: %v", err)
	}
	defer Finance().db.Close()
	ctx := context.Background()
	in := func(n int64) *int64 { return &n }

	if err := Finance().EnsureFinancePeriode(ctx, "628", "2026-01"); err != nil {
		t.Fatalf("EnsureFinancePeriode: %v", err)
	}
	p, found, err := Finance().GetFinancePeriode(ctx, "628", "2026-01")
	if err != nil || !found || p.SaldoAwalKas != 0 || p.SaldoAwalRekening != 0 {
		t.Fatalf("periode baru = %+v found=%v err=%v", p, found, err)
	}

	if err := Finance().SetFinancePeriodeAwal(ctx, "628", "2026-01", 100, 200); err != nil {
		t.Fatalf("SetFinancePeriodeAwal: %v", err)
	}
	if err := Finance().SetFinancePeriodeAwal(ctx, "628", "2026-02", 150, 250); err != nil {
		t.Fatalf("SetFinancePeriodeAwal 2: %v", err)
	}
	p, _, _ = Finance().GetFinancePeriode(ctx, "628", "2026-01")
	if p.SaldoAwalKas != 100 || p.SaldoAwalRekening != 200 {
		t.Errorf("saldo awal = %+v, mau 100/200", p)
	}

	open, err := Finance().OpenFinancePeriodes(ctx, "628", "2026-03")
	if err != nil || len(open) != 2 {
		t.Fatalf("OpenFinancePeriodes = %d (err=%v), mau 2", len(open), err)
	}

	if err := Finance().MarkFinancePeriodeReported(ctx, "628", "2026-01", 111, "arsip.xlsx"); err != nil {
		t.Fatalf("MarkFinancePeriodeReported: %v", err)
	}
	open, _ = Finance().OpenFinancePeriodes(ctx, "628", "2026-03")
	if len(open) != 1 || open[0].Periode != "2026-02" {
		t.Fatalf("setelah ditandai, open = %+v, mau hanya 2026-02", open)
	}

	// Reset: hapus transaksi rentang periode.
	if _, err := Finance().InsertKeuangan(ctx, "ID2", "628", "kopi", nil, in(50), "kas", "2026-01-10", ""); err != nil {
		t.Fatalf("insert: %v", err)
	}
	n, err := Finance().DeleteKeuanganPeriode(ctx, "628", "2026-01-01", "2026-01-31")
	if err != nil || n != 1 {
		t.Fatalf("DeleteKeuanganPeriode = %d (err=%v), mau 1", n, err)
	}
	left, _ := Finance().ListKeuanganRange(ctx, "628", "2026-01-01", "2026-01-31")
	if len(left) != 0 {
		t.Errorf("masih ada %d baris setelah reset", len(left))
	}
}
