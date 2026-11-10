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

// TestFinanceResetOwnerInitial menguji inisialisasi fitur: arsipkan (baca
// semua) lalu mulai ulang dengan saldo awal.
func TestFinanceResetOwnerInitial(t *testing.T) {
	dir := t.TempDir()
	if err := InitFinanceDB(filepath.Join(dir, "finance.db")); err != nil {
		t.Fatalf("InitFinanceDB: %v", err)
	}
	defer Finance().db.Close()
	ctx := context.Background()
	in := func(n int64) *int64 { return &n }

	if _, err := Finance().InsertKeuangan(ctx, "A", "628", "gaji", in(5000), nil, "kas", "2026-01-01", ""); err != nil {
		t.Fatalf("insert A: %v", err)
	}
	if _, err := Finance().InsertKeuangan(ctx, "B", "628", "sewa", nil, in(2000), "rekening", "2026-02-01", ""); err != nil {
		t.Fatalf("insert B: %v", err)
	}

	has, err := Finance().HasFinancePeriode(ctx, "628")
	if err != nil || has {
		t.Fatalf("HasFinancePeriode awal = %v (err=%v), mau false", has, err)
	}
	all, err := Finance().ListAllKeuangan(ctx, "628")
	if err != nil || len(all) != 2 {
		t.Fatalf("ListAllKeuangan = %d (err=%v), mau 2", len(all), err)
	}

	if err := Finance().ResetFinanceOwnerInitial(ctx, "628", "2026-10", 3000, -2000); err != nil {
		t.Fatalf("ResetFinanceOwnerInitial: %v", err)
	}
	has, _ = Finance().HasFinancePeriode(ctx, "628")
	if !has {
		t.Error("HasFinancePeriode setelah reset harus true")
	}
	if all, _ = Finance().ListAllKeuangan(ctx, "628"); len(all) != 0 {
		t.Errorf("transaksi lama masih ada: %d", len(all))
	}
	p, found, _ := Finance().GetFinancePeriode(ctx, "628", "2026-10")
	if !found || p.SaldoAwalKas != 3000 || p.SaldoAwalRekening != -2000 {
		t.Errorf("periode setelah reset = %+v found=%v", p, found)
	}
}

// TestFinanceRunningBalances memverifikasi saldo_kas dan saldo_rekening
// terhitung otomatis saat insert, edit, delete, dan migrasi.
func TestFinanceRunningBalances(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "finance.db")
	if err := InitFinanceDB(dbPath); err != nil {
		t.Fatalf("InitFinanceDB: %v", err)
	}
	defer Finance().db.Close()
	ctx := context.Background()
	in := func(n int64) *int64 { return &n }

	// Set saldo awal periode 2026-03: kas=1000, rekening=2000
	if err := Finance().SetFinancePeriodeAwal(ctx, "628", "2026-03", 1000, 2000); err != nil {
		t.Fatalf("SetFinancePeriodeAwal: %v", err)
	}

	// Insert 1: kas masuk 500
	if _, err := Finance().InsertKeuangan(ctx, "TX1", "628", "jual barang", in(500), nil, "kas", "2026-03-01", ""); err != nil {
		t.Fatalf("insert 1: %v", err)
	}
	// Insert 2: rekening keluar 300
	if _, err := Finance().InsertKeuangan(ctx, "TX2", "628", "beli pulsa", nil, in(300), "rekening", "2026-03-02", ""); err != nil {
		t.Fatalf("insert 2: %v", err)
	}

	row1, err := Finance().GetKeuangan(ctx, "628", "TX1")
	if err != nil || row1 == nil || row1.SaldoKas == nil || *row1.SaldoKas != 1500 || row1.SaldoRekening == nil || *row1.SaldoRekening != 2000 || row1.Saldo == nil || *row1.Saldo != 3500 {
		t.Fatalf("TX1 saldo tidak cocok: kas=%v rek=%v saldo=%v", row1.SaldoKas, row1.SaldoRekening, row1.Saldo)
	}

	row2, err := Finance().GetKeuangan(ctx, "628", "TX2")
	if err != nil || row2 == nil || row2.SaldoKas == nil || *row2.SaldoKas != 1500 || row2.SaldoRekening == nil || *row2.SaldoRekening != 1700 || row2.Saldo == nil || *row2.Saldo != 3200 {
		t.Fatalf("TX2 saldo tidak cocok: kas=%v rek=%v saldo=%v", row2.SaldoKas, row2.SaldoRekening, row2.Saldo)
	}

	// Edit TX1: nominal kas masuk jadi 800 (saldo kas harus jadi 1800, total saldo jadi 3500)
	if ok, err := Finance().UpdateKeuangan(ctx, "628", "TX1", "", false, in(800), nil, true, "", "", false, false); err != nil || !ok {
		t.Fatalf("update TX1: ok=%v err=%v", ok, err)
	}

	row2After, _ := Finance().GetKeuangan(ctx, "628", "TX2")
	if row2After == nil || row2After.SaldoKas == nil || *row2After.SaldoKas != 1800 || *row2After.SaldoRekening != 1700 || row2After.Saldo == nil || *row2After.Saldo != 3500 {
		t.Fatalf("TX2 saldo setelah edit TX1: kas=%v rek=%v saldo=%v, mau 1800/1700/3500", row2After.SaldoKas, row2After.SaldoRekening, row2After.Saldo)
	}

	// Delete TX1: saldo awal 1000 + (TX2 rek keluar 300) -> kas 1000, rek 1700, total saldo 2700
	if ok, err := Finance().DeleteKeuangan(ctx, "628", "TX1"); err != nil || !ok {
		t.Fatalf("delete TX1: ok=%v err=%v", ok, err)
	}
	row2AfterDel, _ := Finance().GetKeuangan(ctx, "628", "TX2")
	if row2AfterDel == nil || *row2AfterDel.SaldoKas != 1000 || *row2AfterDel.SaldoRekening != 1700 || row2AfterDel.Saldo == nil || *row2AfterDel.Saldo != 2700 {
		t.Fatalf("TX2 saldo setelah delete TX1: kas=%v rek=%v saldo=%v, mau 1000/1700/2700", row2AfterDel.SaldoKas, row2AfterDel.SaldoRekening, row2AfterDel.Saldo)
	}
}

// TestFinanceMigrationExistingData menguji skenario di mana database sudah ada data
// dari skema lama (v2), lalu dibuka kembali dan migrasi ke v3 otomatis menghitung
// seluruh running balance data lama tersebut.
func TestFinanceMigrationExistingData(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "finance.db")

	// 1. Buat DB v2 secara manual
	db, err := openSQLite(dbPath)
	if err != nil {
		t.Fatalf("openSQLite: %v", err)
	}
	v2Schema := `
	CREATE TABLE IF NOT EXISTS keuangan (
		id            TEXT PRIMARY KEY,
		owner         TEXT NOT NULL,
		transaksi     TEXT NOT NULL,
		masuk         INTEGER NULL,
		keluar        INTEGER NULL,
		sumber        TEXT NOT NULL,
		tanggal       TEXT NOT NULL,
		client_req_id TEXT NULL UNIQUE,
		created_at    INTEGER NOT NULL DEFAULT (strftime('%s','now')),
		input_at      TEXT NULL
	);
	CREATE TABLE IF NOT EXISTS finance_periode (
		owner               TEXT NOT NULL,
		periode             TEXT NOT NULL,
		saldo_awal_kas      INTEGER NOT NULL DEFAULT 0,
		saldo_awal_rekening INTEGER NOT NULL DEFAULT 0,
		dilaporkan_at       INTEGER NULL,
		arsip_file          TEXT NULL,
		PRIMARY KEY (owner, periode)
	);
	PRAGMA user_version = 2;
	`
	if _, err := db.Exec(v2Schema); err != nil {
		db.Close()
		t.Fatalf("setup v2: %v", err)
	}
	// Insert saldo awal periode 2026-03: kas=5000, rekening=10000
	if _, err := db.Exec(`INSERT INTO finance_periode (owner, periode, saldo_awal_kas, saldo_awal_rekening) VALUES ('628', '2026-03', 5000, 10000)`); err != nil {
		db.Close()
		t.Fatalf("insert periode: %v", err)
	}
	// Insert transaksi v2 (tanpa kolom saldo_kas & saldo_rekening)
	if _, err := db.Exec(`INSERT INTO keuangan (id, owner, transaksi, masuk, keluar, sumber, tanggal, input_at) VALUES ('T1', '628', 'makan', NULL, 2000, 'kas', '2026-03-01', '2026-03-01 10:00:00')`); err != nil {
		db.Close()
		t.Fatalf("insert T1: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO keuangan (id, owner, transaksi, masuk, keluar, sumber, tanggal, input_at) VALUES ('T2', '628', 'transfer', 5000, NULL, 'rekening', '2026-03-02', '2026-03-02 11:00:00')`); err != nil {
		db.Close()
		t.Fatalf("insert T2: %v", err)
	}
	db.Close()

	// 2. Sekarang buka melalui InitFinanceDB (migrasi v2 -> v3)
	if err := InitFinanceDB(dbPath); err != nil {
		t.Fatalf("InitFinanceDB migrasi: %v", err)
	}
	defer Finance().db.Close()
	ctx := context.Background()

	// 3. Verifikasi T1 dan T2 sudah memiliki saldo terhitung
	t1, err := Finance().GetKeuangan(ctx, "628", "T1")
	if err != nil || t1 == nil {
		t.Fatalf("GetKeuangan T1: %v", err)
	}
	if t1.SaldoKas == nil || *t1.SaldoKas != 3000 || t1.SaldoRekening == nil || *t1.SaldoRekening != 10000 || t1.Saldo == nil || *t1.Saldo != 13000 {
		t.Fatalf("T1 saldo salah setelah migrasi: kas=%v rek=%v saldo=%v (mau 3000/10000/13000)", t1.SaldoKas, t1.SaldoRekening, t1.Saldo)
	}

	t2, err := Finance().GetKeuangan(ctx, "628", "T2")
	if err != nil || t2 == nil {
		t.Fatalf("GetKeuangan T2: %v", err)
	}
	if t2.SaldoKas == nil || *t2.SaldoKas != 3000 || t2.SaldoRekening == nil || *t2.SaldoRekening != 15000 || t2.Saldo == nil || *t2.Saldo != 18000 {
		t.Fatalf("T2 saldo salah setelah migrasi: kas=%v rek=%v saldo=%v (mau 3000/15000/18000)", t2.SaldoKas, t2.SaldoRekening, t2.Saldo)
	}
}

// TestFinanceTableColumnOrder memverifikasi urutan kolom fisik tabel keuangan
// sesuai yang diminta: id, tanggal, transaksi, sumber, masuk, keluar, saldo_kas, saldo_rekening, saldo, owner, input_at, created_at, client_req_id.
func TestFinanceTableColumnOrder(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "finance.db")

	if err := InitFinanceDB(dbPath); err != nil {
		t.Fatalf("InitFinanceDB: %v", err)
	}
	defer Finance().db.Close()

	expectedCols := []string{
		"id", "tanggal", "transaksi", "sumber", "masuk", "keluar",
		"saldo_kas", "saldo_rekening", "saldo", "owner", "input_at",
		"created_at", "client_req_id",
	}

	rows, err := Finance().db.Query(`PRAGMA table_info(keuangan)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info: %v", err)
	}
	defer rows.Close()

	var actualCols []string
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt interface{}
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		actualCols = append(actualCols, name)
	}

	if len(actualCols) != len(expectedCols) {
		t.Fatalf("panjang kolom = %d, mau %d\nactual: %v\nexpected: %v", len(actualCols), len(expectedCols), actualCols, expectedCols)
	}

	for i, exp := range expectedCols {
		if actualCols[i] != exp {
			t.Errorf("kolom index %d = %q, mau %q", i, actualCols[i], exp)
		}
	}

	// Uji migrasi dari DB skema v4 (urutan kolom lama) dengan data yang sudah ada
	dir2 := t.TempDir()
	dbPath2 := filepath.Join(dir2, "finance_v4.db")
	db2, err := openSQLite(dbPath2)
	if err != nil {
		t.Fatalf("openSQLite v4: %v", err)
	}
	v4OldSchema := `
	CREATE TABLE keuangan (
		id            TEXT PRIMARY KEY,
		owner         TEXT NOT NULL,
		transaksi     TEXT NOT NULL,
		masuk         INTEGER NULL,
		keluar        INTEGER NULL,
		sumber        TEXT NOT NULL,
		tanggal       TEXT NOT NULL,
		client_req_id TEXT NULL UNIQUE,
		created_at    INTEGER NOT NULL DEFAULT (strftime('%s','now')),
		input_at      TEXT NULL,
		saldo_kas     INTEGER NULL,
		saldo_rekening INTEGER NULL,
		saldo         INTEGER NULL
	);
	PRAGMA user_version = 4;
	`
	if _, err := db2.Exec(v4OldSchema); err != nil {
		db2.Close()
		t.Fatalf("setup v4: %v", err)
	}
	if _, err := db2.Exec(`INSERT INTO keuangan (id, owner, transaksi, masuk, keluar, sumber, tanggal, input_at, saldo_kas, saldo_rekening, saldo)
		VALUES ('K1', '628', 'belanja', NULL, 15000, 'kas', '2026-03-05', '2026-03-05 14:00:00', -15000, 0, -15000)`); err != nil {
		db2.Close()
		t.Fatalf("insert v4 data: %v", err)
	}
	db2.Close()

	// Buka dengan InitFinanceDB
	if err := InitFinanceDB(dbPath2); err != nil {
		t.Fatalf("InitFinanceDB on v4: %v", err)
	}
	defer Finance().db.Close()

	// 1. Verifikasi urutan kolom setelah migrasi
	rows2, err := Finance().db.Query(`PRAGMA table_info(keuangan)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info v4 migrated: %v", err)
	}
	defer rows2.Close()
	var actualCols2 []string
	for rows2.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt interface{}
		if err := rows2.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("Scan v4 migrated: %v", err)
		}
		actualCols2 = append(actualCols2, name)
	}
	for i, exp := range expectedCols {
		if actualCols2[i] != exp {
			t.Errorf("v4 migrated kolom index %d = %q, mau %q", i, actualCols2[i], exp)
		}
	}

	// 2. Verifikasi data tetap utuh
	ctx := context.Background()
	rowK1, err := Finance().GetKeuangan(ctx, "628", "K1")
	if err != nil || rowK1 == nil {
		t.Fatalf("GetKeuangan K1: %v", err)
	}
	if rowK1.Transaksi != "belanja" || rowK1.Sumber != "kas" || rowK1.Tanggal != "2026-03-05" || rowK1.Keluar == nil || *rowK1.Keluar != 15000 {
		t.Errorf("data K1 rusak setelah reorganisasi kolom: %+v", rowK1)
	}
}

// TestFinanceInsertOutOfOrderPhysicalSorting memverifikasi transaksi yang diinput susulan
// (misal tanggal 8 diinput duluan, lalu tanggal 6 disusulkan) otomatis tersusun
// berdekatan dengan tanggal yang sama secara fisik di database (bahkan pada SELECT tanpa ORDER BY).
func TestFinanceInsertOutOfOrderPhysicalSorting(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "finance.db")
	if err := InitFinanceDB(dbPath); err != nil {
		t.Fatalf("InitFinanceDB: %v", err)
	}
	defer Finance().db.Close()
	ctx := context.Background()
	in := func(n int64) *int64 { return &n }

	// 1. Input transaksi tanggal 06 pertama
	if _, err := Finance().InsertKeuangan(ctx, "T_06_A", "628", "makan pagi tgl 6", nil, in(10000), "kas", "2026-03-06", ""); err != nil {
		t.Fatalf("insert T_06_A: %v", err)
	}

	// 2. Input transaksi tanggal 08
	if _, err := Finance().InsertKeuangan(ctx, "T_08", "628", "bensin tgl 8", nil, in(50000), "kas", "2026-03-08", ""); err != nil {
		t.Fatalf("insert T_08: %v", err)
	}

	// 3. User lupa dan menyusulkan transaksi tanggal 06 kedua (diinput setelah tanggal 08)
	if _, err := Finance().InsertKeuangan(ctx, "T_06_B", "628", "kopi sore tgl 6", nil, in(15000), "kas", "2026-03-06", ""); err != nil {
		t.Fatalf("insert T_06_B: %v", err)
	}

	// 4. Verifikasi urutan fisik baris dalam SQLite mentah (SELECT tanpa ORDER BY)
	rows, err := Finance().db.Query(`SELECT id, tanggal FROM keuangan`)
	if err != nil {
		t.Fatalf("query raw: %v", err)
	}
	defer rows.Close()

	var actualIDs []string
	for rows.Next() {
		var id, tgl string
		if err := rows.Scan(&id, &tgl); err != nil {
			t.Fatalf("scan raw: %v", err)
		}
		actualIDs = append(actualIDs, id)
	}

	// Harapannya: T_06_A dan T_06_B berurutan / berdampingan, sebelum T_08
	expectedIDs := []string{"T_06_A", "T_06_B", "T_08"}
	if len(actualIDs) != len(expectedIDs) {
		t.Fatalf("jumlah baris fisik = %d, mau %d (actual: %v)", len(actualIDs), len(expectedIDs), actualIDs)
	}
	for i, exp := range expectedIDs {
		if actualIDs[i] != exp {
			t.Errorf("baris fisik index %d = %q, mau %q (urutan: %v)", i, actualIDs[i], exp, actualIDs)
		}
	}

	// 5. Verifikasi saldo berjalan konsisten secara kronologis
	t06A, _ := Finance().GetKeuangan(ctx, "628", "T_06_A")
	t06B, _ := Finance().GetKeuangan(ctx, "628", "T_06_B")
	t08, _ := Finance().GetKeuangan(ctx, "628", "T_08")

	if *t06A.SaldoKas != -10000 {
		t.Errorf("saldo T_06_A = %d, mau -10000", *t06A.SaldoKas)
	}
	if *t06B.SaldoKas != -25000 {
		t.Errorf("saldo T_06_B = %d, mau -25000", *t06B.SaldoKas)
	}
	if *t08.SaldoKas != -75000 {
		t.Errorf("saldo T_08 = %d, mau -75000", *t08.SaldoKas)
	}
}
