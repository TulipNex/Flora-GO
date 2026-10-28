package external

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// FetchTwitter mengambil media tweet via xhancer API (port cobalt.js):
// video bitrate tertinggi + foto kualitas original, plus teks tweet.
func FetchTwitter(ctx context.Context, pageURL string) (*ScrapeResult, error) {
	return scrapeCache.GetOrLoad(ctx, pageURL, func(ctx context.Context) (*ScrapeResult, error) {
		id := extractTweetID(pageURL)
	if id == "" {
		return nil, fmt.Errorf("xhancer: URL tweet tidak valid")
	}

	var r xhancerResp
	api := "https://xhancer.com/api/tweet?url=" + urlQueryEscapeGo("https://x.com/i/status/"+id)
	headers := map[string]string{"Referer": "https://xhancer.com/x-twitter-viewer"}
	if err := fetchJSON(ctx, "xhancer", api, headers, &r); err != nil {
		return nil, err
	}

	var items []ScrapeItem
	for _, v := range r.Videos {
		best := ""
		bestRate := -1
		for _, variant := range v.Variants {
			if variant.MimeType == "video/mp4" && variant.Bitrate > bestRate && variant.URL != "" {
				best, bestRate = variant.URL, variant.Bitrate
			}
		}
		if best == "" && len(v.Variants) > 0 {
			best = v.Variants[0].URL
		}
		if best != "" {
			items = append(items, ScrapeItem{URL: best, Kind: KindVideo})
		}
	}
	for _, p := range r.Photos {
		best := ""
		for _, variant := range p.Variants {
			if variant.Quality == "original" && variant.URL != "" {
				best = variant.URL
				break
			}
		}
		if best == "" && len(p.Variants) > 0 {
			best = p.Variants[0].URL
		}
		if best != "" {
			items = append(items, ScrapeItem{URL: best, Kind: KindPhoto})
		}
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("xhancer: tidak ada media di tweet ini")
	}
	author := ""
	if r.User.ScreenName != "" {
		author = "@" + r.User.ScreenName
	}
	return &ScrapeResult{
		Title:  r.Text,
		Author: author,
		Items:  items,
		Source: "xhancer",
	}, nil
	})
}

var tweetIDRe = regexp.MustCompile(`status/([0-9]+)`)

func extractTweetID(input string) string {
	if m := tweetIDRe.FindStringSubmatch(input); m != nil {
		return m[1]
	}
	if trimmed := strings.TrimSpace(input); trimmed != "" {
		isDigits := true
		for _, c := range trimmed {
			if c < '0' || c > '9' {
				isDigits = false
				break
			}
		}
		if isDigits {
			return trimmed
		}
	}
	return ""
}

type xhancerVariant struct {
	URL      string `json:"url"`
	MimeType string `json:"mimeType"`
	Bitrate  int    `json:"bitrate"`
	Quality  string `json:"quality"`
}

type xhancerResp struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Videos []struct {
		Variants []xhancerVariant `json:"variants"`
	} `json:"videos"`
	Photos []struct {
		Variants []xhancerVariant `json:"variants"`
	} `json:"photos"`
	User struct {
		Name       string `json:"name"`
		ScreenName string `json:"screen_name"`
	} `json:"user"`
}
