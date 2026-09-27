package store

import (
	"context"
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// GroupDB membungkus koneksi SQLite khusus untuk data pengaturan grup.
// Terpisah dari data/session.db (punya whatsmeow) supaya tidak saling
// mengganggu — file ini sepenuhnya milik bot sendiri.
type GroupDB struct {
	db *sql.DB
}

// groupDB adalah instance global, diisi sekali lewat InitGroupDB di main.go,
// lalu dipakai oleh plugin manapun lewat store.Groups().
var groupDB *GroupDB

// InitGroupDB membuka (atau membuat) file data/group.db beserta tabel yang
// dibutuhkan. Dipanggil sekali di main.go sebelum client.Connect().
func InitGroupDB(path string) error {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("gagal membuka group.db: %w", err)
	}
	if err := db.Ping(); err != nil {
		return fmt.Errorf("gagal ping group.db: %w", err)
	}

	schema := `
	CREATE TABLE IF NOT EXISTS group_settings (
		chat_jid    TEXT PRIMARY KEY,
		is_closed   INTEGER NOT NULL DEFAULT 0,
		name        TEXT NOT NULL DEFAULT '',
		description TEXT NOT NULL DEFAULT '',
		updated_at  INTEGER NOT NULL DEFAULT (strftime('%s','now'))
	);
	CREATE TABLE IF NOT EXISTS banned_users (
		user_jid   TEXT PRIMARY KEY,
		user_part  TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
	);
	CREATE TABLE IF NOT EXISTS banned_groups (
		chat_jid   TEXT PRIMARY KEY,
		created_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
	);
	CREATE TABLE IF NOT EXISTS bot_settings (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL DEFAULT ''
	);
	CREATE TABLE IF NOT EXISTS group_moderation (
		chat_jid   TEXT PRIMARY KEY,
		antilink   INTEGER NOT NULL DEFAULT 0,
		updated_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
	);
	CREATE TABLE IF NOT EXISTS user_warnings (
		chat_jid   TEXT NOT NULL,
		user_jid   TEXT NOT NULL,
		warn_count INTEGER NOT NULL DEFAULT 0,
		updated_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
		PRIMARY KEY (chat_jid, user_jid)
	);
	CREATE TABLE IF NOT EXISTS group_greetings (
		chat_jid    TEXT PRIMARY KEY,
		welcome     INTEGER NOT NULL DEFAULT 1,
		bye         INTEGER NOT NULL DEFAULT 1,
		welcome_msg TEXT NOT NULL DEFAULT '',
		bye_msg     TEXT NOT NULL DEFAULT '',
		updated_at  INTEGER NOT NULL DEFAULT (strftime('%s','now'))
	);
	CREATE TABLE IF NOT EXISTS scheduled_calls (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		target_phone TEXT NOT NULL,
		chat_jid     TEXT NOT NULL,
		scheduled_at INTEGER NOT NULL,
		status       TEXT NOT NULL DEFAULT 'pending',
		created_at   INTEGER NOT NULL DEFAULT (strftime('%s','now'))
	);
	CREATE TABLE IF NOT EXISTS autodownload (
		chat_jid   TEXT PRIMARY KEY,
		enabled    INTEGER NOT NULL DEFAULT 0,
		updated_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
	);
	`
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("gagal membuat schema group.db: %w", err)
	}

	// Migrasi untuk database LAMA yang tabel group_greetings-nya sudah ada
	// TANPA kolom bye/welcome_msg/bye_msg (dibuat sebelum fitur perpisahan +
	// template ini ditambahkan). Pola yang sama dengan migrasi kolom
	// model_nexarouter di aisettingsdb.go: abaikan error "duplicate column".
	for _, alter := range []string{
		`ALTER TABLE group_greetings ADD COLUMN bye INTEGER NOT NULL DEFAULT 1`,
		`ALTER TABLE group_greetings ADD COLUMN welcome_msg TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE group_greetings ADD COLUMN bye_msg TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := db.Exec(alter); err != nil && !isDuplicateColumnError(err) {
			return fmt.Errorf("gagal migrasi group_greetings: %w", err)
		}
	}

	groupDB = &GroupDB{db: db}
	return nil
}

// Groups mengembalikan instance GroupDB global. Panic kalau InitGroupDB
// belum dipanggil, supaya kesalahan urutan init ketahuan langsung saat
// development, bukan diam-diam nil di production.
func Groups() *GroupDB {
	if groupDB == nil {
		panic("store.Groups() dipanggil sebelum store.InitGroupDB() — cek urutan di main.go")
	}
	return groupDB
}

// upsert memastikan baris untuk chatJID ada, lalu mengembalikan settingnya.
// Dipakai internal supaya semua fungsi publik di bawah selalu punya baris
// untuk dibaca/ditulis walau grup belum pernah tersentuh sebelumnya.
func (g *GroupDB) ensureRow(ctx context.Context, chatJID string) error {
	_, err := g.db.ExecContext(ctx, `
		INSERT INTO group_settings (chat_jid) VALUES (?)
		ON CONFLICT(chat_jid) DO NOTHING
	`, chatJID)
	return err
}

// IsClosed mengembalikan true kalau grup sedang dikunci (.close),
// artinya hanya admin yang boleh kirim pesan lewat pengaturan grup WhatsApp.
func (g *GroupDB) IsClosed(ctx context.Context, chatJID string) (bool, error) {
	if err := g.ensureRow(ctx, chatJID); err != nil {
		return false, err
	}
	var closed int
	err := g.db.QueryRowContext(ctx, `SELECT is_closed FROM group_settings WHERE chat_jid = ?`, chatJID).Scan(&closed)
	if err != nil {
		return false, err
	}
	return closed == 1, nil
}

// SetClosed menyimpan status open/close grup di sisi bot (dipakai untuk
// catatan/log internal; penguncian sungguhan tetap lewat API whatsmeow
// SetGroupAnnounce, lihat plugins/groupsetting.go).
func (g *GroupDB) SetClosed(ctx context.Context, chatJID string, closed bool) error {
	if err := g.ensureRow(ctx, chatJID); err != nil {
		return err
	}
	val := 0
	if closed {
		val = 1
	}
	_, err := g.db.ExecContext(ctx, `
		UPDATE group_settings SET is_closed = ?, updated_at = strftime('%s','now') WHERE chat_jid = ?
	`, val, chatJID)
	return err
}

// Close menutup koneksi database, dipanggil saat bot shutdown (opsional).
func (g *GroupDB) Close() error {
	return g.db.Close()
}
