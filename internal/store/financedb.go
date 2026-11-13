package store

// File ini menyimpan KEUANGAN Flora di SQLite lokal (data/finance.db),
// menggantikan Google Sheet + Apps Script + Drive sepenuhnya (keputusan
// owner: mulai nol, tanpa mirror, tanpa kolom bukti, tanpa nomor tersimpan).
//
// Desain disalin dari pola groupdb.go: file sendiri (backup/retensi
// mandiri), WAL + busy_timeout, CREATE TABLE IF NOT EXISTS, instance
// global lewat Finance(), init sekali di main.go.
//
// Skema:
//   id TEXT PK (UUID stabil dari pemanggil — kunci mutasi, bukan nomor),
//   tanggal ('YYYY-MM-DD' teks), transaksi, sumber ('kas'/'rekening'),
//   masuk/keluar (INTEGER NULL — NULL berarti sisi kosong),
//   saldo_kas, saldo_rekening, saldo (running balance per transaksi),
//   owner TEXT (isolasi per nomor owner, cermin tab-per-owner Sheet),
//   input_at ('YYYY-MM-DD HH:MM:SS' lokal, NULL baris lama),
//   created_at (unix), client_req_id UNIQUE (anti-ganda; NULL = tanpa proteksi).
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
	"time"

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
		tanggal       TEXT NOT NULL,
		transaksi     TEXT NOT NULL,
		sumber        TEXT NOT NULL,
		masuk         INTEGER NULL,
		keluar        INTEGER NULL,
		saldo_kas     INTEGER NULL,
		saldo_rekening INTEGER NULL,
		saldo         INTEGER NULL,
		owner         TEXT NOT NULL,
		input_at      TEXT NULL,
		created_at    INTEGER NOT NULL DEFAULT (strftime('%s','now')),
		client_req_id TEXT NULL UNIQUE
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
	// saldo_kas & saldo_rekening & saldo: running balance per transaksi.
	alters := []string{
		`ALTER TABLE keuangan ADD COLUMN input_at TEXT NULL`,
		`ALTER TABLE keuangan ADD COLUMN saldo_kas INTEGER NULL`,
		`ALTER TABLE keuangan ADD COLUMN saldo_rekening INTEGER NULL`,
		`ALTER TABLE keuangan ADD COLUMN saldo INTEGER NULL`,
	}
	if err := applySchema(db, "finance.db", schema, alters, financeSchemaVersion); err != nil {
		return err
	}
	if err := reorderKeuanganTable(db); err != nil {
		return fmt.Errorf("reorder tabel keuangan: %w", err)
	}
	f := &FinanceDB{db: db}
	if err := f.recalculateAllOwners(context.Background()); err != nil {
		return fmt.Errorf("recalculate saldo awal finance: %w", err)
	}
	financeDB = f
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
	ID            string
	Transaksi     string
	Masuk         *int64
	Keluar        *int64
	Sumber        string
	Tanggal       string
	InputAt       string // waktu input 'YYYY-MM-DD HH:MM:SS' ("" bila baris lama)
	SaldoKas      *int64 // saldo kas berjalan setelah mutasi
	SaldoRekening *int64 // saldo rekening berjalan setelah mutasi
	Saldo         *int64 // saldo total (kas + rekening) berjalan setelah mutasi
	ClientReqID   string
	CreatedAt     int64
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
	var masuk, keluar, sKas, sRek, sTot sql.NullInt64
	var reqID, inputAt sql.NullString
	if err := rows.Scan(&r.ID, &r.Transaksi, &masuk, &keluar, &r.Sumber, &r.Tanggal, &reqID, &r.CreatedAt, &inputAt, &sKas, &sRek, &sTot); err != nil {
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
	if sKas.Valid {
		v := sKas.Int64
		r.SaldoKas = &v
	}
	if sRek.Valid {
		v := sRek.Int64
		r.SaldoRekening = &v
	}
	if sTot.Valid {
		v := sTot.Int64
		r.Saldo = &v
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
// Setelah insert, saldo berjalan dihitung ulang agar konsisten.
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
	if rerr := f.RecalculateRunningBalances(ctx, owner); rerr != nil {
		return false, fmt.Errorf("gagal hitung ulang saldo: %w", rerr)
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
		SELECT id, transaksi, masuk, keluar, sumber, tanggal, client_req_id, created_at, input_at, saldo_kas, saldo_rekening, saldo
		FROM keuangan WHERE `+where+` ORDER BY tanggal DESC, COALESCE(input_at, '') DESC, id DESC LIMIT ?
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

// FinanceSaldo adalah saldo BERJALAN seorang owner: saldo awal periode
// berjalan ditambah mutasi bulan berjalan (masuk − keluar), per sumber dana.
// Tidak ada tabel/cache saldo — angka ini selalu dihitung dari `keuangan` +
// `finance_periode`, jadi tidak mungkin basi karena mutasi yang terlewat.
type FinanceSaldo struct {
	Owner        string
	Periode      string // 'YYYY-MM'
	AwalKas      int64  // saldo awal bulan (dibawa dari bulan lalu)
	AwalRekening int64
	Kas          int64 // saldo akhir kas = AwalKas + masuk kas − keluar kas
	Rekening     int64
	Total        int64 // Kas + Rekening
	Masuk        int64 // total masuk bulan berjalan (kas + rekening)
	Keluar       int64 // total keluar bulan berjalan
	Count        int64 // jumlah transaksi bulan berjalan
}

// SaldoBerjalan menghitung saldo bulan berjalan memakai zona aktif timex.
// Satu pintu hitung untuk command owner (.saldo), tool AI (cek_saldo), dan
// blok system prompt — pemanggil TIDAK boleh menghitung ulang sendiri.
func (f *FinanceDB) SaldoBerjalan(ctx context.Context, owner string) (FinanceSaldo, error) {
	now := timex.Now()
	out := FinanceSaldo{Owner: owner, Periode: now.Format("2006-01")}

	p, found, err := f.GetFinancePeriode(ctx, owner, out.Periode)
	if err != nil {
		return out, err
	}
	if found {
		out.AwalKas, out.AwalRekening = p.SaldoAwalKas, p.SaldoAwalRekening
	}

	// Batasi ke bulan berjalan supaya baris sisa bulan lain (kalau ada) tidak
	// ikut terhitung ke saldo yang saldo awalnya hanya milik bulan ini.
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	last := first.AddDate(0, 1, -1)
	r, err := f.RekapKeuangan(ctx, FinanceFilter{
		Owner:  owner,
		Dari:   first.Format("2006-01-02"),
		Sampai: last.Format("2006-01-02"),
	})
	if err != nil {
		return out, err
	}

	out.Count, out.Masuk, out.Keluar = r.Count, r.TotalMasuk, r.TotalKeluar
	out.Kas = out.AwalKas + r.PerSumber["kas"].Saldo
	out.Rekening = out.AwalRekening + r.PerSumber["rekening"].Saldo
	out.Total = out.Kas + out.Rekening
	return out, nil
}

// GetKeuangan mengambil satu baris milik owner (untuk inferensi edit).
func (f *FinanceDB) GetKeuangan(ctx context.Context, owner, id string) (*FinanceRow, error) {
	var r FinanceRow
	var masuk, keluar, sKas, sRek, sTot sql.NullInt64
	var reqID, inputAt sql.NullString
	err := f.db.QueryRowContext(ctx, `
		SELECT id, transaksi, masuk, keluar, sumber, tanggal, client_req_id, created_at, input_at, saldo_kas, saldo_rekening, saldo
		FROM keuangan WHERE owner = ? AND id = ?
	`, owner, id).Scan(&r.ID, &r.Transaksi, &masuk, &keluar, &r.Sumber, &r.Tanggal, &reqID, &r.CreatedAt, &inputAt, &sKas, &sRek, &sTot)
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
	if sKas.Valid {
		v := sKas.Int64
		r.SaldoKas = &v
	}
	if sRek.Valid {
		v := sRek.Int64
		r.SaldoRekening = &v
	}
	if sTot.Valid {
		v := sTot.Int64
		r.Saldo = &v
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
	if n > 0 {
		_ = f.RecalculateRunningBalances(ctx, owner)
	}
	return n > 0, nil
}

// DeleteKeuangan menghapus baris milik owner; true bila ada yang terhapus.
func (f *FinanceDB) DeleteKeuangan(ctx context.Context, owner, id string) (bool, error) {
	res, err := f.db.ExecContext(ctx, `DELETE FROM keuangan WHERE owner = ? AND id = ?`, owner, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		_ = f.RecalculateRunningBalances(ctx, owner)
	}
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
// status laporan), lalu menyinkronkan running balance transaksi jika ada.
func (f *FinanceDB) SetFinancePeriodeAwal(ctx context.Context, owner, periode string, kas, rekening int64) error {
	_, err := f.db.ExecContext(ctx, `
		INSERT INTO finance_periode (owner, periode, saldo_awal_kas, saldo_awal_rekening)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(owner, periode) DO UPDATE SET
			saldo_awal_kas = excluded.saldo_awal_kas,
			saldo_awal_rekening = excluded.saldo_awal_rekening
	`, owner, periode, kas, rekening)
	if err != nil {
		return err
	}
	_ = f.RecalculateRunningBalances(ctx, owner)
	return nil
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
		SELECT id, transaksi, masuk, keluar, sumber, tanggal, client_req_id, created_at, input_at, saldo_kas, saldo_rekening, saldo
		FROM keuangan WHERE owner = ? AND tanggal >= ? AND tanggal <= ?
		ORDER BY tanggal ASC, COALESCE(input_at, '') ASC, id ASC
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
		SELECT id, transaksi, masuk, keluar, sumber, tanggal, client_req_id, created_at, input_at, saldo_kas, saldo_rekening, saldo
		FROM keuangan WHERE owner = ? ORDER BY tanggal ASC, COALESCE(input_at, '') ASC, id ASC
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

// RecalculateRunningBalances menghitung ulang seluruh saldo_kas dan
// saldo_rekening untuk seorang owner secara kronologis (tanggal ASC, input_at ASC, id ASC).
// Memperhitungkan saldo awal periode terawal yang relevan jika ada.
func (f *FinanceDB) RecalculateRunningBalances(ctx context.Context, owner string) error {
	type rowMutasi struct {
		id      string
		tanggal string
		sumber  string
		masuk   int64
		keluar  int64
	}

	rows, err := f.db.QueryContext(ctx, `
		SELECT id, tanggal, sumber, COALESCE(masuk, 0), COALESCE(keluar, 0)
		FROM keuangan
		WHERE owner = ?
		ORDER BY tanggal ASC, COALESCE(input_at, '') ASC, id ASC
	`, owner)
	if err != nil {
		return err
	}
	defer rows.Close()

	var list []rowMutasi
	earliestPeriode := ""
	for rows.Next() {
		var rm rowMutasi
		if err := rows.Scan(&rm.id, &rm.tanggal, &rm.sumber, &rm.masuk, &rm.keluar); err != nil {
			return err
		}
		list = append(list, rm)
		if len(rm.tanggal) >= 7 && (earliestPeriode == "" || rm.tanggal[:7] < earliestPeriode) {
			earliestPeriode = rm.tanggal[:7]
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(list) == 0 {
		return nil
	}

	var saldoKas, saldoRekening int64
	if earliestPeriode != "" {
		var sKas, sRek sql.NullInt64
		err := f.db.QueryRowContext(ctx, `
			SELECT saldo_awal_kas, saldo_awal_rekening
			FROM finance_periode
			WHERE owner = ? AND periode = ?
		`, owner, earliestPeriode).Scan(&sKas, &sRek)
		if err == nil {
			if sKas.Valid {
				saldoKas = sKas.Int64
			}
			if sRek.Valid {
				saldoRekening = sRek.Int64
			}
		}
	}

	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `UPDATE keuangan SET saldo_kas = ?, saldo_rekening = ?, saldo = ? WHERE id = ?`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, m := range list {
		if m.sumber == "rekening" {
			saldoRekening += m.masuk - m.keluar
		} else {
			saldoKas += m.masuk - m.keluar
		}
		totalSaldo := saldoKas + saldoRekening
		if _, err := stmt.ExecContext(ctx, saldoKas, saldoRekening, totalSaldo, m.id); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	return f.reorderPhysicalRows(ctx)
}

func (f *FinanceDB) recalculateAllOwners(ctx context.Context) error {
	rows, err := f.db.QueryContext(ctx, `SELECT DISTINCT owner FROM keuangan`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var owners []string
	for rows.Next() {
		var o string
		if err := rows.Scan(&o); err != nil {
			return err
		}
		owners = append(owners, o)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, o := range owners {
		if err := f.RecalculateRunningBalances(ctx, o); err != nil {
			return fmt.Errorf("owner %s: %w", o, err)
		}
	}
	return nil
}

// reorderKeuanganTable memastikan urutan kolom fisik tabel keuangan sesuai
// dengan struktur yang ditentukan:
// id, tanggal, transaksi, sumber, masuk, keluar, saldo_kas, saldo_rekening, saldo, owner, input_at, created_at, client_req_id
func reorderKeuanganTable(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(keuangan)`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var currentCols []string
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		currentCols = append(currentCols, name)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(currentCols) == 0 {
		return nil
	}

	targetCols := []string{
		"id", "tanggal", "transaksi", "sumber", "masuk", "keluar",
		"saldo_kas", "saldo_rekening", "saldo", "owner", "input_at",
		"created_at", "client_req_id",
	}

	if len(currentCols) == len(targetCols) {
		match := true
		for i, c := range targetCols {
			if currentCols[i] != c {
				match = false
				break
			}
		}
		if match {
			return nil
		}
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	createSQL := `
	CREATE TABLE keuangan_new (
		id            TEXT PRIMARY KEY,
		tanggal       TEXT NOT NULL,
		transaksi     TEXT NOT NULL,
		sumber        TEXT NOT NULL,
		masuk         INTEGER NULL,
		keluar        INTEGER NULL,
		saldo_kas     INTEGER NULL,
		saldo_rekening INTEGER NULL,
		saldo         INTEGER NULL,
		owner         TEXT NOT NULL,
		input_at      TEXT NULL,
		created_at    INTEGER NOT NULL DEFAULT (strftime('%s','now')),
		client_req_id TEXT NULL UNIQUE
	);`
	if _, err := tx.Exec(createSQL); err != nil {
		return fmt.Errorf("create keuangan_new: %w", err)
	}

	copySQL := `
	INSERT INTO keuangan_new (id, tanggal, transaksi, sumber, masuk, keluar, saldo_kas, saldo_rekening, saldo, owner, input_at, created_at, client_req_id)
	SELECT id, tanggal, transaksi, sumber, masuk, keluar, saldo_kas, saldo_rekening, saldo, owner, input_at, created_at, client_req_id
	FROM keuangan;`
	if _, err := tx.Exec(copySQL); err != nil {
		return fmt.Errorf("copy data to keuangan_new: %w", err)
	}

	if _, err := tx.Exec(`DROP TABLE keuangan`); err != nil {
		return fmt.Errorf("drop table keuangan: %w", err)
	}

	if _, err := tx.Exec(`ALTER TABLE keuangan_new RENAME TO keuangan`); err != nil {
		return fmt.Errorf("rename keuangan_new: %w", err)
	}

	indexSQL := `
	CREATE INDEX IF NOT EXISTS idx_keuangan_owner_tanggal ON keuangan(owner, tanggal);
	CREATE INDEX IF NOT EXISTS idx_keuangan_owner_sumber ON keuangan(owner, sumber);`
	if _, err := tx.Exec(indexSQL); err != nil {
		return fmt.Errorf("recreate indices: %w", err)
	}

	return tx.Commit()
}

// reorderPhysicalRows menyusun ulang baris fisik tabel keuangan agar berurutan
// kronologis (owner ASC, tanggal ASC, input_at ASC, id ASC).
// Dengan demikian, transaksi yang diinput susulan (mis. tanggal 6 diinput di tanggal 8)
// otomatis tertulis berdekatan dengan tanggal 6 lainnya, baik di database maupun
// saat diekspor ke Excel (.backup / export).
func (f *FinanceDB) reorderPhysicalRows(ctx context.Context) error {
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS keuangan_sorted`); err != nil {
		return fmt.Errorf("drop temp keuangan_sorted: %w", err)
	}

	createTmp := `
	CREATE TEMP TABLE keuangan_sorted AS
	SELECT id, tanggal, transaksi, sumber, masuk, keluar, saldo_kas, saldo_rekening, saldo, owner, input_at, created_at, client_req_id
	FROM keuangan
	ORDER BY owner ASC, tanggal ASC, COALESCE(input_at, '') ASC, id ASC;
	`
	if _, err := tx.ExecContext(ctx, createTmp); err != nil {
		return fmt.Errorf("create temp table: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM keuangan`); err != nil {
		return fmt.Errorf("clear keuangan: %w", err)
	}

	insertBack := `
	INSERT INTO keuangan (id, tanggal, transaksi, sumber, masuk, keluar, saldo_kas, saldo_rekening, saldo, owner, input_at, created_at, client_req_id)
	SELECT id, tanggal, transaksi, sumber, masuk, keluar, saldo_kas, saldo_rekening, saldo, owner, input_at, created_at, client_req_id
	FROM keuangan_sorted;
	`
	if _, err := tx.ExecContext(ctx, insertBack); err != nil {
		return fmt.Errorf("re-insert sorted: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS keuangan_sorted`); err != nil {
		return fmt.Errorf("cleanup temp: %w", err)
	}

	return tx.Commit()
}
