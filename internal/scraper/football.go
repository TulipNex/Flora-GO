package scraper

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// TableRow adalah satu baris klasemen liga.
type TableRow struct {
	Pos     int
	Team    string
	TeamID  string // id tim ESPN (kunci cache logo)
	LogoURL string // logo tim ESPN (boleh kosong)
	Played  string
	Won     string
	Draw    string
	Lost    string
	GF      string // gol memasukkan (pointsFor)
	GA      string // gol kemasukan (pointsAgainst)
	GD      string
	Points  string
	Zone    string // deskripsi zona ESPN ("Champions League", "Relegation", ...)
}

// LeagueTable adalah klasemen penuh satu kompetisi.
type LeagueTable struct {
	League string
	Season string
	Rows   []TableRow
}

type espnStandingsResp struct {
	Name     string `json:"name"`
	Children []struct {
		Standings *struct {
			Season      int    `json:"season"`
			DisplayName string `json:"displayName"`
			Entries     []struct {
				Team *struct {
					ID               string `json:"id"`
					ShortDisplayName string `json:"shortDisplayName"`
					Abbreviation     string `json:"abbreviation"`
					DisplayName      string `json:"displayName"`
					Logos            []struct {
						Href string `json:"href"`
					} `json:"logos"`
				} `json:"team"`
				Note *struct {
					Description string `json:"description"`
				} `json:"note"`
				Stats []struct {
					Name         string `json:"name"`
					DisplayValue string `json:"displayValue"`
				} `json:"stats"`
			} `json:"entries"`
		} `json:"standings"`
	} `json:"children"`
}

// FetchLeagueTable mengambil klasemen via ESPN hidden API (tanpa key):
// site.web.api.espn.com/apis/v2/sports/soccer/{code}/standings.
// CATATAN: varian host site.api.espn.com/apis/site/v2/... me-return {}
// kosong — wajib host + path ini (terverifikasi Okt 2026).
func FetchLeagueTable(ctx context.Context, code string) (*LeagueTable, error) {
	const engine = "espn"
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, fmt.Errorf("%s: kode liga kosong", engine)
	}
	api := "https://site.web.api.espn.com/apis/v2/sports/soccer/" + code + "/standings"
	var r espnStandingsResp
	tctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := fetchJSON(tctx, engine, api, nil, &r); err != nil {
		return nil, err
	}
	if len(r.Children) == 0 || r.Children[0].Standings == nil || len(r.Children[0].Standings.Entries) == 0 {
		return nil, fmt.Errorf("%s: klasemen kosong", engine)
	}
	st := r.Children[0].Standings
	out := &LeagueTable{League: r.Name, Rows: make([]TableRow, 0, len(st.Entries))}
	if st.Season > 0 {
		out.Season = fmt.Sprintf("%d-%02d", st.Season, (st.Season+1)%100)
	}
	for i, e := range st.Entries {
		if e.Team == nil {
			continue
		}
		team := firstNonEmpty(e.Team.ShortDisplayName, e.Team.Abbreviation, e.Team.DisplayName)
		logo := ""
		if len(e.Team.Logos) > 0 {
			logo = e.Team.Logos[0].Href
		}
		stat := map[string]string{}
		for _, s := range e.Stats {
			stat[s.Name] = s.DisplayValue
		}
		zone := ""
		if e.Note != nil {
			zone = e.Note.Description
		}
		out.Rows = append(out.Rows, TableRow{
			Pos: i + 1, Team: team, TeamID: e.Team.ID, LogoURL: logo,
			GF:     firstNonEmpty(stat["pointsFor"], "-"),
			GA:     firstNonEmpty(stat["pointsAgainst"], "-"),
			Played: firstNonEmpty(stat["gamesPlayed"], "-"),
			Won:    firstNonEmpty(stat["wins"], "-"),
			Draw:   firstNonEmpty(stat["ties"], "-"),
			Lost:   firstNonEmpty(stat["losses"], "-"),
			GD:     firstNonEmpty(stat["pointDifferential"], "-"),
			Points: firstNonEmpty(stat["points"], "-"),
			Zone:   zone,
		})
	}
	if len(out.Rows) == 0 {
		return nil, fmt.Errorf("%s: klasemen kosong", engine)
	}
	return out, nil
}
