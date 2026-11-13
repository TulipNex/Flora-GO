package ai

import (
	"context"
	"path/filepath"
	"strings"
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

// TestDelModelsBatch memverifikasi hapus model lewat nomor urut (bisa banyak
// sekaligus): nomor diresolusi terhadap SNAPSHOT daftar sebelum penghapusan,
// acuan boleh dicampur nama, dan acuan bermasalah hanya dilewati — bukan
// membatalkan seluruh batch.
func TestDelModelsBatch(t *testing.T) {
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

	// Pindahkan model aktif ke nomor 2 supaya nomor 1 bebas dihapus.
	if err := SetActiveModel(ctx, "ciora-coding-free"); err != nil {
		t.Fatalf("SetActiveModel: %v", err)
	}

	// "1" dipakai dua kali (dedupe), "99" di luar rentang (dilewati),
	// "ciora-vision-free" diketik beda kapitalisasi (tetap kena).
	res, err := DelModels(ctx, []string{"1", "1", "99", "ciora-vision-free", "CIORA-VISION-FREE"})
	if err != nil {
		t.Fatalf("DelModels: %v", err)
	}
	if got, want := res.Deleted, []string{"ciora-ai-free", "ciora-vision-free"}; !sameStrings(got, want) {
		t.Fatalf("deleted salah: %v, ingin %v", got, want)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Ref != "99" {
		t.Fatalf("skipped salah: %+v (ingin hanya 99 di luar rentang)", res.Skipped)
	}
	if !strings.Contains(res.Skipped[0].Reason, "di luar rentang 1-6") {
		t.Fatalf("alasan out-of-range salah: %q", res.Skipped[0].Reason)
	}

	// Nomor mengikuti snapshot daftar: setelah penghapusan, no. 1 =
	// ciora-coding-free (aktif → dilewati), no. 3 = poolside/free (dihapus).
	res, err = DelModels(ctx, []string{"1", "3"})
	if err != nil {
		t.Fatalf("DelModels kedua: %v", err)
	}
	if got, want := res.Deleted, []string{"poolside/laguna-s-2.1-free"}; !sameStrings(got, want) {
		t.Fatalf("deleted kedua salah: %v, ingin %v", got, want)
	}
	if len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0].Reason, "sedang aktif") {
		t.Fatalf("model aktif harus dilewati: %+v", res.Skipped)
	}

	// Batch yang menyapu semua nomor tetap menyisakan model aktif.
	res, err = DelModels(ctx, []string{"1", "2", "3"})
	if err != nil {
		t.Fatalf("DelModels ketiga: %v", err)
	}
	if len(res.Deleted) != 2 || len(res.Skipped) != 1 {
		t.Fatalf("sapu semua harus menyisakan model aktif: %+v", res)
	}
	if models := ListModels(ctx, "ciora"); len(models) != 1 || models[0] != "ciora-coding-free" {
		t.Fatalf("sisa model salah: %v", models)
	}
	if got := GetActiveModelName(ctx); got != "ciora-coding-free" {
		t.Fatalf("model aktif harus tetap: %s", got)
	}

	// Model terakhir tidak bisa dihapus.
	res, err = DelModels(ctx, []string{"1"})
	if err != nil {
		t.Fatalf("DelModels terakhir: %v", err)
	}
	if len(res.Deleted) != 0 || len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0].Reason, "satu-satunya") {
		t.Fatalf("model terakhir harus ditolak: %+v", res)
	}

	// Nama tak dikenal juga dilewati, bukan error.
	res, err = DelModels(ctx, []string{"tidak-ada-model-ini"})
	if err != nil {
		t.Fatalf("DelModels nama asing: %v", err)
	}
	if len(res.Deleted) != 0 || len(res.Skipped) != 1 || res.Skipped[0].Reason != "tidak ada di daftar model" {
		t.Fatalf("nama asing harus dilewati: %+v", res)
	}
}

// sameStrings membandingkan dua slice string secara berurutan.
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
