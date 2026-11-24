package external

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"wabot-go/internal/netx"
	_ "wabot-go/internal/envload"
)

// Flora Gateway API client & DTOs
// Mengalihkan scraper internal ke server RestAPI dedicated (XMPanels)
// untuk menghemat CPU & RAM bot di container Pterodactyl.

const (
	defaultFloraGatewayURL = "https://florago.dnsp.my.id"
)

var floraHTTPClient = &http.Client{
	Timeout:   30 * time.Second,
	Transport: netx.TunedTransport(),
}

// FloraGatewayURL mengembalikan base URL API (selalu berakhiran /api/v1).
func FloraGatewayURL() string {
	u := strings.TrimSpace(os.Getenv("FLORA_GATEWAY_URL"))
	if u == "" {
		u = strings.TrimSpace(os.Getenv("FLORA_API_URL"))
	}
	if u == "" {
		u = defaultFloraGatewayURL
	}
	u = strings.TrimRight(u, "/")
	if !strings.HasSuffix(u, "/api/v1") {
		u = u + "/api/v1"
	}
	return u
}

// FloraGatewayKey mengembalikan secret key otentikasi header X-API-Key murni dari .env.
// Seluruh fitur yang menggunakan RestAPI Flora wajib merujuk ke FLORA_GATEWAY_KEY (atau FLORA_API_KEY).
func FloraGatewayKey() string {
	k := strings.TrimSpace(os.Getenv("FLORA_GATEWAY_KEY"))
	if k == "" {
		k = strings.TrimSpace(os.Getenv("FLORA_API_KEY"))
	}
	return k
}

// floraGetJSON melakukan HTTP GET terotentikasi ke Flora Gateway dan unmarshal respons ke target.
func floraGetJSON(ctx context.Context, path string, q url.Values, target any) error {
	base := FloraGatewayURL()
	endpoint := base + "/" + strings.TrimLeft(path, "/")
	if len(q) > 0 {
		endpoint += "?" + q.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("flora gateway request: %w", err)
	}

	req.Header.Set("User-Agent", scraperUA)
	req.Header.Set("Accept", "application/json")
	if key := FloraGatewayKey(); key != "" {
		req.Header.Set("X-API-Key", key)
	}

	resp, err := floraHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("flora gateway do: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return fmt.Errorf("flora gateway read: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errResp struct {
			Success bool   `json:"success"`
			Error   string `json:"error"`
			Code    string `json:"code"`
		}
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			return fmt.Errorf("flora gateway [%s]: %s", errResp.Code, errResp.Error)
		}
		return fmt.Errorf("flora gateway HTTP %d: %s", resp.StatusCode, string(body))
	}

	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("flora gateway json decode: %w", err)
	}

	return nil
}

// floraPostJSON melakukan HTTP POST JSON terotentikasi ke Flora Gateway.
func floraPostJSON(ctx context.Context, path string, payload any, target any) error {
	base := FloraGatewayURL()
	endpoint := base + "/" + strings.TrimLeft(path, "/")

	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("flora gateway marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("flora gateway request: %w", err)
	}

	req.Header.Set("User-Agent", scraperUA)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if key := FloraGatewayKey(); key != "" {
		req.Header.Set("X-API-Key", key)
	}

	resp, err := floraHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("flora gateway do: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return fmt.Errorf("flora gateway read: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errResp struct {
			Success bool   `json:"success"`
			Error   string `json:"error"`
			Code    string `json:"code"`
		}
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			return fmt.Errorf("flora gateway [%s]: %s", errResp.Code, errResp.Error)
		}
		return fmt.Errorf("flora gateway HTTP %d: %s", resp.StatusCode, string(body))
	}

	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("flora gateway json decode: %w", err)
	}

	return nil
}

// =========================================================================
// 1. Downloader Medsos Universal (TikTok, Instagram, Facebook, Twitter, Pinterest)
// =========================================================================

type floraItemDTO struct {
	URL   string `json:"url"`
	Thumb string `json:"thumb"`
	Mime  string `json:"mime"`
	Kind  string `json:"kind"`
}

type floraDownloaderDTO struct {
	Success  bool           `json:"success"`
	Platform string         `json:"platform"`
	Engine   string         `json:"engine"`
	Title    string         `json:"title"`
	Author   string         `json:"author"`
	Duration any            `json:"duration"`
	Cover    string         `json:"cover"`
	Items    []floraItemDTO `json:"items"`
	Error    string         `json:"error"`
	Code     string         `json:"code"`
}

// FloraFetchDownloader mengambil media dari endpoint /download/{platform}.
func FloraFetchDownloader(ctx context.Context, platform, pageURL string) (*ScrapeResult, error) {
	q := url.Values{}
	q.Set("url", pageURL)

	var dto floraDownloaderDTO
	if err := floraGetJSON(ctx, "download/"+platform, q, &dto); err != nil {
		return nil, err
	}

	if !dto.Success {
		return nil, fmt.Errorf("downloader gagal: %s", dto.Error)
	}

	var items []ScrapeItem
	for _, it := range dto.Items {
		k := KindVideo
		if strings.EqualFold(it.Kind, "photo") || strings.EqualFold(it.Kind, "image") {
			k = KindPhoto
		} else if strings.EqualFold(it.Kind, "audio") {
			k = KindAudio
		}
		items = append(items, ScrapeItem{
			URL:   it.URL,
			Thumb: it.Thumb,
			Mime:  it.Mime,
			Kind:  k,
		})
	}

	if len(items) == 0 {
		return nil, fmt.Errorf("tidak ada media yang ditemukan dari %s", platform)
	}

	dur := 0
	if dNum, ok := dto.Duration.(float64); ok {
		dur = int(dNum)
	}

	source := "flora-gateway"
	if dto.Engine != "" {
		source += " (" + dto.Engine + ")"
	}

	return &ScrapeResult{
		Title:    dto.Title,
		Author:   dto.Author,
		Duration: dur,
		Items:    items,
		Source:   source,
	}, nil
}

// =========================================================================
// 2. YouTube Downloader
// =========================================================================

type floraYouTubeFormatDTO struct {
	Quality    string `json:"quality"`
	Resolution string `json:"resolution"`
	Label      string `json:"label"`
	URL        string `json:"url"`
	Kind       string `json:"kind"`
}

type floraYouTubeDTO struct {
	Success  bool                    `json:"success"`
	Platform string                  `json:"platform"`
	Engine   string                  `json:"engine"`
	ID       string                  `json:"id"`
	Title    string                  `json:"title"`
	Duration string                  `json:"duration"`
	Cover    string                  `json:"cover"`
	Items    []floraYouTubeFormatDTO `json:"items"`
	Videos   []floraYouTubeFormatDTO `json:"videos"`
	Audios   []floraYouTubeFormatDTO `json:"audios"`
	Error    string                  `json:"error"`
}

// FloraFetchYouTube mengambil media YouTube dari Flora Gateway (/download/youtube).
func FloraFetchYouTube(ctx context.Context, pageURL string, audioOnly bool) (*ScrapeResult, error) {
	q := url.Values{}
	q.Set("url", pageURL)

	var dto floraYouTubeDTO
	if err := floraGetJSON(ctx, "download/youtube", q, &dto); err != nil {
		return nil, err
	}

	if !dto.Success {
		return nil, fmt.Errorf("youtube gagal: %s", dto.Error)
	}

	var items []ScrapeItem
	if audioOnly {
		for _, a := range dto.Audios {
			if a.URL != "" {
				items = append(items, ScrapeItem{
					URL:  a.URL,
					Kind: KindAudio,
				})
			}
		}
	} else {
		for _, v := range dto.Videos {
			if v.URL != "" {
				items = append(items, ScrapeItem{
					URL:  v.URL,
					Kind: KindVideo,
				})
			}
		}
	}

	// Fallback ke items umum jika list videos/audios kosong
	if len(items) == 0 {
		for _, it := range dto.Items {
			if audioOnly && it.Kind == "audio" {
				items = append(items, ScrapeItem{URL: it.URL, Kind: KindAudio})
			} else if !audioOnly && it.Kind == "video" {
				items = append(items, ScrapeItem{URL: it.URL, Kind: KindVideo})
			}
		}
	}

	if len(items) == 0 && len(dto.Items) > 0 {
		// Jika tetap kosong, ambil item pertama apa adanya
		first := dto.Items[0]
		k := KindVideo
		if first.Kind == "audio" {
			k = KindAudio
		}
		items = append(items, ScrapeItem{URL: first.URL, Kind: k})
	}

	if len(items) == 0 {
		return nil, fmt.Errorf("tidak ada stream YouTube yang tersedia")
	}

	source := "flora-gateway"
	if dto.Engine != "" {
		source += " (" + dto.Engine + ")"
	}

	return &ScrapeResult{
		Title:  dto.Title,
		Items:  items,
		Source: source,
	}, nil
}

// =========================================================================
// 3. Spotify Track Downloader
// =========================================================================

type floraSpotifyDownloadDTO struct {
	Success bool `json:"success"`
	Data    struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		Artists     string `json:"artists"`
		Duration    string `json:"duration"`
		Thumbnail   string `json:"thumbnail"`
		DownloadURL string `json:"download_url"`
		Source      string `json:"source"`
	} `json:"data"`
	Error string `json:"error"`
}

// FloraFetchSpotify mengunduh lagu Spotify dari Flora Gateway (/download/spotify).
func FloraFetchSpotify(ctx context.Context, pageURL string) (*ScrapeResult, error) {
	q := url.Values{}
	q.Set("url", pageURL)

	var dto floraSpotifyDownloadDTO
	if err := floraGetJSON(ctx, "download/spotify", q, &dto); err != nil {
		return nil, err
	}

	if !dto.Success || dto.Data.DownloadURL == "" {
		if dto.Error != "" {
			return nil, fmt.Errorf("spotify gagal: %s", dto.Error)
		}
		return nil, fmt.Errorf("spotify download URL kosong")
	}

	title := dto.Data.Title
	if dto.Data.Artists != "" {
		title = fmt.Sprintf("%s - %s", dto.Data.Title, dto.Data.Artists)
	}

	source := "flora-gateway"
	if dto.Data.Source != "" {
		source += " (" + dto.Data.Source + ")"
	}

	return &ScrapeResult{
		Title:  title,
		Author: dto.Data.Artists,
		Items: []ScrapeItem{
			{
				URL:   dto.Data.DownloadURL,
				Thumb: dto.Data.Thumbnail,
				Kind:  KindAudio,
			},
		},
		Source: source,
	}, nil
}

// =========================================================================
// 4. Stock Photo Downloader (Shutterstock, Getty, iStock)
// =========================================================================

type floraStockDTO struct {
	Success     bool   `json:"success"`
	Platform    string `json:"platform"`
	DownloadURL string `json:"download_url"`
	Filename    string `json:"filename"`
	Error       string `json:"error"`
}

// FloraFetchStock mengunduh gambar stok dari Flora Gateway (/download/stock).
func FloraFetchStock(ctx context.Context, input, toolType string) (string, error) {
	q := url.Values{}
	q.Set("url", input)
	if toolType != "" {
		q.Set("type", toolType)
	}

	var dto floraStockDTO
	if err := floraGetJSON(ctx, "download/stock", q, &dto); err != nil {
		return "", err
	}

	if !dto.Success || dto.DownloadURL == "" {
		if dto.Error != "" {
			return "", fmt.Errorf("stock dl gagal: %s", dto.Error)
		}
		return "", fmt.Errorf("URL gambar stok kosong")
	}

	return dto.DownloadURL, nil
}

// =========================================================================
// 5. BK Video Downloader (xpvid, vidovr, dll.)
// =========================================================================

type floraBKDTO struct {
	Success  bool   `json:"success"`
	Platform string `json:"platform"`
	ID       string `json:"id"`
	Title    string `json:"title"`
	Domain   string `json:"domain"`
	VideoURL string `json:"videoUrl"`
	IsHLS    bool   `json:"isHLS"`
	Referer  string `json:"referer"`
	Error    string `json:"error"`
}

// FloraFetchBK me-resolve video embed BK dari Flora Gateway (/download/bk).
func FloraFetchBK(ctx context.Context, pageURL string) (*XpvidInfo, error) {
	q := url.Values{}
	q.Set("url", pageURL)

	var dto floraBKDTO
	if err := floraGetJSON(ctx, "download/bk", q, &dto); err != nil {
		return nil, err
	}

	if !dto.Success || dto.VideoURL == "" {
		if dto.Error != "" {
			return nil, fmt.Errorf("%s", dto.Error)
		}
		return nil, fmt.Errorf("video URL kosong")
	}

	return &XpvidInfo{
		Title:    dto.Title,
		VideoURL: dto.VideoURL,
		IsHLS:    dto.IsHLS,
		Referer:  dto.Referer,
	}, nil
}

// =========================================================================
// 6. Spotify Search
// =========================================================================

type floraSpotifyTrackDTO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Artists     string `json:"artists"`
	Album       string `json:"album"`
	DurationMs  int64  `json:"duration_ms"`
	DurationFmt string `json:"duration_fmt"`
	CoverURL    string `json:"cover_url"`
	URI         string `json:"uri"`
	ReleaseYear string `json:"release_year"`
}

type floraSpotifySearchDTO struct {
	Success bool                   `json:"success"`
	Query   string                 `json:"query"`
	Count   int                    `json:"count"`
	Data    []floraSpotifyTrackDTO `json:"data"`
	Error   string                 `json:"error"`
}

// FloraSearchSpotify mencari lagu di Spotify via Flora Gateway (/spotify/search).
func FloraSearchSpotify(ctx context.Context, query string, limit int) ([]SpotifyTrack, error) {
	q := url.Values{}
	q.Set("q", query)
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}

	var dto floraSpotifySearchDTO
	if err := floraGetJSON(ctx, "spotify/search", q, &dto); err != nil {
		return nil, err
	}

	if !dto.Success || len(dto.Data) == 0 {
		if dto.Error != "" {
			return nil, fmt.Errorf("%s", dto.Error)
		}
		return nil, fmt.Errorf("lagu tidak ditemukan")
	}

	var out []SpotifyTrack
	for _, t := range dto.Data {
		out = append(out, SpotifyTrack{
			ID:          t.ID,
			Name:        t.Name,
			Artists:     t.Artists,
			Album:       t.Album,
			DurationMs:  t.DurationMs,
			DurationFmt: t.DurationFmt,
			CoverURL:    t.CoverURL,
			URI:         t.URI,
			ReleaseYear: t.ReleaseYear,
		})
	}

	return out, nil
}

// =========================================================================
// 7. Lirik Lagu (LRCLIB)
// =========================================================================

type floraLyricsDTO struct {
	ID           int64   `json:"id"`
	Title        string  `json:"title"`
	Artist       string  `json:"artist"`
	Album        string  `json:"album"`
	DurationSec  float64 `json:"duration_sec"`
	Instrumental bool    `json:"instrumental"`
	PlainLyrics  string  `json:"plainLyrics"`
	SyncedLyrics string  `json:"syncedLyrics"`
}

type floraLyricsSearchDTO struct {
	Success bool             `json:"success"`
	Query   string           `json:"query"`
	Count   int              `json:"count"`
	Data    []floraLyricsDTO `json:"data"`
	Error   string           `json:"error"`
}

// FloraSearchLyrics mencari lirik lagu via Flora Gateway (/tools/lyrics).
func FloraSearchLyrics(ctx context.Context, query string, limit int, lrcType string) ([]LrclibSong, error) {
	q := url.Values{}
	q.Set("q", query)
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if lrcType != "" {
		q.Set("type", lrcType)
	}

	var dto floraLyricsSearchDTO
	if err := floraGetJSON(ctx, "tools/lyrics", q, &dto); err != nil {
		return nil, err
	}

	if !dto.Success || len(dto.Data) == 0 {
		if dto.Error != "" {
			return nil, fmt.Errorf("%s", dto.Error)
		}
		return nil, fmt.Errorf("lirik tidak ditemukan")
	}

	var out []LrclibSong
	for _, s := range dto.Data {
		out = append(out, LrclibSong{
			TrackName:    s.Title,
			ArtistName:   s.Artist,
			AlbumName:    s.Album,
			Duration:     s.DurationSec,
			Instrumental: s.Instrumental,
			PlainLyrics:  s.PlainLyrics,
			SyncedLyrics: s.SyncedLyrics,
		})
	}

	return out, nil
}

// =========================================================================
// 8. Klasemen Liga Sepak Bola (Football Standings)
// =========================================================================

type floraStandingRowDTO struct {
	Position       int    `json:"position"`
	TeamID         string `json:"team_id"`
	TeamName       string `json:"team_name"`
	LogoURL        string `json:"logo_url"`
	Played         string `json:"played"`
	Won            string `json:"won"`
	Drawn          string `json:"drawn"`
	Lost           string `json:"lost"`
	GoalsFor       string `json:"goals_for"`
	GoalsAgainst   string `json:"goals_against"`
	GoalDifference string `json:"goal_difference"`
	Points         string `json:"points"`
	Zone           string `json:"zone"`
}

type floraStandingsDTO struct {
	Success    bool                  `json:"success"`
	LeagueCode string                `json:"league_code"`
	LeagueName string                `json:"league_name"`
	Season     string                `json:"season"`
	Standings  []floraStandingRowDTO `json:"standings"`
	Error      string                `json:"error"`
}

// FloraFetchStandings mengambil klasemen sepak bola via Flora Gateway (/football/standings).
func FloraFetchStandings(ctx context.Context, leagueCode string) (*LeagueTable, error) {
	q := url.Values{}
	q.Set("league", leagueCode)

	var dto floraStandingsDTO
	if err := floraGetJSON(ctx, "football/standings", q, &dto); err != nil {
		return nil, err
	}

	if !dto.Success || len(dto.Standings) == 0 {
		if dto.Error != "" {
			return nil, fmt.Errorf("%s", dto.Error)
		}
		return nil, fmt.Errorf("klasemen kosong")
	}

	var rows []TableRow
	for _, r := range dto.Standings {
		rows = append(rows, TableRow{
			Pos:     r.Position,
			Team:    r.TeamName,
			TeamID:  r.TeamID,
			LogoURL: r.LogoURL,
			Played:  r.Played,
			Won:     r.Won,
			Draw:    r.Drawn,
			Lost:    r.Lost,
			GF:      r.GoalsFor,
			GA:      r.GoalsAgainst,
			GD:      r.GoalDifference,
			Points:  r.Points,
			Zone:    r.Zone,
		})
	}

	return &LeagueTable{
		League: dto.LeagueName,
		Season: dto.Season,
		Rows:   rows,
	}, nil
}

// =========================================================================
// 9. Jadwal & Hasil Sepak Bola (Schedule & Results)
// =========================================================================

type floraMatchDTO struct {
	ID         string `json:"id"`
	Kickoff    string `json:"kickoff"`
	TimePeriod string `json:"time_period"`
	Status     string `json:"status"`
	Link       string `json:"link"`
	HomeTeam   struct {
		Name  string `json:"name"`
		Score string `json:"score"`
		Logo  string `json:"logo"`
	} `json:"home_team"`
	AwayTeam struct {
		Name  string `json:"name"`
		Score string `json:"score"`
		Logo  string `json:"logo"`
	} `json:"away_team"`
}

type floraMatchesDTO struct {
	Success    bool            `json:"success"`
	Type       string          `json:"type"`
	LeagueCode string          `json:"league_code"`
	LeagueName string          `json:"league_name"`
	Schedule   []floraMatchDTO `json:"schedule"`
	Results    []floraMatchDTO `json:"results"`
	Error      string          `json:"error"`
}

// FloraFetchMatches mengambil jadwal atau hasil pertandingan dari Flora Gateway.
func FloraFetchMatches(ctx context.Context, leagueCode, matchType string) ([]OfSection, error) {
	q := url.Values{}
	q.Set("league", leagueCode)

	endpoint := "football/schedule"
	if matchType == "results" {
		endpoint = "football/results"
	}

	var dto floraMatchesDTO
	if err := floraGetJSON(ctx, endpoint, q, &dto); err != nil {
		return nil, err
	}

	list := dto.Schedule
	if matchType == "results" && len(dto.Results) > 0 {
		list = dto.Results
	}

	if !dto.Success || len(list) == 0 {
		if dto.Error != "" {
			return nil, fmt.Errorf("%s", dto.Error)
		}
		return nil, fmt.Errorf("tidak ada data pertandingan")
	}

	var matches []OfMatch
	for _, m := range list {
		var kickoff time.Time
		if m.Kickoff != "" {
			if t, err := time.Parse(time.RFC3339, m.Kickoff); err == nil {
				kickoff = t
			}
		}

		st := OfUpcoming
		statusUpper := strings.ToUpper(m.Status)
		if statusUpper == "FINISHED" || strings.Contains(statusUpper, "FULL TIME") || statusUpper == "FT" {
			st = OfFinished
		} else if statusUpper == "LIVE" || strings.Contains(statusUpper, "HALF") || statusUpper == "HT" {
			st = OfLive
		}

		matches = append(matches, OfMatch{
			Home:      m.HomeTeam.Name,
			Away:      m.AwayTeam.Name,
			HomeScore: m.HomeTeam.Score,
			AwayScore: m.AwayTeam.Score,
			Kickoff:   kickoff,
			Status:    st,
			LiveLabel: m.TimePeriod,
			Link:      m.Link,
		})
	}

	secTitle := "Pekan Berjalan"
	if matchType == "results" {
		secTitle = "Pekan Selesai"
	}

	return []OfSection{
		{
			Title:   secTitle,
			Matches: matches,
		},
	}, nil
}

// =========================================================================
// 10. PIHPS Sembako (Harga Pangan BI)
// =========================================================================

type floraSembakoRowDTO struct {
	No     string           `json:"no"`
	Name   string           `json:"name"`
	Level  int              `json:"level"`
	Prices map[string]int64 `json:"prices"`
}

type floraSembakoDTO struct {
	Success  bool `json:"success"`
	Province struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"province"`
	Period struct {
		Start   string   `json:"start"`
		End     string   `json:"end"`
		Columns []string `json:"columns"`
	} `json:"period"`
	TotalCommodities int                  `json:"total_commodities"`
	Items            []floraSembakoRowDTO `json:"items"`
	Error            string               `json:"error"`
}

// FloraFetchSembako mengambil grid harga sembako PIHPS via Flora Gateway (/info/sembako).
func FloraFetchSembako(ctx context.Context, provinsi, periode, startDate, endDate string) ([]PihpsRow, []string, string, int, error) {
	q := url.Values{}
	if provinsi != "" {
		q.Set("provinsi", provinsi)
	}
	if periode != "" {
		q.Set("periode", periode)
	}
	if startDate != "" {
		q.Set("start_date", startDate)
	}
	if endDate != "" {
		q.Set("end_date", endDate)
	}

	var dto floraSembakoDTO
	if err := floraGetJSON(ctx, "info/sembako", q, &dto); err != nil {
		return nil, nil, "", 0, err
	}

	if !dto.Success || len(dto.Items) == 0 {
		if dto.Error != "" {
			return nil, nil, "", 0, fmt.Errorf("%s", dto.Error)
		}
		return nil, nil, "", 0, fmt.Errorf("data sembako kosong")
	}

	var rows []PihpsRow
	for _, r := range dto.Items {
		prices := make(map[string]*int64, len(r.Prices))
		for d, p := range r.Prices {
			v := p
			prices[d] = &v
		}
		rows = append(rows, PihpsRow{
			No:     r.No,
			Nama:   r.Name,
			Level:  r.Level,
			Prices: prices,
			Dates:  dto.Period.Columns,
		})
	}

	return rows, dto.Period.Columns, dto.Province.Name, dto.Province.ID, nil
}

// floraGetRaw melakukan HTTP GET terotentikasi ke Flora Gateway dan mengembalikan raw binary bytes.
func floraGetRaw(ctx context.Context, path string, q url.Values) ([]byte, error) {
	base := FloraGatewayURL()
	endpoint := base + "/" + strings.TrimLeft(path, "/")
	if len(q) > 0 {
		endpoint += "?" + q.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("flora gateway request: %w", err)
	}

	req.Header.Set("User-Agent", scraperUA)
	if key := FloraGatewayKey(); key != "" {
		req.Header.Set("X-API-Key", key)
	}

	resp, err := floraHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("flora gateway do: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 50<<20))
	if err != nil {
		return nil, fmt.Errorf("flora gateway read: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errResp struct {
			Success bool   `json:"success"`
			Error   string `json:"error"`
			Code    string `json:"code"`
		}
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			return nil, fmt.Errorf("flora gateway [%s]: %s", errResp.Code, errResp.Error)
		}
		return nil, fmt.Errorf("flora gateway HTTP %d: %s", resp.StatusCode, string(body))
	}

	return body, nil
}

// floraPostRaw melakukan HTTP POST JSON terotentikasi ke Flora Gateway dan mengembalikan raw binary bytes.
func floraPostRaw(ctx context.Context, path string, payload any) ([]byte, error) {
	base := FloraGatewayURL()
	endpoint := base + "/" + strings.TrimLeft(path, "/")

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("flora gateway marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("flora gateway request: %w", err)
	}

	req.Header.Set("User-Agent", scraperUA)
	req.Header.Set("Content-Type", "application/json")
	if key := FloraGatewayKey(); key != "" {
		req.Header.Set("X-API-Key", key)
	}

	resp, err := floraHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("flora gateway do: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 50<<20))
	if err != nil {
		return nil, fmt.Errorf("flora gateway read: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errResp struct {
			Success bool   `json:"success"`
			Error   string `json:"error"`
			Code    string `json:"code"`
		}
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			return nil, fmt.Errorf("flora gateway [%s]: %s", errResp.Code, errResp.Error)
		}
		return nil, fmt.Errorf("flora gateway HTTP %d: %s", resp.StatusCode, string(body))
	}

	return body, nil
}

// =========================================================================
// 11. Maker (Brat & Smeme via Flora Gateway)
// =========================================================================

// FloraFetchBrat membuat stiker/media brat (image/video) via Flora Gateway (/maker/brat).
func FloraFetchBrat(ctx context.Context, text, kind string) ([]byte, error) {
	q := url.Values{}
	q.Set("text", text)
	if kind == "video" {
		q.Set("type", "video")
	} else {
		q.Set("type", "image")
	}
	q.Set("format", "raw")

	return floraGetRaw(ctx, "maker/brat", q)
}

// FloraFetchSmeme merender stiker meme Impact (top & bottom text) di atas gambar via Flora Gateway (/maker/smeme).
func FloraFetchSmeme(ctx context.Context, imageBytes []byte, top, bottom string) ([]byte, error) {
	payload := map[string]string{
		"imageBase64": base64.StdEncoding.EncodeToString(imageBytes),
		"top":         top,
		"bottom":      bottom,
		"format":      "raw",
	}

	return floraPostRaw(ctx, "maker/smeme", payload)
}
