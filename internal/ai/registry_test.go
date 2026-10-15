package ai

import (
	"context"
	"path/filepath"
	"testing"

	"wabot-go/internal/store"
)

// TestAIRegistryGuards memverifikasi guard DelProvider/DelModel di layer ai
// dengan DB temporer.
func TestAIRegistryGuards(t *testing.T) {
	dir := t.TempDir()
	if err := store.InitAIDB(filepath.Join(dir, "ai.db")); err != nil {
		t.Fatalf("InitAIDB: %v", err)
	}
	defer store.AI().Close()
	if err := store.InitAISettingsDB(); err != nil {
		t.Fatalf("InitAISettingsDB: %v", err)
	}
	ctx := context.Background()
	invalidateSettingsCache()

	if err := DelProvider(ctx, "ciora"); err == nil {
		t.Fatal("hapus provider aktif harus error")
	}
	if err := AddProvider(ctx, "Add", "https://x.id/v1/chat/completions"); err == nil {
		t.Fatal("nama reserved harus error")
	}
	if err := AddProvider(ctx, "Bad Name!", "https://x.id/v1/chat/completions"); err == nil {
		t.Fatal("nama invalid harus error")
	}
	if err := AddProvider(ctx, "baru", "bukan-url"); err == nil {
		t.Fatal("endpoint invalid harus error")
	}
	if err := AddProvider(ctx, "baru", "https://baru.id/v1/chat/completions"); err != nil {
		t.Fatalf("AddProvider: %v", err)
	}
	if err := SetActiveProvider(ctx, "baru"); err != nil {
		t.Fatalf("SetActiveProvider: %v", err)
	}
	if got := GetActiveProviderName(ctx); got != "baru" {
		t.Fatalf("active provider salah: %s", got)
	}
	if err := DelProvider(ctx, "baru"); err == nil {
		t.Fatal("hapus provider aktif harus error")
	}
	if err := SetActiveProvider(ctx, "ciora"); err != nil {
		t.Fatalf("SetActiveProvider ciora: %v", err)
	}
	if err := DelProvider(ctx, "baru"); err != nil {
		t.Fatalf("DelProvider: %v", err)
	}
	if err := DelModel(ctx, "ciora-ai-free"); err == nil {
		t.Fatal("hapus model aktif harus error")
	}
	if err := SetActiveModel(ctx, "cohere/north-mini-code-free"); err != nil {
		t.Fatalf("SetActiveModel: %v", err)
	}
	if err := DelModel(ctx, "ciora-ai-free"); err != nil {
		t.Fatalf("DelModel non-aktif: %v", err)
	}
	if got := GetActiveModelName(ctx); got != "cohere/north-mini-code-free" {
		t.Fatalf("active model salah: %s", got)
	}
	if ListProviders(ctx)[0].Name != "ciora" {
		t.Fatal("urutan provider salah")
	}
	if k := providerAPIKey("openagentic"); k != "" {
		t.Fatalf("env tak diset harus kosong, dapat %q", k)
	}
	t.Setenv("OPENAGENTIC_API_KEY", "sk-test")
	if k := providerAPIKey("openagentic"); k != "sk-test" {
		t.Fatalf("aturan env salah: %q", k)
	}
}
