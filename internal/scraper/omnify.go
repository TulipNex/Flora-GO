package scraper

import (
	"context"
	"fmt"
	"strings"
)

// FetchOmnify mengambil media APAPUN via Omnify AIO
// (api-aio.omnifylabs.sbs, 45+ platform: TikTok, IG, YouTube, Facebook,
// Pinterest, X, Spotify, ...). Satu POST JSON, tanpa key/signing/cookies.
//
// Envelope respons: {status, platform, data:{type, title|description,
// cover, formats:[{id, type, label, ext, url}]}} dengan URL stream langsung.
func FetchOmnify(ctx context.Context, pageURL string) (*ScrapeResult, error) {
	return fetchOmnifyMedia(ctx, pageURL, false)
}

// FetchOmnifyAudio varian audio (dipakai mode --mp3 / .youtubemp3): memilih
// format bertipe audio, bukan video.
func FetchOmnifyAudio(ctx context.Context, pageURL string) (*ScrapeResult, error) {
	return fetchOmnifyMedia(ctx, pageURL, true)
}

type omnifyFormat struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Label string `json:"label"`
	Ext   string `json:"ext"`
	URL   string `json:"url"`
}

type omnifyResp struct {
	Status   string `json:"status"`
	Message  string `json:"message"`
	Platform string `json:"platform"`
	Data     *struct {
		Type        string         `json:"type"`
		Title       string         `json:"title"`
		Description string         `json:"description"`
		Cover       string         `json:"cover"`
		Formats     []omnifyFormat `json:"formats"`
	} `json:"data"`
}

func fetchOmnifyMedia(ctx context.Context, pageURL string, audioOnly bool) (*ScrapeResult, error) {
	const engine = "omnify"
	body := `{"url":` + jsonString(pageURL) + `}`
	headers := map[string]string{"Content-Type": "application/json"}
	var r omnifyResp
	if err := postJSONBody(ctx, engine, "https://api-aio.omnifylabs.sbs/api/v1/media/resolve", body, headers, &r); err != nil {
		return nil, err
	}
	if r.Status != "success" || r.Data == nil || len(r.Data.Formats) == 0 {
		msg := firstNonEmpty(r.Message, "media tidak ditemukan")
		return nil, fmt.Errorf("%s: %s", engine, msg)
	}
	d := r.Data
	title := truncateRunes(firstNonEmpty(d.Title, d.Description), 900)
	if audioOnly {
		for _, f := range d.Formats {
			if strings.EqualFold(f.Type, "audio") && strings.TrimSpace(f.URL) != "" {
				return &ScrapeResult{
					Title:  title,
					Items:  []ScrapeItem{{URL: f.URL, Thumb: d.Cover, Kind: KindAudio}},
					Source: "omnify",
				}, nil
			}
		}
		return nil, fmt.Errorf("%s: tidak ada format audio", engine)
	}
	if strings.EqualFold(d.Type, "photo") {
		items := make([]ScrapeItem, 0, len(d.Formats))
		for _, f := range d.Formats {
			if !strings.EqualFold(f.Type, "image") || strings.TrimSpace(f.URL) == "" {
				continue
			}
			items = append(items, ScrapeItem{URL: f.URL, Kind: KindPhoto})
		}
		if len(items) == 0 {
			return nil, fmt.Errorf("%s: tidak ada foto valid", engine)
		}
		return &ScrapeResult{Title: title, Items: items, Source: "omnify"}, nil
	}
	// Video (atau tipe tak dikenal yang membawa format video): satu item,
	// preferensi 720p sesuai keputusan (id/label mengandung "720").
	best := -1
	for i, f := range d.Formats {
		if !strings.EqualFold(f.Type, "video") || strings.TrimSpace(f.URL) == "" {
			continue
		}
		if best < 0 {
			best = i
		}
		if strings.Contains(f.ID, "720") || strings.Contains(f.Label, "720") {
			best = i
			break
		}
	}
	if best < 0 {
		return nil, fmt.Errorf("%s: tidak ada video valid", engine)
	}
	f := d.Formats[best]
	return &ScrapeResult{
		Title:  title,
		Items:  []ScrapeItem{{URL: f.URL, Thumb: d.Cover, Kind: KindVideo}},
		Source: "omnify",
	}, nil
}

// jsonString meng-quote string Go jadi literal JSON (tanpa import
// encoding/json di tiap pemanggil — cukup untuk satu field URL).
func jsonString(s string) string {
	var sb strings.Builder
	sb.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			sb.WriteString(`\"`)
		case '\\':
			sb.WriteString(`\\`)
		case '\n':
			sb.WriteString(`\n`)
		default:
			sb.WriteRune(r)
		}
	}
	sb.WriteByte('"')
	return sb.String()
}

// truncateRunes memotong teks ke max rune (caption WA terbatas; deskripsi
// YouTube bisa sangat panjang).
func truncateRunes(s string, max int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= max {
		return string(r)
	}
	return string(r[:max])
}
