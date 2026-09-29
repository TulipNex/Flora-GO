package scraper

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// FetchFacebook mengambil video/foto Facebook via rantai:
// chative → nexray → fdown.vn → (omnify via universal di level engine).
// fdown.vn penting untuk postingan multi-foto: Facebook mengubahnya jadi
// video slideshow, dan fdown.vn satu-satunya yang menawarkan MP4-nya
// (engine lain hanya memberi 1 foto sampul / null).
func FetchFacebook(ctx context.Context, pageURL string) (*ScrapeResult, error) {
	if res, err := chativeFetch(ctx, pageURL); err == nil {
		res.Source = "chative"
		return res, nil
	} else {
		err1 := err
		if res, err := nexrayFacebookFetch(ctx, pageURL); err == nil {
			res.Source = "nexray-fb"
			return res, nil
		} else {
			err2 := err
			if res, err := fdownvnFetch(ctx, pageURL); err == nil {
				res.Source = "fdownvn"
				return res, nil
			} else {
				return nil, fmt.Errorf("facebook gagal semua: chative: %v | nexray: %v | fdownvn: %v", err1, err2, err)
			}
		}
	}
}

// chativeGateway adalah API gateway milik chative.io (diambil dari JS bundle
// situsnya). Tanpa auth/key — GET langsung. Bisa berubah saat mereka
// redeploy; kalau 404, engine gagal dan fallback jalan.
const chativeGateway = "https://serverless-tooly-gateway-6n4h522y.ue.gateway.dev"

// chativeFetch memakai endpoint /facebook/video: {success, title,
// videos:{hd:{size,url}, sd:{size,url}}}.
func chativeFetch(ctx context.Context, pageURL string) (*ScrapeResult, error) {
	const engine = "chative"
	var r struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
		Title   string `json:"title"`
		Videos  *struct {
			HD struct {
				Size string `json:"size"`
				URL  string `json:"url"`
			} `json:"hd"`
			SD struct {
				Size string `json:"size"`
				URL  string `json:"url"`
			} `json:"sd"`
		} `json:"videos"`
	}
	api := chativeGateway + "/facebook/video?url=" + urlQueryEscapeGo(pageURL)
	if err := fetchJSON(ctx, engine, api, nil, &r); err != nil {
		return nil, err
	}
	if !r.Success || r.Videos == nil {
		return nil, fmt.Errorf("%s: %s", engine, firstNonEmpty(r.Error, "media tidak ditemukan"))
	}
	hd := strings.TrimSpace(r.Videos.HD.URL)
	sd := strings.TrimSpace(r.Videos.SD.URL)
	if hd == "" && sd == "" {
		return nil, fmt.Errorf("%s: tidak ada URL video (kemungkinan postingan foto)", engine)
	}
	link := firstNonEmpty(hd, sd)
	return &ScrapeResult{
		Title:  strings.TrimSpace(r.Title),
		Items:  []ScrapeItem{{URL: link, Kind: KindVideo}},
		Source: engine,
	}, nil
}

// nexrayFacebookFetch memakai /downloader/facebook (pola sama dengan engine
// spotify yang sudah jalan): {status, result:{title, video_hd, video_sd,
// audio}}. Judul mengandung HTML entities — di-decode.
func nexrayFacebookFetch(ctx context.Context, pageURL string) (*ScrapeResult, error) {
	const engine = "nexray-fb"
	var r struct {
		Status bool `json:"status"`
		Result *struct {
			Title   string `json:"title"`
			VideoHD string `json:"video_hd"`
			VideoSD string `json:"video_sd"`
			Audio   string `json:"audio"`
		} `json:"result"`
	}
	api := "https://api.nexray.web.id/downloader/facebook?url=" + urlQueryEscapeGo(pageURL)
	headers := map[string]string{"Referer": "https://api.nexray.web.id/"}
	if err := fetchJSON(ctx, engine, api, headers, &r); err != nil {
		return nil, err
	}
	if !r.Status || r.Result == nil {
		return nil, fmt.Errorf("%s: respons gagal/kosong", engine)
	}
	hd := strings.TrimSpace(r.Result.VideoHD)
	sd := strings.TrimSpace(r.Result.VideoSD)
	title := html.UnescapeString(strings.TrimSpace(r.Result.Title))
	if hd == "" && sd == "" {
		return nil, fmt.Errorf("%s: tidak ada URL video", engine)
	}
	return &ScrapeResult{
		Title:  title,
		Items:  []ScrapeItem{{URL: firstNonEmpty(hd, sd), Kind: KindVideo}},
		Source: engine,
	}, nil
}

// fdownvnFetch memakai fdown.vn: session cookie → CSRF → POST /api/download
// (HTML tombol kualitas) → render → poll sampai URL final. Satu-satunya
// engine yang menawarkan MP4 slideshow untuk postingan multi-foto.
func fdownvnFetch(ctx context.Context, pageURL string) (*ScrapeResult, error) {
	const engine = "fdownvn"
	const base = "https://fdown.vn"

	tctx, cancel := context.WithTimeout(ctx, 150*time.Second)
	defer cancel()

	jar, _ := cookiejar.New(nil)
	cli := &http.Client{Timeout: 30 * time.Second, Jar: jar}

	// 1. Session + CSRF (Laravel: cookie XSRF-TOKEN → header X-XSRF-TOKEN
	// yang sudah di-decode).
	{
		req, err := http.NewRequestWithContext(tctx, http.MethodGet, base+"/en", nil)
		if err != nil {
			return nil, fmt.Errorf("%s: home: %w", engine, err)
		}
		req.Header.Set("User-Agent", scraperUA)
		resp, err := cli.Do(req)
		if err != nil {
			return nil, fmt.Errorf("%s: home: %w", engine, err)
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	}
	xsrf := ""
	for _, c := range jar.Cookies(mustParseURL(base + "/")) {
		if c.Name == "XSRF-TOKEN" {
			xsrf, _ = url.QueryUnescape(c.Value)
		}
	}
	if xsrf == "" {
		return nil, fmt.Errorf("%s: csrf tidak ditemukan", engine)
	}

	// Helper POST JSON dengan header CSRF.
	post := func(path, body string, v any) error {
		var reader io.Reader
		reader = strings.NewReader(body)
		req, err := http.NewRequestWithContext(tctx, http.MethodPost, base+path, reader)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", scraperUA)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-XSRF-TOKEN", xsrf)
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		resp, err := cli.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if err != nil {
			return err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		if err := json.Unmarshal(raw, v); err != nil {
			return fmt.Errorf("JSON invalid: %w", err)
		}
		return nil
	}

	// 2. Resolve → HTML tombol kualitas.
	var dl struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		HTML    string `json:"html"`
	}
	if err := post("/api/download", `{"url":`+jsonString(pageURL)+`}`, &dl); err != nil {
		return nil, fmt.Errorf("%s: download: %w", engine, err)
	}
	if !dl.Success {
		return nil, fmt.Errorf("%s: %s", engine, firstNonEmpty(dl.Message, "resolve gagal"))
	}
	cands := parseFdownvnButtons(dl.HTML)
	if len(cands) == 0 {
		return nil, fmt.Errorf("%s: tidak ada tombol unduhan", engine)
	}
	// Pilih MP4 kualitas tertinggi (rank 1440p>1080p>720p>...).
	best := -1
	bestRank := -1
	for i, c := range cands {
		if r := fdownvnQualityRank(c.quality); r > bestRank {
			best, bestRank = i, r
		}
	}
	if best < 0 {
		return nil, fmt.Errorf("%s: tidak ada varian MP4", engine)
	}
	chosen := cands[best]

	// 3. Render → process_id.
	var rn struct {
		Success   bool   `json:"success"`
		ProcessID string `json:"process_id"`
	}
	rbody, _ := json.Marshal(map[string]string{"download_url": chosen.url, "provider": chosen.provider})
	if err := post("/api/download/render", string(rbody), &rn); err != nil {
		return nil, fmt.Errorf("%s: render: %w", engine, err)
	}
	if !rn.Success || rn.ProcessID == "" {
		return nil, fmt.Errorf("%s: render ditolak", engine)
	}

	// 4. Poll sampai completed (3 detik, maks ~60 detik).
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-tctx.Done():
			return nil, fmt.Errorf("%s: timeout menunggu render", engine)
		case <-time.After(3 * time.Second):
		}
		var pr struct {
			Status      string `json:"status"`
			Progress    int    `json:"progress"`
			DownloadURL string `json:"download_url"`
		}
		pbody, _ := json.Marshal(map[string]string{
			"process_id": rn.ProcessID, "download_url": chosen.url, "provider": chosen.provider,
		})
		if err := post("/api/download/progress", string(pbody), &pr); err != nil {
			continue
		}
		if pr.Status == "completed" && pr.DownloadURL != "" {
			return &ScrapeResult{
				Items:  []ScrapeItem{{URL: pr.DownloadURL, Thumb: chosen.thumb, Kind: KindVideo}},
				Source: "fdownvn",
			}, nil
		}
		if pr.Status == "failed" {
			return nil, fmt.Errorf("%s: render gagal di server", engine)
		}
	}
	return nil, fmt.Errorf("%s: timeout menunggu render", engine)
}

// fdownvnCand adalah satu tombol unduhan (MP4 saja yang dikumpulkan;
// tombol MP3 dilewati karena rantai ini untuk video).
type fdownvnCand struct {
	quality  string
	url      string
	provider string
	thumb    string
}

// fdownvnQualityRe menangkap label kualitas ("1080p", "720p", ...).
var fdownvnQualityRe = regexp.MustCompile(`(?i)(\d{3,4})p`)

// fdownvnQualityRank mengurutkan label kualitas ( researched: tombol
// 1440p/1080p/720p/480p/360p/240p). Tak ter-parse = rank 0.
func fdownvnQualityRank(label string) int {
	m := fdownvnQualityRe.FindStringSubmatch(label)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// parseFdownvnButtons memetik tombol unduhan per BLOK div.media-item
// (hasClass cocok token utuh — "media-item__action" tidak ikut cocok).
// Label kualitas ("1080p" dkk) MUNCUL SEBELUM tombolnya di tiap blok
// (terbukti dari HTML asli), jadi ancestor-climb TIDAK bisa — harus
// per-blok seperti parseSavereelsBlock. Blob terenkripsi diteruskan apa
// adanya (server men-decrypt saat render). Tombol MP3 dilewati.
func parseFdownvnButtons(page string) []fdownvnCand {
	doc, err := htmlParse(page)
	if err != nil {
		return nil
	}
	thumb := ""
	var cands []fdownvnCand
	var walk func(*htmlNode)
	walk = func(n *htmlNode) {
		if n.Type == htmlElementNode && n.Data == "div" && hasClass(n, "media-item") {
			if c := parseFdownvnBlock(n); c != nil {
				cands = append(cands, *c)
			}
			return
		}
		if n.Type == htmlElementNode && n.Data == "img" && hasClass(n, "media-result__thumb") {
			if src := strings.TrimSpace(attrVal(n, "src")); src != "" && thumb == "" {
				thumb = unescapeHTMLAttr(src)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	for i := range cands {
		cands[i].thumb = thumb
	}
	return cands
}

// parseFdownvnBlock memetik satu blok media-item: tombol .btn-render
// pertama + angka kualitas pertama di blok yang sama.
func parseFdownvnBlock(div *htmlNode) *fdownvnCand {
	var du, pv string
	found := false
	var walk func(*htmlNode)
	walk = func(n *htmlNode) {
		if found {
			return
		}
		if n.Type == htmlElementNode && n.Data == "button" && hasClass(n, "btn-render") {
			du = strings.TrimSpace(attrVal(n, "data-download-url"))
			pv = strings.TrimSpace(attrVal(n, "data-provider"))
			if du != "" && pv != "" {
				found = true
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(div)
	if !found {
		return nil
	}
	text := strings.ToUpper(textContent(div))
	if strings.Contains(text, "MP3") && !strings.Contains(text, "MP4") {
		return nil
	}
	q := fdownvnQualityRe.FindString(textContent(div))
	return &fdownvnCand{quality: q, url: du, provider: pv}
}

// unescapeHTMLAttr mengembalikan &amp; → & dkk pada atribut HTML (URL
// thumbnail mengandung & yang ter-escape).
func unescapeHTMLAttr(s string) string {
	return html.UnescapeString(s)
}

func mustParseURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		return &url.URL{Scheme: "https", Host: "fdown.vn"}
	}
	return u
}
