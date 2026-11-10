package geminiapi

import (
	"context"
	"errors"
	"os"
	"testing"
)

// TestAskEmptyAnswerSavesDump memastikan jawaban kosong meninggalkan bukti di
// folder temp, bukan hanya error tanpa jejak. Isi stream seperti ini yang
// menunjukkan protokol berubah.
func TestAskEmptyAnswerSavesDump(t *testing.T) {
	dir := t.TempDir()
	oldDump := rawDumpDir
	rawDumpDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { rawDumpDir = oldDump })

	f := newFakeGemini(t, "")
	f.mu.Lock()
	f.chatBody = ")]}'\n\n[[\"e\",4,null]]\n"
	f.mu.Unlock()

	c := New()
	_, err := c.Ask(context.Background(), "halo")
	if !errors.Is(err, ErrEmpty) {
		t.Fatalf("error = %v, mau ErrEmpty", err)
	}

	path := c.LastDumpPath()
	if path == "" {
		t.Fatal("dump kosong: tidak ada path")
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("dump tidak ada di disk: %v", statErr)
	}
	body, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("baca dump: %v", readErr)
	}
	if len(body) == 0 {
		t.Fatal("dump kosong")
	}
}
