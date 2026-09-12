package store

import (
	"context"
	"fmt"
)

// InitFactsDB membuat tabel user_facts di database yang sama dengan
// ai_history (data/ai.db). Sengaja TIDAK membuka koneksi baru — reuse
// aiDB yang sudah diinisialisasi lewat InitAIDB, supaya tidak perlu
// panggilan Init terpisah lagi di main.go dan tidak ada dua *sql.DB
// bersaing mengunci file yang sama.
//
// Dipanggil sekali di main.go, TEPAT SETELAH InitAIDB(...).
func InitFactsDB() error {
	if aiDB == nil {
		return fmt.Errorf("store.InitFactsDB() dipanggil sebelum store.InitAIDB()")
	}

	schema := `
	CREATE TABLE IF NOT EXISTS user_facts (
		sender_jid  TEXT PRIMARY KEY,
		facts_json  TEXT NOT NULL,
		updated_at  INTEGER NOT NULL DEFAULT (strftime('%s','now'))
	);
	`
	if _, err := aiDB.db.Exec(schema); err != nil {
		return fmt.Errorf("gagal membuat schema user_facts: %w", err)
	}
	return nil
}

// GetFacts mengambil fakta tersimpan (mentah, masih JSON string) untuk
// satu sender JID. Mengembalikan string kosong kalau belum ada catatan
// sama sekali (bukan error — itu kondisi normal untuk user baru).
func (a *AIDB) GetFacts(ctx context.Context, senderJID string) (string, error) {
	var factsJSON string
	err := a.db.QueryRowContext(ctx, `
		SELECT facts_json FROM user_facts WHERE sender_jid = ?
	`, senderJID).Scan(&factsJSON)
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			return "", nil
		}
		return "", err
	}
	return factsJSON, nil
}

// UpsertFacts menyimpan (atau menimpa total) fakta untuk satu sender JID.
// Fase 5 sengaja replace-seluruhnya, bukan merge sebagian di level SQL —
// merge/gabung fakta lama+baru dilakukan di sisi internal/ai (lewat LLM
// yang diberi fakta lama sebagai konteks), jadi yang sampai ke sini
// memang sudah versi final gabungan.
func (a *AIDB) UpsertFacts(ctx context.Context, senderJID, factsJSON string) error {
	_, err := a.db.ExecContext(ctx, `
		INSERT INTO user_facts (sender_jid, facts_json, updated_at)
		VALUES (?, ?, strftime('%s','now'))
		ON CONFLICT(sender_jid) DO UPDATE SET
			facts_json = excluded.facts_json,
			updated_at = excluded.updated_at
	`, senderJID, factsJSON)
	return err
}
