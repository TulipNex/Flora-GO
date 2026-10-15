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
	// (deployment baru dari nol). Skema hanya berisi provider yang didukung
	// (nexarouter & ciora) — kolom model_google/model_openrouter peninggalan
	// provider lama sengaja tidak ada di skema baru.
	createSchema := `
	CREATE TABLE IF NOT EXISTS ai_settings (
		id                INTEGER PRIMARY KEY CHECK (id = 1),
		active_provider   TEXT NOT NULL DEFAULT 'ciora',
		model_nexarouter  TEXT NOT NULL DEFAULT '',
		model_ciora       TEXT NOT NULL DEFAULT '',
		voice_reply       INTEGER NOT NULL DEFAULT 0
	);
	`
	if _, err := aiDB.db.Exec(createSchema); err != nil {
		return fmt.Errorf("gagal membuat schema ai_settings: %w", err)
	}

	// Langkah 2: migrasi ringan untuk deployment LAMA.
	// (a) Kolom model_nexarouter / model_ciora / voice_reply belum ada di
	// tabel yang dibuat sebelum provider ini ditambahkan — tambahkan bila
	// belum ada. SQLite tidak punya "ADD COLUMN IF NOT EXISTS" bawaan, jadi
	// errornya ditangani manual: kalau errornya "duplicate column", abaikan.
	// (b) Baris yang masih memakai provider lama (google/openrouter) yang
	// sudah dihapus dipindahkan ke ciora (provider utama) supaya bot tidak
	// error "provider tidak dikenal" saat start berikutnya.
	for _, alter := range []string{
		`ALTER TABLE ai_settings ADD COLUMN model_nexarouter TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE ai_settings ADD COLUMN model_ciora TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE ai_settings ADD COLUMN voice_reply INTEGER NOT NULL DEFAULT 0`,
	} {
		if _, err := aiDB.db.Exec(alter); err != nil && !isDuplicateColumnError(err) {
			return fmt.Errorf("gagal migrasi ai_settings (%s): %w", alter, err)
		}
	}

	if _, err := aiDB.db.Exec(`UPDATE ai_settings SET active_provider = 'ciora' WHERE active_provider IN ('google', 'openrouter')`); err != nil {
		return fmt.Errorf("gagal migrasi provider lama ke ciora: %w", err)
	}

	// Langkah 3: pastikan baris setting (id=1) ada, BARU SEKARANG aman
	// menyebut kolom baru karena langkah 1+2 di atas menjamin kolomnya sudah
	// pasti ada di titik ini, baik deployment baru maupun lama.
	insertDefault := `
	INSERT OR IGNORE INTO ai_settings (id, active_provider, model_nexarouter, model_ciora, voice_reply)
	VALUES (1, 'ciora', '', '', 0);
	`
	if _, err := aiDB.db.Exec(insertDefault); err != nil {
		return fmt.Errorf("gagal insert default ai_settings: %w", err)
	}

	// Langkah 4: registry provider & model runtime (command-driven).
	// ai_providers = provider OpenAI-compatible (nama, endpoint
	// chat-completions penuh). ai_models = daftar model per provider
	// (pos = urutan tampil + default = pos terkecil). ai_settings.active_model
	// menggantikan kolom model_<provider> per-provider (yang dibiarkan
	// dorman — tidak dibaca lagi, tidak dihapus demi migrasi aman).
	if _, err := aiDB.db.Exec(`CREATE TABLE IF NOT EXISTS ai_providers (
		name     TEXT PRIMARY KEY,
		endpoint TEXT NOT NULL DEFAULT '',
		pos      INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		return fmt.Errorf("gagal membuat tabel ai_providers: %w", err)
	}
	if _, err := aiDB.db.Exec(`CREATE TABLE IF NOT EXISTS ai_models (
		provider TEXT NOT NULL,
		model    TEXT NOT NULL,
		pos      INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (provider, model)
	)`); err != nil {
		return fmt.Errorf("gagal membuat tabel ai_models: %w", err)
	}
	if _, err := aiDB.db.Exec(`ALTER TABLE ai_settings ADD COLUMN active_model TEXT NOT NULL DEFAULT ''`); err != nil && !isDuplicateColumnError(err) {
		return fmt.Errorf("gagal migrasi ai_settings (active_model): %w", err)
	}

	// Seed provider & model bawaan (INSERT OR IGNORE: pilihan owner via
	// command tidak pernah ditimpa). nexarouter SENGAJA tidak di-seed
	// (dihapus, diganti openagentic) — migrasi active-nya di bawah.
	for i, p := range [][2]string{
		{"ciora", "https://ciora.id/v1/chat/completions"},
		{"openagentic", "https://aimurah.my.id/api/v1/chat/completions"},
	} {
		if _, err := aiDB.db.Exec(`INSERT OR IGNORE INTO ai_providers (name, endpoint, pos) VALUES (?, ?, ?)`, p[0], p[1], i); err != nil {
			return fmt.Errorf("gagal seed provider %s: %w", p[0], err)
		}
	}
	seedModels := map[string][]string{
		"ciora": {
			"ciora-ai-free",
			"ciora-coding-free",
			"ciora-vision-free",
			"cohere/north-mini-code-free",
			"poolside/laguna-s-2.1-free",
			"ciora/gemini-3.8-flash",
		},
		"openagentic": {
			"claude-haiku-5.5-free",
			"space-bunny-free",
			"mimo-v2.6-flash",
		},
	}
	for provider, models := range seedModels {
		for i, m := range models {
			if _, err := aiDB.db.Exec(`INSERT OR IGNORE INTO ai_models (provider, model, pos) VALUES (?, ?, ?)`, provider, m, i); err != nil {
				return fmt.Errorf("gagal seed model %s/%s: %w", provider, m, err)
			}
		}
	}

	// Kepatuhan docs: canonical base URL adalah aimurah.my.id (seluruh
	// contoh docs memakai host itu; openagentic.id peninggalan homepage).
	// Hanya baris yang masih endpoint lama yang dipindah — endpoint custom
	// owner tidak disentuh.
	if _, err := aiDB.db.Exec(`UPDATE ai_providers SET endpoint = 'https://aimurah.my.id/api/v1/chat/completions' WHERE name = 'openagentic' AND endpoint = 'https://openagentic.id/api/v1/chat/completions'`); err != nil {
		return fmt.Errorf("gagal migrasi endpoint openagentic: %w", err)
	}

	// Pembersihan ID basi: 5 ID openagentic dari docs ternyata tidak eksis
	// di API live (probe langsung Okt 2026) — hapus tepat ID itu saja.
	// Tambahan owner (mis. minimax-m3) tidak disentuh. Model aktif yang
	// menunjuk ID basi otomatis fallback via self-heal GetActiveModelName.
	if _, err := aiDB.db.Exec(`DELETE FROM ai_models WHERE provider = 'openagentic' AND model IN ('claude-sonnet-4.5','claude-haiku-4.5','deepseek-3.2','minimax-m2.5','minimax-m2.1')`); err != nil {
		return fmt.Errorf("gagal bersihkan model basi openagentic: %w", err)
	}
	// Migrasi pengguna nexarouter (provider dihapus): pendaratan aman =
	// ciora + model default (active_model dikosongkan supaya fallback
	// defaultModelFor yang memutuskan). Pengguna ciora membawa model
	// lama mereka (model_ciora -> active_model) sekali saja — urutan
	// penting: salin model ciora DULU, baru pindahkan baris nexarouter,
	// supaya model_ciora basi milik eks-pengguna nexarouter tidak ikut
	// tersalin.
	if _, err := aiDB.db.Exec(`UPDATE ai_settings SET active_model = model_ciora WHERE active_provider = 'ciora' AND (active_model IS NULL OR active_model = '') AND model_ciora <> ''`); err != nil {
		return fmt.Errorf("gagal migrasi model ciora: %w", err)
	}
	if _, err := aiDB.db.Exec(`UPDATE ai_settings SET active_provider = 'ciora', active_model = '' WHERE active_provider = 'nexarouter'`); err != nil {
		return fmt.Errorf("gagal migrasi nexarouter ke ciora: %w", err)
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
// Kolom model_nexarouter/model_ciora/model_google/model_openrouter
// peninggalan provider yang dihapus sengaja tidak dibaca lagi — bila masih
// ada di DB lama, diabaikan (dorman).
type AISettings struct {
	ActiveProvider string
	ActiveModel    string
	VoiceReply     bool
}

// ProviderEntry adalah satu baris registry provider runtime.
type ProviderEntry struct {
	Name     string
	Endpoint string
}

// GetAISettings membaca baris setting (selalu ada tepat 1 baris, dijamin
// oleh INSERT OR IGNORE di InitAISettingsDB).
func (a *AIDB) GetAISettings(ctx context.Context) (AISettings, error) {
	var s AISettings
	var voiceReply int
	err := a.db.QueryRowContext(ctx, `
		SELECT active_provider, active_model, voice_reply FROM ai_settings WHERE id = 1
	`).Scan(&s.ActiveProvider, &s.ActiveModel, &voiceReply)
	if err != nil {
		return s, err
	}
	s.VoiceReply = voiceReply != 0
	return s, nil
}

// SetActiveProvider mengganti provider aktif. Tidak menyentuh model
// tersimpan — supaya ganti provider bolak-balik tidak mereset pilihan
// model yang sudah diatur sebelumnya.
func (a *AIDB) SetActiveProvider(ctx context.Context, provider string) error {
	_, err := a.db.ExecContext(ctx, `
		UPDATE ai_settings SET active_provider = ? WHERE id = 1
	`, provider)
	return err
}

// SetVoiceReply menyalakan/mematikan mode suara Flora (command
// `.agent voice on/off`): true = balasan AI dikirim sebagai voice note,
// false = teks bubble seperti biasa.
func (a *AIDB) SetVoiceReply(ctx context.Context, on bool) error {
	val := 0
	if on {
		val = 1
	}
	_, err := a.db.ExecContext(ctx, `
		UPDATE ai_settings SET voice_reply = ? WHERE id = 1
	`, val)
	return err
}

// SetActiveModel menyimpan model aktif (satu untuk provider yang sedang
// aktif — ganti provider tidak meresetnya, tapi GetActiveModelName
// memvalidasi terhadap daftar model provider aktif dan fallback ke default
// bila tak cocok).
func (a *AIDB) SetActiveModel(ctx context.Context, model string) error {
	_, err := a.db.ExecContext(ctx, `UPDATE ai_settings SET active_model = ? WHERE id = 1`, model)
	return err
}

// ListProviders mengembalikan registry provider runtime terurut pos.
// Tabel selalu ter-seed (InitAISettingsDB), jadi kosong = DB rusak.
func (a *AIDB) ListProviders(ctx context.Context) ([]ProviderEntry, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT name, endpoint FROM ai_providers ORDER BY pos ASC, name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProviderEntry
	for rows.Next() {
		var p ProviderEntry
		if err := rows.Scan(&p.Name, &p.Endpoint); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// AddProvider menambah provider (pos = akhir antrean). Duplikat nama
// mengembalikan error ramah (bukan error SQL mentah).
func (a *AIDB) AddProvider(ctx context.Context, name, endpoint string) error {
	res, err := a.db.ExecContext(ctx, `INSERT OR IGNORE INTO ai_providers (name, endpoint, pos)
		SELECT ?, ?, COALESCE(MAX(pos), -1) + 1 FROM ai_providers`, name, endpoint)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("provider %q sudah ada", name)
	}
	return nil
}

// DelProvider menghapus provider beserta daftar modelnya.
func (a *AIDB) DelProvider(ctx context.Context, name string) error {
	if _, err := a.db.ExecContext(ctx, `DELETE FROM ai_models WHERE provider = ?`, name); err != nil {
		return err
	}
	res, err := a.db.ExecContext(ctx, `DELETE FROM ai_providers WHERE name = ?`, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("provider %q tidak ada", name)
	}
	return nil
}

// ListModels mengembalikan daftar model satu provider terurut pos.
func (a *AIDB) ListModels(ctx context.Context, provider string) ([]string, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT model FROM ai_models WHERE provider = ? ORDER BY pos ASC, model ASC`, provider)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// AddModel menambah model ke provider (pos = akhir antrean).
func (a *AIDB) AddModel(ctx context.Context, provider, model string) error {
	var exists string
	if err := a.db.QueryRowContext(ctx, `SELECT name FROM ai_providers WHERE name = ?`, provider).Scan(&exists); err != nil {
		return fmt.Errorf("provider %q tidak ada", provider)
	}
	res, err := a.db.ExecContext(ctx, `INSERT OR IGNORE INTO ai_models (provider, model, pos)
		SELECT ?, ?, COALESCE(MAX(pos), -1) + 1 FROM ai_models WHERE provider = ?`, provider, model, provider)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("model %q sudah ada untuk provider %s", model, provider)
	}
	return nil
}

// DelModel menghapus satu model dari provider.
func (a *AIDB) DelModel(ctx context.Context, provider, model string) error {
	res, err := a.db.ExecContext(ctx, `DELETE FROM ai_models WHERE provider = ? AND model = ?`, provider, model)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("model %q tidak ada untuk provider %s", model, provider)
	}
	return nil
}
