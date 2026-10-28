package plugins

import (
	"os"
	"path/filepath"
	"testing"
)

// TestVideoCounterCountsFrames memastikan sink video hanya menghitung
// frame/byte (tidak menulis file) dan berhenti menghitung setelah Close.
func TestVideoCounterCountsFrames(t *testing.T) {
	c := &videoCounter{}
	frames := [][]byte{
		{1, 2, 3},
		make([]byte, 10),
		{4, 5},
	}
	for _, f := range frames {
		if err := c.WriteVideo(f); err != nil {
			t.Fatalf("WriteVideo: %v", err)
		}
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Tulisan setelah Close harus diabaikan (kontrak VideoSink).
	if err := c.WriteVideo([]byte{9, 9}); err != nil {
		t.Fatalf("WriteVideo setelah Close: %v", err)
	}
	n, bytes := c.stats()
	if n != 3 {
		t.Errorf("frames = %d, want 3", n)
	}
	if bytes != 15 {
		t.Errorf("bytes = %d, want 15", bytes)
	}
	// Close ganda aman.
	if err := c.Close(); err != nil {
		t.Fatalf("Close kedua: %v", err)
	}
}

// TestCleanStaleCallTemp memastikan sisa rekaman/transcode panggilan dihapus
// saat startup, sementara file temp milik fitur lain tidak ikut terhapus.
func TestCleanStaleCallTemp(t *testing.T) {
	t.Chdir(t.TempDir())
	tmp := filepath.Join(".", "wabot-tmp")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	callFiles := []string{
		"vcall-628123-1700000000.h264",
		"vcall-audio-1700000000000.wav",
	}
	callDirs := []string{"vcall-abc123", "vcall-aud-def456"}
	keepFiles := []string{"wabot-dl-keep.mp4", "catatan.txt"}

	for _, name := range callFiles {
		if err := os.WriteFile(filepath.Join(tmp, name), []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	for _, name := range callDirs {
		if err := os.MkdirAll(filepath.Join(tmp, name), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(tmp, name, "input.mp4"), []byte("x"), 0o600); err != nil {
			t.Fatalf("write isi %s: %v", name, err)
		}
	}
	for _, name := range keepFiles {
		if err := os.WriteFile(filepath.Join(tmp, name), []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	CleanStaleCallTemp()

	for _, name := range append(append([]string{}, callFiles...), callDirs...) {
		if _, err := os.Stat(filepath.Join(tmp, name)); !os.IsNotExist(err) {
			t.Errorf("%s masih ada setelah cleanup (err=%v)", name, err)
		}
	}
	for _, name := range keepFiles {
		if _, err := os.Stat(filepath.Join(tmp, name)); err != nil {
			t.Errorf("%s ikut terhapus: %v", name, err)
		}
	}
}
