package external

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// SpotifyTrack adalah satu lagu hasil pencarian/meta Spotify.
// Port dari spotify-meta.js (fallbackSearchSpotify + getTrack).
type SpotifyTrack struct {
	ID          string
	Name        string
	Artists     string
	Album       string
	DurationMs  int64
	DurationFmt string
	CoverURL    string
	URI         string
	// ReleaseYear hanya tahun (YYYY) dari album.date.isoString — "-" bila tak ada.
	ReleaseYear string
}

const (
	spotifySearchHash = "eff59fa0a3d026b88b56fddbcf4bdfa16a186b8175a5c1a358c072e053c2e5b0"
	spotifyTrackHash  = "ae85b52abb74d20a4c331d4143d4772c95f34757bfa8c625474b912b9055b5c0"
	spotifyPartner    = "https://api-partner.spotify.com/pathfinder/v2/query"
)

// _spTokenCache meniru _tokenCache di spotify-meta.js (embed token, exp 30 mnt).
var _spTokenCache = struct {
	sync.Mutex
	value string
	exp   time.Time
}{}

var spotifyTokenRe = regexp.MustCompile(`"accessToken":"([^"]+)"`)

func getSpotifyEmbedToken(ctx context.Context) (string, error) {
	_spTokenCache.Lock()
	if _spTokenCache.value != "" && time.Now().Before(_spTokenCache.exp) {
		v := _spTokenCache.value
		_spTokenCache.Unlock()
		return v, nil
	}
	_spTokenCache.Unlock()

	tctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(tctx, http.MethodGet, "https://open.spotify.com/embed/track/4VTX2s5N7KrQYL7iAHhdyt", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/122")
	req.Header.Set("Accept", "text/html")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("spotify-search: token HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	m := spotifyTokenRe.FindSubmatch(raw)
	if m == nil {
		return "", fmt.Errorf("spotify-search: gagal ambil Spotify token")
	}
	tok := string(m[1])
	_spTokenCache.Lock()
	_spTokenCache.value = tok
	_spTokenCache.exp = time.Now().Add(30 * time.Minute)
	_spTokenCache.Unlock()
	return tok, nil
}

func spotifyPartnerHeaders(token string) map[string]string {
	return map[string]string{
		"Accept":          "application/json",
		"Accept-Language": "id",
		"Authorization":   "Bearer " + token,
		"Content-Type":    "application/json;charset=UTF-8",
		"Origin":          "https://open.spotify.com",
		"Referer":         "https://open.spotify.com/",
	}
}

type spotifyCoverSrc struct {
	URL    string `json:"url"`
	Height int    `json:"height"`
}

type spotifySearchResp struct {
	Data *struct {
		SearchV2 *struct {
			TracksV2 *struct {
				Items []struct {
					Item *struct {
						Data *struct {
							ID          string `json:"id"`
							Name        string `json:"name"`
							URI         string `json:"uri"`
							Playability *struct {
								Playable bool `json:"playable"`
							} `json:"playability"`
							Artists *struct {
								Items []struct {
									Profile *struct {
										Name string `json:"name"`
									} `json:"profile"`
								} `json:"items"`
							} `json:"artists"`
							AlbumOfTrack *struct {
								Name     string `json:"name"`
								CoverArt *struct {
									Sources []spotifyCoverSrc `json:"sources"`
								} `json:"coverArt"`
							} `json:"albumOfTrack"`
							Duration *struct {
								TotalMilliseconds int64 `json:"totalMilliseconds"`
							} `json:"duration"`
						} `json:"data"`
					} `json:"item"`
				} `json:"items"`
			} `json:"tracksV2"`
		} `json:"searchV2"`
	} `json:"data"`
}

func biggestCover(srcs []spotifyCoverSrc) string {
	if len(srcs) == 0 {
		return ""
	}
	best := srcs[0]
	for _, s := range srcs[1:] {
		if s.Height > best.Height {
			best = s
		}
	}
	return best.URL
}

func fmtMsGo(ms int64) string {
	if ms < 0 {
		ms = 0
	}
	m := ms / 60000
	s := (ms % 60000) / 1000
	return fmt.Sprintf("%d:%02d", m, s)
}

// SearchSpotify mencari lagu via embed-token + pathfinder v2 (port
// fallbackSearchSpotify — tanpa TOTP/clientToken yang rapuh).
func SearchSpotify(ctx context.Context, query string, limit int) ([]SpotifyTrack, error) {
	const engine = "spotify-search"
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("%s: kata kunci kosong", engine)
	}
	if limit <= 0 || limit > 15 {
		limit = 5
	}
	token, err := getSpotifyEmbedToken(ctx)
	if err != nil {
		return nil, err
	}
	body := `{"variables":{"searchTerm":` + jsonString(query) +
		`,"offset":0,"limit":` + fmt.Sprint(limit) +
		`,"numberOfTopResults":5,"includeAudiobooks":false,"includeArtistHasConcertsField":false,` +
		`"includePreReleases":false,"includeAlbumPreReleases":false,"includeAuthors":false,` +
		`"includeEpisodeContentRatingsV2":false,"isPrefix":null,"sectionFilters":["GENERIC"]},` +
		`"operationName":"searchDesktop","extensions":{"persistedQuery":{"version":1,"sha256Hash":"` + spotifySearchHash + `"}}}`

	var r spotifySearchResp
	tctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := postJSONBody(tctx, engine, spotifyPartner, body, spotifyPartnerHeaders(token), &r); err != nil {
		return nil, err
	}
	if r.Data == nil || r.Data.SearchV2 == nil || r.Data.SearchV2.TracksV2 == nil {
		return nil, fmt.Errorf("%s: respons kosong", engine)
	}
	var out []SpotifyTrack
	for _, it := range r.Data.SearchV2.TracksV2.Items {
		if it.Item == nil || it.Item.Data == nil {
			continue
		}
		t := it.Item.Data
		if t.Playability != nil && !t.Playability.Playable {
			continue
		}
		artists := ""
		if t.Artists != nil {
			var names []string
			for _, a := range t.Artists.Items {
				if a.Profile != nil && strings.TrimSpace(a.Profile.Name) != "" {
					names = append(names, a.Profile.Name)
				}
			}
			artists = strings.Join(names, ", ")
		}
		var ms int64
		if t.Duration != nil {
			ms = t.Duration.TotalMilliseconds
		}
		var cover string
		if t.AlbumOfTrack != nil && t.AlbumOfTrack.CoverArt != nil {
			cover = biggestCover(t.AlbumOfTrack.CoverArt.Sources)
		}
		album := ""
		if t.AlbumOfTrack != nil {
			album = t.AlbumOfTrack.Name
		}
		out = append(out, SpotifyTrack{
			ID: t.ID, Name: t.Name, Artists: artists, Album: album,
			DurationMs: ms, DurationFmt: fmtMsGo(ms), CoverURL: cover, URI: t.URI,
		})
		if len(out) >= limit {
			break
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: lagu tidak ditemukan", engine)
	}
	return out, nil
}

type spotifyTrackResp struct {
	Data *struct {
		TrackUnion *struct {
			Name        string `json:"name"`
			URI         string `json:"uri"`
			FirstArtist *struct {
				Items []struct {
					Profile *struct {
						Name string `json:"name"`
					} `json:"profile"`
				} `json:"items"`
			} `json:"firstArtist"`
			OtherArtists *struct {
				Items []struct {
					Profile *struct {
						Name string `json:"name"`
					} `json:"profile"`
				} `json:"items"`
			} `json:"otherArtists"`
			Duration *struct {
				TotalMilliseconds int64 `json:"totalMilliseconds"`
			} `json:"duration"`
			AlbumOfTrack *struct {
				ID   string `json:"id"`
				Name string `json:"name"`
				Date *struct {
					IsoString string `json:"isoString"`
				} `json:"date"`
				CoverArt *struct {
					Sources []spotifyCoverSrc `json:"sources"`
				} `json:"coverArt"`
			} `json:"albumOfTrack"`
		} `json:"trackUnion"`
	} `json:"data"`
}

// GetSpotifyTrack mengambil meta satu track untuk caption hasil unduhan.
func GetSpotifyTrack(ctx context.Context, id string) (*SpotifyTrack, error) {
	const engine = "spotify-search"
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("%s: id kosong", engine)
	}
	token, err := getSpotifyEmbedToken(ctx)
	if err != nil {
		return nil, err
	}
	body := `{"variables":{"uri":"spotify:track:` + id + `"},` +
		`"operationName":"getTrack","extensions":{"persistedQuery":{"version":1,"sha256Hash":"` + spotifyTrackHash + `"}}}`

	var r spotifyTrackResp
	tctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := postJSONBody(tctx, engine, spotifyPartner, body, spotifyPartnerHeaders(token), &r); err != nil {
		return nil, err
	}
	if r.Data == nil || r.Data.TrackUnion == nil {
		return nil, fmt.Errorf("%s: track tidak ditemukan", engine)
	}
	t := r.Data.TrackUnion
	var names []string
	if t.FirstArtist != nil {
		for _, a := range t.FirstArtist.Items {
			if a.Profile != nil && strings.TrimSpace(a.Profile.Name) != "" {
				names = append(names, a.Profile.Name)
			}
		}
	}
	if t.OtherArtists != nil {
		for _, a := range t.OtherArtists.Items {
			if a.Profile != nil && strings.TrimSpace(a.Profile.Name) != "" {
				names = append(names, a.Profile.Name)
			}
		}
	}
	var ms int64
	if t.Duration != nil {
		ms = t.Duration.TotalMilliseconds
	}
	album, cover, year := "", "", ""
	if t.AlbumOfTrack != nil {
		album = t.AlbumOfTrack.Name
		if t.AlbumOfTrack.CoverArt != nil {
			srcs := append([]spotifyCoverSrc(nil), t.AlbumOfTrack.CoverArt.Sources...)
			sort.Slice(srcs, func(i, j int) bool { return srcs[i].Height > srcs[j].Height })
			cover = biggestCover(srcs)
		}
		if t.AlbumOfTrack.Date != nil {
			year = spotifyYearOnly(t.AlbumOfTrack.Date.IsoString)
		}
	}
	return &SpotifyTrack{
		ID: id, Name: t.Name, Artists: strings.Join(names, ", "), Album: album,
		DurationMs: ms, DurationFmt: fmtMsGo(ms), CoverURL: cover, URI: t.URI,
		ReleaseYear: year,
	}, nil
}

// spotifyYearOnly mengambil 4 digit tahun dari ISO string (2023-05-12T...).
// Kosong bila tak dapat dipastikan — pemanggil menampilkan "-".
func spotifyYearOnly(iso string) string {
	iso = strings.TrimSpace(iso)
	if len(iso) < 4 {
		return ""
	}
	y := iso[:4]
	for _, r := range y {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return y
}
