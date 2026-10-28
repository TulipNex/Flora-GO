package geminiapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"wabot-go/internal/logfmt"
)

// maxMediaBytes membatasi unduhan hasil media. Gambar hasil Gemini biasanya di
// bawah 5 MB; batas ini hanya pagar supaya respons aneh tidak memakan memori.
const maxMediaBytes = 32 << 20

// sizeSuffixRe mencocokkan parameter ukuran Google (`=s2048`, `=s512-rw`).
var sizeSuffixRe = regexp.MustCompile(`=s\d+(?:-[a-z0-9]+)?$`)

// MediaAttempt adalah satu percobaan unduh dalam tangga DownloadMedia. Hanya
// berisi status/tipe/ukuran — URL lengkap tidak pernah disimpan supaya tanda
// tangannya tidak bocor ke chat atau log.
type MediaAttempt struct {
	Name        string // "sesi", "publik", "sesi-tanpa-ukuran", "sesi-s0"
	Status      int
	ContentType string
	Bytes       int
	Snippet     string // potongan body saat gagal (≤160 rune), untuk diagnosa
	OK          bool
	Err         error
}

// MediaReport adalah hasil lengkap satu upaya unduh media: semua percobaan,
// byte gambar bila ada, dan nama percobaan yang berhasil.
type MediaReport struct {
	Host     string
	Shape    string // ringkasan bentuk URL (panjang/query/sufiks), tanpa nilai tanda tangan
	Attempts []MediaAttempt
	Data     []byte
	Used     string
}

// DownloadMedia mengunduh hasil media (biasanya gambar) memakai sesi bot.
// URL `gg-dl` menolak permintaan tanpa konteks browser/sesi (HTTP 403), jadi
// beberapa bentuk dicoba berurutan: sesi + header mirip browser, tanpa cookie,
// lalu URL tanpa parameter ukuran dan versi `=s0`. Kalau semuanya gagal,
// Chrome panel dipakai sebagai jalur terakhir (lihat chrome.go).
func (c *Client) DownloadMedia(ctx context.Context, rawURL string) ([]byte, error) {
	rep := c.downloadMediaReport(ctx, rawURL, true)
	if len(rep.Data) > 0 {
		return rep.Data, nil
	}
	// Kegagalan sebelum ada percobaan HTTP (cookie kosong/URL kosong) lebih
	// berguna apa adanya supaya errors.Is(ErrNoCookie) tetap bekerja.
	if len(rep.Attempts) == 1 && rep.Attempts[0].Status == 0 && rep.Attempts[0].Err != nil {
		return nil, rep.Attempts[0].Err
	}
	return nil, fmt.Errorf("unduh media %s: %s", rep.Host, mediaAttemptSummary(rep.Attempts))
}

// DownloadMediaReport menjalankan seluruh tangga percobaan dan mengembalikan
// rinciannya. Dipakai `.gemini diag image` agar kegagalan bisa dibedakan
// (cookie vs header vs CDN) tanpa menebak dari log.
func (c *Client) DownloadMediaReport(ctx context.Context, rawURL string) MediaReport {
	return c.downloadMediaReport(ctx, rawURL, true)
}

// downloadMediaReport menjalankan tangga HTTP; allowChrome=false mematikan
// jalur Chrome (dipakai diagnostik untuk kandidat kedua dan seterusnya supaya
// Chrome tidak dinyalakan berulang kali).
func (c *Client) downloadMediaReport(ctx context.Context, rawURL string, allowChrome bool) MediaReport {
	rawURL = sanitizeMediaURL(strings.TrimSpace(rawURL))
	rep := MediaReport{Host: mediaHost(rawURL), Shape: mediaURLShape(rawURL)}
	if rawURL == "" {
		rep.Attempts = append(rep.Attempts, MediaAttempt{Name: "url", Err: fmt.Errorf("%w: url media kosong", ErrEmpty)})
		return rep
	}
	if err := c.ensureCookies(); err != nil {
		rep.Attempts = append(rep.Attempts, MediaAttempt{Name: "sesi", Err: err})
		return rep
	}
	cs, _, _, _ := c.snapshot()

	for _, step := range mediaSteps(rawURL) {
		attempt, data := c.mediaGet(ctx, step.URL, cs, step.WithCookies)
		attempt.Name = step.Name
		rep.Attempts = append(rep.Attempts, attempt)
		if len(data) > 0 {
			rep.Data = data
			rep.Used = step.Name
			logfmt.Info("gemini: media ok via %s (%d B)\n", step.Name, len(data))
			return rep
		}
	}

	if allowChrome && isGoogleMediaHost(rep.Host) {
		attempt, data := c.mediaChrome(ctx, rawURL)
		rep.Attempts = append(rep.Attempts, attempt)
		if len(data) > 0 {
			rep.Data = data
			rep.Used = attempt.Name
			logfmt.Info("gemini: media ok via %s (%d B)\n", attempt.Name, len(data))
			return rep
		}
	}

	logfmt.Warn("gemini: media %s gagal: %s\n", rep.Host, mediaAttemptSummary(rep.Attempts))
	for _, a := range rep.Attempts {
		if a.Snippet != "" {
			logfmt.Warn("gemini: media %s %s: %s\n", rep.Host, a.Name, a.Snippet)
		}
	}
	return rep
}

// mediaStep adalah satu anak tangga: URL yang dicoba dan apakah cookie ikut
// dikirim.
type mediaStep struct {
	Name        string
	URL         string
	WithCookies bool
}

// mediaSteps menyusun urutan percobaan. Percobaan publik diletakkan setelah
// sesi karena URL yang butuh sesi harus dicoba dengan cookie lebih dulu;
// varian ukuran menyusul karena sebagian URL bertanda tangan menolak `=sNNNN`.
func mediaSteps(rawURL string) []mediaStep {
	steps := []mediaStep{
		{Name: "sesi", URL: rawURL, WithCookies: true},
		{Name: "publik", URL: rawURL, WithCookies: false},
	}
	trimmed := sizeSuffixRe.ReplaceAllString(rawURL, "")
	if trimmed != rawURL {
		steps = append(steps, mediaStep{Name: "sesi-tanpa-ukuran", URL: trimmed, WithCookies: true})
		steps = append(steps, mediaStep{Name: "sesi-s0", URL: trimmed + "=s0", WithCookies: true})
	}
	return steps
}

// mediaURLVariants mengembalikan semua URL yang mungkin dicoba (tanpa nama
// percobaan); dipertahankan untuk tes varian.
func mediaURLVariants(rawURL string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, step := range mediaSteps(rawURL) {
		if seen[step.URL] {
			continue
		}
		seen[step.URL] = true
		out = append(out, step.URL)
	}
	return out
}

func (c *Client) mediaGet(ctx context.Context, target string, cs *CookieSet, withCookies bool) (MediaAttempt, []byte) {
	attempt := MediaAttempt{Status: 0}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		attempt.Err = err
		return attempt, nil
	}
	applyMediaHeaders(req, cs, withCookies)

	resp, err := c.http.Do(req)
	if err != nil {
		attempt.Err = fmt.Errorf("unduh media %s: %w", mediaHost(target), err)
		return attempt, nil
	}
	defer resp.Body.Close()
	attempt.Status = resp.StatusCode
	attempt.ContentType = resp.Header.Get("Content-Type")

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxMediaBytes))
	if err != nil {
		if resp.StatusCode < 400 {
			attempt.Err = fmt.Errorf("baca media %s: %w", mediaHost(target), err)
		}
	}
	attempt.Bytes = len(data)

	if resp.StatusCode >= 400 || len(data) == 0 {
		if len(data) > 0 {
			attempt.Snippet = snippet(data)
		}
		return attempt, nil
	}
	if !isImageResponse(attempt.ContentType, data) {
		attempt.Snippet = snippet(data)
		attempt.Err = fmt.Errorf("unduh media %s: bukan gambar (%s)", mediaHost(target), emptyDash(attempt.ContentType))
		return attempt, nil
	}
	attempt.OK = true
	return attempt, data
}

// mediaChrome mencoba jalur browser (chrome.go). Jalur ini hanya diaktifkan
// untuk host media Google: URL `gg-dl` menolak klien HTTP biasa, sedangkan
// host lokal (dipakai tes) tidak boleh menyalakan Chrome.
func (c *Client) mediaChrome(ctx context.Context, target string) (MediaAttempt, []byte) {
	attempt := MediaAttempt{Name: "chrome"}
	res := fetchMediaViaChrome(ctx, target)
	attempt.Status = res.Status
	attempt.ContentType = res.ContentType
	attempt.Bytes = len(res.Bytes)
	if res.Snippet != "" {
		attempt.Snippet = snippet([]byte(res.Snippet))
	}
	if res.Err != nil {
		attempt.Err = res.Err
	}
	if len(res.Bytes) > 0 && isImageResponse(res.ContentType, res.Bytes) {
		attempt.OK = true
		return attempt, res.Bytes
	}
	if len(res.Bytes) > 0 && attempt.Snippet == "" {
		attempt.Snippet = snippet(res.Bytes)
	}
	if attempt.Err == nil {
		attempt.Err = fmt.Errorf("unduh media %s: chrome bukan gambar", mediaHost(target))
	}
	return attempt, nil
}

// isGoogleMediaHost melaporkan apakah host termasuk CDN media Google yang
// hanya mau melayani permintaan dari konteks browser.
func isGoogleMediaHost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "googleusercontent.com" {
		return true
	}
	return strings.HasSuffix(h, ".googleusercontent.com") || strings.HasSuffix(h, ".usercontent.google.com")
}

// applyMediaHeaders meniru permintaan <img> dari browser. Origin sengaja tidak
// dikirim (browser tidak mengirimnya untuk gambar) dan Sec-Fetch* disertakan
// karena sebagian endpoint Google membedakan konteks fetch.
func applyMediaHeaders(req *http.Request, cs *CookieSet, withCookies bool) {
	req.Header.Set("User-Agent", defaultUA)
	req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8")
	req.Header.Set("Accept-Language", "id-ID,id;q=0.9,en-US;q=0.8,en;q=0.7")
	req.Header.Set("Referer", baseURL+"/")
	req.Header.Set("Sec-Fetch-Dest", "image")
	req.Header.Set("Sec-Fetch-Mode", "no-cors")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	if withCookies {
		req.Header.Set("Cookie", cs.Header())
	}
}

// isImageResponse menerima respons yang tipenya gambar, atau yang isinya
// terdeteksi gambar walau header hilang/salah. Halaman login berstatus 200
// tidak boleh lolos dan terkirim ke WhatsApp sebagai gambar rusak.
func isImageResponse(contentType string, data []byte) bool {
	if strings.HasPrefix(strings.ToLower(contentType), "image/") {
		return true
	}
	if len(data) >= 12 && strings.HasPrefix(http.DetectContentType(data), "image/") {
		return true
	}
	return false
}

// mediaAttemptSummary meringkas semua percobaan untuk pesan error: nama +
// status/tipe, bukan URL.
func mediaAttemptSummary(attempts []MediaAttempt) string {
	if len(attempts) == 0 {
		return "tidak ada percobaan"
	}
	parts := make([]string, 0, len(attempts))
	for _, a := range attempts {
		switch {
		case a.Err != nil && a.Status == 0:
			msg := a.Err.Error()
			if len(msg) > 100 {
				msg = msg[:100] + "…"
			}
			parts = append(parts, a.Name+": "+msg)
		case a.Status == 0:
			parts = append(parts, a.Name+" kosong")
		case a.Status >= 400:
			parts = append(parts, fmt.Sprintf("%s %d", a.Name, a.Status))
		case !a.OK:
			parts = append(parts, a.Name+" bukan gambar")
		default:
			parts = append(parts, a.Name+" ok")
		}
	}
	return strings.Join(parts, "; ")
}

// mediaHost hanya menyebut host di error/log: URL lengkap memuat tanda tangan
// yang cepat kedaluwarsa dan tidak perlu bocor ke log.
func mediaHost(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return "media"
	}
	return u.Host
}

// mediaURLShape meringkas bentuk URL untuk diagnosa tanpa membocorkan tanda
// tangannya: panjang, jumlah parameter query, sufiks ukuran, dan 6 karakter
// terakhir. URL yang terpotong/terlalu pendek langsung terlihat dari sini.
func mediaURLShape(rawURL string) string {
	if rawURL == "" {
		return "kosong"
	}
	parts := []string{fmt.Sprintf("%d char", len(rawURL))}
	u, err := url.Parse(rawURL)
	if err != nil {
		return strings.Join(append(parts, "tidak valid"), " · ")
	}
	if n := len(u.Query()); n > 0 {
		parts = append(parts, fmt.Sprintf("%d param", n))
	}
	if m := sizeSuffixRe.FindString(rawURL); m != "" {
		parts = append(parts, m)
	}
	if len(rawURL) >= 6 {
		parts = append(parts, "…"+rawURL[len(rawURL)-6:])
	}
	return strings.Join(parts, " · ")
}
