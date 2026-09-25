package store

import (
	"context"
	"fmt"
)

// InitAISettingsDB membuat tabel ai_settings di database yang sama dengan
// ai_history & user_facts (data/ai.db). Sama seperti InitFactsDB, sengaja
// reuse aiDB yang sudah diinisialisasi lewat InitAIDB, tidak membuka
// koneksi baru.
//
// Dipanggil sekali di main.go, setelah InitAIDB(...).
func InitAISettingsDB() error {
	if aiDB == nil {
		return fmt.Errorf("store.InitAISettingsDB() dipanggil sebelum store.InitAIDB()")
	}

	// Langkah 1: buat tabel kalau BENAR-BENAR belum ada sama sekali
	// (deployment baru dari nol). Skema di sini sudah termasuk
	// model_nexarouter & model_ciora — untuk deployment BARU ini cukup,
	// tidak perlu migrasi apapun.
	createSchema := `
	CREATE TABLE IF NOT EXISTS ai_settings (
		id                INTEGER PRIMARY KEY CHECK (id = 1),
		active_provider   TEXT NOT NULL DEFAULT 'google',
		model_google      TEXT NOT NULL DEFAULT '',
		model_openrouter  TEXT NOT NULL DEFAULT '',
		model_nexarouter  TEXT NOT NULL DEFAULT '',
		model_ciora       TEXT NOT NULL DEFAULT ''
	);
	`
	if _, err := aiDB.db.Exec(createSchema); err != nil {
		return fmt.Errorf("gagal membuat schema ai_settings: %w", err)
	}

	// Langkah 2: migrasi ringan untuk deployment LAMA yang tabelnya sudah
	// ada TANPA kolom model_nexarouter / model_ciora (dibuat sebelum provider
	// ini ditambahkan). HARUS dijalankan SEBELUM langkah 3 (INSERT) di bawah —
	// kalau tidak, INSERT yang menyebut kolom baru akan gagal duluan pada
	// deployment lama karena kolomnya belum ada. SQLite tidak punya
	// "ADD COLUMN IF NOT EXISTS" bawaan, jadi errornya ditangani manual:
	// kalau errornya "duplicate column" (tabel BARU yang kolomnya memang
	// sudah ada dari langkah 1), abaikan; error lain tetap dilaporkan.
	for _, alter := range []string{
		`ALTER TABLE ai_settings ADD COLUMN model_nexarouter TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE ai_settings ADD COLUMN model_ciora TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := aiDB.db.Exec(alter); err != nil && !isDuplicateColumnError(err) {
			return fmt.Errorf("gagal migrasi ai_settings (%s): %w", alter, err)
		}
	}

	// Langkah 3: pastikan baris setting (id=1) ada, BARU SEKARANG aman
	// menyebut kolom baru karena langkah 1+2 di atas menjamin kolomnya sudah
	// pasti ada di titik ini, baik deployment baru maupun lama.
	insertDefault := `
	INSERT OR IGNORE INTO ai_settings (id, active_provider, model_google, model_openrouter, model_nexarouter, model_ciora)
	VALUES (1, 'google', '', '', '', '');
	`
	if _, err := aiDB.db.Exec(insertDefault); err != nil {
		return fmt.Errorf("gagal insert default ai_settings: %w", err)
	}

	return nil
}

// isDuplicateColumnError mengecek apakah error dari ALTER TABLE ADD COLUMN
// disebabkan kolom yang sudah ada (aman diabaikan), bukan error lain.
func isDuplicateColumnError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return len(msg) > 0 && (contains(msg, "duplicate column") || contains(msg, "already exists"))
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// AISettings adalah snapshot pengaturan provider/model aktif saat ini.
type AISettings struct {
	ActiveProvider  string
	ModelGoogle     string
	ModelOpenRouter string
	ModelNexaRouter string
	ModelCiora      string
}

// GetAISettings membaca baris setting (selalu ada tepat 1 baris, dijamin
// oleh INSERT OR IGNORE di InitAISettingsDB).
func (a *AIDB) GetAISettings(ctx context.Context) (AISettings, error) {
	var s AISettings
	err := a.db.QueryRowContext(ctx, `
		SELECT active_provider, model_google, model_openrouter, model_nexarouter, model_ciora FROM ai_settings WHERE id = 1
	`).Scan(&s.ActiveProvider, &s.ModelGoogle, &s.ModelOpenRouter, &s.ModelNexaRouter, &s.ModelCiora)
	return s, err
}

// SetActiveProvider mengganti provider aktif (mis. "google" -> "openrouter").
// Tidak menyentuh model tersimpan tiap provider — supaya ganti provider
// bolak-balik tidak mereset pilihan model yang sudah diatur sebelumnya.
func (a *AIDB) SetActiveProvider(ctx context.Context, provider string) error {
	_, err := a.db.ExecContext(ctx, `
		UPDATE ai_settings SET active_provider = ? WHERE id = 1
	`, provider)
	return err
}

// SetModelForProvider menyimpan model pilihan untuk SATU provider tertentu.
// Nama kolom di-whitelist manual (bukan string interpolation ke SQL) demi
// keamanan — provider tidak valid mengembalikan error, bukan dieksekusi
// sebagai SQL.
func (a *AIDB) SetModelForProvider(ctx context.Context, provider, model string) error {
	var err error
	switch provider {
	case "google":
		_, err = a.db.ExecContext(ctx, `UPDATE ai_settings SET model_google = ? WHERE id = 1`, model)
	case "openrouter":
		_, err = a.db.ExecContext(ctx, `UPDATE ai_settings SET model_openrouter = ? WHERE id = 1`, model)
	case "nexarouter":
		_, err = a.db.ExecContext(ctx, `UPDATE ai_settings SET model_nexarouter = ? WHERE id = 1`, model)
	case "ciora":
		_, err = a.db.ExecContext(ctx, `UPDATE ai_settings SET model_ciora = ? WHERE id = 1`, model)
	default:
		return fmt.Errorf("provider tidak dikenal: %s", provider)
	}
	return err
}
