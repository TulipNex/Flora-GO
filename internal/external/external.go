// Package external adalah kumpulan ADAPTER SUMBER LUAR (port dari koleksi
// D:\scrape): selain mengunduh media dari platform sosial via API scraper
// pihak ketiga (tiktok, youtube, instagram, facebook, twitter, pinterest,
// spotify, xpvid), paket ini juga mengambil data
// NON-media (lirik `lrclib`, identifikasi lagu `shazam`, `flaticon`,
// `football`, `pihps`, `metadata`). Jadi nama "external" berarti "pengambil
// dari sumber luar", bukan hanya media.
//
// TIDAK ADA fallback universal lintas platform: omnify (dan j2download
// sebelumnya) sudah DIHAPUS. Endpoint omnify menuntut API key sejak Okt 2026
// (401 "Akses ditolak" untuk semua request), jadi ia bukan jaring pengaman
// lagi. Rantai cadangan sekarang milik tiap adapter sendiri — tambahkan
// engine baru di adapter platform yang bersangkutan, bukan di sini.
//
// Paket ini TIDAK lagi meng-exec yt-dlp — YouTube di sini murni via API
// scraper. (Catatan: plugin `plugins/downloader.go` justru memakai yt-dlp
// sebagai PRIMER untuk YouTube, dengan scraper sebagai fallback; itu jalur
// di luar paket ini.)
//
// PENTING: API-API ini tidak resmi, tanpa SLA, bisa mati/berubah kapan
// saja — makanya tiap platform punya RANTAI fallback berjenjang (meniru
// pola tiktok.js / instagram.js aslinya). Jangan andalkan satu sumber saja.
//
// Karena lirik/shazam ikut memakai helper di external.go (mis. DownloadBytes,
// audio/http), paket ini sengaja TIDAK dipecah fisik jadi dua — memecahnya
// hanya menambah paket dasar bersama tanpa manfaat nyata.
package external

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"wabot-go/internal/apicache"
	"wabot-go/internal/memlog"
	"wabot-go/internal/netx"
	"wabot-go/internal/spool"

	"golang.org/x/time/rate"
)

// MediaKind adalah jenis satu item hasil scrape.
type MediaKind string

const (
	KindVideo MediaKind = "video"
	KindPhoto MediaKind = "photo"
	KindAudio MediaKind = "audio"
)

// ScrapeItem adalah satu file media (URL langsung, siap download).
type ScrapeItem struct {
	URL   string
	Thumb string
	Mime  string
	Kind  MediaKind
}

// ScrapeResult adalah hasil normalisasi SEMUA adapter: judul, author,
// durasi (detik, 0 kalau tak diketahui), daftar item, dan nama engine
// yang berhasil (untuk log/debug: "tikwm", "j2download", ...).
type ScrapeResult struct {
	Title    string
	Author   string
	Duration int
	Items    []ScrapeItem
	Source   string
}

// scraperUA meniru browser desktop supaya API scraper tidak menolak.
const scraperUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/148.0.0.0 Safari/537.36"

// httpClient bersama: timeout 20 detik per request API (bukan download).
var httpClient = &http.Client{Timeout: 20 * time.Second, Transport: netx.TunedTransport()}

// rateLimiters per engine (Tahap 4): membatasi laju request API eksternal.
var (
	rateMu       sync.Mutex
	rateLimiters = map[string]*rate.Limiter{}
)

func externalRateRPS() float64 {
	raw := strings.TrimSpace(os.Getenv("EXTERNAL_RATE_RPS"))
	if raw == "" {
		return 10
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v <= 0 {
		return 10
	}
	if v > 50 {
		return 50
	}
	return v
}

func rateLimiterFor(engine string) *rate.Limiter {
	engine = strings.TrimSpace(engine)
	if engine == "" {
		engine = "default"
	}
	rateMu.Lock()
	defer rateMu.Unlock()
	if l, ok := rateLimiters[engine]; ok {
		return l
	}
	l := rate.NewLimiter(rate.Limit(externalRateRPS()), int(externalRateRPS()*2))
	if l.Burst() < 1 {
		l.SetBurst(1)
	}
	rateLimiters[engine] = l
	return l
}

// scrapeCache menyimpan ScrapeResult per URL (TTL 5 menit) supaya download
// berulang/autodownload tidak memanggil scraper berkali-kali.
var scrapeCache = apicache.New[*ScrapeResult](5*time.Minute, 512)

// dlClient KHUSUS mengunduh byte media (bukan API): timeout 10 menit.
// KASUS NYATA (Sep 2026): audio YouTube 15,9MB butuh ~38 detik bahkan dari
// jaringan cepat — timeout 20 detik yang dipakai bersama menjamin gagal
// (context deadline exceeded while reading body) untuk file belasan MB di
// panel yang lebih lambat. Cap 100MB tetap (lihat downloadBytes).
var dlClient = &http.Client{Timeout: 10 * time.Minute, Transport: netx.TunedTransport()}

// dlClientIPv4 mengunduh media dengan koneksi DIPAKSA IPv4. KASUS NYATA
// (Okt 2026, laptop owner): koneksi IPv6 ke CDN Apple (is1-ssl.mzstatic.com,
// 2403:300:...) diputus paksa di tengah body — "wsarecv: An existing
// connection was forcibly closed by the remote host" — sehingga cover Shazam
// selalu gagal. Happy Eyeballs hanya menolong saat connect, bukan saat
// transfer putus; karena itu IP literal IPv4 harus dipaksa. Client ini HANYA
// dipakai sebagai fallback setelah dlClient gagal.
var dlClientIPv4 = &http.Client{
	Timeout: 10 * time.Minute,
	Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp4", addr)
		},
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        64,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 15 * time.Second,
	},
}

// fetchJSON GET JSON dan decode ke v. Error menyebut nama engine supaya
// rantai fallback bisa merangkai pesan gabungan yang informatif.
func fetchJSON(ctx context.Context, engine, rawURL string, headers map[string]string, v any) error {
	if err := rateLimiterFor(engine).Wait(ctx); err != nil {
		return fmt.Errorf("%s: rate limit: %w", engine, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("%s: %w", engine, err)
	}
	req.Header.Set("User-Agent", scraperUA)
	for k, val := range headers {
		req.Header.Set(k, val)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", engine, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s: HTTP %d", engine, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("%s: %w", engine, err)
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("%s: JSON invalid: %w", engine, err)
	}
	return nil
}

// postFormJSON POST form-urlencoded dan decode JSON ke v.
func postFormJSON(ctx context.Context, engine, rawURL string, form map[string]string, headers map[string]string, v any) error {
	if err := rateLimiterFor(engine).Wait(ctx); err != nil {
		return fmt.Errorf("%s: rate limit: %w", engine, err)
	}
	values := url.Values{}
	for k, val := range form {
		values.Set(k, val)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, strings.NewReader(values.Encode()))
	if err != nil {
		return fmt.Errorf("%s: %w", engine, err)
	}
	req.Header.Set("User-Agent", scraperUA)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	for k, val := range headers {
		req.Header.Set(k, val)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", engine, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s: HTTP %d", engine, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("%s: %w", engine, err)
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("%s: JSON invalid: %w", engine, err)
	}
	return nil
}

// DownloadBytes adalah versi exported untuk dipakai plugins (engine
// downloader mengunduh tiap item hasil scrape lewat sini).
func DownloadBytes(ctx context.Context, rawURL string) ([]byte, error) {
	return DownloadBytesWithHeaders(ctx, rawURL, nil)
}

// DownloadBytesWithHeaders seperti DownloadBytes tapi dengan header HTTP
// tambahan (mis. Referer untuk CDN anti-hotlink seperti overfetch).
// Header User-Agent standar selalu dipasang; header custom menimpanya
// kecuali User-Agent yang tidak boleh dioverride diam-diam.
func DownloadBytesWithHeaders(ctx context.Context, rawURL string, headers map[string]string) ([]byte, error) {
	return downloadBytesVia(ctx, rawURL, headers, dlClient)
}

// DownloadBlobLimit mengunduh media ke spool: berkas <= 32MB tetap di RAM,
// di atasnya dialirkan ke file wabot-tmp dan dibaca bertahap oleh pemanggil.
//
// Ini jalur untuk berkas besar (.bk sampai 500MB). Menampung seluruh body di
// RAM membuat puncak RSS 1,6GB di panel 1,6GB (dua OOM kill Nov 2026),
// sedangkan jalur file hanya butuh buffer 32KB - isi berkas tidak pernah
// utuh di memori.
func DownloadBlobLimit(ctx context.Context, rawURL string, headers map[string]string, maxBytes int64) (*spool.Blob, error) {
	if maxBytes <= 0 {
		maxBytes = maxDownloadBytes
	}
	return downloadBlobViaLimit(ctx, rawURL, headers, dlClient, maxBytes)
}

// DownloadBytesIPv4 seperti DownloadBytes tetapi koneksinya DIPAKSA IPv4
// (lihat dlClientIPv4). Dipakai sebagai fallback saat jalur IPv6 memutus
// transfer di tengah body (kasus CDN Apple dari jaringan owner).
func DownloadBytesIPv4(ctx context.Context, rawURL string) ([]byte, error) {
	return downloadBytesVia(ctx, rawURL, nil, dlClientIPv4)
}

// maxDownloadBytes adalah batas bawaan unduhan media (100MB).
const maxDownloadBytes = 100 << 20

// downloadBytesVia adalah inti unduhan media: validasi URL publik, UA
// scraper, dan cap 100MB. Client dipilih pemanggil (default / IPv4).
func downloadBytesVia(ctx context.Context, rawURL string, headers map[string]string, client *http.Client) ([]byte, error) {
	return downloadBytesViaLimit(ctx, rawURL, headers, client, maxDownloadBytes)
}

// mediaTooLarge adalah satu-satunya pesan untuk cap ukuran, supaya pesan ke
// user dan pencocokan tes tidak bercabang.
func mediaTooLarge(maxBytes int64) error {
	return fmt.Errorf("file media terlalu besar (>%dMB)", maxBytes>>20)
}

// mediaGet menyiapkan request unduhan media: validasi SSRF, UA scraper,
// header tambahan, dan cek status. Body milik pemanggil (wajib Close).
func mediaGet(ctx context.Context, rawURL string, headers map[string]string, client *http.Client) (*http.Response, error) {
	if err := checkPublicURL(ctx, rawURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", scraperUA)
	for k, v := range headers {
		if strings.EqualFold(k, "User-Agent") {
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, fmt.Errorf("media HTTP %d", resp.StatusCode)
	}
	return resp, nil
}

// readAllLimit membaca body dengan cap maxBytes. hint = Content-Length
// (boleh -1): kalau diketahui, buffer dialokasikan PERSIS sebesar itu
// sehingga tidak ada pertumbuhan bertahap 100+200+400+500MB seperti
// io.ReadAll (TotalAlloc turun ~2x lipat).
func readAllLimit(r io.Reader, maxBytes, hint int64) ([]byte, error) {
	if hint > maxBytes {
		return nil, mediaTooLarge(maxBytes)
	}
	if hint > 0 {
		buf := make([]byte, hint)
		n, err := io.ReadFull(r, buf)
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			// Content-Length menjanjikan hint byte tapi koneksi berhenti
			// lebih awal: jangan pernah mengembalikan media terpotong
			// sebagai sukses (io.ReadAll dulu melaporkan error ini).
			return nil, fmt.Errorf("media terpotong (%d dari %d byte)", n, hint)
		}
		if err != nil {
			return nil, err
		}
		// Server mengirim lebih dari Content-Length: baca sisanya dengan cap.
		rest, err := io.ReadAll(io.LimitReader(r, maxBytes-int64(n)+1))
		if err != nil {
			return nil, err
		}
		if int64(len(rest)) > maxBytes-int64(n) {
			return nil, mediaTooLarge(maxBytes)
		}
		return append(buf, rest...), nil
	}
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, mediaTooLarge(maxBytes)
	}
	return data, nil
}

// downloadBytesViaLimit sama seperti downloadBytesVia tetapi cap ukurannya
// parameter.
//
// Setiap pemanggilan tercatat di riwayat memori (.memlog top) dengan label
// host sumber: unduhan media adalah alokasi besar yang sah, dan tanpa catatan
// ini kenaikan heap/RSS dari file 50MB tidak bisa dibedakan dari kebocoran.
//
// Content-Length diperiksa SEBELUM membaca body: berkas 4GB yang dibatasi
// 500MB dulu tetap dibaca 500MB+1 byte lalu ditolak - alokasi ~1,2GB untuk
// hasil nol. Sekarang ditolak dalam satu request header.
func downloadBytesViaLimit(ctx context.Context, rawURL string, headers map[string]string, client *http.Client, maxBytes int64) (data []byte, err error) {
	op := memlog.Begin("download", urlHostLabel(rawURL))
	defer func() { op.End(err == nil, "") }()

	// Pakai client media (timeout 10 menit), BUKAN httpClient 20-detik yang
	// hanya untuk API JSON.
	resp, err := mediaGet(ctx, rawURL, headers, client)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.ContentLength > maxBytes {
		return nil, mediaTooLarge(maxBytes)
	}
	return readAllLimit(resp.Body, maxBytes, resp.ContentLength)
}

// downloadBlobViaLimit adalah versi spool dari downloadBytesViaLimit: badan
// respons dialirkan ke spool (RAM <=32MB, selebihnya file) sehingga berkas
// besar tidak pernah utuh di memori.
func downloadBlobViaLimit(ctx context.Context, rawURL string, headers map[string]string, client *http.Client, maxBytes int64) (blob *spool.Blob, err error) {
	op := memlog.Begin("download", urlHostLabel(rawURL))
	defer func() { op.End(err == nil, "") }()

	resp, err := mediaGet(ctx, rawURL, headers, client)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.ContentLength > maxBytes {
		return nil, mediaTooLarge(maxBytes)
	}

	blob, err = spool.ReadFrom(io.LimitReader(resp.Body, maxBytes+1), 0)
	if err != nil {
		return nil, err
	}
	if blob.Size() > maxBytes {
		blob.Close()
		return nil, mediaTooLarge(maxBytes)
	}
	return blob, nil
}

// urlHostLabel mengambil host dari URL sebagai label operasi (grup .memlog
// top jadi "download/cdninstagram.com" alih-alih satu baris raksasa). URL
// rusak = label kosong; telemetri tidak boleh mengganggu unduhan.
func urlHostLabel(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if len(host) > 48 {
		host = host[:48]
	}
	return host
}

// checkPublicURL menolak URL non-http(s) dan host internal (loopback,
// private, link-local, multicast) — anti SSRF untuk URL media yang datang
// dari API pihak ketiga.
func checkPublicURL(ctx context.Context, rawURL string) error {
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return fmt.Errorf("skema URL tidak diizinkan")
	}
	host := rawURL
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(strings.ToLower(host), "[]")
	if ip := net.ParseIP(host); ip != nil {
		if !netx.IsPublicIP(ip) {
			return fmt.Errorf("host internal tidak diizinkan")
		}
		return nil
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(addrs) == 0 {
		return fmt.Errorf("host tidak bisa di-resolve")
	}
	for _, a := range addrs {
		if !netx.IsPublicIP(a.IP) {
			return fmt.Errorf("host mengarah ke alamat internal")
		}
	}
	return nil
}

// firstNonEmpty mengembalikan string non-kosong pertama.
func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
