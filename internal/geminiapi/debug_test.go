package geminiapi

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestDebugPage hanya alat bantu saat protokol halaman berubah. Jalankan:
//
//	GEMINI_DEBUG=1 go test ./internal/geminiapi/ -run TestDebugPage -v
func TestDebugPage(t *testing.T) {
	if strings.TrimSpace(os.Getenv("GEMINI_DEBUG")) == "" {
		t.Skip("set GEMINI_DEBUG=1")
	}
	path := filepath.Join("..", "..", "data", "gemini-cookie.txt")
	cs, err := LoadCookieFile(path)
	if err != nil {
		t.Fatalf("cookie: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://gemini.google.com/app", nil)
	applyBrowserHeaders(req, cs)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	html := string(body)
	out := filepath.Join(os.TempDir(), "gemini-app.html")
	_ = os.WriteFile(out, body, 0o600)
	t.Logf("status=%d len=%d file=%s", resp.StatusCode, len(html), out)

	for _, pat := range []string{
		`boq_assistant[A-Za-z0-9_.\-]*`,
		`"bl":"[^"]{0,80}"`,
		`cfb2h`,
		`SNlM0e`,
		`FdrFJe`,
		`qKIAYe`,
		`BardChatUi`,
		`StreamGenerate`,
		`assistant\.lamda`,
	} {
		re := regexp.MustCompile(pat)
		matches := re.FindAllString(html, 5)
		t.Logf("%-28s %d %v", pat, len(matches), truncateList(matches, 120))
	}
}

func truncateList(in []string, n int) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if len(s) > n {
			s = s[:n] + "…"
		}
		out = append(out, s)
	}
	return out
}
