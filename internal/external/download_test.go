package external

import (
	"bytes"
	"strings"
	"testing"
)

func TestReadAllLimitWithKnownSize(t *testing.T) {
	payload := bytes.Repeat([]byte("a"), 1000)
	got, err := readAllLimit(bytes.NewReader(payload), 2000, int64(len(payload)))
	if err != nil {
		t.Fatalf("readAllLimit: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("isi berubah: %d byte", len(got))
	}
}

// Server yang mengirim lebih banyak dari Content-Length tidak boleh membuat
// buffer tumbuh melewati cap.
func TestReadAllLimitRejectsBeyondCap(t *testing.T) {
	payload := bytes.Repeat([]byte("b"), 5000)
	if _, err := readAllLimit(bytes.NewReader(payload), 1000, 10); err == nil {
		t.Fatal("harus error saat body melewati cap")
	} else if !strings.Contains(err.Error(), "terlalu besar") {
		t.Fatalf("pesan tidak sesuai: %v", err)
	}
}

func TestReadAllLimitRejectsDeclaredSizeBeforeRead(t *testing.T) {
	// Content-Length > cap: tidak boleh membaca body sama sekali.
	if _, err := readAllLimit(strings.NewReader("x"), 100, 101); err == nil {
		t.Fatal("harus langsung error dari Content-Length")
	}
}

// Body yang lebih pendek dari Content-Length adalah media terpotong, bukan
// sukses dengan sebagian data (dulu io.ReadAll melaporkan io.ErrUnexpectedEOF).
func TestReadAllLimitRejectsTruncatedBody(t *testing.T) {
	payload := bytes.Repeat([]byte("t"), 100)
	_, err := readAllLimit(bytes.NewReader(payload), 1000, 500)
	if err == nil {
		t.Fatal("harus error saat body terpotong")
	}
	if !strings.Contains(err.Error(), "terpotong") {
		t.Fatalf("pesan tidak sesuai: %v", err)
	}
}

func TestReadAllLimitUnknownSize(t *testing.T) {
	payload := bytes.Repeat([]byte("c"), 999)
	got, err := readAllLimit(bytes.NewReader(payload), 1000, -1)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("unknown size gagal: err=%v len=%d", err, len(got))
	}
	if _, err := readAllLimit(bytes.NewReader(bytes.Repeat([]byte("d"), 1001)), 1000, -1); err == nil {
		t.Fatal("harus error saat melewati cap")
	}
}

func TestReadAllLimitExactCapAndEmpty(t *testing.T) {
	payload := bytes.Repeat([]byte("e"), 100)
	got, err := readAllLimit(bytes.NewReader(payload), 100, 100)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("tepat di cap: err=%v len=%d", err, len(got))
	}
	empty, err := readAllLimit(bytes.NewReader(nil), 100, -1)
	if err != nil || len(empty) != 0 {
		t.Fatalf("body kosong: err=%v len=%d", err, len(empty))
	}
}
