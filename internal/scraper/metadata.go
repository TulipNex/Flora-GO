package scraper

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// maxMetaHTML membatasi halaman yang dibaca (metadata ada di <head>,
// tak perlu seluruh body).
const maxMetaHTML = 2 << 20 // 2MB

// PageMeta adalah metadata kartu link sebuah URL.
type PageMeta struct {
	Title       string
	Description string
	ImageURL    string
	FaviconURL  string
}

// FetchPageMeta mengambil metadata kartu link: title, deskripsi, URL
// thumbnail (og:image), dan URL favicon. Tiap field fail-soft independen
// (kosong bila tidak ada) — pemanggil yang memutuskan-CH tampilan untuk
// field kosong, bukan error di sini. Error hanya untuk kegagalan fetch
// atau halaman tak terbaca sama sekali.
func FetchPageMeta(ctx context.Context, rawURL string) (*PageMeta, error) {
	if err := checkPublicURL(ctx, rawURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("URL tidak valid.")
	}
	req.Header.Set("User-Agent", scraperUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("situs tidak merespons.")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("situs menjawab HTTP %d.", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMetaHTML+1))
	if err != nil {
		return nil, fmt.Errorf("gagal baca halaman.")
	}
	if len(body) > maxMetaHTML {
		return nil, fmt.Errorf("halaman terlalu besar.")
	}

	base, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("URL tidak valid.")
	}
	doc, err := htmlParse(string(body))
	if err != nil {
		return nil, fmt.Errorf("halaman bukan HTML valid.")
	}

	meta := &PageMeta{}
	var ogTitle, htmlTitle, ogDesc, metaDesc, ogImage, iconHref string
	var walk func(*htmlNode)
	walk = func(n *htmlNode) {
		if n.Type == htmlElementNode {
			switch strings.ToLower(n.Data) {
			case "title":
				if htmlTitle == "" {
					htmlTitle = textContent(n)
				}
			case "meta":
				get := func(key string) string {
					for _, a := range n.Attr {
						if strings.EqualFold(a.Key, key) {
							return a.Val
						}
					}
					return ""
				}
				prop, name, content := get("property"), get("name"), get("content")
				switch {
				case strings.EqualFold(prop, "og:title") && ogTitle == "":
					ogTitle = content
				case strings.EqualFold(prop, "og:description") && ogDesc == "":
					ogDesc = content
				case strings.EqualFold(prop, "og:image") && ogImage == "":
					ogImage = content
				case strings.EqualFold(name, "description") && metaDesc == "":
					metaDesc = content
				}
			case "link":
				var rel, href string
				for _, a := range n.Attr {
					if strings.EqualFold(a.Key, "rel") {
						rel = a.Val
					}
					if strings.EqualFold(a.Key, "href") {
						href = a.Val
					}
				}
				// rel bisa multi-nilai ("shortcut icon", "apple-touch-icon").
				if iconHref == "" && href != "" && strings.Contains(strings.ToLower(rel), "icon") {
					iconHref = href
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	if ogTitle != "" {
		meta.Title = ogTitle
	} else {
		meta.Title = htmlTitle
	}
	if ogDesc != "" {
		meta.Description = ogDesc
	} else {
		meta.Description = metaDesc
	}
	meta.Title = collapseMetaSpace(meta.Title)
	meta.Description = collapseMetaSpace(meta.Description)
	meta.ImageURL = absolutizeMetaURL(base, strings.TrimSpace(ogImage))

	iconAbs := absolutizeMetaURL(base, strings.TrimSpace(iconHref))
	if iconAbs == "" {
		// Fallback favicon konvensional — dicek via HEAD supaya tidak
		// mengembalikan URL mati.
		fallback := (&url.URL{Scheme: base.Scheme, Host: base.Host, Path: "/favicon.ico"}).String()
		if metaFaviconOK(ctx, fallback) {
			iconAbs = fallback
		}
	}
	meta.FaviconURL = iconAbs
	return meta, nil
}

// absolutizeMetaURL mengubah URL relatif jadi absolut terhadap halaman,
// "" tetap "".
func absolutizeMetaURL(base *url.URL, ref string) string {
	if ref == "" {
		return ""
	}
	u, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	return base.ResolveReference(u).String()
}

// metaFaviconOK mengecek kandidat favicon via HEAD (tanpa download isi).
func metaFaviconOK(ctx context.Context, rawURL string) bool {
	if err := checkPublicURL(ctx, rawURL); err != nil {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, rawURL, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", scraperUA)
	resp, err := httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// collapseMetaSpace memadatkan whitespace (judul/deskripsi sering
// mengandung newline + indentasi HTML).
func collapseMetaSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
