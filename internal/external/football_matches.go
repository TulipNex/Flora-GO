package external

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// football_matches.go — jadwal + hasil per MATCHDAY via onefootball.
//
// Sumber: JSON __NEXT_DATA__ yang tertanam di HTML halaman kompetisi
// (tanpa key, cukup UA browser — terverifikasi Okt 2026):
// https://onefootball.com/en/competition/{slug}/fixtures
// https://onefootball.com/en/competition/{slug}/results
// Laga sudah dikelompokkan per "Matchday N" oleh sumbernya (satu matchday
// bisa membentang berhari-hari), jadi tidak ada heuristik rentang tanggal.
//
// Skema kartu halaman (page schema): period numerik (1 = upcoming,
// 11 = full time — terverifikasi), timePeriod string ("Full time"),
// skor string numerik ("" bila belum main). Nilai period lain diperlakukan
// sebagai live/berjalan dan ditampilkan apa adanya (defensif).

// OfStatus adalah status normalisasi satu laga.
type OfStatus int

const (
	// OfUpcoming laga belum mulai.
	OfUpcoming OfStatus = iota
	// OfLive laga berjalan (atau status tak dikenal — tampil apa adanya).
	OfLive
	// OfFinished laga selesai penuh.
	OfFinished
)

// OfMatch adalah satu laga ternormalisasi.
type OfMatch struct {
	Home      string
	Away      string
	HomeScore string // "" bila belum main
	AwayScore string // "" bila belum main
	Kickoff   time.Time
	Status    OfStatus
	LiveLabel string // label live apa adanya (mis. "Second half"), "" bila tak ada
	Link      string // "/en/match/{id}" — kunci overlap fixtures↔results
}

// OfSection adalah satu grup matchday: judul + laga-laganya.
type OfSection struct {
	Title   string // mis. "Matchday 7" (string sumber apa adanya)
	Matches []OfMatch
}

// ofTeam adalah sisi home/away di kartu onefootball.
type ofTeam struct {
	Name  string `json:"name"`
	Score string `json:"score"`
}

// ofMatchCard adalah satu kartu laga di __NEXT_DATA__.
type ofMatchCard struct {
	Kickoff    string `json:"kickoff"`
	Period     any    `json:"period"`
	TimePeriod string `json:"timePeriod"`
	Link       string `json:"link"`
	Home       ofTeam `json:"homeTeam"`
	Away       ofTeam `json:"awayTeam"`
}

// ofNextDataCap membatasi HTML kompetisi yang dibaca (halaman ~450KB).
const ofNextDataCap = 4 << 20

// FetchOfSections mengambil section matchday dari tab onefootball.
// slug = slug kompetisi (mis. "premier-league-9"), tab = "fixtures"
// atau "results". Urutan section = urutan tampil di situs.
func FetchOfSections(ctx context.Context, slug, tab string) ([]OfSection, error) {
	const engine = "onefootball"
	if tab != "fixtures" && tab != "results" {
		return nil, fmt.Errorf("%s: tab %q invalid", engine, tab)
	}
	rawURL := "https://onefootball.com/en/competition/" + slug + "/" + tab
	if err := checkPublicURL(ctx, rawURL); err != nil {
		return nil, fmt.Errorf("%s: %w", engine, err)
	}
	tctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(tctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", engine, err)
	}
	req.Header.Set("User-Agent", scraperUA)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", engine, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s: HTTP %d", engine, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, ofNextDataCap+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", engine, err)
	}
	if len(body) > ofNextDataCap {
		return nil, fmt.Errorf("%s: halaman terlalu besar", engine)
	}
	data, err := extractNextData(body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", engine, err)
	}
	sections, err := parseOfSections(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", engine, err)
	}
	if len(sections) == 0 {
		return nil, fmt.Errorf("%s: tidak ada data pertandingan", engine)
	}
	return sections, nil
}

// extractNextData mengambil isi JSON __NEXT_DATA__ dari HTML.
func extractNextData(html []byte) ([]byte, error) {
	idx := bytes.Index(html, []byte(`id="__NEXT_DATA__"`))
	if idx < 0 {
		return nil, fmt.Errorf("data tertanam tidak ditemukan")
	}
	gt := bytes.IndexByte(html[idx:], '>')
	if gt < 0 {
		return nil, fmt.Errorf("data tertanam rusak")
	}
	start := idx + gt + 1
	end := bytes.Index(html[start:], []byte("</script>"))
	if end < 0 {
		return nil, fmt.Errorf("data tertanam rusak")
	}
	return html[start : start+end], nil
}

// parseOfSections memindai section (subtitle + matchCards) sesuai URUTAN
// dokumen. Bentuk situs: {"matchCards":[...],...,"sectionHeader":
// {"subtitle":"Matchday N"}} — subtitle MENGIKUTI array-nya, jadi tiap
// array dipasangkan dengan subtitle pertama SETELAH array itu (sebelum
// array berikutnya). Sengaja tidak navigasi pohon containers (bentuknya
// dalam, berubah-ubah) — pemindaian urutan dokumen tahan terhadap itu.
func parseOfSections(data []byte) ([]OfSection, error) {
	type span struct {
		arr   []byte
		end   int // offset setelah ']' array
		title string
	}
	var spans []span
	search := data
	base := 0
	for len(search) > 0 {
		iCards := bytes.Index(search, []byte(`"matchCards":[`))
		if iCards < 0 {
			break
		}
		arrStart := base + iCards + len(`"matchCards":`)
		arr, next, err := sliceJSONArray(data[arrStart:])
		if err != nil {
			// Array rusak: lewati, lanjutkan pemindaian setelahnya.
			rel := arrStart - base + 1
			search = search[rel:]
			base += rel
			continue
		}
		spans = append(spans, span{arr: arr, end: arrStart + next})
		rel := arrStart - base + next
		search = search[rel:]
		base += rel
	}
	var out []OfSection
	for i, sp := range spans {
		// Batas subtitle: sebelum array berikutnya (atau akhir dokumen).
		lim := len(data)
		if i+1 < len(spans) {
			lim = spans[i+1].end - len(spans[i+1].arr) - len(`"matchCards":[`)
		}
		title := ""
		if m := bytes.Index(data[sp.end:lim], []byte(`"subtitle":"`)); m >= 0 {
			rest := data[sp.end+m+len(`"subtitle":"`):lim]
			if end := bytes.IndexByte(rest, '"'); end >= 0 {
				title = string(rest[:end])
			}
		}
		var cards []ofMatchCard
		if err := json.Unmarshal(sp.arr, &cards); err != nil || len(cards) == 0 {
			continue
		}
		sec := OfSection{Title: title}
		for _, c := range cards {
			mm, ok := normalizeOfCard(c)
			if !ok {
				continue
			}
			sec.Matches = append(sec.Matches, mm)
		}
		if len(sec.Matches) > 0 {
			out = append(out, sec)
		}
	}
	return out, nil
}

// sliceJSONArray mengambil array JSON lengkap dari awal '[' (menghitung
// kurung dengan kesadaran string/escape) + offset setelah ']'.
func sliceJSONArray(b []byte) (arr []byte, next int, err error) {
	if len(b) == 0 || b[0] != '[' {
		return nil, 0, fmt.Errorf("bukan array")
	}
	depth := 0
	inStr := false
	esc := false
	for i, c := range b {
		if inStr {
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return b[:i+1], i + 1, nil
			}
		}
	}
	return nil, 0, fmt.Errorf("array tak berakhir")
}

// normalizeOfCard menormalisasi satu kartu; false bila kartu tak punya
// nama tim (bukan laga valid).
func normalizeOfCard(c ofMatchCard) (OfMatch, bool) {
	home := strings.TrimSpace(c.Home.Name)
	away := strings.TrimSpace(c.Away.Name)
	if home == "" || away == "" {
		return OfMatch{}, false
	}
	m := OfMatch{
		Home:      home,
		Away:      away,
		HomeScore: strings.TrimSpace(c.Home.Score),
		AwayScore: strings.TrimSpace(c.Away.Score),
		Link:      strings.TrimSpace(c.Link),
	}
	if t, err := time.Parse(time.RFC3339, strings.TrimSpace(c.Kickoff)); err == nil {
		m.Kickoff = t
	}
	switch ofPeriodKind(c.Period) {
	case "upcoming":
		m.Status = OfUpcoming
	case "finished":
		m.Status = OfFinished
	default:
		m.Status = OfLive
		m.LiveLabel = strings.TrimSpace(c.TimePeriod)
	}
	return m, true
}

// ofPeriodKind memetakan period halaman (numerik 1/11 — terverifikasi;
// string PRE_MATCH/FULL_TIME/Varian lain) ke upcoming/finished/live.
func ofPeriodKind(v any) string {
	switch p := v.(type) {
	case float64:
		switch p {
		case 1:
			return "upcoming"
		case 11:
			return "finished"
		}
	case string:
		s := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(p), "_", ""))
		switch s {
		case "prematch", "pre", "scheduled", "1":
			return "upcoming"
		case "fulltime", "full time", "ft", "finished", "post", "11":
			return "finished"
		}
	}
	return "live"
}
