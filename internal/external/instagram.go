package external

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// FetchInstagram mengambil media Instagram via rantai: igexport → savereels.
// fastdl SUDAH DIHAPUS (signing-nya ditolak server per akhir Sep 2026).
// Port dari instagram.js + savereels.js + script igexport (igexport.com).
// (snapinsta butuh bypass Cloudflare + eval JS — slotnya diganti universal
// j2download di level engine.)
func FetchInstagram(ctx context.Context, pageURL string) (*ScrapeResult, error) {
	if res, err := igexportFetch(ctx, pageURL); err == nil {
		res.Source = "igexport"
		return res, nil
	} else {
		err1 := err
		if res, err := savereelsFetch(ctx, pageURL); err == nil {
			res.Source = "savereels"
			return res, nil
		} else {
			return nil, fmt.Errorf("instagram gagal semua: igexport: %v | savereels: %v", err1, err)
		}
	}
}

// igPostRe memvalidasi URL post/reel/tv Instagram.
var igPostRe = regexp.MustCompile(`instagram\.com/(p|reel|tv)/`)

// --- igexport (utama): GET igexport.com/api/ig-photo, URL CDN langsung ---

type igexportItem struct {
	Type         string `json:"type"`
	URL          string `json:"url"`
	ThumbnailURL string `json:"thumbnailUrl"`
}

type igexportResp struct {
	OK    bool `json:"ok"`
	Media *struct {
		Shortcode string         `json:"shortcode"`
		Items     []igexportItem `json:"items"`
	} `json:"media"`
}

// igexportFetch mengambil foto carousel maupun reel: respons berisi URL CDN
// langsung kualitas penuh (tanpa varian resolusi yang perlu dipilih).
// API tidak menyediakan judul/author — caption dikosongkan.
func igexportFetch(ctx context.Context, pageURL string) (*ScrapeResult, error) {
	const engine = "igexport"
	clean := strings.TrimSuffix(strings.SplitN(pageURL, "?", 2)[0], "/") + "/"
	if !igPostRe.MatchString(clean) {
		return nil, fmt.Errorf("%s: bukan URL post/reel/tv", engine)
	}
	var r igexportResp
	headers := map[string]string{
		"Referer": "https://igexport.com/",
		"Accept":  "application/json",
	}
	api := "https://igexport.com/api/ig-photo/?url=" + urlQueryEscapeGo(pageURL)
	if err := fetchJSON(ctx, engine, api, headers, &r); err != nil {
		return nil, err
	}
	if !r.OK || r.Media == nil || len(r.Media.Items) == 0 {
		return nil, fmt.Errorf("%s: media tidak ditemukan", engine)
	}
	items := make([]ScrapeItem, 0, len(r.Media.Items))
	for _, it := range r.Media.Items {
		link := strings.TrimSpace(it.URL)
		if link == "" {
			continue
		}
		var kind MediaKind
		switch strings.ToLower(it.Type) {
		case "video":
			kind = KindVideo
		case "image", "photo":
			kind = KindPhoto
		default:
			continue
		}
		items = append(items, ScrapeItem{URL: link, Thumb: it.ThumbnailURL, Kind: kind})
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("%s: tidak ada item valid", engine)
	}
	return &ScrapeResult{Items: items}, nil
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

// resDimRe mem-parse label resolusi "1575x1970" (spasi opsional, x/X/×).
var resDimRe = regexp.MustCompile(`(?i)(\d+)\s*[x×]\s*(\d+)`)

// resLabelArea mengembalikan luas area dari label resolusi + true kalau
// ter-parse. Dipakai memilih varian TERBESAR dari <option> savereels —
// PANJANG URL tidak berkorelasi dengan ukuran gambar (URL token JWT yang
// panjangnya acak), dan heuristik lama "value terpanjang menang" terbukti
// mengambil varian kecil (750x750) sementara yang penuh (1575x1970)
// justru terpendek.
func resLabelArea(label string) (int64, bool) {
	m := resDimRe.FindStringSubmatch(label)
	if m == nil {
		return 0, false
	}
	w, err1 := strconv.Atoi(m[1])
	h, err2 := strconv.Atoi(m[2])
	if err1 != nil || err2 != nil || w <= 0 || h <= 0 {
		return 0, false
	}
	return int64(w) * int64(h), true
}

func parseSavereelsBlock(div *htmlNode) savereelsItem {
	var it savereelsItem
	it.kind = KindPhoto
	var bestRes string
	var bestArea int64
	var hasArea bool
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
				// Prioritas: luas area terbesar dari label (mis. 1575x1970
				// mengalahkan 750x750). Tanpa label ter-parse (blok video
				// berlabel non-dimensi) = fallback terpanjang seperti dulu
				// supaya tidak ada regresi video.
				if area, ok := resLabelArea(textContent(n)); ok {
					if !hasArea || area > bestArea {
						bestArea, bestRes, hasArea = area, val, true
					}
				} else if !hasArea && (bestRes == "" || len(val) > len(bestRes)) {
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
