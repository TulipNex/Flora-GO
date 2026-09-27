package scraper

import (
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// Alias tipe supaya adapter tidak perlu import x/net/html langsung.
type htmlNode = html.Node

const htmlElementNode = html.ElementNode

// htmlParse mem-parse string HTML jadi tree DOM.
func htmlParse(page string) (*htmlNode, error) {
	return html.Parse(strings.NewReader(page))
}

// textContent mengambil seluruh teks di bawah node (rekursif).
func textContent(n *htmlNode) string {
	var sb strings.Builder
	var walk func(*htmlNode)
	walk = func(m *htmlNode) {
		if m.Type == html.TextNode {
			sb.WriteString(m.Data)
		}
		for c := m.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.TrimSpace(sb.String())
}

// setBrowserHeaders menempelkan header browser Android generik (meniru
// BASE_HEADERS musically) supaya situs scraper tidak menolak request.
func setBrowserHeaders(req *http.Request) {
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8")
	req.Header.Set("Accept-Language", "id-ID,id;q=0.9,en-US;q=0.8,en;q=0.7")
	req.Header.Set("Cache-Control", "max-age=0")
	req.Header.Set("Sec-Ch-Ua-Mobile", "?1")
	req.Header.Set("Sec-Ch-Ua-Platform", `"Android"`)
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/127.0.0.0 Mobile Safari/537.36")
}

// urlQueryEscapeGo escape query ala url.QueryEscape stdlib.
func urlQueryEscapeGo(s string) string {
	return url.QueryEscape(s)
}

// urlValues mengubah map jadi url.Values untuk body form POST.
func urlValues(m map[string]string) url.Values {
	v := url.Values{}
	for k, val := range m {
		v.Set(k, val)
	}
	return v
}
