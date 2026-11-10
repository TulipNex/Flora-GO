package plugins

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/polymorfa/hypermeow/util/cbcutil"
)

// TestUploadReaderSameFileEncryptsInPlace mengunci asumsi jalur streaming
// upload: cbcutil.EncryptStream boleh membaca plaintext dan menulis
// ciphertext ke FILE YANG SAMA (yang dilakukan UploadReader saat kita
// mengirim f, f). Kalau asumsi ini rusak (mis. library berubah), berkas
// besar bisa terkirim korup - jadi diuji langsung, bukan diasumsikan.
func TestUploadReaderSameFileEncryptsInPlace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "media.bin")

	plain := make([]byte, 3*32*1024+123) // beberapa blok + sisa
	if _, err := rand.Read(plain); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, plain, 0600); err != nil {
		t.Fatal(err)
	}

	key := make([]byte, 32)
	iv := make([]byte, 16)
	macKey := make([]byte, 32)
	for _, b := range [][]byte{key, iv, macKey} {
		if _, err := rand.Read(b); err != nil {
			t.Fatal(err)
		}
	}

	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	plainHash, cipherHash, fileLen, uploadLen, err := cbcutil.EncryptStream(key, iv, macKey, f, f)
	if err != nil {
		t.Fatalf("EncryptStream: %v", err)
	}
	if fileLen != uint64(len(plain)) {
		t.Fatalf("FileLength = %d, mau %d", fileLen, len(plain))
	}
	wantPlain := sha256.Sum256(plain)
	if !bytes.Equal(plainHash, wantPlain[:]) {
		t.Fatal("hash plaintext tidak cocok - isi file berubah saat dibaca")
	}

	// Ciphertext di file harus persis uploadLen byte (padding + 10 MAC).
	got := make([]byte, uploadLen)
	if _, err := f.ReadAt(got, 0); err != nil && err != io.EOF {
		t.Fatalf("baca ciphertext: %v", err)
	}
	if bytes.Equal(got[:len(plain)], plain) {
		t.Fatal("isi file masih plaintext - enkripsi in-place tidak terjadi")
	}
	sum := sha256.Sum256(got)
	if !bytes.Equal(cipherHash, sum[:]) {
		t.Fatal("hash ciphertext tidak cocok dengan isi file")
	}

	// Dekripsi membuktikan isinya tidak korup (bukan sekadar 'berbeda').
	// 10 byte terakhir adalah MAC, bukan ciphertext.
	dec, err := cbcutil.Decrypt(key, iv, got[:len(got)-10])
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(dec, plain) {
		t.Fatal("hasil dekripsi tidak sama dengan plaintext asli")
	}
}
