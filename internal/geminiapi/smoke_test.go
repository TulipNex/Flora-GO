package geminiapi

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// Smoke test live ke Gemini mode tamu. Tidak jalan pada `go test ./...` biasa
// karena memakai kuota (dan jaringan) sungguhan. Jalankan manual:
//
//	GEMINI_SMOKE=1 go test ./internal/geminiapi/ -run TestSmoke -v   (satu tanya)
//	GEMINI_SMOKE=2 go test ./internal/geminiapi/ -run TestSmoke -v   (dua giliran + injeksi riwayat)
//	GEMINI_SMOKE=3 go test ./internal/geminiapi/ -run TestSmoke -v   (diag: cookie → meta → chat)
func TestSmoke(t *testing.T) {
	level := strings.TrimSpace(os.Getenv("GEMINI_SMOKE"))
	if level == "" || level == "0" {
		t.Skip("set GEMINI_SMOKE=1..3 untuk uji live")
	}
	client := New()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	switch level {
	case "1":
		res, err := client.Ask(ctx, "Sebutkan tiga warna dalam satu baris.")
		if err != nil {
			t.Fatalf("ask: %v", err)
		}
		t.Logf("jawaban: %s", res.Text)
		if strings.TrimSpace(res.Text) == "" {
			t.Fatal("jawaban kosong")
		}

	case "2":
		// Mode tamu tidak bisa melanjutkan percakapan di server, jadi riwayat
		// dibawa di dalam prompt — persis seperti plugins/gemini.go.
		first, err := client.Ask(ctx, "Nama saya Raka. Jawab singkat.")
		if err != nil {
			t.Fatalf("ask pertama: %v", err)
		}
		t.Logf("giliran 1: %s", first.Text)

		prompt := "Percakapan sebelumnya:\n" +
			"User: Nama saya Raka. Jawab singkat.\n" +
			"Gemini: " + first.Text + "\n\n" +
			"User: Siapa nama saya? Jawab satu kata."
		second, err := client.Ask(ctx, prompt)
		if err != nil {
			t.Fatalf("ask kedua: %v", err)
		}
		t.Logf("giliran 2: %s", second.Text)
		if !strings.Contains(strings.ToLower(second.Text), "raka") {
			t.Fatalf("riwayat tidak terbaca: %q", second.Text)
		}

	case "3":
		d := client.Diag(ctx)
		t.Logf("cookie: ok=%v %s", d.Cookie.OK, d.Cookie.Detail)
		t.Logf("meta: ok=%v %s", d.Meta.OK, d.Meta.Detail)
		t.Logf("chat: ok=%v %s (%s)", d.Chat.OK, d.Chat.Detail, d.Latency.Round(time.Millisecond))
		if d.Answer != "" {
			t.Logf("jawaban: %s", d.Answer)
		}
		if d.Dump != "" {
			t.Logf("dump: %s", d.Dump)
		}
		if !d.Cookie.OK || !d.Meta.OK || !d.Chat.OK {
			t.Fatal("diag gagal")
		}

	default:
		t.Fatalf("GEMINI_SMOKE=%s tidak dikenal (pakai 1..3)", level)
	}
}
