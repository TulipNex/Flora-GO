package scraper

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// FetchTikTok mengambil media TikTok via rantai: faa → tikwm → tiktokdl →
// musically. FAA utama (SD ringan, hemat CPU); sisanya fallback.
// Port dari tiktok.js + tikwm.js + tiktokdl.js + musically.js.
func FetchTikTok(ctx context.Context, pageURL string) (*ScrapeResult, error) {
	if res, err := faaTikTokFetch(ctx, pageURL); err == nil {
		res.Source = "faa"
		return res, nil
	} else {
		err0 := err
		if res, err := tikwmFetch(ctx, pageURL); err == nil {
			res.Source = "tikwm"
			return res, nil
		} else {
			err1 := err
			if res, err := tiktokdlFetch(ctx, pageURL); err == nil {
				res.Source = "tiktokdl"
				return res, nil
			} else {
				err2 := err
				if res, err := musicallyFetch(ctx, pageURL); err == nil {
					res.Source = "musically"
					return res, nil
				} else {
					return nil, fmt.Errorf("tiktok gagal semua: faa: %v | tikwm: %v | tiktokdl: %v | musically: %v", err0, err1, err2, err)
				}
			}
		}
	}
}

// --- faa (utama): GET api-faa.my.id/faa/tiktok, SD ringan dulu ---

// faaMaxSlides membatasi foto slide per perintah (album tanpa batas =
// 1 link bisa sesakkan server; 5 cukup untuk chat).
const faaMaxSlides = 5

type faaResp struct {
	Status bool `json:"status"`
	Result struct {
		Type     string `json:"type"`
		Title    string `json:"title"`
		Duration string `json:"duration"`
		Cover    string `json:"cover"`
		Data     string `json:"data"`
		Images   []string `json:"images"`
		Alternatives struct {
			HD       string `json:"hd"`
			SD       string `json:"sd"`
			WM       string `json:"wm"`
			Selected string `json:"selected"`
		} `json:"alternatives"`
		Author struct {
			Nickname string `json:"nickname"`
			Username string `json:"username"`
		} `json:"author"`
	} `json:"result"`
}

// faaParseDuration mengambil angka depan "11 seconds" -> 11. Gagal -> 0.
func faaParseDuration(s string) int {
	f := strings.Fields(strings.TrimSpace(s))
	if len(f) == 0 {
		return 0
	}
	n, err := strconv.Atoi(f[0])
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func faaTikTokFetch(ctx context.Context, pageURL string) (*ScrapeResult, error) {
	const engine = "faa"
	timeoutCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var r faaResp
	api := "https://api-faa.my.id/faa/tiktok?url=" + urlQueryEscapeGo(pageURL)
	if err := fetchJSON(timeoutCtx, engine, api, nil, &r); err != nil {
		return nil, err
	}
	if !r.Status {
		return nil, fmt.Errorf("%s: status false", engine)
	}
	d := r.Result
	// Slide foto (tanpa video): images[] -> item foto, dibatasi.
	if len(d.Images) > 0 {
		items := make([]ScrapeItem, 0, len(d.Images))
		for _, u := range d.Images {
			if strings.TrimSpace(u) == "" {
				continue
			}
			items = append(items, ScrapeItem{URL: u, Thumb: d.Cover, Kind: KindPhoto})
			if len(items) >= faaMaxSlides {
				break
			}
		}
		if len(items) == 0 {
			return nil, fmt.Errorf("%s: slideshow tanpa gambar", engine)
		}
		return &ScrapeResult{
			Title:  firstNonEmpty(d.Title, "TikTok"),
			Author: firstNonEmpty(d.Author.Nickname, d.Author.Username),
			Items:  items,
		}, nil
	}
	// Video: SD dulu (ringan, peluang lolos remux besar), HD cadangan.
	// Contoh nyata: data == hd == selected, sd terpisah lebih kecil.
	video := firstNonEmpty(d.Alternatives.SD, d.Data, d.Alternatives.HD, d.Alternatives.Selected, d.Alternatives.WM)
	if video == "" {
		return nil, fmt.Errorf("%s: tidak ada URL video", engine)
	}
	return &ScrapeResult{
		Title:    firstNonEmpty(d.Title, "TikTok"),
		Author:   firstNonEmpty(d.Author.Nickname, d.Author.Username),
		Duration: faaParseDuration(d.Duration),
		Items:    []ScrapeItem{{URL: video, Thumb: d.Cover, Kind: KindVideo}},
	}, nil
}

// --- tikwm (utama): GET tikwm.com/api, respons JSON kaya ---

type tikwmResp struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		Title       string `json:"title"`
		Play        string `json:"play"`
		HDPlay      string `json:"hdplay"`
		Cover       string `json:"cover"`
		OriginCover string `json:"origin_cover"`
		Duration    int    `json:"duration"`
		Author      struct {
			UniqueID string `json:"unique_id"`
			Nickname string `json:"nickname"`
		} `json:"author"`
		Images []string `json:"images"`
	} `json:"data"`
}

func tikwmFetch(ctx context.Context, pageURL string) (*ScrapeResult, error) {
	var r tikwmResp
	api := "https://www.tikwm.com/api/?url=" + urlQueryEscapeGo(pageURL) + "&hd=1"
	if err := fetchJSON(ctx, "tikwm", api, nil, &r); err != nil {
		return nil, err
	}
	if r.Code != 0 {
		return nil, fmt.Errorf("tikwm: %s", firstNonEmpty(r.Msg, "request failed"))
	}
	d := r.Data
	// Slideshow foto (tanpa video).
	if len(d.Images) > 0 {
		items := make([]ScrapeItem, 0, len(d.Images))
		for _, u := range d.Images {
			if strings.TrimSpace(u) == "" {
				continue
			}
			items = append(items, ScrapeItem{URL: u, Kind: KindPhoto})
		}
		if len(items) == 0 {
			return nil, fmt.Errorf("tikwm: slideshow tanpa gambar")
		}
		return &ScrapeResult{
			Title:  d.Title,
			Author: firstNonEmpty(d.Author.Nickname, d.Author.UniqueID),
			Items:  items,
		}, nil
	}
	video := firstNonEmpty(d.HDPlay, d.Play)
	if video == "" {
		return nil, fmt.Errorf("tikwm: tidak ada URL video")
	}
	return &ScrapeResult{
		Title:    d.Title,
		Author:   firstNonEmpty(d.Author.Nickname, d.Author.UniqueID),
		Duration: d.Duration,
		Items:    []ScrapeItem{{URL: video, Thumb: firstNonEmpty(d.Cover, d.OriginCover), Kind: KindVideo}},
	}, nil
}

// --- tiktokdl (fallback 1): videoId/audioId base64 ---

type tiktokdlResp struct {
	Status      bool   `json:"status"`
	Description string `json:"description"`
	Author      string `json:"author"`
	VideoID     string `json:"videoId"`
}

func tiktokdlFetch(ctx context.Context, pageURL string) (*ScrapeResult, error) {
	var r tiktokdlResp
	if err := fetchJSON(ctx, "tiktokdl", "https://www.tiktokdl.web.id/api/tiktok?url="+urlQueryEscapeGo(pageURL), nil, &r); err != nil {
		return nil, err
	}
	if !r.Status {
		return nil, fmt.Errorf("tiktokdl: status false")
	}
	video, err := base64.StdEncoding.DecodeString(strings.TrimSpace(r.VideoID))
	if err != nil || len(video) == 0 {
		return nil, fmt.Errorf("tiktokdl: no videoId")
	}
	return &ScrapeResult{
		Title:  r.Description,
		Author: r.Author,
		Items:  []ScrapeItem{{URL: string(video), Kind: KindVideo}},
	}, nil
}

// --- musically (fallback 2): scrape form musicaldown.com ---

func musicallyFetch(ctx context.Context, pageURL string) (*ScrapeResult, error) {
	const engine = "musically"
	// 1. GET halaman form (token + cookies).
	formReq, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://musicaldown.com/en", nil)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", engine, err)
	}
	setBrowserHeaders(formReq)
	formResp, err := httpClient.Do(formReq)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", engine, err)
	}
	formHTML, err := io.ReadAll(io.LimitReader(formResp.Body, 2<<20))
	formResp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", engine, err)
	}
	inputs, urlField, cookies := parseMusicalForm(string(formHTML), formResp)
	if urlField == "" {
		return nil, fmt.Errorf("%s: form tidak dikenali (situs berubah?)", engine)
	}
	inputs[urlField] = pageURL

	// 2. POST download.
	postReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://musicaldown.com/download", strings.NewReader(urlValues(inputs).Encode()))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", engine, err)
	}
	setBrowserHeaders(postReq)
	postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postReq.Header.Set("Origin", "https://musicaldown.com")
	postReq.Header.Set("Referer", "https://musicaldown.com/en")
	if cookies != "" {
		postReq.Header.Set("Cookie", cookies)
	}
	postResp, err := httpClient.Do(postReq)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", engine, err)
	}
	resultHTML, err := io.ReadAll(io.LimitReader(postResp.Body, 2<<20))
	postResp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", engine, err)
	}

	// 3. Parse link hasil (a.download) + deskripsi.
	links, desc := parseMusicalResult(string(resultHTML))
	if len(links) == 0 {
		return nil, fmt.Errorf("%s: tidak ada link download", engine)
	}
	best := links[0]
	for _, l := range links {
		lt := strings.ToLower(l.typ + " " + l.quality)
		if strings.Contains(lt, "mp4") && !strings.Contains(lt, "watermark") {
			best = l
			break
		}
	}
	return &ScrapeResult{
		Title: desc,
		Items: []ScrapeItem{{URL: best.url, Kind: KindVideo}},
	}, nil
}

type musicalLink struct {
	typ     string
	quality string
	url     string
}

// parseMusicalForm mengambil input form #submit-form + field URL kosong.
func parseMusicalForm(page string, resp *http.Response) (map[string]string, string, string) {
	inputs := map[string]string{}
	cookies := ""
	if resp != nil {
		var parts []string
		for _, c := range resp.Cookies() {
			parts = append(parts, c.Name+"="+c.Value)
		}
		cookies = strings.Join(parts, "; ")
	}
	doc, err := htmlParse(page)
	if err != nil {
		return inputs, "", cookies
	}
	var form *htmlNode
	var findForm func(*htmlNode)
	findForm = func(n *htmlNode) {
		if n.Type == htmlElementNode && n.Data == "form" {
			for _, a := range n.Attr {
				if a.Key == "id" && a.Val == "submit-form" {
					form = n
					return
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			findForm(c)
		}
	}
	findForm(doc)
	if form == nil {
		return inputs, "", cookies
	}
	urlField := ""
	var walk func(*htmlNode)
	walk = func(n *htmlNode) {
		if n.Type == htmlElementNode && n.Data == "input" {
			var name, val string
			for _, a := range n.Attr {
				if a.Key == "name" {
					name = a.Val
				}
				if a.Key == "value" {
					val = a.Val
				}
			}
			if name != "" {
				inputs[name] = val
				if val == "" && urlField == "" {
					urlField = name
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(form)
	return inputs, urlField, cookies
}

// parseMusicalResult mengambil link a.download + deskripsi .video-desc.
func parseMusicalResult(page string) ([]musicalLink, string) {
	doc, err := htmlParse(page)
	if err != nil {
		return nil, ""
	}
	var links []musicalLink
	var desc strings.Builder
	var walk func(*htmlNode)
	walk = func(n *htmlNode) {
		if n.Type == htmlElementNode && n.Data == "a" {
			var href, dataEvent string
			isDl := false
			for _, a := range n.Attr {
				switch a.Key {
				case "href":
					href = strings.TrimSpace(a.Val)
				case "data-event":
					dataEvent = a.Val
				case "class":
					for _, c := range strings.Fields(a.Val) {
						if c == "download" {
							isDl = true
						}
					}
				}
			}
			if isDl && href != "" {
				typ := strings.TrimSpace(strings.TrimSuffix(dataEvent, "_download_click"))
				links = append(links, musicalLink{typ: typ, quality: textContent(n), url: href})
			}
		}
		if n.Type == htmlElementNode && n.Data == "div" {
			for _, a := range n.Attr {
				if a.Key == "class" && strings.Contains(a.Val, "video-desc") {
					desc.WriteString(textContent(n))
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return links, desc.String()
}
