package geminiapi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Smoke test live ke Gemini. Tidak jalan pada `go test ./...` biasa karena
// memakai cookie asli dan kuota akun. Jalankan manual:
//
//	GEMINI_SMOKE=1 go test ./internal/geminiapi/ -run TestSmoke -v
//	GEMINI_SMOKE=2 go test ./internal/geminiapi/ -run TestSmoke -v   (unggah gambar)
//	GEMINI_SMOKE=3 go test ./internal/geminiapi/ -run TestSmoke -v   (minta gambar)
//	GEMINI_SMOKE=4 go test ./internal/geminiapi/ -run TestSmoke -v   (percakapan 2 giliran)
func TestSmoke(t *testing.T) {
	level := strings.TrimSpace(os.Getenv("GEMINI_SMOKE"))
	if level == "" || level == "0" {
		t.Skip("set GEMINI_SMOKE=1..4 untuk uji live")
	}
	cookiePath := filepath.Join("..", "..", "data", "gemini-cookie.txt")
	if v := strings.TrimSpace(os.Getenv("GEMINI_COOKIE_PATH")); v != "" {
		cookiePath = v
	}
	client := New(cookiePath)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	if err := client.LoadCookies(); err != nil {
		t.Fatalf("muat cookie: %v", err)
	}
	cs, _, _, _ := client.snapshot()
	t.Logf("cookie: %s", cs)

	if err := client.rotate(ctx, true); err != nil {
		t.Fatalf("rotasi: %v", err)
	}
	t.Logf("rotasi ok")
	if err := client.ensureMeta(ctx); err != nil {
		t.Fatalf("meta: %v", err)
	}
	_, at, bl, sid := client.snapshot()
	t.Logf("at=%s… bl=%s sid=%s push=%s", trunc(at, 8), bl, trunc(sid, 6), client.pushIDValue())

	switch level {
	case "2", "4":
		f, err := client.Upload(ctx, "uji.png", "image/png", tinyPNG(t))
		if err != nil {
			t.Fatalf("unggah: %v", err)
		}
		t.Logf("lampiran terunggah: %s", trunc(f.ID, 40))
		req := Request{Prompt: "Sebutkan warna dominan gambar ini dalam satu kata, atau 'tidak tahu' kalau bukan gambar.", Files: []File{f}}
		if level == "4" {
			req.Prompt = "Halo, balas satu kata."
		}
		res, err := client.Chat(ctx, req)
		if err != nil {
			t.Fatalf("chat lampiran: %v", err)
		}
		t.Logf("jawaban: %q gambar=%d conv=%s", trunc(res.Text, 300), len(res.Images), trunc(convID(res), 10))
		if level == "4" {
			conv := res.Conv
			if conv == nil {
				t.Fatal("conv nil")
			}
			for turn := 1; turn <= 2; turn++ {
				conv.Turn = turn
				res2, err := client.Chat(ctx, Request{Prompt: "Ingat topik sebelumnya? Balas satu kalimat.", Conv: conv})
				if err != nil {
					t.Fatalf("chat giliran %d: %v", turn+1, err)
				}
				t.Logf("jawaban giliran %d: %q conv=%s", turn+1, trunc(res2.Text, 300), trunc(convID(res2), 10))
				if res2.Conv == nil || res2.Conv.ID == "" {
					t.Fatalf("conv id giliran %d kosong", turn+1)
				}
				conv = res2.Conv
			}
		}
	case "3":
		res, err := client.Chat(ctx, Request{Prompt: "Buatkan gambar kucing oranye memakai topi biru, gaya ilustrasi sederhana."})
		if err != nil {
			t.Fatalf("chat gambar: %v", err)
		}
		t.Logf("teks: %q gambar=%d", trunc(res.Text, 300), len(res.Images))
		for _, u := range res.Images {
			t.Logf("gambar: %s", u)
		}
		if len(res.Images) == 0 {
			t.Fatal("tidak ada URL gambar")
		}
	default:
		res, err := client.Chat(ctx, Request{Prompt: "Balas tepat satu kata: halo"})
		if err != nil {
			t.Fatalf("chat: %v", err)
		}
		t.Logf("jawaban: %q gambar=%d conv=%s resp=%s", trunc(res.Text, 300), len(res.Images), trunc(convID(res), 10), trunc(respID(res), 10))
		if res.Conv == nil {
			t.Fatal("conv nil")
		}
	}
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func convID(r *Result) string {
	if r.Conv == nil {
		return ""
	}
	return r.Conv.ID
}

func respID(r *Result) string {
	if r.Conv == nil {
		return ""
	}
	return r.Conv.ResponseID
}
