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
	"sync/atomic"
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

	// consentCookie adalah penanda "persetujuan cookie diterima". Di IP
	// datacenter Eropa (panel bot) Google memasang dinding persetujuan: tanpa
	// penanda ini server tidak memberi cookie `NID`, dan permintaan chat
	// dianggap sesi tamu yang berbeda-beda sehingga percakapan lanjutan selalu
	// dijawab `1097` ("Can't continue conversation"). Nilainya bukan rahasia
	// siapa pun — hanya status persetujuan, sama seperti yang dikirim browser
	// setelah tombol "Accept all" ditekan.
	consentCookie = "SOCS=CAI"
	// metaTTL: build label `bl` berubah tiap deploy Gemini, dan `f.sid`
	// terikat pada halaman yang menyajikannya. Satu jam cukup hemat.
	metaTTL = time.Hour

	// ticketCooldown: setelah server menolak tiket percakapan, tiket dimatikan
	// selama ini. Dipilih sama dengan umur cookie tamu: begitu sesi tamu
	// diperbarui, tiket dicoba sekali lagi. Tanpa jeda ini setiap giliran
	// membakar dua permintaan (satu selalu ditolak) — terbukti di panel.
	ticketCooldown = guestCookieTTL

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

// ticketNow bisa diganti di tes supaya masa jeda tiket dapat diuji tanpa tidur.
var ticketNow = time.Now

// Result adalah jawaban satu giliran. ConversationID/ResponseID/ReplyID
// adalah "tiket percakapan" dari server: bila diisi, giliran berikutnya bisa
// melanjutkan percakapan tanpa menyalin ulang riwayat ke prompt.
type Result struct {
	Text           string
	ConversationID string
	ResponseID     string
	ReplyID        string
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

	// uid adalah identitas klien tamu (UUID), dikirim di payload dan header
	// supaya rangkaian permintaan tampak berasal dari satu klien.
	uid string
	// fullPayload memilih bentuk f.req lengkap (80 slot, seperti browser).
	// GEMINI_FULL_PAYLOAD=0 memaksa bentuk minimal lama.
	fullPayload bool
	// payloadFallback menandai bentuk penuh sudah ditolak server pada proses
	// ini, sehingga sisanya memakai bentuk minimal.
	payloadFallback atomic.Bool
	// resumeRejectedAt mencatat kapan tiket percakapan ditolak server (unix
	// nano, 0 = belum pernah). Selama masih dalam ticketCooldown, tiket tidak
	// dikirim maupun disimpan supaya tidak ada permintaan yang terbuang di
	// setiap giliran (dulu: 2 permintaan per balasan). Setelah cookie tamu
	// diperbarui, tiket dicoba sekali lagi — kalau server mengaktifkan resume
	// untuk tamu, bot langsung memakainya lagi.
	resumeRejectedAt atomic.Int64

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
		http:        &http.Client{Timeout: 0, Transport: newTransport(pageHeaderTimeout)},
		chat:        &http.Client{Timeout: 0, Transport: newTransport(chatHeaderTimeout)},
		uid:         newUID(),
		fullPayload: envBool("GEMINI_FULL_PAYLOAD", true),
	}
}

// UID mengembalikan identitas klien yang sedang dipakai. Pemanggil yang ingin
// percakapan tetap dikenali setelah restart menyimpan nilai ini bersama tiket
// percakapan, lalu mengirimnya kembali lewat AskRequest.UID.
func (c *Client) UID() string { return c.uid }

// TicketEnabled melaporkan apakah tiket percakapan benar-benar dipakai, yaitu
// hanya pada bentuk permintaan lengkap dan selama server belum menolaknya.
// Bila bentuk itu dimatikan (GEMINI_FULL_PAYLOAD=0) atau tiket sedang ditolak,
// pemanggil harus membawa konteks lewat riwayat di dalam prompt.
func (c *Client) TicketEnabled() bool {
	return c.fullPayloadActive() && !c.ticketBlocked()
}

// TicketRejected melaporkan apakah tiket percakapan sedang dimatikan karena
// ditolak server. Dipakai teks status/diagnosa supaya owner tahu konteks
// dibawa lewat riwayat, bukan diingat server.
func (c *Client) TicketRejected() bool { return c.ticketBlocked() }

// ticketBlocked true bila penolakan tiket masih dalam masa jeda. Dibaca atomik
// karena Diag memanggil send tanpa reqMu.
func (c *Client) ticketBlocked() bool {
	at := c.resumeRejectedAt.Load()
	return at != 0 && ticketNow().Sub(time.Unix(0, at)) < ticketCooldown
}

// fullPayloadActive true bila bentuk permintaan lengkap boleh dipakai.
// payloadFallback dibaca atomik karena Diag memanggil send tanpa reqMu.
func (c *Client) fullPayloadActive() bool {
	return c.fullPayload && !c.payloadFallback.Load()
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
//
// Kredensial lama dikembalikan setelah selesai. Cookie baru berarti sesi tamu
// baru (`NID` lain), dan tiket percakapan yang sedang hidup langsung jadi
// asing; tanpa pemulihan ini `.gemini status` (yang memanggil Ping untuk baris
// Koneksi) membunuh percakapan yang sedang berjalan — bug nyata Nov 2026.
func (c *Client) Ping(ctx context.Context) error {
	// Kredensial lama hanya dikembalikan bila memang ada yang perlu dijaga:
	// saat tembolok kosong (proses baru), cookie hasil Ping justru lebih baik
	// dipakai supaya giliran berikutnya tidak mengulang HEAD.
	if prev := c.takeCredentials(); prev.cookie != "" {
		defer c.restoreCredentials(prev)
	}
	c.forget()
	_, _, _, err := c.credentials(ctx)
	return err
}

// credentialsSnapshot adalah salinan kredensial tamu yang sedang dipakai.
// Hanya dipakai di dalam proses: tidak pernah ditulis ke disk.
type credentialsSnapshot struct {
	cookie   string
	cookieAt time.Time
	bl, sid  string
	metaAt   time.Time
}

func (c *Client) takeCredentials() credentialsSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return credentialsSnapshot{
		cookie:   c.cookie,
		cookieAt: c.cookieAt,
		bl:       c.bl,
		sid:      c.sid,
		metaAt:   c.metaAt,
	}
}

func (c *Client) restoreCredentials(s credentialsSnapshot) {
	c.mu.Lock()
	c.cookie, c.cookieAt = s.cookie, s.cookieAt
	c.bl, c.sid, c.metaAt = s.bl, s.sid, s.metaAt
	c.mu.Unlock()
}

// forget membuang kredensial di memori supaya permintaan berikutnya mengambil
// cookie + parameter halaman baru.
func (c *Client) forget() {
	c.mu.Lock()
	c.cookie, c.bl, c.sid = "", "", ""
	c.cookieAt, c.metaAt = time.Time{}, time.Time{}
	c.mu.Unlock()
}

// Ask mengirim satu prompt tanpa tiket percakapan. Setara AskRequest dengan
// Resume kosong.
func (c *Client) Ask(ctx context.Context, prompt string) (*Result, error) {
	return c.AskRequest(ctx, AskRequest{Prompt: prompt})
}

// AskWith mengirim satu prompt dengan tiket percakapan. Bila server menolak
// tiket (ErrStaleConv), error dikembalikan apa adanya supaya pemanggil bisa
// mengulang memakai riwayat di dalam prompt.
func (c *Client) AskWith(ctx context.Context, prompt string, resume Resume) (*Result, error) {
	return c.AskRequest(ctx, AskRequest{Prompt: prompt, Resume: resume})
}

// AskRequest mengirim satu giliran. Karena server tidak menyimpan percakapan
// tamu tanpa tiket, konteks bisa dibawa dua cara: tiket (Resume) atau riwayat
// yang ditempel pemanggil ke prompt. Tiket hanya akselerator.
func (c *Client) AskRequest(ctx context.Context, req AskRequest) (res *Result, err error) {
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return nil, fmt.Errorf("%w: prompt kosong", ErrProtocol)
	}

	c.reqMu.Lock()
	defer c.reqMu.Unlock()

	op := memlog.Begin("gemini", "chat")
	defer func() { op.End(err == nil, "") }()

	uid := req.UID
	if uid == "" {
		uid = c.uid
	}
	resume := req.Resume
	if !c.TicketEnabled() {
		// Bentuk minimal tidak membawa slot tiket; begitu pula setelah server
		// menolak resume untuk sesi tamu ini — jangan kirim ulang tiap giliran.
		resume = Resume{}
	}

	res, err = c.askOnce(ctx, prompt, resume, uid)

	// Bentuk permintaan lengkap ditolak server (balasan kosong atau 400):
	// kembali ke bentuk minimal untuk sisa proses supaya bot tetap menjawab.
	if err != nil && c.fullPayloadActive() && isShapeRejection(err) {
		logfmt.Warn("gemini: bentuk permintaan penuh ditolak (%v), beralih ke bentuk minimal\n", err)
		c.payloadFallback.Store(true)
		res, err = c.askOnce(ctx, prompt, Resume{}, uid)
	}

	// Tiket ditolak: jangan bakar kuota untuk percobaan kredensial baru,
	// karena yang bermasalah tiketnya. Pemanggil memutuskan jalan cadangan.
	// Penanda resumeRejected mematikan tiket sampai cookie tamu diperbarui,
	// supaya giliran berikutnya tidak mengulang penolakan yang sama.
	if err != nil && !resume.Empty() && errors.Is(err, ErrStaleConv) {
		c.resumeRejectedAt.Store(ticketNow().UnixNano())
		return nil, err
	}

	if err != nil && retryable(err) {
		// Kredensial tamu bisa basi kapan saja (build label berubah, cookie
		// dirotasi server). Ambil ulang sekali sebelum menyerah.
		logfmt.Warn("gemini: percobaan pertama gagal (%v), ambil kredensial baru\n", err)
		c.forget()
		res, err = c.askOnce(ctx, prompt, resume, uid)
	}
	return res, err
}

// isShapeRejection menandai kegagalan yang biasanya berarti bentuk permintaan
// tidak diterima (bukan jaringan/kuota): balasan kosong atau HTTP 400.
func isShapeRejection(err error) bool {
	if errors.Is(err, ErrEmpty) {
		return true
	}
	var se *statusError
	return errors.As(err, &se) && se.status == http.StatusBadRequest
}

// retryable menandai kegagalan yang masuk akal diperbaiki oleh kredensial
// baru. ErrLimit dan ErrEmpty sengaja TIDAK diulang: yang pertama menghabiskan
// kuota, yang kedua berarti permintaan sudah diproses server.
func retryable(err error) bool {
	return errors.Is(err, ErrGuestCookie) || errors.Is(err, ErrMeta) || errors.Is(err, ErrStaleConv)
}

func (c *Client) askOnce(ctx context.Context, prompt string, r Resume, uid string) (*Result, error) {
	cookie, bl, sid, err := c.credentials(ctx)
	if err != nil {
		return nil, err
	}
	// Tiket dari sesi tamu lain (cookie/NID sudah berganti) pasti dijawab
	// `1097`. Jangan kirim sama sekali: satu permintaan yang gagal tetap
	// memakai kuota, dan baris log-nya menyesatkan ("tiket ditolak" padahal
	// penyebabnya rotasi cookie).
	if !r.Empty() && r.CookieID != "" && r.CookieID != cookieIdentity(cookie) {
		return nil, fmt.Errorf("%w: sidik jari sesi tamu berbeda", ErrTicketStale)
	}
	return c.send(ctx, cookie, bl, sid, prompt, r, uid)
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
// mengirim `NID` (dan beberapa cookie lain) yang dipakai endpoint chat tanpa
// login. Cookie persetujuan dikirim lebih dulu supaya IP yang kena dinding
// persetujuan tetap mendapat `NID` (lihat consentCookie).
func (c *Client) fetchGuestCookie(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, rootURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", defaultUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "id-ID,id;q=0.9,en-US;q=0.8,en;q=0.7")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	req.Header.Set("Cookie", consentCookie)

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

	cookie := guestCookieJar(consentCookie, resp)
	if cookie == "" {
		return "", fmt.Errorf("%w: Set-Cookie kosong", ErrGuestCookie)
	}
	return cookie, nil
}

// guestCookieJar merangkai SELURUH Set-Cookie menjadi satu nilai header Cookie,
// dimulai dari cookie persetujuan.
//
// Versi lama hanya memakai Set-Cookie pertama, dan itu penyebab bug nyata: di
// panel (IP datacenter Eropa) server mengirim `__Secure-ENID` lebih dulu dan
// menaruh `NID` di urutan berikutnya, sehingga `NID` terbuang. Tanpa `NID`
// setiap permintaan dianggap sesi tamu baru dan percakapan lanjutan dijawab
// `1097` — terbukti lewat probe: `COMPASS` saja → `1097`, `NID` disertakan →
// jawaban benar. Browser mengirim semua cookie, jadi bot juga harus begitu.
func guestCookieJar(seed string, resp *http.Response) string {
	var order []string
	values := map[string]string{}
	add := func(name, value string) {
		if _, seen := values[name]; !seen {
			order = append(order, name)
		}
		values[name] = value
	}
	if name, value, ok := strings.Cut(seed, "="); ok && name != "" && value != "" {
		add(name, value)
	}

	fromServer := 0
	for _, raw := range resp.Header.Values("Set-Cookie") {
		raw = strings.TrimSpace(raw)
		if i := strings.IndexByte(raw, ';'); i >= 0 {
			raw = raw[:i]
		}
		name, value, ok := strings.Cut(strings.TrimSpace(raw), "=")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if !ok || name == "" || value == "" {
			continue
		}
		add(name, value)
		fromServer++
	}
	// Tanpa satu pun Set-Cookie, halaman utama tidak memberi sesi tamu: itu
	// kegagalan yang harus dilaporkan, bukan "cookie persetujuan saja".
	if fromServer == 0 {
		return ""
	}

	parts := make([]string, 0, len(order))
	for _, name := range order {
		parts = append(parts, name+"="+values[name])
	}
	return strings.Join(parts, "; ")
}

// CookieNames mengembalikan nama cookie tamu yang sedang dipakai (tanpa nilai),
// dipakai `.gemini diag` supaya hilangnya `NID` langsung terlihat: tanpa `NID`
// server menolak melanjutkan percakapan (`1097`).
func (c *Client) CookieNames() []string {
	c.mu.Lock()
	cookie := c.cookie
	c.mu.Unlock()
	var names []string
	for _, part := range strings.Split(cookie, ";") {
		name, _, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && name != "" {
			names = append(names, name)
		}
	}
	return names
}

// CookieID mengembalikan sidik jari sesi tamu yang sedang dipakai (hash pendek
// dari nilai cookie `NID`), bukan nilainya — aman ditampilkan/disimpan.
//
// Tiket percakapan hanya berlaku selama sidik jari ini sama. Percakapan tamu
// terikat NILAI `NID`, bukan uid: probe Nov 2026 menunjukkan `NID` baru dengan
// uid sama tetap ditolak `1097`, sedangkan `NID` sama dengan uid berbeda tetap
// dilanjutkan. Kosong berarti cookie belum diambil (proses baru).
func (c *Client) CookieID() string {
	c.mu.Lock()
	cookie := c.cookie
	c.mu.Unlock()
	return cookieIdentity(cookie)
}

// cookieIdentity menghitung sidik jari dari nilai `NID` saja bila ada, dan dari
// seluruh header cookie bila tidak. Hanya `NID` yang menentukan identitas
// percakapan tamu, jadi cookie lain yang berubah (atau urutan cookie yang
// berbeda) tidak boleh membatalkan tiket.
func cookieIdentity(cookie string) string {
	if cookie == "" {
		return ""
	}
	id := cookie
	for _, part := range strings.Split(cookie, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || strings.TrimSpace(name) != "NID" {
			continue
		}
		if value = strings.TrimSpace(value); value != "" {
			id = "NID=" + value
		}
		break
	}
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:8])
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
func (c *Client) send(ctx context.Context, cookie, bl, sid, prompt string, r Resume, uid string) (*Result, error) {
	full := c.fullPayloadActive()
	form := url.Values{}
	if full {
		form.Set("f.req", buildChatRequestFull(prompt, defaultLang, r, uid))
	} else {
		form.Set("f.req", buildChatRequest(prompt, defaultLang))
	}

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
	if full {
		// Header internal halaman Gemini. Nilainya tidak dipakai untuk
		// autentikasi, hanya supaya permintaan berbentuk sama dengan browser:
		// parameter model, identitas klien, dan dua saklar UI.
		req.Header.Set("x-goog-ext-525001261-jspb", fmt.Sprintf(
			`[1,null,null,null,%q,null,null,0,[4,6],null,null,1,null,null,1,null,%q]`,
			hexToken(8), uid))
		req.Header.Set("x-goog-ext-525005358-jspb", fmt.Sprintf(`[%q,1]`, uid))
		req.Header.Set("x-goog-ext-73010990-jspb", "[0,0,0]")
		req.Header.Set("x-goog-ext-73010989-jspb", "[0]")
	}

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
		return nil, &statusError{err: ErrHTTP, status: resp.StatusCode, snippet: snippet}
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

// statusError membawa kode HTTP supaya pemanggil bisa membedakan 400 (bentuk
// permintaan ditolak) dari status lain tanpa membaca teks error. Pesan yang
// dihasilkan tetap sama seperti sebelumnya.
type statusError struct {
	err     error
	status  int
	snippet string
}

func (e *statusError) Error() string {
	if e.snippet == "" {
		return fmt.Sprintf("%v: chat %d", e.err, e.status)
	}
	return fmt.Sprintf("%v: chat %d: %s", e.err, e.status, e.snippet)
}

func (e *statusError) Unwrap() error { return e.err }

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
