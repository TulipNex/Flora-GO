package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// AIDB membungkus koneksi SQLite khusus untuk histori percakapan AI dan
// fakta jangka panjang per pengguna. Terpisah dari data/group.db dan
// data/session.db (whatsmeow) supaya tidak saling mengganggu — file ini
// sepenuhnya milik fitur AI.
type AIDB struct {
	db *sql.DB
}

// aiDB adalah instance global, diisi sekali lewat InitAIDB di main.go,
// lalu dipakai oleh internal/ai lewat store.AI().
var aiDB *AIDB

// InitAIDB membuka (atau membuat) file data/ai.db beserta tabel yang
// dibutuhkan. Dipanggil sekali di main.go sebelum client.Connect(),
// sejajar dengan InitGroupDB.
func InitAIDB(path string) error {
	db, err := openSQLite(path)
	if err != nil {
		return fmt.Errorf("gagal membuka ai.db: %w", err)
	}

	schema := `
	CREATE TABLE IF NOT EXISTS ai_history (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		session_key TEXT NOT NULL,
		role        TEXT NOT NULL,
		content     TEXT NOT NULL,
		created_at  INTEGER NOT NULL DEFAULT (strftime('%s','now'))
	);
	CREATE INDEX IF NOT EXISTS idx_ai_history_session ON ai_history(session_key, id);
	`
	if err := applySchema(db, "ai.db", schema, nil, aiSchemaVersion); err != nil {
		return err
	}

	aiDB = &AIDB{db: db}
	return nil
}

// AI mengembalikan instance AIDB global. Panic kalau InitAIDB belum
// dipanggil, supaya kesalahan urutan init ketahuan langsung saat
// development, bukan diam-diam nil di production (sama seperti Groups()).
func AI() *AIDB {
	if aiDB == nil {
		panic("store.AI() dipanggil sebelum store.InitAIDB() — cek urutan di main.go")
	}
	return aiDB
}

// aiHistoryMaxPerSession adalah batas baris ai_history per kunci sesi.
//
// Ini jaring pengaman untuk dua kasus yang tak tertutup pembersihan idle
// normal: (a) provider AI mati/belum diset sehingga ringkasan sesi selalu
// GAGAL (riwayat sengaja dipertahankan), dan (b) bot restart saat sesi masih
// berjalan sehingga baris lamanya tak tersentuh penyapu. Nilai 100 jauh di
// atas historyLoadLimit (20) dan summaryBackstopTurns (40) supaya konteks
// yang benar-benar dipakai tak pernah ikut terpangkas.
const aiHistoryMaxPerSession = 100

// AppendHistory menambahkan satu giliran percakapan ke histori sebuah sesi,
// lalu memangkasnya ke aiHistoryMaxPerSession baris terbaru.
func (a *AIDB) AppendHistory(ctx context.Context, sessionKey, role, content string) error {
	if _, err := a.db.ExecContext(ctx, `
		INSERT INTO ai_history (session_key, role, content) VALUES (?, ?, ?)
	`, sessionKey, role, content); err != nil {
		return err
	}
	// FIFO per kunci (indeks idx_ai_history_session menopangnya).
	_, err := a.db.ExecContext(ctx, `
		DELETE FROM ai_history
		WHERE session_key = ? AND id NOT IN (
			SELECT id FROM ai_history WHERE session_key = ? ORDER BY id DESC LIMIT ?
		)
	`, sessionKey, sessionKey, aiHistoryMaxPerSession)
	return err
}

// HistoryEntry adalah satu baris histori yang dibaca kembali dari DB.
//
// CreatedAt ditambahkan di Fase 6 supaya pemanggil (khususnya
// session.hydrateFromDB) bisa tahu KAPAN sungguhan pesan terakhir terjadi,
// bukan cuma isinya — dipakai untuk memperbaiki akurasi idle-timeout
// setelah bot restart (lihat catatan di session.go).
type HistoryEntry struct {
	Role      string
	Content   string
	CreatedAt time.Time
}

// LoadHistory mengambil `limit` entri histori TERBARU untuk sebuah sesi,
// dikembalikan dalam urutan KRONOLOGIS (lama → baru) supaya langsung
// bisa dipakai sebagai []ai.Content tanpa perlu dibalik lagi di pemanggil.
func (a *AIDB) LoadHistory(ctx context.Context, sessionKey string, limit int) ([]HistoryEntry, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT role, content, created_at FROM (
			SELECT role, content, created_at, id FROM ai_history
			WHERE session_key = ?
			ORDER BY id DESC
			LIMIT ?
		) ORDER BY id ASC
	`, sessionKey, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []HistoryEntry
	for rows.Next() {
		var e HistoryEntry
		var createdAtUnix int64
		if err := rows.Scan(&e.Role, &e.Content, &createdAtUnix); err != nil {
			return nil, err
		}
		e.CreatedAt = time.Unix(createdAtUnix, 0)
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// ClearHistory menghapus seluruh histori sebuah sesi (dipakai ".agent reset").
func (a *AIDB) ClearHistory(ctx context.Context, sessionKey string) error {
	_, err := a.db.ExecContext(ctx, `DELETE FROM ai_history WHERE session_key = ?`, sessionKey)
	return err
}

// ListHistorySessionKeys mengembalikan semua kunci sesi yang masih punya
// baris di ai_history. Dipakai saat startup (AdoptPersistedSessions) supaya
// sesi yang tersimpan ikut diproses penyapu idle — tanpa ini, baris milik
// sesi yang tak lagi diakses setelah bot restart akan menetap selamanya.
func (a *AIDB) ListHistorySessionKeys(ctx context.Context) ([]string, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT DISTINCT session_key FROM ai_history ORDER BY session_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		out = append(out, key)
	}
	return out, rows.Err()
}

// Close menutup koneksi database, dipanggil saat bot shutdown (opsional).
func (a *AIDB) Close() error {
	return a.db.Close()
}
