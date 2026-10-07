// Package scraper mengambil media langsung dari platform sosial via API
// scraper pihak ketiga (port dari koleksi D:\scrape). yt-dlp SUDAH DIHAPUS
// total (keputusan owner) — tidak ada fallback exec lagi.
//
// PENTING: API-API ini tidak resmi, tanpa SLA, bisa mati/berubah kapan
// saja — makanya tiap platform punya RANTAI fallback berjenjang (meniru
// pola tiktok.js / instagram.js aslinya). Jangan andalkan satu sumber saja.
package scraper

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
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
var httpClient = &http.Client{Timeout: 20 * time.Second}

// dlClient KHUSUS mengunduh byte media (bukan API): timeout 10 menit.
// KASUS NYATA (Sep 2026): audio YouTube 15,9MB butuh ~38 detik bahkan dari
// jaringan cepat — timeout 20 detik yang dipakai bersama menjamin gagal
// (context deadline exceeded while reading body) untuk file belasan MB di
// panel yang lebih lambat. Cap 100MB tetap (lihat downloadBytes).
var dlClient = &http.Client{Timeout: 10 * time.Minute}

// fetchJSON GET JSON dan decode ke v. Error menyebut nama engine supaya
// rantai fallback bisa merangkai pesan gabungan yang informatif.
func fetchJSON(ctx context.Context, engine, rawURL string, headers map[string]string, v any) error {
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
	resp, err := dlClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("media HTTP %d", resp.StatusCode)
	}
	// Batas 100MB per file. Pakai dlClient (timeout 10 menit), BUKAN
	// httpClient 20-detik yang hanya untuk API JSON.
	data, err := io.ReadAll(io.LimitReader(resp.Body, (100<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 100<<20 {
		return nil, fmt.Errorf("file media terlalu besar (>100MB)")
	}
	return data, nil
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
		if !isPublicIP(ip) {
			return fmt.Errorf("host internal tidak diizinkan")
		}
		return nil
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(addrs) == 0 {
		return fmt.Errorf("host tidak bisa di-resolve")
	}
	for _, a := range addrs {
		if !isPublicIP(a.IP) {
			return fmt.Errorf("host mengarah ke alamat internal")
		}
	}
	return nil
}

func isPublicIP(ip net.IP) bool {
	return !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() &&
		!ip.IsLinkLocalMulticast() && !ip.IsUnspecified() && !ip.IsMulticast()
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
