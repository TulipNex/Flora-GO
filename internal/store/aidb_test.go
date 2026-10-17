package store

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
)

// TestAppendHistoryCapsPerSession memverifikasi plafon aiHistoryMaxPerSession:
// menyimpan jauh lebih banyak dari batas hanya menyisakan N baris terbaru
// untuk kunci itu, dan kunci lain tidak ikut terpangkas. DB temporer — TIDAK
// menyentuh data/.
func TestAppendHistoryCapsPerSession(t *testing.T) {
	dir := t.TempDir()
	if err := InitAIDB(filepath.Join(dir, "ai.db")); err != nil {
		t.Fatalf("InitAIDB: %v", err)
	}
	defer AI().Close()

	ctx := context.Background()
	const key = "chat@g.us:user@s.whatsapp.net"
	other := "user2@s.whatsapp.net"

	total := aiHistoryMaxPerSession + 25
	for i := 0; i < total; i++ {
		if err := AI().AppendHistory(ctx, key, "user", "pesan-"+strconv.Itoa(i)); err != nil {
			t.Fatalf("AppendHistory ke-%d: %v", i, err)
		}
	}
	if err := AI().AppendHistory(ctx, other, "user", "lain"); err != nil {
		t.Fatalf("AppendHistory kunci lain: %v", err)
	}

	var n int
	if err := AI().db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM ai_history WHERE session_key = ?`, key).Scan(&n); err != nil {
		t.Fatalf("hitung baris: %v", err)
	}
	if n != aiHistoryMaxPerSession {
		t.Fatalf("baris tersimpan = %d, mau %d", n, aiHistoryMaxPerSession)
	}

	// Yang tersisa harus yang TERBARU: entri terakhir = "pesan-<total-1>",
	// dan yang terlama tersisa = "pesan-<total-N>".
	var last, first string
	if err := AI().db.QueryRowContext(ctx,
		`SELECT content FROM ai_history WHERE session_key = ? ORDER BY id DESC LIMIT 1`, key).Scan(&last); err != nil {
		t.Fatalf("baca terbaru: %v", err)
	}
	if want := "pesan-" + strconv.Itoa(total-1); last != want {
		t.Fatalf("baris terbaru = %q, mau %q", last, want)
	}
	if err := AI().db.QueryRowContext(ctx,
		`SELECT content FROM ai_history WHERE session_key = ? ORDER BY id ASC LIMIT 1`, key).Scan(&first); err != nil {
		t.Fatalf("baca terlama: %v", err)
	}
	if want := "pesan-" + strconv.Itoa(total-aiHistoryMaxPerSession); first != want {
		t.Fatalf("baris terlama = %q, mau %q", first, want)
	}

	// Kunci lain tak tersentuh plafon kunci pertama.
	if err := AI().db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM ai_history WHERE session_key = ?`, other).Scan(&n); err != nil {
		t.Fatalf("hitung kunci lain: %v", err)
	}
	if n != 1 {
		t.Fatalf("baris kunci lain = %d, mau 1", n)
	}

	keys, err := AI().ListHistorySessionKeys(ctx)
	if err != nil {
		t.Fatalf("ListHistorySessionKeys: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("kunci sesi = %v, mau 2", keys)
	}
}
