// Package spool menyimpan hasil unduhan media di RAM hanya untuk berkas
// kecil; berkas besar dialirkan ke file sementara di wabot-tmp.
//
// Latar belakang (data .memlog Nov 2026): satu .bk ~500MB ditampung utuh di
// RAM lewat io.ReadAll (TotalAlloc 1,18GB karena slice tumbuh dobel), lalu
// diupload sebagai dokumen yang membuat salinan ciphertext + append ~1GB
// lagi - puncak RSS 1.652MB di panel dengan batas cgroup 1.645MB, dan bot
// di-OOM-kill dua kali berturut-turut.
//
// Aturan paket ini: pemanggil TIDAK PERNAH memegang seluruh isi berkas besar
// sebagai []byte. Blob kecil tetap di memori (jalur lama, tanpa tambahan
// I/O); Blob besar berupa file yang bisa dibaca bertahap oleh http client
// maupun oleh UploadReader (enkripsi streaming).
package spool

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DefaultMemLimit adalah ambang bawaan: blob <= 32MB tetap di RAM.
// Di atas itu isinya pindah ke file. Angka ini dipilih supaya media kecil
// (foto, audio, video pendek) tidak pernah menyentuh disk - jalur tercepat
// tetap jalur lama.
const DefaultMemLimit = 32 << 20

// filePrefix menandai file milik paket ini supaya CleanStale tidak pernah
// menghapus artefak temp lain di wabot-tmp.
const filePrefix = "wabot-spool-"

var (
	dirMu sync.RWMutex
	dir   string
)

// Configure menetapkan direktori file sementara (wajib wabot-tmp, bukan
// /tmp: panel memakai tmpfs 100MB sehingga video besar mati Errno 28).
// Dipanggil sekali dari main setelah store siap.
func Configure(tempDir string) {
	dirMu.Lock()
	dir = strings.TrimSpace(tempDir)
	dirMu.Unlock()
}

// Dir mengembalikan direktori spool yang sedang dipakai ("" = belum diatur).
func Dir() string {
	dirMu.RLock()
	defer dirMu.RUnlock()
	return dir
}

// Blob adalah hasil unduhan: data di RAM (kecil) atau file di disk (besar).
// Selalu Close() setelah selesai supaya file sementara tidak tertinggal.
type Blob struct {
	data []byte
	path string
	size int64
}

// FromBytes membungkus byte yang sudah ada di memori.
func FromBytes(data []byte) *Blob {
	return &Blob{data: data, size: int64(len(data))}
}

// ReadFrom membaca r sampai habis. Selama isinya masih <= memLimit hasilnya
// disimpan di RAM; begitu melewati ambang, buffer yang sudah terbaca dipindah
// ke file dan sisa aliran diteruskan langsung ke disk (tanpa menampung
// seluruh berkas di memori).
//
// memLimit <= 0 memakai DefaultMemLimit.
func ReadFrom(r io.Reader, memLimit int64) (*Blob, error) {
	if memLimit <= 0 {
		memLimit = DefaultMemLimit
	}

	var buf bytes.Buffer
	n, err := io.CopyN(&buf, r, memLimit+1)
	if err == io.EOF {
		// Seluruh isi muat di memori - jalur cepat, tanpa file.
		return &Blob{data: buf.Bytes(), size: n}, nil
	}
	if err != nil {
		return nil, err
	}

	// Ambang terlampaui: pindahkan buffer ke file, lalu lanjutkan streaming.
	f, path, err := create()
	if err != nil {
		return nil, err
	}
	written, werr := f.Write(buf.Bytes())
	if werr == nil && written != buf.Len() {
		werr = io.ErrShortWrite
	}
	if werr != nil {
		f.Close()
		os.Remove(path)
		return nil, werr
	}

	copied, cerr := io.Copy(f, r)
	if cerr == nil {
		cerr = f.Close()
	} else {
		f.Close()
	}
	if cerr != nil {
		os.Remove(path)
		return nil, cerr
	}
	return &Blob{path: path, size: n + copied}, nil
}

// create membuat file spool 0600 dengan nama acak.
func create() (*os.File, string, error) {
	base := Dir()
	if base == "" {
		return nil, "", fmt.Errorf("spool: direktori temp belum diatur (spool.Configure)")
	}
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return nil, "", err
	}
	name := filePrefix + hex.EncodeToString(suffix[:]) + ".bin"
	path := filepath.Join(base, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return nil, "", err
	}
	return f, path, nil
}

// Size adalah ukuran isi blob dalam byte.
func (b *Blob) Size() int64 {
	if b == nil {
		return 0
	}
	return b.size
}

// InMemory true kalau isinya masih di RAM (jalur cepat).
func (b *Blob) InMemory() bool {
	return b != nil && b.path == ""
}

// Path mengembalikan lokasi file ("" untuk blob di RAM).
func (b *Blob) Path() string {
	if b == nil {
		return ""
	}
	return b.path
}

// Bytes menyalin seluruh isi ke memori. Hanya untuk blob kecil atau saat
// pemanggil memang butuh []byte (mis. jalur video <=100MB); jangan dipakai
// untuk berkas ratusan MB.
func (b *Blob) Bytes() ([]byte, error) {
	if b == nil {
		return nil, nil
	}
	if b.path == "" {
		return b.data, nil
	}
	return os.ReadFile(b.path)
}

// Reader membuka isi blob untuk dibaca bertahap. Pemanggil wajib Close.
func (b *Blob) Reader() (io.ReadCloser, error) {
	if b == nil {
		return io.NopCloser(bytes.NewReader(nil)), nil
	}
	if b.path == "" {
		return io.NopCloser(bytes.NewReader(b.data)), nil
	}
	return os.Open(b.path)
}

// File membuka file spool dengan mode baca-tulis. Dipakai UploadReader, yang
// menimpa plaintext menjadi ciphertext di file yang sama sehingga tidak perlu
// salinan kedua di RAM maupun di disk.
func (b *Blob) File() (*os.File, error) {
	if b == nil || b.path == "" {
		return nil, fmt.Errorf("spool: blob tidak berbasis file")
	}
	return os.OpenFile(b.path, os.O_RDWR, 0)
}

// Close menghapus file sementara. Idempoten dan aman untuk blob RAM.
func (b *Blob) Close() error {
	if b == nil || b.path == "" {
		return nil
	}
	path := b.path
	b.path = ""
	return os.Remove(path)
}

// CleanStale menghapus file spool yang lebih tua dari maxAge, mis. sisa
// proses yang mati di tengah unduhan. Dijalankan sekali saat startup.
// Mengembalikan jumlah file yang dihapus.
func CleanStale(maxAge time.Duration) int {
	base := Dir()
	if base == "" {
		return 0
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return 0
	}
	cutoff := time.Now().Add(-maxAge)
	removed := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), filePrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(base, e.Name())); err == nil {
			removed++
		}
	}
	return removed
}
