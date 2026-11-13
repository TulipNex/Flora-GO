package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestAIRegistryMigration memverifikasi migrasi + registry provider/model
// dengan DB temporer (TIDAK menyentuh data/).
func TestAIRegistryMigration(t *testing.T) {
	dir := t.TempDir()
	if err := InitAIDB(filepath.Join(dir, "ai.db")); err != nil {
		t.Fatalf("InitAIDB: %v", err)
	}
	defer AI().Close()
	if err := InitAISettingsDB(); err != nil {
		t.Fatalf("InitAISettingsDB: %v", err)
	}
	ctx := context.Background()

	ps, err := AI().ListProviders(ctx)
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if len(ps) != 2 || ps[0].Name != "ciora" || ps[1].Name != "openagentic" {
		t.Fatalf("seed provider salah: %+v", ps)
	}
	ms, err := AI().ListModels(ctx, "openagentic")
	if err != nil || len(ms) != 3 || ms[0] != "claude-haiku-5.5-free" {
		t.Fatalf("seed model openagentic salah: %+v err=%v", ms, err)
	}

	// Pembersihan ID basi: simulasi DB lama berisi ID docs + tambahan owner,
	// lalu init ulang harus menghapus tepat ID basi saja.
	if _, err := aiDB.db.Exec(`INSERT OR IGNORE INTO ai_models (provider, model, pos) VALUES ('openagentic','deepseek-3.2',99),('openagentic','minimax-m3',99)`); err != nil {
		t.Fatalf("setup basi: %v", err)
	}
	if err := InitAISettingsDB(); err != nil {
		t.Fatalf("re-init bersih: %v", err)
	}
	ms, _ = AI().ListModels(ctx, "openagentic")
	for _, m := range ms {
		if m == "deepseek-3.2" || m == "claude-sonnet-4.5" {
			t.Fatalf("ID basi harus hilang: %+v", ms)
		}
	}
	keep := false
	for _, m := range ms {
		if m == "minimax-m3" {
			keep = true
		}
	}
	if !keep {
		t.Fatalf("tambahan owner harus dipertahankan: %+v", ms)
	}

	// Migrasi pengguna nexarouter lama.
	if _, err := aiDB.db.Exec(`UPDATE ai_settings SET active_provider='nexarouter', active_model='', model_ciora='ciora-vision-free'`); err != nil {
		t.Fatalf("setup legacy: %v", err)
	}
	if err := InitAISettingsDB(); err != nil {
		t.Fatalf("re-init: %v", err)
	}
	s, err := AI().GetAISettings(ctx)
	if err != nil {
		t.Fatalf("GetAISettings: %v", err)
	}
	if s.ActiveProvider != "ciora" || s.ActiveModel != "" {
		t.Fatalf("migrasi nexarouter salah: %+v", s)
	}

	// Migrasi pengguna ciora membawa model lama.
	if _, err := aiDB.db.Exec(`UPDATE ai_settings SET active_model='', model_ciora='ciora-coding-free'`); err != nil {
		t.Fatalf("setup ciora: %v", err)
	}
	if err := InitAISettingsDB(); err != nil {
		t.Fatalf("re-init2: %v", err)
	}
	s, _ = AI().GetAISettings(ctx)
	if s.ActiveModel != "ciora-coding-free" {
		t.Fatalf("migrasi model ciora salah: %+v", s)
	}

	// CRUD registry.
	if err := AI().AddProvider(ctx, "tesprov", "https://contoh.id/v1/chat/completions"); err != nil {
		t.Fatalf("AddProvider: %v", err)
	}
	if err := AI().AddProvider(ctx, "tesprov", "https://x"); err == nil {
		t.Fatal("duplikat provider harus error")
	}
	if err := AI().AddModel(ctx, "tesprov", "model-a"); err != nil {
		t.Fatalf("AddModel: %v", err)
	}
	if err := AI().AddModel(ctx, "tesprov", "model-a"); err == nil {
		t.Fatal("duplikat model harus error")
	}
	// Guard model-terakhir/aktif ada di layer ai (DelModel), bukan store:
	// store menghapus apa adanya.
	if err := AI().DelModel(ctx, "tesprov", "model-a"); err != nil {
		t.Fatalf("DelModel: %v", err)
	}
	if ms, _ := AI().ListModels(ctx, "tesprov"); len(ms) != 0 {
		t.Fatalf("model harus sudah hilang: %+v", ms)
	}
	if err := AI().DelProvider(ctx, "tesprov"); err != nil {
		t.Fatalf("DelProvider: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ai.db")); err != nil {
		t.Fatalf("db hilang: %v", err)
	}
}

// TestAIDBDelModelsBatch memverifikasi hapus banyak model dalam satu transaksi:
// baris yang tidak ada diabaikan, dan jumlah yang dilaporkan hanya yang benar
// benar terhapus.
func TestAIDBDelModelsBatch(t *testing.T) {
	dir := t.TempDir()
	if err := InitAIDB(filepath.Join(dir, "ai.db")); err != nil {
		t.Fatalf("InitAIDB: %v", err)
	}
	defer AI().Close()
	if err := InitAISettingsDB(); err != nil {
		t.Fatalf("InitAISettingsDB: %v", err)
	}
	ctx := context.Background()

	if err := AI().AddProvider(ctx, "tesprov", "https://contoh.id/v1/chat/completions"); err != nil {
		t.Fatalf("AddProvider: %v", err)
	}
	for _, m := range []string{"model-a", "model-b", "model-c"} {
		if err := AI().AddModel(ctx, "tesprov", m); err != nil {
			t.Fatalf("AddModel %s: %v", m, err)
		}
	}

	n, err := AI().DelModels(ctx, "tesprov", []string{"model-a", "model-c", "model-hilang"})
	if err != nil {
		t.Fatalf("DelModels: %v", err)
	}
	if n != 2 {
		t.Fatalf("jumlah terhapus salah: %d, ingin 2", n)
	}
	ms, err := AI().ListModels(ctx, "tesprov")
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(ms) != 1 || ms[0] != "model-b" {
		t.Fatalf("sisa model salah: %v", ms)
	}

	// Batch kosong = tidak menyentuh apa pun.
	if n, err := AI().DelModels(ctx, "tesprov", nil); err != nil || n != 0 {
		t.Fatalf("batch kosong harus no-op: n=%d err=%v", n, err)
	}
}
