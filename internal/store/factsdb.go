package store

import (
	"context"
	"fmt"
	"strings"
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

// DeleteFacts menghapus SELURUH ingatan jangka panjang satu sender JID
// (dipakai ".agent reset -memory"). Mengembalikan true kalau memang ada
// baris yang terhapus.
func (a *AIDB) DeleteFacts(ctx context.Context, senderJID string) (bool, error) {
	res, err := a.db.ExecContext(ctx, `DELETE FROM user_facts WHERE sender_jid = ?`, senderJID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DeleteFactsForKeys menghapus ingatan jangka panjang untuk SEJUMLAH kunci
// sender sekaligus (dipakai ".agent reset -memory @target" yang mencoba
// beberapa bentuk JID: target + hasil resolve alt-JID). Mengembalikan
// jumlah baris yang benar-benar terhapus (0 = tidak ada yang cocok).
func (a *AIDB) DeleteFactsForKeys(ctx context.Context, keys []string) (int64, error) {
	clean := make([]string, 0, len(keys))
	seen := make(map[string]bool, len(keys))
	for _, k := range keys {
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		clean = append(clean, k)
	}
	if len(clean) == 0 {
		return 0, nil
	}
	placeholders := strings.Repeat("?,", len(clean)-1) + "?"
	args := make([]any, len(clean))
	for i, k := range clean {
		args[i] = k
	}
	res, err := a.db.ExecContext(ctx, `DELETE FROM user_facts WHERE sender_jid IN (`+placeholders+`)`, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
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
