package external

import (
	"strings"
	"testing"
)

// ofSample meniru bentuk __NEXT_DATA__ halaman onefootball: subtitle
// MENGIKUTI array matchCards-nya; skema period campuran (numerik
// page-schema + string API-schema).
const ofSample = `{"x":{"matchCardsList":{"matchCards":[` +
	`{"kickoff":"2026-10-17T11:30:00Z","timePeriod":"","period":1,` +
	`"homeTeam":{"name":"Everton","score":""},"awayTeam":{"name":"Chelsea","score":""}},` +
	`{"kickoff":"2026-10-17T14:00:00Z","timePeriod":"Second half","period":"SECOND_HALF",` +
	`"homeTeam":{"name":"Arsenal","score":"1"},"awayTeam":{"name":"Fulham","score":"0"}}` +
	`],"trackingEvents":[],"sectionHeader":{"title":"","subtitle":"Matchday 7"}}}}`

func TestParseOfSections(t *testing.T) {
	secs, err := parseOfSections([]byte(ofSample))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(secs) != 1 {
		t.Fatalf("dapat %d section, mau 1", len(secs))
	}
	s := secs[0]
	if s.Title != "Matchday 7" {
		t.Fatalf("judul %q", s.Title)
	}
	if len(s.Matches) != 2 {
		t.Fatalf("dapat %d laga, mau 2", len(s.Matches))
	}
	if s.Matches[0].Status != OfUpcoming {
		t.Fatalf("laga 0 status %v, mau upcoming", s.Matches[0].Status)
	}
	if s.Matches[1].Status != OfLive {
		t.Fatalf("laga 1 status %v, mau live", s.Matches[1].Status)
	}
	if s.Matches[1].LiveLabel != "Second half" {
		t.Fatalf("label live %q", s.Matches[1].LiveLabel)
	}
	if s.Matches[1].HomeScore != "1" || s.Matches[1].AwayScore != "0" {
		t.Fatalf("skor live %q-%q", s.Matches[1].HomeScore, s.Matches[1].AwayScore)
	}
	if s.Matches[0].Kickoff.IsZero() {
		t.Fatal("kickoff tidak ke-parse")
	}
}

func TestOfPeriodKind(t *testing.T) {
	cases := map[any]string{
		float64(1): "upcoming", float64(11): "finished", float64(5): "live",
		"PRE_MATCH": "upcoming", "FULL_TIME": "finished",
		"Full time": "finished", "SECOND_HALF": "live",
	}
	for in, want := range cases {
		if got := ofPeriodKind(in); got != want {
			t.Fatalf("period %v: dapat %q, mau %q", in, got, want)
		}
	}
}

func TestSliceJSONArray(t *testing.T) {
	in := []byte(`[{"a":"]"},{"b":[1,2]}] ,"x":1`)
	arr, next, err := sliceJSONArray(in)
	if err != nil {
		t.Fatalf("slice: %v", err)
	}
	if !strings.HasSuffix(string(arr), "]") || !strings.Contains(string(arr), `"b":[1,2]`) {
		t.Fatalf("array terpotong: %s", arr)
	}
	if string(in[next:next+1]) != " " {
		t.Fatalf("offset lanjut salah: %q", in[next:])
	}
	if _, _, err := sliceJSONArray([]byte(`[{"a":1`)); err == nil {
		t.Fatal("array tak berakhir harus error")
	}
}
