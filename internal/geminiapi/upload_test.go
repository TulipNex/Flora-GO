package geminiapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// uploadCapture menampung header/body yang diterima server tiruan.
type uploadCapture struct {
	optionCalls int
	startMime   string
	startLength string
	startName   string
	finalMime   string
	finalCmd    string
	finalOffset string
	finalBody   []byte
}

// newUploadServer meniru alur resumable Gemini: OPTIONS, POST start (membalas
// X-Goog-Upload-Url), OPTIONS, lalu POST upload, finalize.
func newUploadServer(t *testing.T, cap *uploadCapture, id string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			cap.optionCalls++
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path == "/upload" {
			cap.startMime = r.Header.Get("X-Goog-Upload-Header-Content-Type")
			cap.startLength = r.Header.Get("X-Goog-Upload-Header-Content-Length")
			body, _ := io.ReadAll(r.Body)
			cap.startName = string(body)
			w.Header().Set("X-Goog-Upload-Url", "http://"+r.Host+"/final")
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.Path == "/final" {
			cap.finalMime = r.Header.Get("Content-Type")
			cap.finalCmd = r.Header.Get("X-Goog-Upload-Command")
			cap.finalOffset = r.Header.Get("X-Goog-Upload-Offset")
			cap.finalBody, _ = io.ReadAll(r.Body)
			_, _ = w.Write([]byte(id))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func pointAtUpload(t *testing.T, srv *httptest.Server) {
	t.Helper()
	old := uploadEndpoint
	uploadEndpoint = srv.URL + "/upload"
	t.Cleanup(func() { uploadEndpoint = old })
}

// TestUploadSendsMimeType: tipe berkas harus dideklarasikan di start maupun
// finalize. Tanpa itu server menerima unggahan tapi giliran chat bisa kosong.
func TestUploadSendsMimeType(t *testing.T) {
	var cap uploadCapture
	srv := newUploadServer(t, &cap, "file-abc")
	pointAtUpload(t, srv)

	c := newLoadedClient(t, "__Secure-1PSID=s1")
	data := []byte("%PDF-1.4 isi dokumen uji")
	f, err := c.Upload(context.Background(), "uji.pdf", "application/pdf", data)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if f.ID != "file-abc" || f.Name != "uji.pdf" {
		t.Fatalf("file = %+v", f)
	}
	if cap.startMime != "application/pdf" {
		t.Fatalf("start mime = %q", cap.startMime)
	}
	if cap.startLength != strconv.Itoa(len(data)) {
		t.Fatalf("start length = %q", cap.startLength)
	}
	if cap.startName != "File name: uji.pdf" {
		t.Fatalf("start body = %q", cap.startName)
	}
	if cap.finalMime != "application/pdf" {
		t.Fatalf("finalize mime = %q", cap.finalMime)
	}
	if cap.finalCmd != "upload, finalize" || cap.finalOffset != "0" {
		t.Fatalf("finalize header = %q/%q", cap.finalCmd, cap.finalOffset)
	}
	if string(cap.finalBody) != string(data) {
		t.Fatalf("finalize body = %q", cap.finalBody)
	}
	if cap.optionCalls != 2 {
		t.Fatalf("OPTIONS = %d, ingin 2", cap.optionCalls)
	}
}

// TestUploadInfersMime: mime kosong dideteksi dari isi berkas.
func TestUploadInfersMime(t *testing.T) {
	var cap uploadCapture
	srv := newUploadServer(t, &cap, "file-png")
	pointAtUpload(t, srv)

	c := newLoadedClient(t, "__Secure-1PSID=s1")
	if _, err := c.Upload(context.Background(), "gambar.png", "", tinyPNG(t)); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if cap.startMime != "image/png" {
		t.Fatalf("start mime = %q", cap.startMime)
	}
	if cap.finalMime != "image/png" {
		t.Fatalf("finalize mime = %q", cap.finalMime)
	}
}

// TestUploadRejectsEmpty: berkas kosong ditolak sebelum menyentuh jaringan.
func TestUploadRejectsEmpty(t *testing.T) {
	c := newLoadedClient(t, "__Secure-1PSID=s1")
	if _, err := c.Upload(context.Background(), "kosong.bin", "application/octet-stream", nil); err == nil {
		t.Fatal("harus gagal untuk berkas kosong")
	}
}
