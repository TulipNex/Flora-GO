package store

// File ini menyimpan KEUANGAN Flora di SQLite lokal (data/finance.db),
// menggantikan Google Sheet + Apps Script + Drive sepenuhnya (keputusan
// owner: mulai nol, tanpa mirror, tanpa kolom bukti, tanpa nomor tersimpan).
//
// Desain disalin dari pola groupdb.go: file sendiri (backup/retensi
// mandiri), WAL + busy_timeout, CREATE TABLE IF NOT EXISTS, instance
// global lewat Finance(), init sekali di main.go.
//
// Skema (7 kolom logika + teknis):
//   id TEXT PK (UUID stabil dari pemanggil — kunci mutasi, bukan nomor),
//   owner TEXT (isolasi per nomor owner, cermin tab-per-owner Sheet),
//   transaksi, masuk/keluar (INTEGER NULL — NULL berarti sisi kosong),
//   sumber ('kas'/'rekening'), tanggal ('YYYY-MM-DD' teks),
//   client_req_id UNIQUE (anti-ganda; NULL = tanpa proteksi),
//   created_at (unix).
// Nomor urut display TIDAK disimpan (keputusan owner): enumerasi 1..N
// dibuat saat export Excel. Rekap = SUM langsung (tanpa tabel saldo,
// tanpa cache — SQLite cukup cepat; lihat diskusi Ide 1 vs Ide 2).

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

// FinanceDB membungkus koneksi SQLite khusus keuangan Flora.
type FinanceDB struct {
	db *sql.DB
}

// financeDB adalah instance global, diisi sekali lewat InitFinanceDB.
var financeDB *FinanceDB

// InitFinanceDB membuka (atau membuat) data/finance.db beserta tabel +
// indeks. Dipanggil sekali di main.go (setelah InitGroupDB).
func InitFinanceDB(path string) error {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("gagal membuka finance.db: %w", err)
	}
	if err := db.Ping(); err != nil {
		return fmt.Errorf("gagal ping finance.db: %w", err)
	}
	schema := `
	CREATE TABLE IF NOT EXISTS keuangan (
		id            TEXT PRIMARY KEY,
		owner         TEXT NOT NULL,
		transaksi     TEXT NOT NULL,
		masuk         INTEGER NULL,
		keluar        INTEGER NULL,
		sumber        TEXT NOT NULL,
		tanggal       TEXT NOT NULL,
		client_req_id TEXT NULL UNIQUE,
		created_at    INTEGER NOT NULL DEFAULT (strftime('%s','now'))
	);
	CREATE INDEX IF NOT EXISTS idx_keuangan_owner_tanggal ON keuangan(owner, tanggal);
	CREATE INDEX IF NOT EXISTS idx_keuangan_owner_sumber ON keuangan(owner, sumber);
	`
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("gagal siapkan skema finance.db: %w", err)
	}
	financeDB = &FinanceDB{db: db}
	return nil
}

// Finance mengembalikan instance global. Panic bila InitFinanceDB belum
// dipanggil — pola sama seperti Groups()/AI().
func Finance() *FinanceDB {
	if financeDB == nil {
		panic("store.Finance() dipanggil sebelum store.InitFinanceDB() — cek urutan di main.go")
	}
	return financeDB
}

// FinanceRow adalah satu baris transaksi (masuk/keluar nil = sisi kosong).
type FinanceRow struct {
	ID          string
	Transaksi   string
	Masuk       *int64
	Keluar      *int64
	Sumber      string
	Tanggal     string
	ClientReqID string
	CreatedAt   int64
}

// FinanceFilter adalah patokan pencarian multi-kriteria (Ide 1): semua
// terisi digabung AND. Nominal cocok bila SALAH SATU sisi memenuhi
// (masuk untuk pemasukan, keluar untuk pengeluaran).
type FinanceFilter struct {
	Owner       string
	Teks        string // substring transaksi (LIKE %..%)
	Dari        string // YYYY-MM-DD inklusif, kosong = terbuka
	Sampai      string // YYYY-MM-DD inklusif, kosong = terbuka
	Sumber      string // kas/rekening, kosong = terbuka
	Nominal     *int64 // tepat (salah satu sisi)
	MinNominal  *int64 // salah satu sisi >=
	MaksNominal *int64 // salah satu sisi <=
	Limit       int    // katup pengaman (0 = 50)
}

// scanFinanceRow memindai satu baris hasil query ke FinanceRow.
func scanFinanceRow(rows *sql.Rows) (FinanceRow, error) {
	var r FinanceRow
	var masuk, keluar sql.NullInt64
	var reqID sql.NullString
	if err := rows.Scan(&r.ID, &r.Transaksi, &masuk, &keluar, &r.Sumber, &r.Tanggal, &reqID, &r.CreatedAt); err != nil {
		return FinanceRow{}, err
	}
	if masuk.Valid {
		v := masuk.Int64
		r.Masuk = &v
	}
	if keluar.Valid {
		v := keluar.Int64
		r.Keluar = &v
	}
	if reqID.Valid {
		r.ClientReqID = reqID.String
	}
	return r, nil
}

// financeWhere menyusun klausa WHERE + argumen dari filter (tanpa ORDER/LIMIT).
func financeWhere(f FinanceFilter) (string, []interface{}) {
	conds := []string{"owner = ?"}
	args := []interface{}{f.Owner}
	if strings.TrimSpace(f.Teks) != "" {
		conds = append(conds, "transaksi LIKE ?")
		args = append(args, "%"+strings.TrimSpace(f.Teks)+"%")
	}
	if f.Dari != "" {
		conds = append(conds, "tanggal >= ?")
		args = append(args, f.Dari)
	}
	if f.Sampai != "" {
		conds = append(conds, "tanggal <= ?")
		args = append(args, f.Sampai)
	}
	if f.Sumber != "" {
		conds = append(conds, "sumber = ?")
		args = append(args, f.Sumber)
	}
	if f.Nominal != nil {
		conds = append(conds, "(masuk = ? OR keluar = ?)")
		args = append(args, *f.Nominal, *f.Nominal)
	}
	if f.MinNominal != nil {
		conds = append(conds, "(masuk >= ? OR keluar >= ?)")
		args = append(args, *f.MinNominal, *f.MinNominal)
	}
	if f.MaksNominal != nil {
		conds = append(conds, "((masuk IS NOT NULL AND masuk <= ?) OR (keluar IS NOT NULL AND keluar <= ?))")
		args = append(args, *f.MaksNominal, *f.MaksNominal)
	}
	return strings.Join(conds, " AND "), args
}

// InsertKeuangan menyimpan transaksi baru. clientReqID kosong = tanpa
// proteksi ganda. Mengembalikan isDup=true bila req_id sudah ada (bukan
// error) supaya pemanggil bisa lapor "sudah tercatat".
func (f *FinanceDB) InsertKeuangan(ctx context.Context, id, owner, transaksi string, masuk, keluar *int64, sumber, tanggal, clientReqID string) (isDup bool, err error) {
	var reqArg interface{}
	if strings.TrimSpace(clientReqID) != "" {
		reqArg = strings.TrimSpace(clientReqID)
	}
	_, err = f.db.ExecContext(ctx, `
		INSERT INTO keuangan (id, owner, transaksi, masuk, keluar, sumber, tanggal, client_req_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, id, owner, transaksi, nullInt64(masuk), nullInt64(keluar), sumber, tanggal, reqArg)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return true, nil
		}
		return false, err
	}
	return false, nil
}

func nullInt64(v *int64) interface{} {
	if v == nil {
		return nil
	}
	return *v
}

// SearchKeuangan mencari rincian (terbaru dulu). Mengambil limit+1 untuk
// mendeteksi pemotongan: truncated=true + baris ekstra dibuang.
func (f *FinanceDB) SearchKeuangan(ctx context.Context, filter FinanceFilter) (rows []FinanceRow, truncated bool, err error) {
	limit := filter.Limit
	if limit < 1 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	where, args := financeWhere(filter)
	args = append(args, limit+1)
	qrows, err := f.db.QueryContext(ctx, `
		SELECT id, transaksi, masuk, keluar, sumber, tanggal, client_req_id, created_at
		FROM keuangan WHERE `+where+` ORDER BY tanggal DESC, id DESC LIMIT ?
	`, args...)
	if err != nil {
		return nil, false, err
	}
	defer qrows.Close()
	for qrows.Next() {
		r, err := scanFinanceRow(qrows)
		if err != nil {
			return nil, false, err
		}
		rows = append(rows, r)
	}
	if err := qrows.Err(); err != nil {
		return nil, false, err
	}
	if len(rows) > limit {
		return rows[:limit], true, nil
	}
	return rows, false, nil
}

// FinanceRekap adalah hasil agregasi (sumber "semua" = total gabungan).
type FinanceRekap struct {
	Count       int64
	TotalMasuk  int64
	TotalKeluar int64
	Saldo       int64
	PerSumber   map[string]FinanceRekap
}

// RekapKeuangan menghitung agregasi langsung (tanpa batas baris, tanpa
// tabel saldo — keputusan owner). Filter sama seperti Search (tanpa limit).
func (f *FinanceDB) RekapKeuangan(ctx context.Context, filter FinanceFilter) (FinanceRekap, error) {
	where, args := financeWhere(filter)
	out := FinanceRekap{PerSumber: map[string]FinanceRekap{}}
	qrows, err := f.db.QueryContext(ctx, `
		SELECT sumber, COUNT(*), COALESCE(SUM(masuk),0), COALESCE(SUM(keluar),0)
		FROM keuangan WHERE `+where+` GROUP BY sumber
	`, args...)
	if err != nil {
		return out, err
	}
	defer qrows.Close()
	for qrows.Next() {
		var sumber string
		var ps FinanceRekap
		var c, m, k int64
		if err := qrows.Scan(&sumber, &c, &m, &k); err != nil {
			return out, err
		}
		ps.Count, ps.TotalMasuk, ps.TotalKeluar = c, m, k
		ps.Saldo = m - k
		out.PerSumber[sumber] = ps
		out.Count += c
		out.TotalMasuk += m
		out.TotalKeluar += k
	}
	if err := qrows.Err(); err != nil {
		return out, err
	}
	out.Saldo = out.TotalMasuk - out.TotalKeluar
	return out, nil
}

// GetKeuangan mengambil satu baris milik owner (untuk inferensi edit).
func (f *FinanceDB) GetKeuangan(ctx context.Context, owner, id string) (*FinanceRow, error) {
	var r FinanceRow
	var masuk, keluar sql.NullInt64
	var reqID sql.NullString
	err := f.db.QueryRowContext(ctx, `
		SELECT id, transaksi, masuk, keluar, sumber, tanggal, client_req_id, created_at
		FROM keuangan WHERE owner = ? AND id = ?
	`, owner, id).Scan(&r.ID, &r.Transaksi, &masuk, &keluar, &r.Sumber, &r.Tanggal, &reqID, &r.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if masuk.Valid {
		v := masuk.Int64
		r.Masuk = &v
	}
	if keluar.Valid {
		v := keluar.Int64
		r.Keluar = &v
	}
	if reqID.Valid {
		r.ClientReqID = reqID.String
	}
	return &r, nil
}

// UpdateKeuangan mengubah field non-nil/non-kosong; mengembalikan true bila
// baris ada dan berubah. Panggil GetKeuangan dulu untuk inferensi bila perlu.
func (f *FinanceDB) UpdateKeuangan(ctx context.Context, owner, id string, transaksi string, ubahTransaksi bool, masuk, keluar *int64, ubahNominal bool, sumber, tanggal string, ubahSumber, ubahTanggal bool) (bool, error) {
	sets := []string{}
	args := []interface{}{}
	if ubahTransaksi {
		sets = append(sets, "transaksi = ?")
		args = append(args, transaksi)
	}
	if ubahNominal {
		sets = append(sets, "masuk = ?", "keluar = ?")
		args = append(args, nullInt64(masuk), nullInt64(keluar))
	}
	if ubahSumber {
		sets = append(sets, "sumber = ?")
		args = append(args, sumber)
	}
	if ubahTanggal {
		sets = append(sets, "tanggal = ?")
		args = append(args, tanggal)
	}
	if len(sets) == 0 {
		return false, nil
	}
	args = append(args, owner, id)
	res, err := f.db.ExecContext(ctx, `UPDATE keuangan SET `+strings.Join(sets, ", ")+` WHERE owner = ? AND id = ?`, args...)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DeleteKeuangan menghapus baris milik owner; true bila ada yang terhapus.
func (f *FinanceDB) DeleteKeuangan(ctx context.Context, owner, id string) (bool, error) {
	res, err := f.db.ExecContext(ctx, `DELETE FROM keuangan WHERE owner = ? AND id = ?`, owner, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
