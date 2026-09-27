package scraper

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// FetchInstagram mengambil media Instagram via rantai: fastdl → savereels.
// Port dari instagram.js + fastdl.js + savereels.js. (snapinsta butuh
// bypass Cloudflare + eval JS — slotnya diganti universal j2download di
// level engine.)
func FetchInstagram(ctx context.Context, pageURL string) (*ScrapeResult, error) {
	if res, err := fastdlFetch(ctx, pageURL); err == nil {
		res.Source = "fastdl"
		return res, nil
	} else {
		err1 := err
		if res, err := savereelsFetch(ctx, pageURL); err == nil {
			res.Source = "savereels"
			return res, nil
		} else {
			return nil, fmt.Errorf("instagram gagal semua: fastdl: %v | savereels: %v", err1, err)
		}
	}
}

// --- fastdl (utama): POST signed ke fastdl.app ---

const (
	fastdlKeyHex   = "792525efde6d921d6055a5d62dcebd39c8b5364e99fa87c5adf0e89391266d9c"
	fastdlTsBase   = 1773148641059
	fastdlEndpoint = "https://api-wh.fastdl.app/api/convert"
	fastdlProxy    = "https://cors.siputzx.my.id/"
)

// igPostRe memvalidasi URL post/reel/tv Instagram.
var igPostRe = regexp.MustCompile(`instagram\.com/(p|reel|tv)/`)

type fastdlItem struct {
	URL   any    `json:"url"`
	Type  string `json:"type"`
	Thumb string `json:"thumb"`
	Meta  *struct {
		Title    string `json:"title"`
		Username string `json:"username"`
	} `json:"meta"`
}

func fastdlItemURL(u any) string {
	switch v := u.(type) {
	case string:
		return v
	case []any:
		if len(v) > 0 {
			if s, ok := v[0].(string); ok {
				return s
			}
		}
	}
	return ""
}

func fastdlFetch(ctx context.Context, pageURL string) (*ScrapeResult, error) {
	const engine = "fastdl"
	clean := strings.TrimSuffix(strings.SplitN(pageURL, "?", 2)[0], "/") + "/"
	if !igPostRe.MatchString(clean) {
		return nil, fmt.Errorf("%s: bukan URL post/reel/tv", engine)
	}
	ts := time.Now().UnixMilli()
	key, err := hex.DecodeString(fastdlKeyHex)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", engine, err)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(clean + strconv.FormatInt(ts, 10)))

	endpoint := fastdlProxy + fastdlEndpoint
	form := map[string]string{
		"sf_url": clean,
		"ts":     strconv.FormatInt(ts, 10),
		"_ts":    strconv.Itoa(fastdlTsBase),
		"_tsc":   "0",
		"_sv":    "2",
		"_s":     hex.EncodeToString(mac.Sum(nil)),
	}
	headers := map[string]string{
		"Origin":  "https://fastdl.app",
		"Referer": "https://fastdl.app/",
	}
	// fastdl mengembalikan array (album) ATAU object (single) — coba array
	// dulu, gagal baru object (keduanya valid, bukan error).
	var arr []fastdlItem
	if err := postFormJSON(ctx, engine, endpoint, form, headers, &arr); err == nil && len(arr) > 0 {
		return fastdlAlbum(arr), nil
	}
	var single fastdlSingle
	if err := postFormJSON(ctx, engine, endpoint, form, headers, &single); err != nil {
		return nil, fmt.Errorf("%s: %v", engine, err)
	}
	return fastdlSingleResult(single)
}

func fastdlAlbum(arr []fastdlItem) *ScrapeResult {
	items := make([]ScrapeItem, 0, len(arr))
	for _, it := range arr {
		link := fastdlItemURL(it.URL)
		if link == "" {
			continue
		}
		kind := KindPhoto
		if it.Type == "video" {
			kind = KindVideo
		}
		items = append(items, ScrapeItem{URL: link, Thumb: it.Thumb, Kind: kind})
	}
	var title, user string
	if arr[0].Meta != nil {
		title, user = arr[0].Meta.Title, arr[0].Meta.Username
	}
	return &ScrapeResult{Title: title, Author: user, Items: items}
}

type fastdlSingle struct {
	URL   any    `json:"url"`
	Thumb string `json:"thumb"`
	Meta  *struct {
		Title    string `json:"title"`
		Username string `json:"username"`
	} `json:"meta"`
}

func fastdlSingleResult(s fastdlSingle) (*ScrapeResult, error) {
	urls := []fastdlURL{}
	switch v := s.URL.(type) {
	case []any:
		for _, e := range v {
			if m, ok := e.(map[string]any); ok {
				var u fastdlURL
				if t, ok := m["type"].(string); ok {
					u.Type = t
				}
				if q, ok := m["quality"].(float64); ok {
					u.Quality = int(q)
				}
				if uu, ok := m["url"].(string); ok {
					u.URL = uu
				}
				urls = append(urls, u)
			}
		}
	case string:
		urls = []fastdlURL{{URL: v, Type: "video"}}
	}
	if len(urls) == 0 {
		return nil, fmt.Errorf("fastdl: tidak ada URL media")
	}
	best := urls[0]
	for _, u := range urls[1:] {
		if u.Quality > best.Quality {
			best = u
		}
	}
	kind := KindVideo
	if best.Type != "" && best.Type != "video" {
		kind = KindPhoto
	}
	var title, user string
	if s.Meta != nil {
		title, user = s.Meta.Title, s.Meta.Username
	}
	return &ScrapeResult{
		Title:  title,
		Author: user,
		Items:  []ScrapeItem{{URL: best.URL, Thumb: s.Thumb, Kind: kind}},
	}, nil
}

type fastdlURL struct {
	URL     string
	Type    string
	Quality int
}

// --- savereels (fallback): POST ajaxSearch + parse HTML ---

type savereelsItem struct {
	link  string
	thumb string
	kind  MediaKind
}

func savereelsFetch(ctx context.Context, pageURL string) (*ScrapeResult, error) {
	const engine = "savereels"
	var raw struct {
		Status string `json:"status"`
		Data   string `json:"data"`
	}
	form := map[string]string{"q": pageURL, "v": "v2"}
	headers := map[string]string{"X-Requested-With": "XMLHttpRequest"}
	if err := postFormJSON(ctx, engine, "https://savereels.io/api/ajaxSearch", form, headers, &raw); err != nil {
		return nil, err
	}
	if raw.Status != "ok" {
		return nil, fmt.Errorf("%s: status %s", engine, raw.Status)
	}
	parsed := parseSavereelsHTML(raw.Data)
	if len(parsed) == 0 {
		return nil, fmt.Errorf("%s: tidak ada item terparse", engine)
	}
	out := make([]ScrapeItem, 0, len(parsed))
	for _, it := range parsed {
		out = append(out, ScrapeItem{URL: it.link, Thumb: it.thumb, Kind: it.kind})
	}
	return &ScrapeResult{Items: out}, nil
}

// parseSavereelsHTML memetik blok .download-items: penanda video adalah
// .icon-dlvideo, thumbnail dari img, link dari a[id^=photo_dl_/video_dl_]
// atau .download-items__btn a[href*=dl.snapcdn.app], terbaik dari option.
func parseSavereelsHTML(page string) []savereelsItem {
	doc, err := htmlParse(page)
	if err != nil {
		return nil
	}
	var items []savereelsItem
	var walk func(*htmlNode)
	walk = func(n *htmlNode) {
		if n.Type == htmlElementNode && n.Data == "div" && hasClass(n, "download-items") {
			if it := parseSavereelsBlock(n); it.link != "" {
				items = append(items, it)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return items
}

func parseSavereelsBlock(div *htmlNode) savereelsItem {
	var it savereelsItem
	it.kind = KindPhoto
	var bestRes string
	var walk func(*htmlNode)
	walk = func(n *htmlNode) {
		if n.Type != htmlElementNode {
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c)
			}
			return
		}
		switch n.Data {
		case "i", "span", "div":
			if hasClass(n, "icon-dlvideo") {
				it.kind = KindVideo
			}
		case "img":
			if src := attrVal(n, "src"); src != "" && it.thumb == "" {
				it.thumb = src
			}
		case "a":
			id := attrVal(n, "id")
			href := strings.TrimSpace(attrVal(n, "href"))
			if href == "" {
				break
			}
			if strings.HasPrefix(id, "photo_dl_") || strings.HasPrefix(id, "video_dl_") {
				it.link = href
			} else if strings.Contains(href, "dl.snapcdn.app") && it.link == "" {
				it.link = href
			}
		case "option":
			if val := strings.TrimSpace(attrVal(n, "value")); val != "" {
				if bestRes == "" || len(val) > len(bestRes) {
					bestRes = val
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(div)
	if bestRes != "" {
		it.link = bestRes
	}
	return it
}

func hasClass(n *htmlNode, class string) bool {
	for _, a := range n.Attr {
		if a.Key == "class" {
			for _, c := range strings.Fields(a.Val) {
				if c == class {
					return true
				}
			}
		}
	}
	return false
}

func attrVal(n *htmlNode, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
