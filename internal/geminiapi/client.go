package geminiapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"wabot-go/internal/store"
)

// Endpoint dan konstanta protokol web Gemini. Nilainya berasal dari perilaku
// halaman gemini.google.com (bukan API resmi) sehingga bisa berubah sewaktu-
// waktu; semua yang mudah berubah dikumpulkan di sini.
const (
	baseURL     = "https://gemini.google.com"
	appPath     = "/app"
	chatPath    = "/_/BardChatUi/data/assistant.lamda.BardFrontendService/StreamGenerate"
	rotateURL   = "https://accounts.google.com/RotateCookies"
	uploadURL   = "https://content-push.googleapis.com/upload/"
	rotateBody  = `[000,"-0000000000000000000"]`
	defaultUA   = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"
	uploadBasic = "Basic c2F2ZXM6cyNMdGhlNmxzd2F2b0RsN3J1d1U="
	defaultPush = "feeds/mcudyrk2a4khkz"
	tenantID    = "bard-storage"

	// metaTTL: token `at`/`bl`/`f.sid` relatif stabil, tapi build label
	// berubah tiap deploy Gemini. Satu jam cukup hemat tanpa basi.
	metaTTL = time.Hour
	// rotateInterval mengikuti petunjuk server ("identity.hfcr" = 600 dtk).
	// Rotasi dipanggil sebelum request bila sudah lewat; penjaga 60 dtk di
	// rotate() mencegah dua rotasi beruntun.
	rotateInterval = 9 * time.Minute
	rotateMinGap   = 60 * time.Second

	maxResponseBytes = 32 << 20
)

var (
	xsrfRe = regexp.MustCompile(`SNlM0e(?:\\?"|"):\\?"([^"\\]+)`)
	// Build label pindah nama antar rilis (dulu boq_assistant-bard-web-server_…,
	// sekarang boq_gemini-web-uiserver_…), jadi ambil dari kunci cfb2h yang
	// stabil dan sisakan pola boq_ sebagai cadangan.
	buildRe  = regexp.MustCompile(`cfb2h(?:\\?"|"):\\?"([^"\\]+)`)
	buildAlt = regexp.MustCompile(`boq_[A-Za-z0-9_.-]+`)
	sidRe    = regexp.MustCompile(`FdrFJe(?:\\?"|"):\\?"([\d-]+)`)
	pushIDRe = regexp.MustCompile(`qKIAYe(?:\\?"|"):\\?"([^"\\]+)`)
)

// Endpoint aktif. Variabel (bukan konstanta) supaya bisa diarahkan ke server
// tiruan di tes; nilai produksinya tetap const di atas.
var (
	appURL         = baseURL + appPath
	rotateEndpoint = rotateURL
	chatEndpoint   = baseURL + chatPath
)

// rawDumpDir menentukan folder dump diagnostik. Variabel supaya tes tidak
// menulis ke folder temp bot yang sebenarnya.
var rawDumpDir = store.BotTempDir

// Conversation adalah penunjuk percakapan di sisi Gemini. Bot tidak menyimpan
// transkrip: hanya tiga id ini (dan nomor giliran) yang membuat server
// melanjutkan konteks.
type Conversation struct {
	ID         string
	ResponseID string
	ChoiceID   string
	Turn       int
}

// File adalah lampiran yang sudah diunggah ke storage Gemini.
type File struct {
	ID   string
	Name string
}

// Request adalah satu giliran percakapan.
type Request struct {
	Prompt   string
	Language string // kode bahasa UI, default "id"
	Model    string // "flash" (default), "pro", "lite"
	Thinking bool
	Files    []File
	Conv     *Conversation
}

// Result adalah jawaban satu giliran.
type Result struct {
	Text   string
	Images []string
	// Candidates memuat semua URL media yang ditemukan di payload (bisa lebih
	// dari Images): dipakai diagnostik untuk membandingkan bentuk URL.
	Candidates []string
	Conv       *Conversation
	Model      string
}

// Client adalah klien web Gemini berbasis cookie. Aman dipakai bergantian
// antar goroutine: seluruh permintaan diserialkan karena satu akun hanya
// boleh punya satu stream berjalan.
type Client struct {
	cookiePath string
	cookies    *CookieSet
	http       *http.Client

	// reqMu menyerialkan Chat (satu akun = satu stream). metaMu melindungi
	// field di bawahnya; dipisah supaya Chat tidak menahan reqMu saat hanya
	// membaca token.
	reqMu  sync.Mutex
	metaMu sync.Mutex

	at        string
	bl        string
	sid       string
	pushID    string
	metaAt    time.Time
	rotatedAt time.Time
	// metaTried menandai tembolok disk sudah dicoba dibaca sekali (supaya
	// berkas yang memang tidak ada tidak dibaca berulang tiap permintaan).
	metaTried bool
	// sessionFP menandai sidik cookie identitas; berubah hanya saat login
	// ulang, dipakai untuk membuang tembolok token halaman lama.
	sessionFP string
	// lastDump adalah path dump diagnostik terakhir (lihat writeRawDump).
	lastDump string
	// saveErr adalah kegagalan terakhir menulis berkas cookie (nil = baik).
	// Ditampilkan `.gemini status` supaya masalah izin/disk tidak menyamar
	// sebagai cookie mati.
	saveErr error
}

// New membuat klien dari berkas cookie. Berkas belum harus ada saat New
// dipanggil kalau pemanggil hanya ingin memeriksa keberadaan cookie lewat
// CookieStatus; Chat akan mengembalikan ErrNoCookie.
func New(cookiePath string) *Client {
	return &Client{
		cookiePath: cookiePath,
		http: &http.Client{
			// Tanpa batas global: streaming jawaban panjang dan unggahan
			// besar diatur per-request lewat context.
			Timeout:   0,
			Transport: newTransport(),
		},
	}
}

// newTransport membangun transport khusus Gemini:
//   - dial paksa IPv4: IPv6 sering blackhole di panel/jaringan tertentu
//     (percobaan koneksi menggantung belasan detik sebelum jatuh ke IPv4);
//   - keep-alive: rotasi, halaman /app, dan chat memakai koneksi yang sama;
//   - ResponseHeaderTimeout: gateway yang diam tidak menahan permintaan
//     selamanya, tapi TIDAK membatasi durasi streaming setelah header tiba.
func newTransport() *http.Transport {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	tr := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp4", addr)
		},
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          8,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	return tr
}

// CookiePath mengembalikan lokasi berkas cookie (untuk pesan status).
func (c *Client) CookiePath() string { return c.cookiePath }

// LoadCookies memuat (ulang) cookie dari disk. Dipanggil otomatis oleh Chat.
func (c *Client) LoadCookies() error {
	cs, err := LoadCookieFile(c.cookiePath)
	if err != nil {
		return err
	}
	fp := sessionFingerprint(cs)
	c.metaMu.Lock()
	changed := c.sessionFP != "" && c.sessionFP != fp
	c.cookies = cs
	c.sessionFP = fp
	if changed {
		// Cookie sesi berganti (login ulang): token halaman lama tidak
		// berlaku lagi, jadi buang tembolok memori + disk.
		c.at, c.bl, c.sid, c.pushID = "", "", "", ""
		c.metaAt = time.Time{}
		c.metaTried = false
	}
	c.metaMu.Unlock()
	if changed {
		c.forgetMetaFile()
	}
	return nil
}

// sessionFingerprint meringkas cookie identitas sesi. Cookie pendamping
// (NID/PSIDCC/TS) sengaja tidak ikut: nilainya berubah tiap rotasi dan tidak
// menandakan sesi baru.
func sessionFingerprint(cs *CookieSet) string {
	sum := sha256.Sum256([]byte(cs.HeaderOnly("__Secure-1PSID", "__Secure-3PSID")))
	return hex.EncodeToString(sum[:8])
}

// HasCookies true bila cookie sesi sudah termuat (tanpa menyentuh jaringan).
func (c *Client) HasCookies() bool {
	c.metaMu.Lock()
	defer c.metaMu.Unlock()
	return c.cookies.HasSession()
}

// Ping memastikan sesi benar-benar sah: mengambil halaman /app dan mencari
// token `at`. Murah (satu GET) dan tidak memakai kuota chat. Tembolok token
// sengaja dilewati: gunanya Ping justru membuktikan cookie masih diterima
// server, bukan mengulang jawaban cache.
func (c *Client) Ping(ctx context.Context) error {
	if err := c.ensureCookies(); err != nil {
		return err
	}
	c.metaMu.Lock()
	c.metaAt = time.Time{}
	c.metaTried = true
	c.metaMu.Unlock()
	return c.ensureMeta(ctx)
}

// Rotate memaksa rotasi cookie sekarang. Dipakai setelah owner menempel
// cookie baru: `__Secure-1PSIDTS` lama tidak berlaku untuk `__Secure-1PSID`
// baru, jadi mint token pendamping dulu sebelum sesi diuji.
func (c *Client) Rotate(ctx context.Context) error {
	return c.rotate(ctx, true)
}

// ensureCookies memuat cookie dari disk bila belum ada di memori.
func (c *Client) ensureCookies() error {
	if c.HasCookies() {
		return nil
	}
	if err := c.LoadCookies(); err != nil {
		return err
	}
	if !c.HasCookies() {
		return fmt.Errorf("%w: __Secure-1PSID kosong", ErrNoCookie)
	}
	return nil
}

func (c *Client) snapshot() (cookies *CookieSet, at, bl, sid string) {
	c.metaMu.Lock()
	defer c.metaMu.Unlock()
	return c.cookies, c.at, c.bl, c.sid
}

// rotateInfo mencatat hasil satu permintaan RotateCookies untuk diagnostik
// (`.gemini diag`). Tidak pernah memuat nilai cookie.
type rotateInfo struct {
	Status   int
	SetNames []string
	Changed  bool
}

// rotate meminta Google memutar __Secure-1PSIDTS. Endpoint ini yang dipakai
// Chrome sendiri; saat ini cukup diautentikasi cookie (tanpa tantangan
// bertanda tangan), tapi bisa berubah bila Google memaksa DBSC.
func (c *Client) rotate(ctx context.Context, force bool) error {
	_, err := c.rotateWithInfo(ctx, force)
	return err
}

func (c *Client) rotateWithInfo(ctx context.Context, force bool) (rotateInfo, error) {
	c.metaMu.Lock()
	last := c.rotatedAt
	cs := c.cookies
	c.metaMu.Unlock()

	if !force && !last.IsZero() && time.Since(last) < rotateInterval {
		return rotateInfo{}, nil
	}
	if !last.IsZero() && time.Since(last) < rotateMinGap {
		return rotateInfo{}, nil
	}
	if cs == nil {
		return rotateInfo{}, fmt.Errorf("%w: cookie kosong", ErrNoCookie)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rotateEndpoint, strings.NewReader(rotateBody))
	if err != nil {
		return rotateInfo{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://accounts.google.com")
	req.Header.Set("User-Agent", defaultUA)
	req.Header.Set("Cookie", cs.Header())

	resp, err := c.http.Do(req)
	if err != nil {
		return rotateInfo{}, fmt.Errorf("gagal rotasi cookie: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))

	info := rotateInfo{Status: resp.StatusCode}
	for _, ck := range resp.Cookies() {
		if ck != nil && ck.Name != "" {
			info.SetNames = append(info.SetNames, ck.Name)
		}
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return info, fmt.Errorf("%w: rotasi cookie %d", ErrCookieRejected, resp.StatusCode)
	case resp.StatusCode >= 400:
		return info, fmt.Errorf("%w: rotasi cookie %d", ErrHTTP, resp.StatusCode)
	}

	info.Changed = cs.MergeSetCookies(resp.Cookies())
	if info.Changed {
		c.saveCookies(cs)
	}
	c.metaMu.Lock()
	c.rotatedAt = time.Now()
	c.metaMu.Unlock()
	return info, nil
}

// saveCookies menulis cookie ke berkas dan mengingat kegagalannya. Cookie di
// memori tetap dipakai walau simpan gagal, tapi kegagalan tidak lagi ditelan
// diam-diam: muncul di log dan di `.gemini status`.
func (c *Client) saveCookies(cs *CookieSet) {
	err := cs.Save(c.cookiePath)
	c.metaMu.Lock()
	c.saveErr = err
	c.metaMu.Unlock()
	if err != nil {
		logfmt.Warn("gemini: simpan cookie gagal: %v\n", err)
	}
}

// SaveError mengembalikan kegagalan terakhir menulis berkas cookie (nil =
// terakhir sukses). Nilainya tidak memuat isi cookie.
func (c *Client) SaveError() error {
	c.metaMu.Lock()
	defer c.metaMu.Unlock()
	return c.saveErr
}

// ensureMeta memastikan token halaman (/app) tersedia. Urutannya sengaja
// bertingkat karena penyebab "cookie ditolak" setelah restart tidak tunggal:
//
//  1. tembolok di memori (proses ini sudah pernah membuka /app);
//  2. tembolok di disk (restart < satu jam: tidak perlu menyentuh jaringan);
//  3. GET /app dengan cookie lengkap;
//  4. rotasi cookie lalu GET /app lagi (cookie pendamping mungkin basi);
//  5. GET /app tanpa cookie volatile (TS basi kadang membuat Google
//     menganggap permintaan tidak sah walau __Secure-1PSID sehat).
//
// Setelah semua gagal, error asli dilaporkan supaya owner tahu sesinya benar-
// benar mati dan bukan cuma jaringan.
func (c *Client) ensureMeta(ctx context.Context) error {
	c.metaMu.Lock()
	fresh := c.at != "" && time.Since(c.metaAt) < metaTTL
	tried := c.metaTried
	cs := c.cookies
	c.metaMu.Unlock()
	if fresh {
		return nil
	}
	if cs == nil {
		return fmt.Errorf("%w: cookie kosong", ErrNoCookie)
	}
	if !tried {
		c.metaMu.Lock()
		c.metaTried = true
		c.metaMu.Unlock()
		if c.loadMetaFile() {
			return nil
		}
	}

	err := c.fetchMetaWith(ctx, cs.Header(), cs)
	if err == nil || !errors.Is(err, ErrCookieRejected) {
		return err
	}

	logfmt.Warn("gemini: /app tanpa SNlM0e, rotasi lalu coba sekali lagi\n")
	if rerr := c.rotate(ctx, true); rerr == nil {
		if err = c.fetchMetaWith(ctx, cs.Header(), cs); err == nil {
			return nil
		}
	}

	logfmt.Warn("gemini: /app masih menolak, coba tanpa cookie volatile\n")
	if header := cs.HeaderExcept(volatileCookieNames()...); header != "" {
		if err2 := c.fetchMetaWith(ctx, header, cs); err2 == nil {
			return nil
		}
	}
	return err
}

// fetchMetaWith melakukan satu GET /app memakai header Cookie tertentu dan
// menyimpan token yang ditemukan. Cookie hasil Set-Cookie tetap diserap ke cs.
func (c *Client) fetchMetaWith(ctx context.Context, header string, cs *CookieSet) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, appURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", defaultUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "id-ID,id;q=0.9,en-US;q=0.8,en;q=0.7")
	req.Header.Set("Cookie", header)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("gagal buka halaman gemini: %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w: halaman /app %d", ErrCookieRejected, resp.StatusCode)
	case resp.StatusCode >= 400:
		return fmt.Errorf("%w: halaman /app %d", ErrHTTP, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("gagal baca halaman gemini: %w", err)
	}
	html := string(body)

	at := firstMatch(xsrfRe, html)
	if at == "" {
		return fmt.Errorf("%w: token SNlM0e tidak ditemukan (sesi mati?)", ErrCookieRejected)
	}
	bl := firstMatch(buildRe, html)
	if bl == "" {
		bl = buildAlt.FindString(html)
	}
	if bl == "" {
		return fmt.Errorf("%w: build label tidak ditemukan", ErrShape)
	}

	c.metaMu.Lock()
	c.at = at
	c.bl = bl
	if sid := firstMatch(sidRe, html); sid != "" {
		c.sid = sid
	}
	if push := firstMatch(pushIDRe, html); push != "" {
		c.pushID = push
	}
	c.metaAt = time.Now()
	c.metaMu.Unlock()

	if changed := cs.MergeSetCookies(resp.Cookies()); changed {
		c.saveCookies(cs)
	}
	c.saveMetaFile()
	return nil
}

func (c *Client) pushIDValue() string {
	c.metaMu.Lock()
	defer c.metaMu.Unlock()
	if c.pushID != "" {
		return c.pushID
	}
	return defaultPush
}

func firstMatch(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

func applyBrowserHeaders(req *http.Request, cs *CookieSet) {
	req.Header.Set("User-Agent", defaultUA)
	req.Header.Set("Accept-Language", "id-ID,id;q=0.9,en-US;q=0.8,en;q=0.7")
	req.Header.Set("Origin", baseURL)
	req.Header.Set("Referer", baseURL+"/")
	req.Header.Set("Cookie", cs.Header())
}

// Chat menjalankan satu giliran percakapan. Semua permintaan diserialkan
// (satu akun = satu stream), rotasi cookie dilakukan sebelum ambil token
// karena rotasi membatalkan token `at` yang sudah ada.
func (c *Client) Chat(ctx context.Context, in Request) (*Result, error) {
	if strings.TrimSpace(in.Prompt) == "" && len(in.Files) == 0 {
		return nil, fmt.Errorf("%w: prompt kosong", ErrShape)
	}
	if err := c.ensureCookies(); err != nil {
		return nil, err
	}

	c.reqMu.Lock()
	defer c.reqMu.Unlock()

	// 1. Rotasi dulu (cookie baru membatalkan `at` lama → meta diambil ulang).
	if err := c.rotate(ctx, false); err != nil {
		if errors.Is(err, ErrCookieRejected) {
			return nil, err
		}
		// Rotasi gagal karena jaringan bukan alasan membatalkan chat:
		// lanjut dengan cookie yang ada.
	}
	// 2. Token halaman.
	if err := c.ensureMeta(ctx); err != nil {
		return nil, err
	}

	res, err := c.sendChat(ctx, in)
	if errors.Is(err, ErrXsrf) {
		// Token `at` basi (build label/token berubah): ambil ulang sekali.
		// Nilainya harus dibuang, bukan cuma metaAt-nya — kalau tidak,
		// tembolok disk bisa mengembalikan token basi yang sama.
		c.metaMu.Lock()
		c.at = ""
		c.metaAt = time.Time{}
		c.metaMu.Unlock()
		if err := c.ensureMeta(ctx); err != nil {
			return nil, err
		}
		res, err = c.sendChat(ctx, in)
	}
	return res, err
}

// ErrXsrf menandai token `at` kedaluwarsa (400 + kata "xsrf").
var ErrXsrf = errors.New("token xsrf kedaluwarsa")

func (c *Client) sendChat(ctx context.Context, in Request) (*Result, error) {
	_, at, bl, sid := c.snapshot()
	lang := strings.TrimSpace(in.Language)
	if lang == "" {
		lang = "id"
	}
	payload := buildChatRequest(in, lang)
	form := url.Values{}
	form.Set("f.req", payload)
	if at != "" {
		form.Set("at", at)
	}

	q := url.Values{}
	q.Set("bl", bl)
	q.Set("hl", lang)
	q.Set("_reqid", strconv.Itoa(100000+rand.Intn(899999)))
	q.Set("rt", "c")
	if sid != "" {
		q.Set("f.sid", sid)
	}
	endpoint := chatEndpoint + "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	cs, _, _, _ := c.snapshot()
	applyBrowserHeaders(req, cs)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=UTF-8")
	req.Header.Set("X-Same-Domain", "1")
	req.Header.Set("Accept", "*/*")

	sendStart := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gagal kirim prompt: %w", err)
	}
	defer resp.Body.Close()

	if changed := cs.MergeSetCookies(resp.Cookies()); changed {
		c.saveCookies(cs)
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("%w: chat %d", ErrCookieRejected, resp.StatusCode)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("%w: chat 429", ErrLimit)
	}
	if resp.StatusCode >= 400 {
		snippet := readSnippet(resp.Body, 512)
		if resp.StatusCode == http.StatusBadRequest && strings.Contains(strings.ToLower(snippet), "xsrf") {
			return nil, ErrXsrf
		}
		return nil, fmt.Errorf("%w: chat %d: %s", ErrHTTP, resp.StatusCode, snippet)
	}

	raw := &limitedBuffer{limit: 128 << 10}
	body := io.TeeReader(io.LimitReader(resp.Body, maxResponseBytes), raw)
	res, err := parseStreamIdle(body, defaultStreamIdle)
	if err != nil {
		return nil, err
	}
	if res.Text == "" && len(res.Images) == 0 {
		// Server membalas tanpa teks/gambar. Tanpa dump, kegagalan (mis. pada
		// permintaan ber-lampiran) hanya tampak sebagai "tidak terbaca".
		c.dumpEmptyPayload(raw.buf, time.Since(sendStart))
		return nil, ErrEmpty
	}
	// Penolakan generik ("… hanya model bahasa …") untuk prompt wajar adalah
	// gejala request/keadaan percakapan yang tidak pas, bukan penolakan
	// kebijakan. Payload mentahnya disimpan supaya bisa diperiksa owner.
	if len(res.Images) == 0 && isCannedRefusal(res.Text) && time.Since(sendStart) < 10*time.Second {
		c.dumpRawPayload(raw.buf, res.Text, time.Since(sendStart))
	}
	return res, nil
}

// cannedRefusalRe mendeteksi kalimat penolakan generik yang kadang dikirim
// server Gemini untuk pertanyaan biasa.
var cannedRefusalRe = regexp.MustCompile(`(?i)(hanya|sebagai)\s+model\s+bahasa`)

func isCannedRefusal(text string) bool {
	return cannedRefusalRe.MatchString(text)
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

// dumpRawPayload menyimpan payload penolakan generik ke folder temp bot.
func (c *Client) dumpRawPayload(raw []byte, text string, dur time.Duration) {
	if path := c.writeRawDump("gemini-raw", "text="+text, raw, dur); path != "" {
		logfmt.Warn("gemini: jawaban penolakan generik; payload mentah disimpan di %s\n", path)
	}
}

// dumpEmptyPayload menyimpan stream ketika server tidak mengirim teks/gambar.
// Inilah bukti yang dibutuhkan saat permintaan ber-lampiran gagal: isinya
// memuat penanda error/struktur balasan yang tidak dikenali parser.
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
	c.metaMu.Lock()
	c.lastDump = name
	c.metaMu.Unlock()
	return name
}

// LastDumpPath mengembalikan path dump diagnostik terakhir (kosong = tidak
// ada). Dipakai `.gemini diag` supaya owner tahu berkas mana yang harus
// diperiksa.
func (c *Client) LastDumpPath() string {
	c.metaMu.Lock()
	defer c.metaMu.Unlock()
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
