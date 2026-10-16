package external

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// douyin.go — TikTok via rantai tikwm → siputzx (port down-tiktok.js
// anya-MD-v3): link langsung ATAU keyword search, lalu foto loop / video /
// audio. Terpisah dari tiktok.go (rantai tikwm → tiktokdl → musically)
// supaya perilaku cermin-Baileys tidak mengganggu downloader utama.

// DouyinResult adalah hasil normalisasi kedua API (field disamakan).
type DouyinResult struct {
	Title     string
	Author    string
	PlayCount int64
	Duration  int64 // detik
	VideoURL  string
	MusicURL  string
	Images    []string
	Cover     string // thumbnail/cover video dari API (untuk preview)
	Source    string
}

var douyinURLRe = regexp.MustCompile(`https://(vt|vm)\.tiktok\.com/[^\s]+|https://www\.tiktok\.com/@[\w.-]+/video/\d+`)

// DouyinFindURL mengekstrak URL TikTok pertama dari teks (atau "").
func DouyinFindURL(text string) string {
	return douyinURLRe.FindString(text)
}

// FetchDouyin mengambil data TikTok via tikwm (hd=1), fallback siputzx.
func FetchDouyin(ctx context.Context, pageURL string) (*DouyinResult, error) {
	if res, err := douyinTikWM(ctx, pageURL); err == nil {
		res.Source = "tikwm"
		return res, nil
	} else {
		err1 := err
		if res, err := douyinSiputzx(ctx, pageURL); err == nil {
			res.Source = "siputzx"
			return res, nil
		} else {
			return nil, fmt.Errorf("tiktok gagal semua: tikwm: %v | siputzx: %v", err1, err)
		}
	}
}

func douyinGetJSON(ctx context.Context, engine, rawURL string, v any) error {
	tctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return fetchJSON(tctx, engine, rawURL, nil, v)
}

// douyinToInt64 menormalisasi angka API (number/string/json.Number) ke int64.
func douyinToInt64(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case float32:
		return int64(n)
	case int:
		return int64(n)
	case int64:
		return n
	case string:
		if i, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64); err == nil {
			return i
		}
		if f, err := strconv.ParseFloat(strings.TrimSpace(n), 64); err == nil {
			return int64(f)
		}
	}
	return 0
}

func douyinTikWM(ctx context.Context, pageURL string) (*DouyinResult, error) {
	const engine = "tikwm-dy"
	var r struct {
		Data *struct {
			Title     string   `json:"title"`
			Author    any      `json:"author"`
			PlayCount any      `json:"play_count"`
			Duration  any      `json:"duration"`
			Play      string   `json:"play"`
			Video    string   `json:"video"`
			NoWater  string   `json:"no_watermark"`
			PlayURL  string   `json:"play_url"`
			VideoURL string   `json:"video_url"`
			Music    string   `json:"music"`
			MusicURL string   `json:"music_url"`
			Audio    string   `json:"audio"`
			Images   []string `json:"images"`
			Image    any      `json:"image"`
			Photo    any      `json:"photo"`
			Cover    string   `json:"cover"`
			Thumb    string   `json:"thumbnail"`
			ImagesURL any     `json:"images_url"`
			MusicInfo *struct {
				Play string `json:"play"`
			} `json:"music_info"`
		} `json:"data"`
	}
	api := "https://www.tikwm.com/api/?url=" + urlQueryEscapeGo(pageURL) + "&hd=1"
	if err := douyinGetJSON(ctx, engine, api, &r); err != nil {
		return nil, err
	}
	if r.Data == nil {
		return nil, fmt.Errorf("%s: respons kosong", engine)
	}
	d := r.Data
	cover := firstNonEmpty(d.Cover, d.Thumb)
	if cover == "" {
		if s, ok := d.Image.(string); ok {
			cover = s
		}
	}
	return &DouyinResult{
		Title:     d.Title,
		Author:    douyinAuthor(d.Author),
		PlayCount: douyinToInt64(d.PlayCount),
		Duration:  douyinToInt64(d.Duration),
		VideoURL:  firstNonEmpty(d.Play, d.Video, d.NoWater, d.PlayURL, d.VideoURL),
		Cover:     cover,
		MusicURL:  firstNonEmpty(func() string {
			if d.MusicInfo != nil {
				return d.MusicInfo.Play
			}
			return ""
		}(), d.Music, d.MusicURL, d.Audio),
		Images: douyinStrList(d.Images, d.Image, d.Photo, d.ImagesURL),
	}, nil
}

func douyinSiputzx(ctx context.Context, pageURL string) (*DouyinResult, error) {
	const engine = "siputzx"
	var r struct {
		Data *struct {
			Title     string `json:"title"`
			Desc      string `json:"desc"`
			Author    any    `json:"author"`
			PlayCount any    `json:"play_count"`
			Stats     *struct {
				PlayCount any `json:"playCount"`
			} `json:"stats"`
			Duration any      `json:"duration"`
			Video    string   `json:"video"`
			NoWater  string   `json:"no_watermark"`
			Play     string   `json:"play"`
			Music    string   `json:"music"`
			Audio    string   `json:"audio"`
			Images   []string `json:"images"`
			Photo    any      `json:"photo"`
			Cover    string   `json:"cover"`
			Image    string   `json:"image"`
			ImagesURL any     `json:"images_url"`
		} `json:"data"`
	}
	api := "https://api.siputzx.my.id/api/d/tiktok?url=" + urlQueryEscapeGo(pageURL)
	if err := douyinGetJSON(ctx, engine, api, &r); err != nil {
		return nil, err
	}
	if r.Data == nil {
		return nil, fmt.Errorf("%s: respons kosong", engine)
	}
	d := r.Data
	pc := d.PlayCount
	if d.Stats != nil && pc == nil {
		pc = d.Stats.PlayCount
	}
	return &DouyinResult{
		Title:     firstNonEmpty(d.Title, d.Desc),
		Author:    douyinAuthor(d.Author),
		PlayCount: douyinToInt64(pc),
		Duration:  douyinToInt64(d.Duration),
		VideoURL:  firstNonEmpty(d.Video, d.NoWater, d.Play),
		Cover:     firstNonEmpty(d.Cover, d.Image),
		MusicURL:  firstNonEmpty(d.Music, d.Audio),
		Images:    douyinStrList(d.Images, d.Photo, d.ImagesURL),
	}, nil
}

// SearchDouyin mencari 1 video teratas via tikwm feed/search lalu fetch
// detailnya (cermin alur search down-tiktok.js).
func SearchDouyin(ctx context.Context, keyword string) (*DouyinResult, error) {
	const engine = "tikwm-search"
	var r struct {
		Data *struct {
			Videos []struct {
				VideoID string `json:"video_id"`
				Author  *struct {
					UniqueID string `json:"unique_id"`
				} `json:"author"`
			} `json:"videos"`
		} `json:"data"`
	}
	api := "https://www.tikwm.com/api/feed/search?keywords=" + urlQueryEscapeGo(keyword) + "&count=1&cursor=0&web=1&hd=1"
	if err := douyinGetJSON(ctx, engine, api, &r); err != nil {
		return nil, err
	}
	if r.Data == nil || len(r.Data.Videos) == 0 {
		return nil, fmt.Errorf("%s: tidak ditemukan untuk %q", engine, keyword)
	}
	v := r.Data.Videos[0]
	uid := ""
	if v.Author != nil {
		uid = v.Author.UniqueID
	}
	if uid == "" || v.VideoID == "" {
		return nil, fmt.Errorf("%s: hasil search tak lengkap", engine)
	}
	return FetchDouyin(ctx, "https://www.tiktok.com/@"+uid+"/video/"+v.VideoID)
}

// douyinAuthor menormalisasi author (object nickname/unique_id atau string).
func douyinAuthor(v any) string {
	switch a := v.(type) {
	case map[string]any:
		for _, k := range []string{"nickname", "unique_id", "name"} {
			if s, ok := a[k].(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
		return "-"
	case string:
		if strings.TrimSpace(a) != "" {
			return strings.TrimSpace(a)
		}
		return "-"
	default:
		return "-"
	}
}

// douyinStrList menggabung kandidat images ([]string atau string / list any).
func douyinStrList(lists ...any) []string {
	var out []string
	for _, v := range lists {
		switch l := v.(type) {
		case []string:
			out = append(out, l...)
		case string:
			if strings.TrimSpace(l) != "" {
				out = append(out, l)
			}
		case []any:
			for _, e := range l {
				if s, ok := e.(string); ok && strings.TrimSpace(s) != "" {
					out = append(out, s)
				}
			}
		}
	}
	return out
}

// FormatDouyinCount memformat views ala toLocaleString (1,234,567).
func FormatDouyinCount(n int64) string {
	s := strconv.FormatInt(n, 10)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	rem := len(s) % 3
	if rem > 0 {
		b.WriteString(s[:rem])
	}
	for i := rem; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

// FormatDouyinDuration memformat detik jadi mm:ss.
func FormatDouyinDuration(sec int64) string {
	if sec < 0 {
		sec = 0
	}
	return fmt.Sprintf("%02d:%02d", sec/60, sec%60)
}
