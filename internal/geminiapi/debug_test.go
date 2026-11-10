package geminiapi

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDebugPage hanya alat bantu saat protokol halaman berubah: menyimpan
// halaman /app (yang dibuka dengan cookie tamu) ke %TEMP%/gemini-app.html.
// Jalankan:
//
//	GEMINI_DEBUG=1 go test ./internal/geminiapi/ -run TestDebugPage -v
func TestDebugPage(t *testing.T) {
	if strings.TrimSpace(os.Getenv("GEMINI_DEBUG")) == "" {
		t.Skip("set GEMINI_DEBUG=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	c := New()
	cookie, err := c.fetchGuestCookie(ctx)
	if err != nil {
		t.Fatalf("cookie: %v", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+appPath, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("User-Agent", defaultUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "id-ID,id;q=0.9,en-US;q=0.8,en;q=0.7")
	req.Header.Set("Cookie", cookie)

	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))

	path := filepath.Join(os.TempDir(), "gemini-app.html")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("tulis: %v", err)
	}
	t.Logf("status %d, %d byte → %s", resp.StatusCode, len(body), path)
	t.Logf("bl=%q sid=%q", firstMatch(buildRe, string(body)), firstMatch(sidRe, string(body)))
}
