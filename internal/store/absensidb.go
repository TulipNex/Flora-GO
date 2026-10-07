package store

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// Absensi presensi via live location: patokan koordinat per grup, sesi
// buka/tutup (manual/jadwal), catatan kehadiran per pengirim, ekspor xlsx.
//
// Skema ditulis mengikuti konvensi DB repo (file sendiri data/absensi.db,
// DSN WAL + busy_timeout, CREATE TABLE IF NOT EXISTS, upsert ON CONFLICT).
// Fail-soft seperti CRM: init gagal = Absensi() nil dan semua method
// nil-guard — absensi tak boleh membunuh bot.

// AbsensiSession adalah satu periode absen di satu chat.
type AbsensiSession struct {
	ID        int64
	Chat      string
	StartTS   int64
	EndTS     int64
	Status    string // "open" / "closed"
	Auto      bool
	CreatedBy string
}

// AbsensiRecord adalah satu kehadiran (first-valid-wins per pengirim).
type AbsensiRecord struct {
	SessionID int64
	Sender    string
	Name      string
	TS        int64
	Lat       float64
	Long      float64
	DistanceM float64
	AccuracyM float64
}

// AbsensiSchedule adalah jadwal harian otomatis satu chat.
type AbsensiSchedule struct {
	Chat     string
	Days     string // CSV weekday Go (0=Minggu..6=Sabtu), mis. "1,2,3,4,5"
	StartMin int    // menit sejak 00:00 zona bot
	EndMin   int
	Enabled  bool
}

// AbsensiConfig adalah titik patokan + radius satu chat.
type AbsensiConfig struct {
	Chat    string
	Lat     float64
	Long    float64
	Name    string
	RadiusM float64
}

// DefaultAbsensiRadiusM adalah radius bawaan (meter) untuk patokan baru —
// sama dengan keputusan awal sebelum ada command .absen radius.
const DefaultAbsensiRadiusM = 20.0

// AbsensiDB membungkus koneksi SQLite arsip absensi (data/absensi.db).
type AbsensiDB struct {
	db *sql.DB
}

// absensiDB adalah instance global, diisi sekali lewat InitAbsensiDB.
// Boleh tetap nil (init gagal) — semua method nil-guard.
var absensiDB *AbsensiDB

// InitAbsensiDB membuka (atau membuat) data/absensi.db + tabel-tabelnya.
// Dipanggil sekali di main.go; error membuat main lanjut tanpa absensi.
func InitAbsensiDB(path string) error {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("gagal membuka absensi.db: %w", err)
	}
	if err := db.Ping(); err != nil {
		return fmt.Errorf("gagal ping absensi.db: %w", err)
	}
	schema := `
	CREATE TABLE IF NOT EXISTS absensi_config (
		chat TEXT PRIMARY KEY,
		lat REAL NOT NULL,
		long REAL NOT NULL,
		name TEXT NOT NULL DEFAULT '',
		radius_m REAL NOT NULL DEFAULT 20,
		updated_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
	);
	CREATE TABLE IF NOT EXISTS absensi_sessions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		chat TEXT NOT NULL,
		start_ts INTEGER NOT NULL,
		end_ts INTEGER NOT NULL,
		status TEXT NOT NULL DEFAULT 'open',
		auto INTEGER NOT NULL DEFAULT 0,
		created_by TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
	);
	CREATE INDEX IF NOT EXISTS idx_absensi_sessions_chat ON absensi_sessions(chat);
	CREATE TABLE IF NOT EXISTS absensi_records (
		session_id INTEGER NOT NULL,
		sender TEXT NOT NULL,
		name TEXT NOT NULL DEFAULT '',
		ts INTEGER NOT NULL,
		lat REAL NOT NULL,
		long REAL NOT NULL,
		distance_m REAL NOT NULL DEFAULT 0,
		accuracy_m REAL NOT NULL DEFAULT 0,
		PRIMARY KEY (session_id, sender)
	);
	CREATE TABLE IF NOT EXISTS absensi_schedules (
		chat TEXT PRIMARY KEY,
		days TEXT NOT NULL DEFAULT '',
		start_min INTEGER NOT NULL DEFAULT 0,
		end_min INTEGER NOT NULL DEFAULT 0,
		enabled INTEGER NOT NULL DEFAULT 0,
		updated_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
	);
	`
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("gagal migrasi absensi.db: %w", err)
	}
	// Migrasi DB lama (dibuat sebelum kolom radius_m ada): SQLite tak punya
	// ADD COLUMN IF NOT EXISTS — toleransi error duplicate column (pola
	// aisettingsdb.go).
	if _, err := db.Exec(`ALTER TABLE absensi_config ADD COLUMN radius_m REAL NOT NULL DEFAULT 20`); err != nil && !isDuplicateColumnError(err) {
		return fmt.Errorf("gagal migrasi radius absensi.db: %w", err)
	}
	absensiDB = &AbsensiDB{db: db}
	return nil
}

// Absensi mengembalikan instance global, atau nil bila mati.
func Absensi() *AbsensiDB {
	return absensiDB
}

// GetConfig mengambil patokan chat (nil,nil bila belum diset).
func (a *AbsensiDB) GetConfig(chat string) (*AbsensiConfig, error) {
	if a == nil || a.db == nil || chat == "" {
		return nil, fmt.Errorf("absensi mati")
	}
	var c AbsensiConfig
	err := a.db.QueryRow(
		`SELECT chat, lat, long, name, radius_m FROM absensi_config WHERE chat = ?`, chat,
	).Scan(&c.Chat, &c.Lat, &c.Long, &c.Name, &c.RadiusM)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if c.RadiusM <= 0 {
		c.RadiusM = DefaultAbsensiRadiusM
	}
	return &c, nil
}

// SetConfig menyimpan/menimpa patokan chat.
func (a *AbsensiDB) SetConfig(chat string, lat, long float64, name string) error {
	if a == nil || a.db == nil || chat == "" {
		return fmt.Errorf("absensi mati")
	}
	_, err := a.db.Exec(
		`INSERT INTO absensi_config (chat, lat, long, name, updated_at)
		 VALUES (?, ?, ?, ?, strftime('%s','now'))
		 ON CONFLICT(chat) DO UPDATE SET lat=excluded.lat, long=excluded.long,
		   name=excluded.name, updated_at=strftime('%s','now')`,
		chat, lat, long, name)
	return err
}

// SetRadius mengubah radius toleransi (meter) patokan chat. Patokan harus
// sudah ada (set via .absen patokan dulu) — radius tanpa titik acuan tak
// bermakna.
func (a *AbsensiDB) SetRadius(chat string, radius float64) error {
	if a == nil || a.db == nil || chat == "" {
		return fmt.Errorf("absensi mati")
	}
	res, err := a.db.Exec(
		`UPDATE absensi_config SET radius_m = ?, updated_at = strftime('%s','now') WHERE chat = ?`,
		radius, chat)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("belum ada patokan")
	}
	return nil
}

// ActiveSession mengembalikan sesi open terbaru chat (nil,nil bila tak ada).
func (a *AbsensiDB) ActiveSession(chat string) (*AbsensiSession, error) {
	if a == nil || a.db == nil || chat == "" {
		return nil, fmt.Errorf("absensi mati")
	}
	var s AbsensiSession
	var auto int
	err := a.db.QueryRow(
		`SELECT id, chat, start_ts, end_ts, status, auto, created_by
		 FROM absensi_sessions WHERE chat = ? AND status = 'open'
		 ORDER BY id DESC LIMIT 1`, chat,
	).Scan(&s.ID, &s.Chat, &s.StartTS, &s.EndTS, &s.Status, &auto, &s.CreatedBy)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.Auto = auto != 0
	return &s, nil
}

// OpenSession membuka sesi baru. Error bila masih ada sesi open (pemanggil
// menampilkan pesan "pakai extend/berhenti").
func (a *AbsensiDB) OpenSession(chat string, durMin int, auto bool, createdBy string, nowTS int64) (int64, error) {
	if a == nil || a.db == nil || chat == "" {
		return 0, fmt.Errorf("absensi mati")
	}
	if durMin < 1 {
		durMin = 1
	}
	if active, err := a.ActiveSession(chat); err != nil {
		return 0, err
	} else if active != nil {
		return 0, fmt.Errorf("sudah ada sesi aktif")
	}
	autoInt := 0
	if auto {
		autoInt = 1
	}
	res, err := a.db.Exec(
		`INSERT INTO absensi_sessions (chat, start_ts, end_ts, status, auto, created_by)
		 VALUES (?, ?, ?, 'open', ?, ?)`,
		chat, nowTS, nowTS+int64(durMin)*60, autoInt, createdBy)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// CloseSession menutup sesi (idempoten).
func (a *AbsensiDB) CloseSession(id int64) error {
	if a == nil || a.db == nil {
		return fmt.Errorf("absensi mati")
	}
	_, err := a.db.Exec(`UPDATE absensi_sessions SET status = 'closed' WHERE id = ?`, id)
	return err
}

// ExtendSession menambah end_ts sesi open. Mengembalikan end baru.
func (a *AbsensiDB) ExtendSession(id int64, addMin int) (int64, error) {
	if a == nil || a.db == nil {
		return 0, fmt.Errorf("absensi mati")
	}
	if addMin < 1 {
		addMin = 1
	}
	if _, err := a.db.Exec(
		`UPDATE absensi_sessions SET end_ts = end_ts + ? WHERE id = ? AND status = 'open'`,
		int64(addMin)*60, id); err != nil {
		return 0, err
	}
	var endTS int64
	if err := a.db.QueryRow(`SELECT end_ts FROM absensi_sessions WHERE id = ?`, id).Scan(&endTS); err != nil {
		return 0, err
	}
	return endTS, nil
}

// GetSession mengambil sesi by ID.
func (a *AbsensiDB) GetSession(id int64) (*AbsensiSession, error) {
	if a == nil || a.db == nil {
		return nil, fmt.Errorf("absensi mati")
	}
	var s AbsensiSession
	var auto int
	err := a.db.QueryRow(
		`SELECT id, chat, start_ts, end_ts, status, auto, created_by
		 FROM absensi_sessions WHERE id = ?`, id,
	).Scan(&s.ID, &s.Chat, &s.StartTS, &s.EndTS, &s.Status, &auto, &s.CreatedBy)
	if err != nil {
		return nil, err
	}
	s.Auto = auto != 0
	return &s, nil
}

// AddRecord mencatat kehadiran (first-valid-wins via INSERT OR IGNORE).
// Mengembalikan true bila baris BARU (hadir tercatat), false bila pengirim
// sudah tercatat sebelumnya di sesi ini.
func (a *AbsensiDB) AddRecord(sessionID int64, sender, name string, ts int64, lat, long, dist, acc float64) (bool, error) {
	if a == nil || a.db == nil || sessionID == 0 || sender == "" {
		return false, fmt.Errorf("absensi mati")
	}
	res, err := a.db.Exec(
		`INSERT OR IGNORE INTO absensi_records
		 (session_id, sender, name, ts, lat, long, distance_m, accuracy_m)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		sessionID, sender, name, ts, lat, long, dist, acc)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ListRecords mengembalikan catatan satu sesi urut waktu masuk.
func (a *AbsensiDB) ListRecords(sessionID int64) ([]AbsensiRecord, error) {
	if a == nil || a.db == nil {
		return nil, fmt.Errorf("absensi mati")
	}
	rows, err := a.db.Query(
		`SELECT session_id, sender, name, ts, lat, long, distance_m, accuracy_m
		 FROM absensi_records WHERE session_id = ? ORDER BY ts ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AbsensiRecord
	for rows.Next() {
		var r AbsensiRecord
		if err := rows.Scan(&r.SessionID, &r.Sender, &r.Name, &r.TS,
			&r.Lat, &r.Long, &r.DistanceM, &r.AccuracyM); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LatestSession mengembalikan sesi terakhir chat (open maupun closed) untuk
// `.absen hasil/export` tanpa argumen.
func (a *AbsensiDB) LatestSession(chat string) (*AbsensiSession, error) {
	if a == nil || a.db == nil || chat == "" {
		return nil, fmt.Errorf("absensi mati")
	}
	var s AbsensiSession
	var auto int
	err := a.db.QueryRow(
		`SELECT id, chat, start_ts, end_ts, status, auto, created_by
		 FROM absensi_sessions WHERE chat = ? ORDER BY id DESC LIMIT 1`, chat,
	).Scan(&s.ID, &s.Chat, &s.StartTS, &s.EndTS, &s.Status, &auto, &s.CreatedBy)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.Auto = auto != 0
	return &s, nil
}

// ListOpenSessions mengembalikan semua sesi open (untuk resume timer
// saat boot). Dipakai sekali di RestoreAbsensiSessions.
func (a *AbsensiDB) ListOpenSessions() []AbsensiSession {
	if a == nil || a.db == nil {
		return nil
	}
	rows, err := a.db.Query(
		`SELECT id, chat, start_ts, end_ts, status, auto, created_by
		 FROM absensi_sessions WHERE status = 'open' ORDER BY id ASC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []AbsensiSession
	for rows.Next() {
		var s AbsensiSession
		var auto int
		if err := rows.Scan(&s.ID, &s.Chat, &s.StartTS, &s.EndTS, &s.Status, &auto, &s.CreatedBy); err != nil {
			continue
		}
		s.Auto = auto != 0
		out = append(out, s)
	}
	return out
}

// SetSchedule menyimpan/menimpa jadwal harian chat (enabled=1).
func (a *AbsensiDB) SetSchedule(chat, days string, startMin, endMin int) error {
	if a == nil || a.db == nil || chat == "" {
		return fmt.Errorf("absensi mati")
	}
	_, err := a.db.Exec(
		`INSERT INTO absensi_schedules (chat, days, start_min, end_min, enabled, updated_at)
		 VALUES (?, ?, ?, ?, 1, strftime('%s','now'))
		 ON CONFLICT(chat) DO UPDATE SET days=excluded.days, start_min=excluded.start_min,
		   end_min=excluded.end_min, enabled=1, updated_at=strftime('%s','now')`,
		chat, days, startMin, endMin)
	return err
}

// ClearSchedule menghapus jadwal chat.
func (a *AbsensiDB) ClearSchedule(chat string) error {
	if a == nil || a.db == nil || chat == "" {
		return fmt.Errorf("absensi mati")
	}
	_, err := a.db.Exec(`DELETE FROM absensi_schedules WHERE chat = ?`, chat)
	return err
}

// GetSchedule mengambil jadwal chat (nil,nil bila tak ada/mati).
func (a *AbsensiDB) GetSchedule(chat string) (*AbsensiSchedule, error) {
	if a == nil || a.db == nil || chat == "" {
		return nil, fmt.Errorf("absensi mati")
	}
	var s AbsensiSchedule
	var enabled int
	err := a.db.QueryRow(
		`SELECT chat, days, start_min, end_min, enabled FROM absensi_schedules WHERE chat = ?`, chat,
	).Scan(&s.Chat, &s.Days, &s.StartMin, &s.EndMin, &enabled)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.Enabled = enabled != 0
	return &s, nil
}

// ListSchedules mengembalikan semua jadwal aktif (untuk ticker).
func (a *AbsensiDB) ListSchedules() ([]AbsensiSchedule, error) {
	if a == nil || a.db == nil {
		return nil, fmt.Errorf("absensi mati")
	}
	rows, err := a.db.Query(
		`SELECT chat, days, start_min, end_min, enabled FROM absensi_schedules WHERE enabled = 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AbsensiSchedule
	for rows.Next() {
		var s AbsensiSchedule
		var enabled int
		if err := rows.Scan(&s.Chat, &s.Days, &s.StartMin, &s.EndMin, &enabled); err != nil {
			return nil, err
		}
		s.Enabled = enabled != 0
		out = append(out, s)
	}
	return out, rows.Err()
}
