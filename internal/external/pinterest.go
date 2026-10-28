package external

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// FetchPinterest mengambil video/gambar pin via klickpin.com (port dari
// alur form situsnya): shortlink pin.it di-resolve dulu (/rp.php), ambil
// CSRF (/get-csrf-token.php), POST /en2/download, petik tombol unduhan
// (a#dlMP4 = video, a#dljpg = foto/thumbnail).
func FetchPinterest(ctx context.Context, pageURL string) (*ScrapeResult, error) {
	return scrapeCache.GetOrLoad(ctx, pageURL, func(ctx context.Context) (*ScrapeResult, error) {
		const engine = "klickpin"
	const base = "https://klickpin.com"
	if !pinURLRe.MatchString(pageURL) {
		return nil, fmt.Errorf("%s: bukan URL pinterest/pin.it", engine)
	}

	tctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	// 1. Canonical: shortlink pin.it di-resolve server-side (tanpa signing).
	canonical := pageURL
	if isPinShortLink(pageURL) {
		if final, err := klickpinResolve(tctx, pageURL); err == nil && pinURLRe.MatchString(final) {
			canonical = final
		}
	}

	// 2. CSRF token.
	csrf, err := klickpinCSRF(tctx)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", engine, err)
	}

	// 3. Submit form.
	form := urlValues(map[string]string{"url": canonical, "csrf_token": csrf}).Encode()
	req, err := http.NewRequestWithContext(tctx, http.MethodPost, base+"/en2/download", strings.NewReader(form))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", engine, err)
	}
	req.Header.Set("User-Agent", scraperUA)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", base)
	req.Header.Set("Referer", base+"/")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", engine, err)
	}
	defer resp.Body.Close()
	page, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", engine, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s: HTTP %d", engine, resp.StatusCode)
	}

	// 4. Petik tombol unduhan.
	items := parseKlickpinResult(string(page))
	if len(items) == 0 {
		return nil, fmt.Errorf("%s: tidak ada media", engine)
	}
	return &ScrapeResult{Items: items, Source: "klickpin"}, nil
	})
}

// pinURLRe memvalidasi URL pinterest / pin.it (cermin regex situsnya).
var pinURLRe = regexp.MustCompile(`(?i)https?://(([\w-]+\.)?pinterest\.(com|co\.uk|de|fr|it|es|nl|se|ch|co\.in|br|au|at|cl|jp|ru|ie|ca|mx|nz|pt|ph)/.+|pin\.it/.+)`)

func isPinShortLink(rawURL string) bool {
	return regexp.MustCompile(`(?i)^https?://(www\.)?pin\.it/`).MatchString(rawURL)
}

// klickpinResolve me-resolve shortlink pin.it via /rp.php (server-side,
// tanpa signing): {hit, finalUrl}.
func klickpinResolve(ctx context.Context, shortURL string) (string, error) {
	var r struct {
		OK       bool   `json:"ok"`
		Hit      bool   `json:"hit"`
		FinalURL string `json:"finalUrl"`
	}
	api := "https://klickpin.com/rp.php?u=" + urlQueryEscapeGo(shortURL) + "&t=" + strconv.FormatInt(time.Now().UnixMilli(), 10)
	if err := fetchJSON(ctx, "klickpin", api, map[string]string{"Accept": "application/json"}, &r); err != nil {
		return "", err
	}
	if !r.Hit || r.FinalURL == "" {
		return "", fmt.Errorf("tidak ter-resolve")
	}
	return r.FinalURL, nil
}

// klickpinCSRF mengambil token CSRF via /get-csrf-token.php.
func klickpinCSRF(ctx context.Context) (string, error) {
	var r struct {
		Token string `json:"csrf_token"`
	}
	api := "https://klickpin.com/get-csrf-token.php?t=" + strconv.FormatInt(time.Now().UnixMilli(), 10)
	if err := fetchJSON(ctx, "klickpin", api, map[string]string{"Accept": "application/json"}, &r); err != nil {
		return "", err
	}
	if r.Token == "" {
		return "", fmt.Errorf("csrf kosong")
	}
	return r.Token, nil
}

// klickpinOnclickRe mengambil URL dari onclick downloadFile("...").
var klickpinOnclickRe = regexp.MustCompile(`downloadFile\("([^"]+)"\)`)

// parseKlickpinResult memetik tombol unduhan dari halaman hasil:
// a#dlMP4[data-download-url] = video, a#dljpg ([data-download-url] atau
// onclick) = foto/thumbnail.
func parseKlickpinResult(page string) []ScrapeItem {
	doc, err := htmlParse(page)
	if err != nil {
		return nil
	}
	var items []ScrapeItem
	var walk func(*htmlNode)
	walk = func(n *htmlNode) {
		if n.Type == htmlElementNode && n.Data == "a" {
			id := attrVal(n, "id")
			switch id {
			case "dlMP4":
				if u := strings.TrimSpace(attrVal(n, "data-download-url")); u != "" {
					items = append(items, ScrapeItem{URL: u, Kind: KindVideo})
				}
			case "dljpg":
				u := strings.TrimSpace(attrVal(n, "data-download-url"))
				if u == "" {
					if m := klickpinOnclickRe.FindStringSubmatch(attrVal(n, "onclick")); m != nil {
						u = m[1]
					}
				}
				if u != "" {
					items = append(items, ScrapeItem{URL: u, Kind: KindPhoto})
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return items
}
