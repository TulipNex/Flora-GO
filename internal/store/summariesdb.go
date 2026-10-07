package store

// File ini menyimpan RINGKASAN SESI Flora (histori yang dipadatkan setelah
// idle) di data/ai.db, reuse koneksi aiDB — pola InitFactsDB. Maksimal 4
// ringkasan per kunci sesi (FIFO: ringkasan ke-5 menghapus yang tertua),
// masing-masing ≤1000 karakter, dipakai sebagai konteks pengganti riwayat
// utuh yang sudah dikosongkan.

import (
	"context"
	"fmt"
)

// summaryMaxPerSession adalah batas FIFO ringkasan per kunci sesi.
const summaryMaxPerSession = 4

// summaryMaxRunes adalah batas keras panjang satu ringkasan (sabuk +
// suspender dari instruksi prompt "≤1000 karakter").
const summaryMaxRunes = 1200

// InitSummariesDB membuat tabel ai_session_summaries. Dipanggil sekali di
// main.go, TEPAT SETELAH InitAIDB (butuh aiDB sudah ada).
func InitSummariesDB() error {
	if aiDB == nil {
		return fmt.Errorf("store.InitSummariesDB() dipanggil sebelum store.InitAIDB()")
	}
	_, err := aiDB.db.Exec(`
	CREATE TABLE IF NOT EXISTS ai_session_summaries (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		session_key TEXT NOT NULL,
		content     TEXT NOT NULL,
		created_at  INTEGER NOT NULL DEFAULT (strftime('%s','now'))
	);
	CREATE INDEX IF NOT EXISTS idx_ai_summaries_session ON ai_session_summaries(session_key, id);
	`)
	if err != nil {
		return fmt.Errorf("gagal siapkan tabel ringkasan: %w", err)
	}
	return nil
}

// SaveSessionSummary menyimpan satu ringkasan lalu menegakkan FIFO
// (sisakan N terbaru). Isi dipotong ke batas bila kepanjangan.
func (a *AIDB) SaveSessionSummary(ctx context.Context, sessionKey, content string) error {
	runes := []rune(content)
	if len(runes) > summaryMaxRunes {
		content = string(runes[:summaryMaxRunes])
	}
	if _, err := a.db.ExecContext(ctx, `
		INSERT INTO ai_session_summaries (session_key, content) VALUES (?, ?)
	`, sessionKey, content); err != nil {
		return err
	}
	_, err := a.db.ExecContext(ctx, `
		DELETE FROM ai_session_summaries WHERE session_key = ? AND id NOT IN (
			SELECT id FROM ai_session_summaries WHERE session_key = ? ORDER BY id DESC LIMIT ?
		)
	`, sessionKey, sessionKey, summaryMaxPerSession)
	return err
}

// ListSessionSummaries mengembalikan ringkasan sebuah sesi, tertua dulu
// (maksimal 4 sesuai FIFO).
func (a *AIDB) ListSessionSummaries(ctx context.Context, sessionKey string) ([]string, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT content FROM ai_session_summaries WHERE session_key = ? ORDER BY id ASC
	`, sessionKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ClearSessionSummaries menghapus seluruh ringkasan sebuah sesi (dipakai
// ".agent reset" — lupa total berarti ringkasan ikut hilang).
func (a *AIDB) ClearSessionSummaries(ctx context.Context, sessionKey string) error {
	_, err := a.db.ExecContext(ctx, `DELETE FROM ai_session_summaries WHERE session_key = ?`, sessionKey)
	return err
}
