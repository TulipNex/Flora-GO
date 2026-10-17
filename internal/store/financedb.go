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
//   created_at (unix), input_at ('YYYY-MM-DD HH:MM:SS' lokal, NULL baris lama).
// Nomor urut display TIDAK disimpan (keputusan owner): enumerasi 1..N
// dibuat saat export Excel. Rekap = SUM langsung (tanpa tabel saldo,
// tanpa cache — SQLite cukup cepat; lihat diskusi Ide 1 vs Ide 2).
//
// finance_periode menyimpan saldo awal per bulan + status laporan: fitur
// reset bulanan (lihat plugins/finance_monthly.go). DB `keuangan` hanya
// menyimpan transaksi bulan berjalan; bulan lampau diarsipkan ke Excel lalu
// dihapus, saldo akhirnya dibawa jadi saldo awal bulan berikutnya.

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"wabot-go/internal/timex"

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
	db, err := openSQLite(path)
	if err != nil {
		return fmt.Errorf("gagal membuka finance.db: %w", err)
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
	CREATE TABLE IF NOT EXISTS finance_periode (
		owner               TEXT NOT NULL,
		periode             TEXT NOT NULL,
		saldo_awal_kas      INTEGER NOT NULL DEFAULT 0,
		saldo_awal_rekening INTEGER NOT NULL DEFAULT 0,
		dilaporkan_at       INTEGER NULL,
		arsip_file          TEXT NULL,
		PRIMARY KEY (owner, periode)
	);
	`
	// input_at: waktu input lokal ('YYYY-MM-DD HH:MM:SS'), NULL untuk baris
	// lama (sebelum fitur ini). Diisi saat INSERT, tak diubah saat edit.
	alters := []string{
		`ALTER TABLE keuangan ADD COLUMN input_at TEXT NULL`,
	}
	if err := applySchema(db, "finance.db", schema, alters, financeSchemaVersion); err != nil {
		return err
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
	InputAt     string // waktu input 'YYYY-MM-DD HH:MM:SS' ("" bila baris lama)
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
	var reqID, inputAt sql.NullString
	if err := rows.Scan(&r.ID, &r.Transaksi, &masuk, &keluar, &r.Sumber, &r.Tanggal, &reqID, &r.CreatedAt, &inputAt); err != nil {
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
	if inputAt.Valid {
		r.InputAt = inputAt.String
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
// input_at diisi waktu input lokal (zona aktif timex) saat insert.
func (f *FinanceDB) InsertKeuangan(ctx context.Context, id, owner, transaksi string, masuk, keluar *int64, sumber, tanggal, clientReqID string) (isDup bool, err error) {
	var reqArg interface{}
	if strings.TrimSpace(clientReqID) != "" {
		reqArg = strings.TrimSpace(clientReqID)
	}
	inputAt := timex.Now().Format("2006-01-02 15:04:05")
	_, err = f.db.ExecContext(ctx, `
		INSERT INTO keuangan (id, owner, transaksi, masuk, keluar, sumber, tanggal, client_req_id, input_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, id, owner, transaksi, nullInt64(masuk), nullInt64(keluar), sumber, tanggal, reqArg, inputAt)
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
		SELECT id, transaksi, masuk, keluar, sumber, tanggal, client_req_id, created_at, input_at
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
	var reqID, inputAt sql.NullString
	err := f.db.QueryRowContext(ctx, `
		SELECT id, transaksi, masuk, keluar, sumber, tanggal, client_req_id, created_at, input_at
		FROM keuangan WHERE owner = ? AND id = ?
	`, owner, id).Scan(&r.ID, &r.Transaksi, &masuk, &keluar, &r.Sumber, &r.Tanggal, &reqID, &r.CreatedAt, &inputAt)
	if isNoRowsError(err) {
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
	if inputAt.Valid {
		r.InputAt = inputAt.String
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

// FinancePeriode adalah saldo awal + status laporan satu bulan owner.
type FinancePeriode struct {
	Owner             string
	Periode           string // 'YYYY-MM'
	SaldoAwalKas      int64
	SaldoAwalRekening int64
	DilaporkanAt      int64  // unix; 0 = belum dilaporkan
	ArsipFile         string // path relatif file arsip ("" = belum ada)
}

// EnsureFinancePeriode memastikan baris periode ada (saldo awal 0 bila baru).
func (f *FinanceDB) EnsureFinancePeriode(ctx context.Context, owner, periode string) error {
	_, err := f.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO finance_periode (owner, periode, saldo_awal_kas, saldo_awal_rekening)
		VALUES (?, ?, 0, 0)
	`, owner, periode)
	return err
}

// GetFinancePeriode mengambil baris periode; found=false bila belum ada.
func (f *FinanceDB) GetFinancePeriode(ctx context.Context, owner, periode string) (FinancePeriode, bool, error) {
	var p FinancePeriode
	var dilaporkan sql.NullInt64
	var arsip sql.NullString
	err := f.db.QueryRowContext(ctx, `
		SELECT owner, periode, saldo_awal_kas, saldo_awal_rekening, dilaporkan_at, arsip_file
		FROM finance_periode WHERE owner = ? AND periode = ?
	`, owner, periode).Scan(&p.Owner, &p.Periode, &p.SaldoAwalKas, &p.SaldoAwalRekening, &dilaporkan, &arsip)
	if isNoRowsError(err) {
		return p, false, nil
	}
	if err != nil {
		return p, false, err
	}
	if dilaporkan.Valid {
		p.DilaporkanAt = dilaporkan.Int64
	}
	if arsip.Valid {
		p.ArsipFile = arsip.String
	}
	return p, true, nil
}

// SetFinancePeriodeAwal menulis saldo awal periode (upsert tanpa menyentuh
// status laporan).
func (f *FinanceDB) SetFinancePeriodeAwal(ctx context.Context, owner, periode string, kas, rekening int64) error {
	_, err := f.db.ExecContext(ctx, `
		INSERT INTO finance_periode (owner, periode, saldo_awal_kas, saldo_awal_rekening)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(owner, periode) DO UPDATE SET
			saldo_awal_kas = excluded.saldo_awal_kas,
			saldo_awal_rekening = excluded.saldo_awal_rekening
	`, owner, periode, kas, rekening)
	return err
}

// MarkFinancePeriodeReported menandai periode sudah dilaporkan + path arsip.
func (f *FinanceDB) MarkFinancePeriodeReported(ctx context.Context, owner, periode string, unix int64, arsipFile string) error {
	_, err := f.db.ExecContext(ctx, `
		UPDATE finance_periode SET dilaporkan_at = ?, arsip_file = ?
		WHERE owner = ? AND periode = ?
	`, unix, arsipFile, owner, periode)
	return err
}

// OpenFinancePeriodes mengembalikan periode < before yang belum dilaporkan
// (untuk catch-up laporan), terurut menaik.
func (f *FinanceDB) OpenFinancePeriodes(ctx context.Context, owner, before string) ([]FinancePeriode, error) {
	rows, err := f.db.QueryContext(ctx, `
		SELECT owner, periode, saldo_awal_kas, saldo_awal_rekening
		FROM finance_periode
		WHERE owner = ? AND periode < ? AND dilaporkan_at IS NULL
		ORDER BY periode ASC
	`, owner, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FinancePeriode
	for rows.Next() {
		var p FinancePeriode
		if err := rows.Scan(&p.Owner, &p.Periode, &p.SaldoAwalKas, &p.SaldoAwalRekening); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListKeuanganRange mengambil SEMUA baris owner pada rentang tanggal
// (inklusif), terurut terlama→terbaru — dipakai laporan bulanan (tanpa
// katup limit 500).
func (f *FinanceDB) ListKeuanganRange(ctx context.Context, owner, dari, sampai string) ([]FinanceRow, error) {
	rows, err := f.db.QueryContext(ctx, `
		SELECT id, transaksi, masuk, keluar, sumber, tanggal, client_req_id, created_at, input_at
		FROM keuangan WHERE owner = ? AND tanggal >= ? AND tanggal <= ?
		ORDER BY tanggal ASC, id ASC
	`, owner, dari, sampai)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FinanceRow
	for rows.Next() {
		r, err := scanFinanceRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteKeuanganPeriode menghapus seluruh baris owner pada rentang tanggal
// (reset bulanan). Mengembalikan jumlah baris terhapus.
func (f *FinanceDB) DeleteKeuanganPeriode(ctx context.Context, owner, dari, sampai string) (int64, error) {
	res, err := f.db.ExecContext(ctx, `DELETE FROM keuangan WHERE owner = ? AND tanggal >= ? AND tanggal <= ?`, owner, dari, sampai)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// HasFinancePeriode true bila owner sudah punya minimal satu baris periode
// (penanda sudah diinisialisasi / sudah pernah "tutup buku").
func (f *FinanceDB) HasFinancePeriode(ctx context.Context, owner string) (bool, error) {
	var n int
	if err := f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM finance_periode WHERE owner = ?`, owner).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// ListAllKeuangan mengambil SEMUA baris owner (terlama→terbaru) — dipakai
// untuk arsip pra-fitur (tanpa katup limit).
func (f *FinanceDB) ListAllKeuangan(ctx context.Context, owner string) ([]FinanceRow, error) {
	rows, err := f.db.QueryContext(ctx, `
		SELECT id, transaksi, masuk, keluar, sumber, tanggal, client_req_id, created_at, input_at
		FROM keuangan WHERE owner = ? ORDER BY tanggal ASC, id ASC
	`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FinanceRow
	for rows.Next() {
		r, err := scanFinanceRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ResetFinanceOwnerInitial menghapus SEMUA transaksi owner lalu membuat baris
// periode berjalan dengan saldo awal — satu transaksi atomik. Dipakai saat
// aktivasi fitur: arsipkan buku lama, mulai ulang dengan saldo awal.
func (f *FinanceDB) ResetFinanceOwnerInitial(ctx context.Context, owner, periode string, kas, rekening int64) error {
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM keuangan WHERE owner = ?`, owner); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO finance_periode (owner, periode, saldo_awal_kas, saldo_awal_rekening)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(owner, periode) DO UPDATE SET
			saldo_awal_kas = excluded.saldo_awal_kas,
			saldo_awal_rekening = excluded.saldo_awal_rekening
	`, owner, periode, kas, rekening); err != nil {
		return err
	}
	return tx.Commit()
}
