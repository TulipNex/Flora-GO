package scraper

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// LrclibSong adalah satu lagu hasil pencarian LRCLIB.
// GET https://lrclib.net/api/search?q=... (tanpa key) mengembalikan array
// JSON berisi meta + lirik sekaligus — tanpa request kedua.
type LrclibSong struct {
	TrackName    string  `json:"trackName"`
	ArtistName   string  `json:"artistName"`
	AlbumName    string  `json:"albumName"`
	Duration     float64 `json:"duration"`
	Instrumental bool    `json:"instrumental"`
	PlainLyrics  string  `json:"plainLyrics"`
	SyncedLyrics string  `json:"syncedLyrics"`
}

// Title mengembalikan judul lagu (fallback ke Name bila trackName kosong).
func (s LrclibSong) Title() string {
	if t := strings.TrimSpace(s.TrackName); t != "" {
		return t
	}
	return "-"
}

// Plain mengembalikan lirik tanpa timestamp (trimmed, bisa kosong).
func (s LrclibSong) Plain() string { return strings.TrimSpace(s.PlainLyrics) }

// Synced mengembalikan lirik ber-timestamp [mm:ss.xx] (trimmed, bisa kosong).
func (s LrclibSong) Synced() string { return strings.TrimSpace(s.SyncedLyrics) }

// SearchLrclib mencari lagu + lirik via lrclib.net (port internet-lrc.js).
func SearchLrclib(ctx context.Context, query string, limit int) ([]LrclibSong, error) {
	const engine = "lrclib"
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("%s: kata kunci kosong", engine)
	}
	if limit <= 0 || limit > 15 {
		limit = 5
	}
	api := "https://lrclib.net/api/search?q=" + url.QueryEscape(query)
	var r []LrclibSong
	tctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := fetchJSON(tctx, engine, api, nil, &r); err != nil {
		return nil, err
	}
	if len(r) == 0 {
		return nil, fmt.Errorf("%s: lirik tidak ditemukan", engine)
	}
	if len(r) > limit {
		r = r[:limit]
	}
	return r, nil
}
