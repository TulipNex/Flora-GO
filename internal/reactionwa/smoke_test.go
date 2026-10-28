package reactionwa

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestSmoke dijalankan manual di panel/laptop (tidak ikut `go test` biasa):
//
//	RWA_SMOKE=1 go test ./internal/reactionwa/ -run TestSmoke -v
//	    -> hanya memastikan Chrome/Xvfb bisa membuat token Turnstile.
//
//	RWA_SMOKE=2 RWA_REFRESH=<refresh token> RWA_SMOKE_LINK=<link pesan> \
//	    go test ./internal/reactionwa/ -run TestSmoke -v
//	    -> satu kirim sungguhan (memakai jatah akun).
//
// RWA_REFRESH ditulis ke token file di direktori tes sementara, jadi kredensial
// tidak tertinggal di data/ saat hanya menguji.
func TestSmoke(t *testing.T) {
	mode := os.Getenv("RWA_SMOKE")
	if mode == "" {
		t.Skip("set RWA_SMOKE=1 (captcha) atau 2 (kirim sungguhan)")
	}
	t.Chdir(t.TempDir())
	if rt := os.Getenv("RWA_REFRESH"); rt != "" {
		if err := WriteRefreshToken(rt); err != nil {
			t.Fatalf("simpan refresh token: %v", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	b := newBrowser()
	tok, err := b.Token(ctx)
	if err != nil {
		t.Fatalf("captcha: %v", err)
	}
	if len(tok) < 100 {
		t.Fatalf("token Turnstile terlalu pendek: %d", len(tok))
	}
	t.Logf("token Turnstile OK (%d karakter)", len(tok))

	if mode == "1" {
		return
	}
	link := os.Getenv("RWA_SMOKE_LINK")
	if link == "" {
		t.Fatal("RWA_SMOKE_LINK wajib untuk mode 2")
	}
	c := newClient(apiURLFromEnv(), defaultTokenURL, b, time.Now)
	c.http = &http.Client{Timeout: requestTimeout}
	res, err := c.React(ctx, link, []string{"😭"})
	if err != nil {
		t.Fatalf("React: %v", err)
	}
	t.Logf("terkirim: message=%q task=%+v vip=%+v", res.Message, res.Task, res.VIP)
}
