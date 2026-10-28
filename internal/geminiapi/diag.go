package geminiapi

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// UploadProbe adalah hasil uji jalur lampiran (`.gemini diag upload`).
// Semua field hanya berisi status/ukuran — bukan isi berkas atau nilai cookie.
type UploadProbe struct {
	Bytes     int
	FileIDLen int
	UploadErr error
	Text      string
	Images    int
	ChatErr   error
	DumpPath  string
}

// DiagUpload menguji rantai lampiran persis seperti yang dipakai owner:
// unggah PNG kecil → kirim satu giliran dengan lampiran itu → laporkan hasil.
// Biayanya satu giliran kuota; dipakai saat chat ber-lampiran gagal tanpa
// pesan error yang jelas.
func (c *Client) DiagUpload(ctx context.Context) UploadProbe {
	p := UploadProbe{}
	png := probePNG()
	p.Bytes = len(png)
	f, err := c.Upload(ctx, "diag.png", "image/png", png)
	if err != nil {
		p.UploadErr = err
		p.DumpPath = c.LastDumpPath()
		return p
	}
	p.FileIDLen = len(f.ID)

	res, err := c.Chat(ctx, Request{Prompt: "Balas satu kata: ok", Files: []File{f}})
	if err != nil {
		p.ChatErr = err
	} else {
		p.Text = snippet([]byte(res.Text))
		p.Images = len(res.Images)
	}
	p.DumpPath = c.LastDumpPath()
	return p
}

// ImageProbe adalah hasil uji jalur gambar hasil generate (`.gemini diag
// image`): satu giliran minta gambar, lalu tangga unduhan untuk tiap kandidat
// URL. Berisi status/ukuran saja — URL bertanda tangan tidak pernah
// dikembalikan.
type ImageProbe struct {
	Prompt     string
	ChatErr    error
	Text       string
	Images     int
	Candidates []string // semua URL media yang ditemukan (untuk berkas uji)
	Media      []MediaReport
	URLFile    string
	DumpPath   string
}

// DiagImage meminta satu gambar lalu menguji unduhannya. Biayanya satu kuota
// generate; dipakai saat gambar dihasilkan tapi gagal dikirim (HTTP 403).
// URL asli disimpan ke berkas temp (bukan ditampilkan di chat) supaya bisa
// diuji manual di browser: incognito vs login memisahkan URL privat dari URL
// rusak maupun blokir IP. Semua kandidat URL di payload disimpan karena
// stream kadang memuat beberapa bentuk (preview vs URL penuh).
func (c *Client) DiagImage(ctx context.Context) ImageProbe {
	const prompt = "Buatkan satu gambar sederhana: apel merah di latar putih polos, tanpa teks."
	p := ImageProbe{Prompt: prompt}
	res, err := c.Chat(ctx, Request{Prompt: prompt})
	if err != nil {
		p.ChatErr = err
		p.DumpPath = c.LastDumpPath()
		return p
	}
	p.Text = snippet([]byte(res.Text))
	p.Images = len(res.Images)

	candidates := res.Candidates
	if len(candidates) == 0 {
		for _, u := range res.Images {
			if v := sanitizeMediaURL(u); v != "" && !containsString(candidates, v) {
				candidates = append(candidates, v)
			}
		}
	}
	// URL yang benar-benar dikirim bot ditaruh paling depan supaya jalur Chrome
	// diuji pada URL itu, bukan pada kandidat lain yang mungkin cuma pratinjau.
	if canonical := firstImageURL(res); canonical != "" {
		for i, u := range candidates {
			if u == canonical {
				reordered := make([]string, 0, len(candidates))
				reordered = append(reordered, canonical)
				reordered = append(reordered, candidates[:i]...)
				reordered = append(reordered, candidates[i+1:]...)
				candidates = reordered
				break
			}
		}
	}
	p.Candidates = candidates
	p.URLFile = c.saveImageURLs(candidates)

	for i, u := range candidates {
		if i >= 4 {
			// Empat kandidat cukup untuk membedakan pola; hindari unduhan
			// beruntun yang memakan waktu di panel 1 core.
			break
		}
		// Chrome hanya dicoba sekali (kandidat pertama) supaya diagnostik
		// tidak menyalakan browser berulang-ulang.
		p.Media = append(p.Media, c.downloadMediaReport(ctx, u, i == 0 && len(res.Images) > 0))
	}
	p.DumpPath = c.LastDumpPath()
	return p
}

// firstImageURL mengembalikan URL kanonik (yang dipakai pengiriman normal),
// sudah dibersihkan, atau "" bila tidak ada gambar.
func firstImageURL(res *Result) string {
	if res == nil || len(res.Images) == 0 {
		return ""
	}
	return sanitizeMediaURL(res.Images[0])
}

// saveImageURLs menulis URL hasil generate ke folder temp bot (0600) supaya
// bisa dibuka owner lewat File Manager. URL tidak pernah ditampilkan di chat.
func (c *Client) saveImageURLs(urls []string) string {
	if len(urls) == 0 {
		return ""
	}
	dir, err := rawDumpDir()
	if err != nil {
		return ""
	}
	name := filepath.Join(dir, "gemini-image-url-"+time.Now().Format("20060102-150405")+".txt")
	var b strings.Builder
	b.WriteString("# URL gambar hasil Gemini untuk uji manual.\n")
	b.WriteString("# Buka di browser incognito DAN saat login; hapus berkas setelah selesai.\n")
	for _, u := range urls {
		b.WriteString(u)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(name, []byte(b.String()), 0o600); err != nil {
		return ""
	}
	return name
}

// probePNG membuat PNG 64×64 polos untuk uji unggah (tanpa berkas di disk).
func probePNG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.RGBA{R: 240, G: 96, B: 48, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil
	}
	return buf.Bytes()
}

// AppProbe adalah hasil satu GET /app untuk `.gemini diag`. Tidak memuat isi
// halaman atau nilai cookie — hanya penanda yang dibutuhkan untuk memisahkan
// penyebab: TS basi, 1PSID mati, atau halaman challenge.
type AppProbe struct {
	Status   int
	Bytes    int
	HasToken bool // SNlM0e ada → sesi dikenali
	HasLogin bool // halaman login/consent → sesi tidak dikenali
	Err      error
}

// Diag adalah laporan satu putaran diagnostik cookie. Semua teks di sini
// sengaja hanya berisi nama/status, bukan nilai cookie.
type Diag struct {
	FilePath     string
	FileNames    []string
	FileHasTS    bool // berkas menyimpan __Secure-1PSIDTS (cookie pendamping)
	Rotate       rotateInfo
	RotateErr    error
	App          AppProbe // cookie lengkap (setelah rotasi)
	AppNoTS      AppProbe // tanpa cookie volatile
	AppOnly1PSID AppProbe // hanya __Secure-1PSID
	SaveErr      error
}

// Diag menjalankan pemeriksaan berurutan: isi berkas → rotasi → /app dengan
// cookie lengkap → /app tanpa TS → /app hanya 1PSID. Tujuannya menjawab satu
// pertanyaan: apakah sesi mati karena TS basi (bisa diperbaiki di bot) atau
// karena 1PSID sendiri sudah tidak berlaku (harus login ulang).
func (c *Client) Diag(ctx context.Context) *Diag {
	d := &Diag{FilePath: c.cookiePath}

	// Isi berkas apa adanya (termasuk TS lama, kalau masih tersimpan).
	if raw, err := os.ReadFile(c.cookiePath); err == nil {
		rawCS := ParseCookieHeader(firstCookieLine(string(raw)))
		d.FileNames = rawCS.Names()
		d.FileHasTS = rawCS.Get("__Secure-1PSIDTS") != ""
	}

	if err := c.LoadCookies(); err != nil {
		d.RotateErr = err
		d.SaveErr = c.SaveError()
		return d
	}

	d.Rotate, d.RotateErr = c.rotateWithInfo(ctx, true)

	c.metaMu.Lock()
	cs := c.cookies
	c.metaMu.Unlock()
	if cs == nil {
		d.SaveErr = c.SaveError()
		return d
	}

	d.App = probeApp(ctx, c, cs.Header())
	d.AppNoTS = probeApp(ctx, c, cs.HeaderExcept(volatileCookieNames()...))
	d.AppOnly1PSID = probeApp(ctx, c, cs.HeaderOnly("__Secure-1PSID"))
	d.SaveErr = c.SaveError()
	return d
}

// probeApp mengambil /app dengan header Cookie tertentu dan melaporkan
// penanda penting tanpa menyimpan isi halaman.
func probeApp(ctx context.Context, c *Client, cookieHeader string) AppProbe {
	if strings.TrimSpace(cookieHeader) == "" {
		return AppProbe{Err: ErrNoCookie}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, appURL, nil)
	if err != nil {
		return AppProbe{Err: err}
	}
	req.Header.Set("User-Agent", defaultUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "id-ID,id;q=0.9,en-US;q=0.8,en;q=0.7")
	req.Header.Set("Cookie", cookieHeader)

	resp, err := c.http.Do(req)
	if err != nil {
		return AppProbe{Err: err}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return AppProbe{Status: resp.StatusCode, Bytes: len(body), Err: err}
	}
	html := string(body)
	return AppProbe{
		Status:   resp.StatusCode,
		Bytes:    len(body),
		HasToken: strings.Contains(html, "SNlM0e"),
		HasLogin: strings.Contains(html, "ServiceLogin") ||
			strings.Contains(html, "accounts.google.com/signin") ||
			strings.Contains(html, "consent.google.com"),
	}
}
