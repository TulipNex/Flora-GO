package scraper

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// FetchYouTube mengambil video/audio YouTube via id-y2mate.com (port dari
// y2mate.js koleksi): cookie session → analyze → pilih format → convert →
// poll sampai URL unduhan keluar. audioOnly=true memilih mp3 128kbps
// (dipakai .youtubemp3 / .yt --mp3), selain itu mp4 720p.
func FetchYouTube(ctx context.Context, pageURL string, audioOnly bool) (*ScrapeResult, error) {
	const engine = "y2mate"
	const base = "https://id-y2mate.com"

	vid := extractYouTubeID(pageURL)
	if vid == "" {
		return nil, fmt.Errorf("%s: ID video tidak dikenali", engine)
	}

	// Deadline total ~100 detik (analyze + convert + poll); panel menunggu
	// dengan pesan status, jadi jangan gantung selamanya.
	tctx, cancel := context.WithTimeout(ctx, 100*time.Second)
	defer cancel()

	jar, _ := cookiejar.New(nil)
	cli := &http.Client{Timeout: 20 * time.Second, Jar: jar}
	ua := "Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/147.0.0.0 Mobile Safari/537.36"
	get := func(url string) error {
		req, err := http.NewRequestWithContext(tctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", ua)
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		resp, err := cli.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return nil
	}
	post := func(path string, form url.Values, v any) error {
		req, err := http.NewRequestWithContext(tctx, http.MethodPost, base+path, strings.NewReader(form.Encode()))
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", ua)
		req.Header.Set("Accept", "*/*")
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
		req.Header.Set("Origin", base)
		req.Header.Set("Referer", base+"/")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		resp, err := cli.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if err != nil {
			return err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		if err := json.Unmarshal(body, v); err != nil {
			return fmt.Errorf("JSON invalid: %w", err)
		}
		return nil
	}

	// 1. Session cookies.
	if err := get(base + "/"); err != nil {
		return nil, fmt.Errorf("%s: home: %w", engine, err)
	}

	// 2. Analyze.
	form := url.Values{"k_query": {pageURL}, "k_page": {"home"}, "hl": {"en"}, "q_auto": {"0"}}
	var an y2analyze
	if err := post("/mates/analyzeV2/ajax", form, &an); err != nil {
		return nil, fmt.Errorf("%s: analyze: %w", engine, err)
	}
	if an.Status != "ok" {
		return nil, fmt.Errorf("%s: analyze gagal", engine)
	}

	// 3. Pilih format: mp4 720p atau mp3 128kbps (fallback: auto, lalu key
	// pertama terurut supaya deterministik — map Go acak urutannya).
	wantType, wantQ := "mp4", "720p"
	if audioOnly {
		wantType, wantQ = "mp3", "128kbps"
	}
	group := an.Links[wantType]
	var ids []string
	for id := range group {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var key, token string
	for _, id := range ids {
		if group[id].Q == wantQ || id == wantQ {
			key, token = id, group[id].K
			break
		}
	}
	if token == "" {
		for _, id := range ids {
			if group[id].Q == "auto" {
				key, token = id, group[id].K
				break
			}
		}
	}
	if token == "" && len(ids) > 0 {
		key, token = ids[0], group[ids[0]].K
		_ = key
	}
	if token == "" {
		return nil, fmt.Errorf("%s: format %s %s tidak tersedia", engine, wantType, wantQ)
	}

	// 4. Convert.
	cform := url.Values{"vid": {firstNonEmpty(an.Vid, vid)}, "k": {token}}
	var cv map[string]any
	if err := post("/mates/convertV2/index", cform, &cv); err != nil {
		return nil, fmt.Errorf("%s: convert: %w", engine, err)
	}
	if dl := findY2DownloadURL(cv); dl != "" {
		return y2Result(an, dl, audioOnly), nil
	}

	// 5. Poll sampai URL keluar / gagal / deadline.
	bid, _ := cv["b_id"].(string)
	if bid == "" {
		if s, ok := cv["bId"].(string); ok {
			bid = s
		}
	}
	if bid == "" {
		return nil, fmt.Errorf("%s: convert tanpa hasil", engine)
	}
	deadline := time.Now().Add(80 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-tctx.Done():
			return nil, fmt.Errorf("%s: timeout menunggu convert", engine)
		case <-time.After(2 * time.Second):
		}
		pform := url.Values{"b_id": {bid}}
		var pv map[string]any
		if err := post("/mates/convertV2/pool", pform, &pv); err != nil {
			continue
		}
		if dl := findY2DownloadURL(pv); dl != "" {
			return y2Result(an, dl, audioOnly), nil
		}
		if failedY2(pv) {
			return nil, fmt.Errorf("%s: convert gagal di server", engine)
		}
	}
	return nil, fmt.Errorf("%s: timeout menunggu convert", engine)
}

type y2link struct {
	Size  string `json:"size"`
	F     string `json:"f"`
	Q     string `json:"q"`
	QText string `json:"q_text"`
	K     string `json:"k"`
}

type y2analyze struct {
	Status    string                       `json:"status"`
	Vid       string                       `json:"vid"`
	Title     string                       `json:"title"`
	T         any                          `json:"t"`
	Extractor string                       `json:"extractor"`
	Links     map[string]map[string]y2link `json:"links"`
}

// y2Result merakit hasil akhir y2mate (thumb dari i.ytimg.com).
func y2Result(an y2analyze, dl string, audioOnly bool) *ScrapeResult {
	thumb := ""
	if an.Vid != "" {
		thumb = "https://i.ytimg.com/vi/" + an.Vid + "/hqdefault.jpg"
	}
	kind := KindVideo
	if audioOnly {
		kind = KindAudio
	}
	return &ScrapeResult{
		Title:  strings.TrimSpace(an.Title),
		Items:  []ScrapeItem{{URL: dl, Thumb: thumb, Kind: kind}},
		Source: "y2mate",
	}
}

// findY2DownloadURL mencari URL unduhan di respons convert/poll: cek kunci
// umum dulu, lalu regex URL apa pun sebagai jaring terakhir.
func findY2DownloadURL(data any) string {
	if data == nil {
		return ""
	}
	if s, ok := data.(string); ok {
		if m := y2URLRe.FindString(s); m != "" {
			return strings.ReplaceAll(m, `\/`, `/`)
		}
		return ""
	}
	if m, ok := data.(map[string]any); ok {
		for _, k := range []string{"dlink", "download", "download_url", "url", "link", "result", "result_url", "file", "href"} {
			if s, ok := m[k].(string); ok && strings.HasPrefix(s, "http") {
				return strings.ReplaceAll(s, `\/`, `/`)
			}
		}
		for _, v := range m {
			if dl := findY2DownloadURL(v); dl != "" {
				return dl
			}
		}
	}
	if arr, ok := data.([]any); ok {
		for _, v := range arr {
			if dl := findY2DownloadURL(v); dl != "" {
				return dl
			}
		}
	}
	return ""
}

var y2URLRe = regexp.MustCompile(`https?://[^\s"'<>]+`)

// failedY2 true kalau respons pool menyatakan gagal eksplisit.
func failedY2(pv map[string]any) bool {
	if s, ok := pv["c_status"].(string); ok && s == "FAILED" {
		return true
	}
	if s, ok := pv["status"].(string); ok && s == "error" {
		return true
	}
	return false
}

// ytIDRe mengekstrak ID video 11-char dari youtu.be/ID, v=ID, /shorts/ID,
// /live/ID, /embed/ID.
var ytIDRe = regexp.MustCompile(`(?:youtu\.be/|youtube\.com/(?:watch\?.*v=|shorts/|live/|embed/))([A-Za-z0-9_-]{11})`)

func extractYouTubeID(rawURL string) string {
	if m := ytIDRe.FindStringSubmatch(rawURL); m != nil {
		return m[1]
	}
	return ""
}
