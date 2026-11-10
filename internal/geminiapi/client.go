package geminiapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"wabot-go/internal/logfmt"
	"wabot-go/internal/memlog"
	"wabot-go/internal/store"
)

// Endpoint dan konstanta protokol web Gemini. Nilainya berasal dari perilaku
// halaman gemini.google.com (bukan API resmi) sehingga bisa berubah sewaktu-
// waktu; semua yang mudah berubah dikumpulkan di sini.
const (
	baseURL  = "https://gemini.google.com"
	appPath  = "/app"
	chatPath = "/_/BardChatUi/data/assistant.lamda.BardFrontendService/StreamGenerate"

	defaultUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"

	// defaultLang adalah bahasa yang diminta ke Gemini untuk isi jawaban
	// (slot kedua f.req). uiLang dipakai sebagai parameter `hl` di URL —
	// nilainya tidak memengaruhi bahasa jawaban.
	defaultLang = "id-ID"
	uiLang      = "en-US"

	// guestCookieTTL: cookie tamu (NID) hanya perlu diambil ulang sesekali.
	// Server tetap menerima cookie yang sama berulang kali; TTL mencegah bot
	// memakai cookie yang sudah terlalu tua tanpa alasan.
	guestCookieTTL = 30 * time.Minute
	// metaTTL: build label `bl` berubah tiap deploy Gemini, dan `f.sid`
	// terikat pada halaman yang menyajikannya. Satu jam cukup hemat.
	metaTTL = time.Hour

	// pageHeaderTimeout: batas menunggu response header untuk HEAD halaman
	// utama dan GET /app. Keduanya memang cepat; batas pendek membuat Ping/
	// `.gemini diag` memberi kabar gagal lebih awal saat jaringan/IP bermasalah.
	pageHeaderTimeout = 30 * time.Second

	// chatHeaderTimeout: batas menunggu response header untuk POST chat. Mode
	// tamu masuk antrean "guest" di sisi Google, jadi balasan pertama bisa jauh
	// lebih lambat daripada halaman biasa — 30 dtk terbukti terlalu ketat di
	// panel (error `http2: timeout awaiting response headers`). Request yang
	// macet total tetap dibatasi context pemanggil (3 menit).
	chatHeaderTimeout = 90 * time.Second

	maxResponseBytes = 32 << 20
)

var (
	// Build label pindah nama antar rilis (dulu boq_assistant-bard-web-server_…,
	// sekarang boq_gemini-web-uiserver_…), jadi ambil dari kunci cfb2h yang
	// stabil dan sisakan pola boq_ sebagai cadangan.
	buildRe  = regexp.MustCompile(`cfb2h(?:\\?"|"):\\?"([^"\\]+)`)
	buildAlt = regexp.MustCompile(`boq_[A-Za-z0-9_.-]+`)
	sidRe    = regexp.MustCompile(`FdrFJe(?:\\?"|"):\\?"([\d-]+)`)
)

// Endpoint aktif. Variabel (bukan konstanta) supaya bisa diarahkan ke server
// tiruan di tes; nilai produksinya tetap const di atas.
var (
	appURL       = baseURL + appPath
	rootURL      = baseURL + "/"
	chatEndpoint = baseURL + chatPath
)

// rawDumpDir menentukan folder dump diagnostik. Variabel supaya tes tidak
// menulis ke folder temp bot yang sebenarnya.
var rawDumpDir = store.BotTempDir

// Result adalah jawaban satu giliran. Mode tamu tidak bisa melanjutkan
// percakapan di server, jadi tidak ada penunjuk percakapan di sini.
type Result struct {
	Text string
}

// Client adalah klien web Gemini mode tamu (tanpa login). Aman dipakai
// bergantian antar goroutine: seluruh permintaan diserialkan karena satu
// identitas tamu hanya boleh punya satu stream berjalan.
type Client struct {
	// http dipakai HEAD halaman utama + GET /app (batas header pendek).
	http *http.Client
	// chat dipakai POST StreamGenerate (batas header jauh lebih panjang).
	// Transport dipisah supaya menunggu antrean chat yang lambat tidak ikut
	// memperlambat deteksi kegagalan di halaman biasa.
	chat *http.Client

	// reqMu menyerialkan Ask. mu melindungi kredensial di bawahnya; dipisah
	// supaya Ask tidak menahan reqMu saat hanya membaca nilai.
	reqMu sync.Mutex
	mu    sync.Mutex

	cookie   string
	cookieAt time.Time
	bl       string
	sid      string
	metaAt   time.Time
	// lastDump adalah path dump diagnostik terakhir (lihat writeRawDump).
	lastDump string
}

// New membuat klien tamu. Tidak ada berkas konfigurasi yang dibutuhkan:
// kredensial diambil otomatis dari gemini.google.com saat permintaan pertama.
func New() *Client {
	return &Client{
		// Timeout global 0: durasi streaming diatur per-request lewat context
		// pemanggil, bukan oleh http.Client.
		http: &http.Client{Timeout: 0, Transport: newTransport(pageHeaderTimeout)},
		chat: &http.Client{Timeout: 0, Transport: newTransport(chatHeaderTimeout)},
	}
}

// newTransport membangun transport khusus Gemini:
//   - dial paksa IPv4: IPv6 sering blackhole di panel/jaringan tertentu
//     (percobaan koneksi menggantung belasan detik sebelum jatuh ke IPv4);
//   - keep-alive: HEAD, halaman /app, dan chat memakai koneksi yang sama;
//   - ResponseHeaderTimeout (headerTimeout): server yang diam tidak menahan
//     permintaan selamanya, tapi TIDAK membatasi durasi streaming setelah
//     header tiba.
func newTransport(headerTimeout time.Duration) *http.Transport {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp4", addr)
		},
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          8,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: headerTimeout,
		ExpectContinueTimeout: time.Second,
	}
}

// Ping memastikan jalur tamu sehat: ambil cookie baru lalu buka /app untuk
// membaca parameter halaman. Murah (HEAD + GET) dan tidak memakai kuota chat.
// Tembolok sengaja dilewati: gunanya Ping justru membuktikan server masih
// menerima permintaan dari IP ini.
func (c *Client) Ping(ctx context.Context) error {
	c.forget()
	_, _, _, err := c.credentials(ctx)
	return err
}

// forget membuang kredensial di memori supaya permintaan berikutnya mengambil
// cookie + parameter halaman baru.
func (c *Client) forget() {
	c.mu.Lock()
	c.cookie, c.bl, c.sid = "", "", ""
	c.cookieAt, c.metaAt = time.Time{}, time.Time{}
	c.mu.Unlock()
}

// Ask mengirim satu prompt dan mengembalikan jawabannya. Karena server tidak
// mengizinkan lanjutan percakapan untuk tamu, konteks percakapan dibawa
// pemanggil di dalam prompt (lihat plugins/gemini.go).
func (c *Client) Ask(ctx context.Context, prompt string) (res *Result, err error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return nil, fmt.Errorf("%w: prompt kosong", ErrProtocol)
	}

	c.reqMu.Lock()
	defer c.reqMu.Unlock()

	op := memlog.Begin("gemini", "chat")
	defer func() { op.End(err == nil, "") }()

	res, err = c.askOnce(ctx, prompt)
	if err != nil && retryable(err) {
		// Kredensial tamu bisa basi kapan saja (build label berubah, cookie
		// dirotasi server). Ambil ulang sekali sebelum menyerah.
		logfmt.Warn("gemini: percobaan pertama gagal (%v), ambil kredensial baru\n", err)
		c.forget()
		res, err = c.askOnce(ctx, prompt)
	}
	return res, err
}

// retryable menandai kegagalan yang masuk akal diperbaiki oleh kredensial
// baru. ErrLimit dan ErrEmpty sengaja TIDAK diulang: yang pertama menghabiskan
// kuota, yang kedua berarti permintaan sudah diproses server.
func retryable(err error) bool {
	return errors.Is(err, ErrGuestCookie) || errors.Is(err, ErrMeta) || errors.Is(err, ErrStaleConv)
}

func (c *Client) askOnce(ctx context.Context, prompt string) (*Result, error) {
	cookie, bl, sid, err := c.credentials(ctx)
	if err != nil {
		return nil, err
	}
	return c.send(ctx, cookie, bl, sid, prompt)
}

// credentials mengembalikan cookie tamu + parameter halaman, mengambil ulang
// yang sudah kedaluwarsa. Cookie baru selalu membatalkan parameter halaman
// lama, jadi keduanya diambil berurutan.
func (c *Client) credentials(ctx context.Context) (cookie, bl, sid string, err error) {
	c.mu.Lock()
	cookie, cookieAt := c.cookie, c.cookieAt
	bl, sid, metaAt := c.bl, c.sid, c.metaAt
	c.mu.Unlock()

	if cookie == "" || time.Since(cookieAt) > guestCookieTTL {
		cookie, err = c.fetchGuestCookie(ctx)
		if err != nil {
			return "", "", "", err
		}
		bl, sid = "", ""
		c.mu.Lock()
		c.cookie, c.cookieAt = cookie, time.Now()
		c.bl, c.sid, c.metaAt = "", "", time.Time{}
		c.mu.Unlock()
	}
	if bl != "" && sid != "" && time.Since(metaAt) < metaTTL {
		return cookie, bl, sid, nil
	}

	bl, sid, err = c.fetchMeta(ctx, cookie)
	if err != nil {
		return "", "", "", err
	}
	c.mu.Lock()
	c.bl, c.sid, c.metaAt = bl, sid, time.Now()
	c.mu.Unlock()
	return cookie, bl, sid, nil
}

// fetchGuestCookie mengambil cookie tamu dari HEAD halaman utama. Server
// mengirim satu Set-Cookie (NID) yang cukup untuk memakai endpoint chat tanpa
// login.
func (c *Client) fetchGuestCookie(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, rootURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", defaultUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "id-ID,id;q=0.9,en-US;q=0.8,en;q=0.7")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrGuestCookie, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))

	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return "", fmt.Errorf("%w: halaman utama 429", ErrLimit)
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return "", fmt.Errorf("%w: halaman utama %d", ErrGuestCookie, resp.StatusCode)
	case resp.StatusCode >= 400:
		return "", fmt.Errorf("%w: halaman utama %d", ErrHTTP, resp.StatusCode)
	}

	cookie := firstSetCookie(resp)
	if cookie == "" {
		return "", fmt.Errorf("%w: Set-Cookie kosong", ErrGuestCookie)
	}
	return cookie, nil
}

// firstSetCookie mengambil pasangan name=value dari header Set-Cookie pertama.
// Urutan dipertahankan seperti yang dikirim server (cookie pertama yang dipakai
// referensi JS adalah yang diterima klien browser).
func firstSetCookie(resp *http.Response) string {
	for _, raw := range resp.Header.Values("Set-Cookie") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if i := strings.IndexByte(raw, ';'); i >= 0 {
			raw = raw[:i]
		}
		raw = strings.TrimSpace(raw)
		name, value, ok := strings.Cut(raw, "=")
		if !ok || strings.TrimSpace(name) == "" || strings.TrimSpace(value) == "" {
			continue
		}
		return raw
	}
	return ""
}

// fetchMeta membaca parameter halaman /app: build label `bl` dan `f.sid`.
// Akun tamu tidak punya token `at` (SNlM0e), jadi token itu tidak diminta.
func (c *Client) fetchMeta(ctx context.Context, cookie string) (bl, sid string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, appURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", defaultUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "id-ID,id;q=0.9,en-US;q=0.8,en;q=0.7")
	req.Header.Set("Cookie", cookie)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("%w: %v", ErrMeta, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return "", "", fmt.Errorf("%w: halaman /app 429", ErrLimit)
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return "", "", fmt.Errorf("%w: halaman /app %d", ErrGuestCookie, resp.StatusCode)
	case resp.StatusCode >= 400:
		return "", "", fmt.Errorf("%w: halaman /app %d", ErrHTTP, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", "", fmt.Errorf("%w: baca halaman: %v", ErrMeta, err)
	}
	html := string(body)

	bl = firstMatch(buildRe, html)
	if bl == "" {
		bl = buildAlt.FindString(html)
	}
	if bl == "" {
		return "", "", fmt.Errorf("%w: build label tidak ditemukan", ErrMeta)
	}
	sid = firstMatch(sidRe, html)
	if sid == "" {
		return "", "", fmt.Errorf("%w: f.sid tidak ditemukan", ErrMeta)
	}
	return bl, sid, nil
}

// send melakukan satu POST StreamGenerate.
func (c *Client) send(ctx context.Context, cookie, bl, sid, prompt string) (*Result, error) {
	form := url.Values{}
	form.Set("f.req", buildChatRequest(prompt, defaultLang))

	q := url.Values{}
	q.Set("bl", bl)
	q.Set("f.sid", sid)
	q.Set("hl", uiLang)
	q.Set("_reqid", strconv.Itoa(100000+rand.Intn(899999)))
	q.Set("rt", "c")
	endpoint := chatEndpoint + "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", defaultUA)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "id-ID,id;q=0.9,en-US;q=0.8,en;q=0.7")
	req.Header.Set("Origin", baseURL)
	req.Header.Set("Referer", baseURL+"/")
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=UTF-8")
	req.Header.Set("X-Same-Domain", "1")

	sendStart := time.Now()
	resp, err := c.chat.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrHTTP, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: chat 429", ErrLimit)
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w: chat %d", ErrGuestCookie, resp.StatusCode)
	case resp.StatusCode >= 400:
		snippet := readSnippet(resp.Body, 512)
		return nil, fmt.Errorf("%w: chat %d: %s", ErrHTTP, resp.StatusCode, snippet)
	}

	raw := &limitedBuffer{limit: 128 << 10}
	body := io.TeeReader(io.LimitReader(resp.Body, maxResponseBytes), raw)
	res, err := parseStreamIdle(body, defaultStreamIdle)
	if err != nil {
		return nil, err
	}
	if res.Text == "" {
		// Tanpa dump, kegagalan hanya tampak sebagai "tidak terbaca".
		c.dumpEmptyPayload(raw.buf, time.Since(sendStart))
		return nil, ErrEmpty
	}
	return res, nil
}

// limitedBuffer menyimpan awal balasan (untuk dump diagnostik) tanpa menahan
// seluruh stream di memori.
type limitedBuffer struct {
	buf   []byte
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - len(b.buf); room > 0 {
		if len(p) < room {
			b.buf = append(b.buf, p...)
		} else {
			b.buf = append(b.buf, p[:room]...)
		}
	}
	return len(p), nil
}

// dumpEmptyPayload menyimpan stream ketika server tidak mengirim teks. Isinya
// memuat penanda error/struktur balasan yang tidak dikenali parser, jadi ini
// bukti utama saat protokol berubah.
func (c *Client) dumpEmptyPayload(raw []byte, dur time.Duration) {
	if path := c.writeRawDump("gemini-empty", "jawaban kosong", raw, dur); path != "" {
		logfmt.Warn("gemini: jawaban kosong; stream mentah disimpan di %s\n", path)
	}
}

// writeRawDump menulis dump diagnostik (fail-soft) dan mengembalikan path-nya.
func (c *Client) writeRawDump(prefix, note string, raw []byte, dur time.Duration) string {
	if len(raw) == 0 {
		return ""
	}
	dir, err := rawDumpDir()
	if err != nil {
		return ""
	}
	name := filepath.Join(dir, prefix+"-"+time.Now().Format("20060102-150405")+".txt")
	body := append([]byte(fmt.Sprintf("# durasi=%s\n# %s\n\n", dur.Round(time.Millisecond), note)), raw...)
	if err := os.WriteFile(name, body, 0o600); err != nil {
		return ""
	}
	c.mu.Lock()
	c.lastDump = name
	c.mu.Unlock()
	return name
}

// LastDumpPath mengembalikan path dump diagnostik terakhir (kosong = tidak
// ada). Dipakai `.gemini diag` supaya owner tahu berkas mana yang harus
// diperiksa.
func (c *Client) LastDumpPath() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastDump
}

func readSnippet(r io.Reader, limit int64) string {
	b, _ := io.ReadAll(io.LimitReader(r, limit))
	s := strings.TrimSpace(string(b))
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 240 {
		s = s[:240] + "…"
	}
	return s
}
