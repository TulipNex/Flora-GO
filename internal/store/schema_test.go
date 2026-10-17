package store

import (
	"path/filepath"
	"testing"
)

// TestApplySchemaVersioning memverifikasi perilaku inti runner skema:
// bootstrap menyetel user_version, idempoten saat diulang, menolak turun
// (downgrade), dan toleran terhadap ALTER "duplicate column".
func TestApplySchemaVersioning(t *testing.T) {
	db, err := openSQLite(filepath.Join(t.TempDir(), "v.db"))
	if err != nil {
		t.Fatalf("openSQLite: %v", err)
	}
	defer db.Close()

	const schema = `CREATE TABLE IF NOT EXISTS t (id INTEGER PRIMARY KEY);`
	alters := []string{`ALTER TABLE t ADD COLUMN x TEXT NOT NULL DEFAULT ''`}

	if err := applySchema(db, "v.db", schema, alters, 3); err != nil {
		t.Fatalf("applySchema bootstrap: %v", err)
	}
	if v, _ := schemaVersion(db); v != 3 {
		t.Fatalf("user_version = %d, mau 3", v)
	}

	// Idempoten: jalankan ulang (ALTER "duplicate column" harus diabaikan).
	if err := applySchema(db, "v.db", schema, alters, 3); err != nil {
		t.Fatalf("applySchema ulang: %v", err)
	}

	// Penjaga downgrade: kode lama tidak boleh menurunkan versi DB.
	if err := syncSchemaVersion(db, "v.db", 1); err != nil {
		t.Fatalf("syncSchemaVersion downgrade: %v", err)
	}
	if v, _ := schemaVersion(db); v != 3 {
		t.Fatalf("user_version turun jadi %d — seharusnya tetap 3", v)
	}
}

// TestInitAIDBLegacyUpgrade meniru ai_db lama (user_version=0, tanpa ALTER)
// yang sudah berisi data: setelah InitAIDB, versi naik ke aiSchemaVersion dan
// baris lama TETAP utuh (CREATE ... IF NOT EXISTS tidak menghapus data).
func TestInitAIDBLegacyUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ai.db")

	// Bangun "DB lama" mentah: tabel + satu baris, user_version tetap 0.
	raw, err := openSQLite(path)
	if err != nil {
		t.Fatalf("openSQLite: %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE ai_history (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_key TEXT NOT NULL,
		role TEXT NOT NULL,
		content TEXT NOT NULL,
		created_at INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		t.Fatalf("buat tabel lama: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO ai_history (session_key, role, content) VALUES ('k', 'user', 'lama')`); err != nil {
		t.Fatalf("insert baris lama: %v", err)
	}
	if v, _ := schemaVersion(raw); v != 0 {
		t.Fatalf("prasyarat: user_version DB lama = %d, mau 0", v)
	}
	raw.Close()

	if err := InitAIDB(path); err != nil {
		t.Fatalf("InitAIDB: %v", err)
	}
	defer AI().Close()

	if v, _ := schemaVersion(AI().db); v != aiSchemaVersion {
		t.Fatalf("user_version = %d, mau %d", v, aiSchemaVersion)
	}
	var content string
	if err := AI().db.QueryRow(`SELECT content FROM ai_history WHERE session_key = 'k'`).Scan(&content); err != nil {
		t.Fatalf("baris lama hilang: %v", err)
	}
	if content != "lama" {
		t.Fatalf("content = %q, mau \"lama\"", content)
	}
}
