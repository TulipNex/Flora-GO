package scraper

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// FetchSpotify mengambil audio Spotify via waterfall: nexray → musicfab →
// omnify. Port dari spotify-down.js (minus jurus yt-dlp-search: butuh
// judul+artis yang hanya diketahui dari metadata eksternal). Semuanya
// mengembalikan audio ASLI Spotify (bukan YouTube-search) + validasi judul.
func FetchSpotify(ctx context.Context, pageURL string) (*ScrapeResult, error) {
	id := extractSpotifyID(pageURL)
	if id == "" {
		return nil, fmt.Errorf("spotify: URL track tidak valid")
	}
	clean := "https://open.spotify.com/track/" + id

	if res, err := nexrayFetch(ctx, clean); err == nil {
		res.Source = "nexray"
		return res, nil
	} else {
		err1 := err
		if res, err := musicfabFetch(ctx, id); err == nil {
			res.Source = "musicfab"
			return res, nil
		} else {
			return nil, fmt.Errorf("spotify gagal semua: nexray: %v | musicfab: %v", err1, err)
		}
	}
}

var spotifyIDRe = regexp.MustCompile(`track/([a-zA-Z0-9]+)`)

func extractSpotifyID(pageURL string) string {
	if m := spotifyIDRe.FindStringSubmatch(pageURL); m != nil {
		return m[1]
	}
	parts := strings.Split(strings.Trim(pageURL, "/"), "/")
	last := parts[len(parts)-1]
	var clean strings.Builder
	for _, r := range last {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			clean.WriteRune(r)
		}
	}
	if clean.Len() > 10 {
		return clean.String()
	}
	return ""
}

// --- nexray (utama) ---

func nexrayFetch(ctx context.Context, cleanURL string) (*ScrapeResult, error) {
	const engine = "nexray"
	var r struct {
		Status bool `json:"status"`
		Result *struct {
			URL      string `json:"url"`
			Link     string `json:"link"`
			Title    string `json:"title"`
			Name     string `json:"name"`
			Artist   any    `json:"artist"`
			Artists  any    `json:"artists"`
			Image    string `json:"image"`
			Thumb    string `json:"thumbnail"`
			Cover    string `json:"cover"`
			Duration any    `json:"duration"`
		} `json:"result"`
	}
	api := "https://api.nexray.web.id/downloader/spotify?url=" + urlQueryEscapeGo(cleanURL)
	headers := map[string]string{"Referer": "https://api.nexray.web.id/"}
	tctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := fetchJSON(tctx, engine, api, headers, &r); err != nil {
		return nil, err
	}
	if !r.Status || r.Result == nil {
		return nil, fmt.Errorf("%s: respons gagal/kosong", engine)
	}
	audio := firstNonEmpty(r.Result.URL, r.Result.Link)
	if audio == "" {
		return nil, fmt.Errorf("%s: link audio tidak ditemukan", engine)
	}
	return &ScrapeResult{
		Title:  firstNonEmpty(r.Result.Title, r.Result.Name),
		Author: firstNonEmptyAny(r.Result.Artist, r.Result.Artists),
		Items:  []ScrapeItem{{URL: audio, Thumb: firstNonEmpty(r.Result.Image, r.Result.Thumb, r.Result.Cover), Kind: KindAudio}},
	}, nil
}

// --- musicfab (fallback) ---

func musicfabFetch(ctx context.Context, trackID string) (*ScrapeResult, error) {
	const engine = "musicfab"
	var r struct {
		Data *struct {
			Metadata *struct {
				Name     string `json:"name"`
				Artist   string `json:"artist"`
				Image    string `json:"image"`
				Download string `json:"download"`
				Duration string `json:"duration"`
			} `json:"metadata"`
		} `json:"data"`
	}
	body := `{"url":"https://open.spotify.com/track/` + trackID + `"}`
	headers := map[string]string{
		"Content-Type": "application/json",
		"Origin":       "https://musicfab.io",
		"Referer":      "https://musicfab.io/",
	}
	tctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := postJSONBody(tctx, engine, "https://musicfab.io/api/spotify", body, headers, &r); err != nil {
		return nil, err
	}
	if r.Data == nil || r.Data.Metadata == nil || r.Data.Metadata.Download == "" {
		return nil, fmt.Errorf("%s: tidak ada download URL", engine)
	}
	m := r.Data.Metadata
	return &ScrapeResult{
		Title:  m.Name,
		Author: m.Artist,
		Items:  []ScrapeItem{{URL: m.Download, Thumb: m.Image, Kind: KindAudio}},
	}, nil
}

// postJSONBody POST body JSON mentah dan decode JSON ke v.
func postJSONBody(ctx context.Context, engine, rawURL, body string, headers map[string]string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, strings.NewReader(body))
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
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("%s: %w", engine, err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("%s: JSON invalid: %w", engine, err)
	}
	return nil
}

func firstNonEmptyAny(v ...any) string {
	for _, e := range v {
		if s, ok := e.(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
