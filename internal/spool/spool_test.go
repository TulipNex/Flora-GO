package spool

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func withTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	Configure(dir)
	t.Cleanup(func() { Configure("") })
	return dir
}

func TestReadFromBoundaryKeepsLimitInMemory(t *testing.T) {
	withTempDir(t)
	const limit = 4096
	payload := bytes.Repeat([]byte("m"), limit)

	blob, err := ReadFrom(bytes.NewReader(payload), limit)
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	defer blob.Close()
	if !blob.InMemory() {
		t.Fatalf("isi tepat %d byte harus tetap di RAM", limit)
	}
	if blob.Size() != limit {
		t.Fatalf("Size = %d, mau %d", blob.Size(), limit)
	}

	// Satu byte di atas ambang harus pindah ke file.
	blob2, err := ReadFrom(bytes.NewReader(append(payload, 'x')), limit)
	if err != nil {
		t.Fatalf("ReadFrom(+1): %v", err)
	}
	defer blob2.Close()
	if blob2.InMemory() {
		t.Fatal("isi limit+1 byte harus pindah ke file")
	}
	if blob2.Size() != limit+1 {
		t.Fatalf("Size = %d, mau %d", blob2.Size(), limit+1)
	}
}

// zeroReader menghasilkan byte nol tanpa mengalokasikan buffer besar; dipakai
// untuk membuktikan pembacaan besar tidak menyerap seluruh isi ke memori.
type zeroReader struct{ buf [32 << 10]byte }

func (z *zeroReader) Read(p []byte) (int, error) {
	return copy(p, z.buf[:]), nil
}

// TestReadFromLargeStreamDoesNotBufferWholeFile adalah penjaga OOM: alokasi
// untuk aliran 16MB dengan ambang 1MB harus tetap kecil (buffer ambang +
// salinan 32KB), bukan tumbuh mengikuti ukuran berkas seperti io.ReadAll.
func TestReadFromLargeStreamDoesNotBufferWholeFile(t *testing.T) {
	withTempDir(t)
	const total = 16 << 20
	const limit = 1 << 20

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	blob, err := ReadFrom(io.LimitReader(&zeroReader{}, total), limit)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	defer blob.Close()
	if blob.InMemory() {
		t.Fatal("aliran 16MB harus pindah ke file")
	}
	if blob.Size() != total {
		t.Fatalf("Size = %d, mau %d", blob.Size(), total)
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 8<<20 {
		t.Fatalf("alokasi %d MB untuk aliran %d MB - ada yang menampung seluruh berkas di RAM", alloc>>20, total>>20)
	}
}

func TestReadFromKeepsSmallBlobInMemory(t *testing.T) {
	dir := withTempDir(t)
	payload := bytes.Repeat([]byte("a"), 1000)

	blob, err := ReadFrom(bytes.NewReader(payload), 4096)
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	defer blob.Close()

	if !blob.InMemory() {
		t.Fatalf("blob kecil harus tetap di memori, path=%q", blob.Path())
	}
	if blob.Size() != int64(len(payload)) {
		t.Fatalf("Size = %d, mau %d", blob.Size(), len(payload))
	}
	got, err := blob.Bytes()
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("Bytes tidak sama: err=%v len=%d", err, len(got))
	}
	// Tidak boleh ada file sama sekali untuk blob RAM.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("direktori spool harus kosong, ada %d entri", len(entries))
	}
}

func TestReadFromSpillsLargeBlobToFile(t *testing.T) {
	dir := withTempDir(t)
	payload := bytes.Repeat([]byte("0123456789"), 10000) // 100KB > ambang 4KB

	blob, err := ReadFrom(bytes.NewReader(payload), 4096)
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	if blob.InMemory() {
		t.Fatal("blob besar harus pindah ke file")
	}
	if blob.Size() != int64(len(payload)) {
		t.Fatalf("Size = %d, mau %d", blob.Size(), len(payload))
	}
	if !strings.HasPrefix(filepath.Base(blob.Path()), filePrefix) {
		t.Fatalf("nama file harus berprefix %q: %q", filePrefix, blob.Path())
	}

	// Isi file harus identik dengan sumber.
	got, err := blob.Bytes()
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("isi file tidak sama: err=%v len=%d", err, len(got))
	}

	// Reader juga harus memberi isi yang sama (dipakai http client).
	rc, err := blob.Reader()
	if err != nil {
		t.Fatalf("Reader: %v", err)
	}
	streamed, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || !bytes.Equal(streamed, payload) {
		t.Fatalf("Reader tidak sama: err=%v len=%d", err, len(streamed))
	}

	path := blob.Path()
	if err := blob.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file harus terhapus setelah Close, err=%v", err)
	}
	// Close kedua harus aman.
	if err := blob.Close(); err != nil {
		t.Fatalf("Close kedua: %v", err)
	}
	_ = dir
}

func TestBlobFileOpensReadWrite(t *testing.T) {
	withTempDir(t)
	payload := bytes.Repeat([]byte("x"), 8192)
	blob, err := ReadFrom(bytes.NewReader(payload), 1024)
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	defer blob.Close()

	f, err := blob.File()
	if err != nil {
		t.Fatalf("File: %v", err)
	}
	defer f.Close()
	// UploadReader menimpa file yang sama: tulis di awal harus terlihat.
	if _, err := f.WriteAt([]byte("ZZ"), 0); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	head := make([]byte, 2)
	if _, err := f.ReadAt(head, 0); err != nil || string(head) != "ZZ" {
		t.Fatalf("baca balik gagal: %q err=%v", head, err)
	}
}

func TestBlobFileRejectsMemoryBlob(t *testing.T) {
	withTempDir(t)
	blob := FromBytes([]byte("kecil"))
	if _, err := blob.File(); err == nil {
		t.Fatal("File() untuk blob RAM harus error")
	}
}

func TestCleanStaleRemovesOnlyOldSpoolFiles(t *testing.T) {
	dir := withTempDir(t)
	old := filepath.Join(dir, filePrefix+"old.bin")
	fresh := filepath.Join(dir, filePrefix+"fresh.bin")
	other := filepath.Join(dir, "lain.bin")
	for _, p := range []string{old, fresh, other} {
		if err := os.WriteFile(p, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	oldTime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(old, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	if n := CleanStale(time.Hour); n != 1 {
		t.Fatalf("CleanStale = %d, mau 1", n)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("file lama harus terhapus")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("file baru tidak boleh terhapus")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("file non-spool tidak boleh disentuh")
	}
}

func TestReadFromWithoutDirFails(t *testing.T) {
	Configure("")
	payload := bytes.Repeat([]byte("z"), 8192)
	if _, err := ReadFrom(bytes.NewReader(payload), 16); err == nil {
		t.Fatal("harus error kalau direktori spool belum diatur")
	}
}
