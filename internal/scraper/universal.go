package scraper

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// FetchUniversal mengambil media APAPUN via j2download.com (mendukung
// puluhan platform: TikTok, IG, FB, Pinterest, Twitter, YouTube, dll).
// Dipakai sebagai fallback universal tiap platform + sumber utama Facebook.
// Port dari j2download.js: GET home (cookies + csrf_token) →
// POST /api/autolink → parse fleksibel (bentuk responsnya tidak dikontrak
// ketat, jadi dicoba beberapa bentuk umum).
func FetchUniversal(ctx context.Context, pageURL string) (*ScrapeResult, error) {
	const engine = "j2download"

	cookies, csrf, err := j2Session(ctx)
	if err != nil {
		return nil, err
	}
	if csrf == "" {
		return nil, fmt.Errorf("%s: csrf token tidak ditemukan (situs berubah?)", engine)
	}

	body, _ := json.Marshal(map[string]any{"data": map[string]any{"url": pageURL, "unlock": true}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://j2download.com/api/autolink", strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", engine, err)
	}
	req.Header.Set("User-Agent", scraperUA)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://j2download.com")
	req.Header.Set("Referer", "https://j2download.com/id")
	req.Header.Set("Cookie", cookies)
	req.Header.Set("X-Csrf-Token", csrf)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", engine, err)
	}
	defer resp.Body.Close()
	rawBytes, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", engine, err)
	}
	var raw map[string]any
	if err := json.Unmarshal(rawBytes, &raw); err != nil {
		return nil, fmt.Errorf("%s: JSON invalid: %w", engine, err)
	}

	items := j2ExtractMedia(raw)
	if len(items) == 0 {
		return nil, fmt.Errorf("%s: tidak ada media dari respons", engine)
	}
	res := &ScrapeResult{Items: items, Source: "j2download"}
	if t, ok := raw["title"].(string); ok {
		res.Title = t
	}
	return res, nil
}

// j2Session GET halaman utama untuk cookies + csrf_token dari Set-Cookie.
func j2Session(ctx context.Context) (cookies, csrf string, err error) {
	const engine = "j2download"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://j2download.com", nil)
	if err != nil {
		return "", "", fmt.Errorf("%s: %w", engine, err)
	}
	req.Header.Set("User-Agent", scraperUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("%s: %w", engine, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))

	var parts []string
	for _, c := range resp.Cookies() {
		parts = append(parts, c.Name+"="+c.Value)
		if c.Name == "csrf_token" {
			csrf = c.Value
		}
	}
	return strings.Join(parts, "; "), csrf, nil
}

// j2ExtractMedia memetik URL media dari respons yang bentuknya longgar:
// dicoba kunci umum (medias[], url, downloadUrl, links[]...), tiap URL
// diklasifikasikan video/foto/audio dari ekstensi/tipe-nya.
func j2ExtractMedia(raw map[string]any) []ScrapeItem {
	var urls []string
	var collect func(v any)
	collect = func(v any) {
		switch t := v.(type) {
		case string:
			if strings.HasPrefix(t, "http") {
				urls = append(urls, t)
			}
		case []any:
			for _, e := range t {
				collect(e)
			}
		case map[string]any:
			for _, k := range []string{"url", "downloadUrl", "download_url", "link", "src", "file"} {
				if s, ok := t[k].(string); ok && strings.HasPrefix(s, "http") {
					urls = append(urls, s)
				}
			}
			for _, k := range []string{"medias", "links", "files", "formats", "results", "data"} {
				if nested, ok := t[k]; ok {
					collect(nested)
				}
			}
		}
	}
	collect(raw)

	seen := map[string]bool{}
	var items []ScrapeItem
	for _, u := range urls {
		lower := strings.ToLower(u)
		if i := strings.Index(lower, "?"); i >= 0 {
			lower = lower[:i]
		}
		if seen[lower] {
			continue
		}
		seen[lower] = true
		switch {
		case strings.HasSuffix(lower, ".mp4") || strings.HasSuffix(lower, ".mov") || strings.HasSuffix(lower, ".webm"):
			items = append(items, ScrapeItem{URL: u, Kind: KindVideo})
		case strings.HasSuffix(lower, ".mp3") || strings.HasSuffix(lower, ".m4a") || strings.HasSuffix(lower, ".ogg") || strings.HasSuffix(lower, ".wav"):
			items = append(items, ScrapeItem{URL: u, Kind: KindAudio})
		case strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".jpeg") || strings.HasSuffix(lower, ".png") || strings.HasSuffix(lower, ".webp") || strings.HasSuffix(lower, ".gif"):
			items = append(items, ScrapeItem{URL: u, Kind: KindPhoto})
		default:
			// Tanpa ekstensi jelas (CDN signed URL) = anggap video, nanti
			// dipastikan ulang dari Content-Type saat download.
			items = append(items, ScrapeItem{URL: u, Kind: KindVideo})
		}
	}
	return items
}
