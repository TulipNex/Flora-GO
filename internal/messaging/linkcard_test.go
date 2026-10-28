package messaging

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestSplitThumbSources(t *testing.T) {
	got := SplitThumbSources(" https://a/x.jpg, https://b/y.png ;https://c/z.gif\nbukan-url ")
	want := []string{"https://a/x.jpg", "https://b/y.png", "https://c/z.gif"}
	if len(got) != len(want) {
		t.Fatalf("jumlah sumber = %d, mau %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sumber[%d] = %q, mau %q", i, got[i], want[i])
		}
	}
	if sources := SplitThumbSources("   "); len(sources) != 0 {
		t.Errorf("string kosong harus jadi 0 sumber, dapat %v", sources)
	}
	if sources := SplitThumbSources("reset"); len(sources) != 0 {
		t.Errorf("teks non-http harus dibuang, dapat %v", sources)
	}
}

// TestDownloadThumbRetriesTransient memastikan 5xx diulang sekali (server
// gambar publik sering gagal sesaat) dan percobaan kedua yang sukses dipakai.
func TestDownloadThumbRetriesTransient(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("jpeg-palsu"))
	}))
	defer srv.Close()

	data, err := downloadThumb(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("downloadThumb error: %v", err)
	}
	if string(data) != "jpeg-palsu" {
		t.Fatalf("byte = %q", data)
	}
	if n := atomic.LoadInt32(&hits); n != 2 {
		t.Fatalf("server dipanggil %d kali, mau 2", n)
	}
}

// TestDownloadThumbPermanentNoRetry memastikan 404 (file dihapus di host)
// TIDAK diulang — mengulang tidak akan mengubah hasilnya.
func TestDownloadThumbPermanentNoRetry(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	if _, err := downloadThumb(context.Background(), srv.URL); err == nil {
		t.Fatal("404 harus jadi error")
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Fatalf("server dipanggil %d kali, mau 1 (tidak retry)", n)
	}
}

// TestDownloadThumbRejectsNonImage memastikan halaman HTML (mis. link blob
// GitHub tanpa ?raw=true) ditolak permanen tanpa retry.
func TestDownloadThumbRejectsNonImage(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html></html>"))
	}))
	defer srv.Close()

	if _, err := downloadThumb(context.Background(), srv.URL); err == nil {
		t.Fatal("content-type non-gambar harus jadi error")
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Fatalf("server dipanggil %d kali, mau 1 (tidak retry)", n)
	}
}

// TestDiskThumbRoundTrip memastikan cadangan byte dipakai saat host mati:
// tulis lalu baca kembali dari folder data/thumb-cache (chdir ke temp supaya
// tidak mengotori repo).
func TestDiskThumbRoundTrip(t *testing.T) {
	t.Chdir(t.TempDir())

	const url = "https://contoh/gambar.jpg"
	if _, ok := readDiskThumb(url); ok {
		t.Fatal("cache harus kosong di awal")
	}
	payload := bytes.Repeat([]byte{0xAB}, 128)
	writeDiskThumb(url, payload)

	got, ok := readDiskThumb(url)
	if !ok {
		t.Fatal("cache tidak ditemukan setelah ditulis")
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("isi cache berubah: %d byte", len(got))
	}
}
