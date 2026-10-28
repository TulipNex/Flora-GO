package geminiapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestChatEmptyAnswerSavesDump memastikan jawaban kosong (mis. giliran dengan
// lampiran) meninggalkan bukti di folder temp, bukan hanya error tanpa jejak.
func TestChatEmptyAnswerSavesDump(t *testing.T) {
	dir := t.TempDir()
	oldDump, oldChat := rawDumpDir, chatEndpoint
	rawDumpDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() {
		rawDumpDir = oldDump
		chatEndpoint = oldChat
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rotate":
			w.WriteHeader(http.StatusOK)
		case "/app":
			_, _ = w.Write([]byte(appTokenPage))
		case "/chat":
			_, _ = w.Write([]byte(")]}'\n\n[[\"e\",4,null]]\n"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	pointAtServer(t, srv)
	chatEndpoint = srv.URL + "/chat"

	c := newLoadedClient(t, "__Secure-1PSID=s1")
	_, err := c.Chat(context.Background(), Request{Prompt: "halo"})
	if !errors.Is(err, ErrEmpty) {
		t.Fatalf("err = %v, ingin ErrEmpty", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("baca folder dump: %v", err)
	}
	var dump string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "gemini-empty-") {
			dump = filepath.Join(dir, e.Name())
		}
	}
	if dump == "" {
		t.Fatalf("tidak ada dump di %s (%d berkas)", dir, len(entries))
	}
	body, err := os.ReadFile(dump)
	if err != nil {
		t.Fatalf("baca dump: %v", err)
	}
	text := string(body)
	if !strings.Contains(text, "jawaban kosong") {
		t.Fatalf("dump tanpa catatan: %q", text)
	}
	if !strings.Contains(text, `"e",4`) {
		t.Fatalf("dump tanpa penanda stream: %q", text)
	}
	if got := c.LastDumpPath(); got != dump {
		t.Fatalf("LastDumpPath = %q, ingin %q", got, dump)
	}
}

// TestWriteRawDumpFailSoft: dump tidak boleh mengganggu alur saat folder tidak
// tersedia.
func TestWriteRawDumpFailSoft(t *testing.T) {
	old := rawDumpDir
	rawDumpDir = func() (string, error) { return filepath.Join(t.TempDir(), "tidak", "ada", "x"), nil }
	t.Cleanup(func() { rawDumpDir = old })

	c := New(newCookieFile(t, "__Secure-1PSID=s1"))
	if path := c.writeRawDump("gemini-empty", "uji", []byte("isi"), 0); path != "" {
		t.Fatalf("path = %q, ingin kosong", path)
	}
	if c.LastDumpPath() != "" {
		t.Fatal("LastDumpPath harus kosong saat tulisan gagal")
	}
}
