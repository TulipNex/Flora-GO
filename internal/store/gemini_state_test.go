package store

import (
	"context"
	"path/filepath"
	"testing"
)

// TestGeminiStateRoundTrip memastikan state percakapan Gemini (kunci
// gemini_hist:*) bisa disimpan, dibaca, ditimpa, dan dihapus.
func TestGeminiStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "group.db")
	if err := InitGroupDB(path); err != nil {
		t.Fatal(err)
	}
	defer Groups().db.Close()

	g := Groups()
	ctx := context.Background()
	const key = "gemini_hist:0123456789abcdef"

	got, err := g.GetGeminiState(ctx, key)
	if err != nil {
		t.Fatalf("baca kosong: %v", err)
	}
	if got != "" {
		t.Fatalf("kunci belum ada harus string kosong, dapat %q", got)
	}

	if err := g.SetGeminiState(ctx, key, `{"turns":[{"user":"halo","bot":"hai"}],"at":1700000000}`); err != nil {
		t.Fatalf("simpan: %v", err)
	}
	got, err = g.GetGeminiState(ctx, key)
	if err != nil {
		t.Fatalf("baca: %v", err)
	}
	if got != `{"turns":[{"user":"halo","bot":"hai"}],"at":1700000000}` {
		t.Fatalf("nilai = %q", got)
	}

	// Menimpa (bukan menambah baris kedua).
	if err := g.SetGeminiState(ctx, key, `{"at":1700000001}`); err != nil {
		t.Fatalf("timpa: %v", err)
	}
	got, err = g.GetGeminiState(ctx, key)
	if err != nil {
		t.Fatalf("baca setelah timpa: %v", err)
	}
	if got != `{"at":1700000001}` {
		t.Fatalf("nilai setelah timpa = %q", got)
	}

	// Nilai kosong = hapus baris.
	if err := g.SetGeminiState(ctx, key, "  "); err != nil {
		t.Fatalf("hapus lewat nilai kosong: %v", err)
	}
	got, err = g.GetGeminiState(ctx, key)
	if err != nil {
		t.Fatalf("baca setelah hapus: %v", err)
	}
	if got != "" {
		t.Fatalf("setelah hapus harus kosong, dapat %q", got)
	}

	// Menghapus kunci yang tidak ada bukan error.
	if err := g.DeleteGeminiState(ctx, key); err != nil {
		t.Fatalf("hapus kunci tidak ada: %v", err)
	}
}
