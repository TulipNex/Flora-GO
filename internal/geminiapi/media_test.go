package geminiapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newLoadedClient membuat Client dengan cookie sesi sudah termuat (tanpa
// menyentuh jaringan).
func newLoadedClient(t *testing.T, header string) *Client {
	t.Helper()
	c := New(newCookieFile(t, header))
	if err := c.LoadCookies(); err != nil {
		t.Fatalf("muat cookie: %v", err)
	}
	return c
}

// TestDownloadMediaSendsSessionHeaders memastikan unduhan hasil gambar memakai
// sesi bot dan header mirip browser (tanpa Origin, yang tidak dikirim browser
// untuk <img>): tanpa konteks itu URL `gg-dl` membalas 403.
func TestDownloadMediaSendsSessionHeaders(t *testing.T) {
	var gotCookie, gotReferer, gotOrigin, gotUA, gotDest string
	payload := []byte("bukan-png-asli-tapi-cukup")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCookie = r.Header.Get("Cookie")
		gotReferer = r.Header.Get("Referer")
		gotOrigin = r.Header.Get("Origin")
		gotUA = r.Header.Get("User-Agent")
		gotDest = r.Header.Get("Sec-Fetch-Dest")
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(payload)
	}))
	t.Cleanup(srv.Close)

	c := newLoadedClient(t, "__Secure-1PSID=s1; NID=n1")
	data, err := c.DownloadMedia(context.Background(), srv.URL+"/gg-dl/abc")
	if err != nil {
		t.Fatalf("DownloadMedia: %v", err)
	}
	if string(data) != string(payload) {
		t.Fatalf("data = %q", data)
	}
	if !strings.Contains(gotCookie, "__Secure-1PSID=s1") {
		t.Fatalf("cookie tidak terkirim: %q", gotCookie)
	}
	if gotReferer != baseURL+"/" || gotDest != "image" {
		t.Fatalf("referer/dest = %q/%q", gotReferer, gotDest)
	}
	if gotOrigin != "" {
		t.Fatalf("Origin tidak boleh dikirim: %q", gotOrigin)
	}
	if !strings.HasPrefix(gotUA, "Mozilla/5.0") {
		t.Fatalf("UA = %q", gotUA)
	}
}

// TestDownloadMediaFallsBackToPublic: bila permintaan bersesi ditolak, percobaan
// tanpa cookie harus dicoba (sebagian URL memang publik).
func TestDownloadMediaFallsBackToPublic(t *testing.T) {
	var cookies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookies = append(cookies, r.Header.Get("Cookie"))
		if r.Header.Get("Cookie") != "" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("jpeg"))
	}))
	t.Cleanup(srv.Close)

	c := newLoadedClient(t, "__Secure-1PSID=s1")
	rep := c.DownloadMediaReport(context.Background(), srv.URL+"/gg-dl/abc")
	if len(rep.Data) != 4 || rep.Used != "publik" {
		t.Fatalf("report = used %q, %d byte", rep.Used, len(rep.Data))
	}
	if len(rep.Attempts) != 2 || rep.Attempts[0].Status != 403 || !rep.Attempts[1].OK {
		t.Fatalf("attempts = %+v", rep.Attempts)
	}
	if len(cookies) != 2 || cookies[0] == "" || cookies[1] != "" {
		t.Fatalf("cookie per percobaan = %q", cookies)
	}
}

// TestDownloadMediaRetriesWithoutSizeSuffix meniru URL bertanda tangan yang
// menolak parameter ukuran `=s2048`: varian tanpa parameter harus dipakai.
func TestDownloadMediaRetriesWithoutSizeSuffix(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "=s2048") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("jpeg-bytes"))
	}))
	t.Cleanup(srv.Close)

	c := newLoadedClient(t, "__Secure-1PSID=s1")
	rep := c.DownloadMediaReport(context.Background(), srv.URL+"/gg-dl/abc=s2048")
	if string(rep.Data) != "jpeg-bytes" {
		t.Fatalf("data = %q", rep.Data)
	}
	if rep.Used != "sesi-tanpa-ukuran" {
		t.Fatalf("used = %q", rep.Used)
	}
	want := []string{"/gg-dl/abc=s2048", "/gg-dl/abc=s2048", "/gg-dl/abc"}
	if len(paths) != len(want) {
		t.Fatalf("paths = %v", paths)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("paths = %v", paths)
		}
	}
}

// TestDownloadMediaReportCollectsStatuses: laporan harus memuat status tiap
// percobaan supaya `.gemini diag image` bisa membedakan 403 vs halaman login.
func TestDownloadMediaReportCollectsStatuses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("<html>Forbidden</html>"))
	}))
	t.Cleanup(srv.Close)

	c := newLoadedClient(t, "__Secure-1PSID=s1")
	rep := c.DownloadMediaReport(context.Background(), srv.URL+"/gg-dl/abc=s2048")
	if len(rep.Data) != 0 || rep.Used != "" {
		t.Fatalf("report tidak boleh sukses: %+v", rep)
	}
	if len(rep.Attempts) != 4 {
		t.Fatalf("attempts = %d, ingin 4", len(rep.Attempts))
	}
	for _, a := range rep.Attempts {
		if a.Status != 403 {
			t.Fatalf("%s status = %d", a.Name, a.Status)
		}
		if !strings.Contains(a.Snippet, "Forbidden") {
			t.Fatalf("%s snippet = %q", a.Name, a.Snippet)
		}
	}
}

// TestDownloadMediaRejectsHTML: halaman login juga membalas 200, tapi HTML
// tidak boleh diteruskan ke WhatsApp sebagai gambar.
func TestDownloadMediaRejectsHTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html>login</html>"))
	}))
	t.Cleanup(srv.Close)

	c := newLoadedClient(t, "__Secure-1PSID=s1")
	data, err := c.DownloadMedia(context.Background(), srv.URL+"/gg-dl/abc")
	if err == nil {
		t.Fatalf("harus gagal, dapat %d byte", len(data))
	}
	if data != nil {
		t.Fatalf("data harus nil, dapat %d byte", len(data))
	}
	if !strings.Contains(err.Error(), "bukan gambar") {
		t.Fatalf("err = %v", err)
	}
}

// TestDownloadMediaWithoutSession memastikan kegagalan cookie dilaporkan
// sebagai ErrNoCookie, bukan error jaringan yang membingungkan.
func TestDownloadMediaWithoutSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gemini-cookie.txt")
	if err := os.WriteFile(path, []byte("NID=n1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := New(path)
	_, err := c.DownloadMedia(context.Background(), "https://example.invalid/x")
	if err == nil {
		t.Fatal("harus gagal tanpa cookie sesi")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "cookie") {
		t.Fatalf("err = %v", err)
	}
}

// TestSanitizeMediaURL memastikan karakter sisa markup tidak ikut masuk ke
// URL: satu karakter tambahan membuat tanda tangan Google ditolak (403).
func TestSanitizeMediaURL(t *testing.T) {
	cases := map[string]string{
		" https://lh3.googleusercontent.com/gg-dl/x=s2048) ":  "https://lh3.googleusercontent.com/gg-dl/x=s2048",
		"https://x/a?b=1&amp;c=2":                             "https://x/a?b=1&c=2",
		"https://x/a.png.,":                                   "https://x/a.png",
		"https://x/a\"junk":                                   "https://x/a",
		"https://x/a\njunk":                                   "https://x/a",
		"https://lh3.googleusercontent.com/gg-dl/x=s2048\r\n": "https://lh3.googleusercontent.com/gg-dl/x=s2048",
	}
	for in, want := range cases {
		if got := sanitizeMediaURL(in); got != want {
			t.Fatalf("sanitize(%q) = %q, mau %q", in, got, want)
		}
	}
}

// TestExtractImagesSanitizes memastikan URL dari payload JSON dibersihkan
// sebelum dipakai mengunduh.
func TestExtractImagesSanitizes(t *testing.T) {
	got := extractImages(testImageBlocks("https://lh3.googleusercontent.com/gg-dl/abc=s2048)"))
	if len(got) != 1 || got[0] != "https://lh3.googleusercontent.com/gg-dl/abc=s2048" {
		t.Fatalf("images = %v", got)
	}
	// Tanpa parameter ukuran: `=s2048` ditambahkan; dengan query: dibiarkan.
	if got := extractImages(testImageBlocks("https://x.test/a")); len(got) != 1 || got[0] != "https://x.test/a=s2048" {
		t.Fatalf("tanpa ukuran = %v", got)
	}
	if got := extractImages(testImageBlocks("https://x.test/a?b")); len(got) != 1 || got[0] != "https://x.test/a?b" {
		t.Fatalf("dengan query = %v", got)
	}
}

// testImageBlocks merakit struktur balasan minimal tempat extractImages
// membaca URL (blocks[0][12][7][0][i][0][3][3]).
func testImageBlocks(rawURL string) []any {
	head := []any{nil, nil, nil, []any{nil, nil, nil, rawURL}}
	entry := []any{head}
	group := []any{[]any{entry}}
	holder := make([]any, 8)
	holder[7] = group
	first := make([]any, 13)
	first[12] = holder
	return []any{first}
}

// TestMediaURLShape memastikan ringkasan bentuk URL tidak membocorkan tanda
// tangan tapi cukup untuk melihat URL terpotong.
func TestMediaURLShape(t *testing.T) {
	if got := mediaURLShape(""); got != "kosong" {
		t.Fatalf("kosong = %q", got)
	}
	got := mediaURLShape("https://lh3.googleusercontent.com/gg-dl/abcdef=s2048")
	for _, want := range []string{"char", "=s2048", "…=s2048"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q tidak memuat %q", got, want)
		}
	}
	if strings.Contains(got, "googleusercontent.com") {
		t.Fatalf("shape tidak boleh memuat host penuh: %q", got)
	}
}

// TestSaveImageURLs menulis URL ke folder dump (bukan chat) supaya bisa diuji
// manual dari browser.
func TestSaveImageURLs(t *testing.T) {
	dir := t.TempDir()
	old := rawDumpDir
	rawDumpDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { rawDumpDir = old })

	c := New(newCookieFile(t, "__Secure-1PSID=s1"))
	urls := []string{"https://x.test/a=s2048", "https://x.test/b=s2048"}
	path := c.saveImageURLs(urls)
	if path == "" {
		t.Fatal("berkas URL tidak dibuat")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("baca: %v", err)
	}
	for _, u := range urls {
		if !strings.Contains(string(body), u) {
			t.Fatalf("berkas tidak memuat %q", u)
		}
	}
}

// TestIsGoogleMediaHost menjaga jalur Chrome hanya untuk CDN media Google:
// host lain (termasuk 127.0.0.1 di tes) tidak boleh menyalakan browser.
func TestIsGoogleMediaHost(t *testing.T) {
	cases := map[string]bool{
		"lh3.googleusercontent.com":        true,
		"work.fife.usercontent.google.com": true,
		"googleusercontent.com":            true,
		"127.0.0.1:8080":                   false,
		"x.test":                           false,
		"googleusercontent.com.evil.test":  false,
		"notgoogleusercontent.com":         false,
	}
	for host, want := range cases {
		if got := isGoogleMediaHost(host); got != want {
			t.Fatalf("%s: %v, mau %v", host, got, want)
		}
	}
}

// TestMediaURLVariants menjaga bentuk URL yang dicoba: asli dulu, lalu tanpa
// parameter ukuran dan versi `=s0`.
func TestMediaURLVariants(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"https://lh3.googleusercontent.com/gg-dl/abc", []string{"https://lh3.googleusercontent.com/gg-dl/abc"}},
		{"https://lh3.googleusercontent.com/gg-dl/abc=s2048", []string{"https://lh3.googleusercontent.com/gg-dl/abc=s2048", "https://lh3.googleusercontent.com/gg-dl/abc", "https://lh3.googleusercontent.com/gg-dl/abc=s0"}},
		{"https://lh3.googleusercontent.com/gg-dl/abc=s512-rw", []string{"https://lh3.googleusercontent.com/gg-dl/abc=s512-rw", "https://lh3.googleusercontent.com/gg-dl/abc", "https://lh3.googleusercontent.com/gg-dl/abc=s0"}},
		{"https://x.test/a?b=c", []string{"https://x.test/a?b=c"}},
	}
	for _, tc := range cases {
		got := mediaURLVariants(tc.in)
		if len(got) != len(tc.want) {
			t.Fatalf("%s: %v", tc.in, got)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("%s: %v", tc.in, got)
			}
		}
	}
}
