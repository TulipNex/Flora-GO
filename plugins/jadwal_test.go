package plugins

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"wabot-go/internal/external"
)

func ofFin(home, away, hs, as string) external.OfMatch {
	return external.OfMatch{Home: home, Away: away, HomeScore: hs, AwayScore: as, Status: external.OfFinished}
}

func ofFinLink(home, away, hs, as, link string) external.OfMatch {
	m := ofFin(home, away, hs, as)
	m.Link = link
	return m
}

func ofUp(home, away string) external.OfMatch {
	return external.OfMatch{Home: home, Away: away, Status: external.OfUpcoming}
}

// testWIB adalah zona eksplisit untuk test format (ekspektasi 20:30 dkk).
// Produksi oper timex.Zone() dari handler — test TIDAK menyentuh env agar
// hermetik dan tidak mengotori binary test paket plugins.
var testWIB = time.FixedZone("WIB", 7*3600)

func TestPickHasilSectionBerjalan(t *testing.T) {
	// Pekan 7 berjalan (1 FT + 1 upcoming, link FT juga di fixtures):
	// hasil harus MD6 + catatan.
	fix := external.OfSection{Title: "Matchday 7", Matches: []external.OfMatch{
		ofFinLink("Everton", "Chelsea", "1", "0", "/en/match/1"), ofUp("Arsenal", "Fulham"),
	}}
	res := []external.OfSection{
		{Title: "Matchday 7", Matches: []external.OfMatch{ofFinLink("Everton", "Chelsea", "1", "0", "/en/match/1")}},
		{Title: "Matchday 6", Matches: []external.OfMatch{ofFin("Leeds", "Newcastle", "4", "1")}},
	}
	sec, note := pickHasilSection(fix, res, 10)
	if sec == nil || sec.Title != "Matchday 6" {
		t.Fatalf("dapat %+v, mau Matchday 6", sec)
	}
	if !strings.Contains(note, "Matchday 7 masih berjalan (1/2 selesai)") {
		t.Fatalf("catatan %q", note)
	}
}

func TestPickHasilSectionSelesai(t *testing.T) {
	// Pekan 7 tak ada di results (belum mulai): hasil = section pertama.
	fix := external.OfSection{Title: "Matchday 7", Matches: []external.OfMatch{ofUp("A", "B")}}
	res := []external.OfSection{
		{Title: "Matchday 6", Matches: []external.OfMatch{ofFin("Leeds", "Newcastle", "4", "1")}},
	}
	sec, note := pickHasilSection(fix, res, 10)
	if sec == nil || sec.Title != "Matchday 6" {
		t.Fatalf("dapat %+v, mau Matchday 6", sec)
	}
	if note != "" {
		t.Fatalf("catatan harus kosong, dapat %q", note)
	}
}

func TestPickHasilSectionLabelGenerik(t *testing.T) {
	// Kasus UCL: semua section berjudul "League Phase". Section terbaru
	// (18/18, tanpa overlap fixtures) HARUS tampil — bukan dilewati.
	mk := func(i int) external.OfMatch {
		return ofFinLink("H"+string(rune('A'+i)), "A"+string(rune('A'+i)), "1", "0",
			fmt.Sprintf("/en/match/res%d", i))
	}
	res0 := external.OfSection{Title: "League Phase"}
	for i := 0; i < 18; i++ {
		res0.Matches = append(res0.Matches, mk(i))
	}
	fix := external.OfSection{Title: "League Phase", Matches: []external.OfMatch{
		{Home: "X", Away: "Y", Status: external.OfUpcoming, Link: "/en/match/fix0"},
	}}
	res := []external.OfSection{res0,
		{Title: "League Phase", Matches: []external.OfMatch{ofFin("O", "P", "2", "2")}},
	}
	sec, _ := pickHasilSection(fix, res, 18)
	if sec == nil || len(sec.Matches) != 18 {
		t.Fatalf("section terbaru harus tampil, dapat %+v", sec)
	}
}

func TestPickHasilSectionParsialDilewati(t *testing.T) {
	// Judul sama, 9/18 kartu tanpa overlap (separuh main): lewati.
	var part []external.OfMatch
	for i := 0; i < 9; i++ {
		part = append(part, ofFin("H", "A", "1", "0"))
	}
	fix := external.OfSection{Title: "League Phase", Matches: []external.OfMatch{ofUp("X", "Y")}}
	res := []external.OfSection{
		{Title: "League Phase", Matches: part},
		{Title: "League Phase", Matches: []external.OfMatch{ofFin("O", "P", "2", "2")}},
	}
	sec, _ := pickHasilSection(fix, res, 18)
	if sec == nil || sec.Matches[0].Home != "O" {
		t.Fatalf("section parsial harus dilewati, dapat %+v", sec)
	}
}

func TestShortTeam(t *testing.T) {
	cases := map[string]string{
		"Manchester United": "Man United", "Manchester City": "Man City",
		"Brighton & Hove Albion": "Brighton", "Liverpool FC": "Liverpool",
		"Leeds United": "Leeds", "Hull City": "Hull", "Ipswich Town": "Ipswich",
		"Chelsea": "Chelsea", "Nottingham Forest": "Nottingham Forest",
	}
	for in, want := range cases {
		if got := shortTeam(in); got != want {
			t.Fatalf("%q: dapat %q, mau %q", in, got, want)
		}
	}
}

func TestResolveJadwalLiga(t *testing.T) {
	if _, _, _, ok := resolveJadwalLiga(""); !ok {
		t.Fatal("kosong harus default EPL")
	}
	name, slug, round, ok := resolveJadwalLiga("laliga")
	if !ok || name != "La Liga" || slug != "laliga-10" || round != 10 {
		t.Fatalf("laliga: %q %q %d %v", name, slug, round, ok)
	}
	if _, _, _, ok := resolveJadwalLiga("liga antah"); ok {
		t.Fatal("liga tak dikenal harus false")
	}
}

func TestFormatHasil(t *testing.T) {
	sec := &external.OfSection{Title: "Matchday 6", Matches: []external.OfMatch{
		ofFin("Leeds United", "Newcastle United", "4", "1"),
	}}
	got := formatHasil("Premier League", sec, "", testWIB)
	if !strings.Contains(got, "*Hasil Premier League — Matchday 6*") {
		t.Fatalf("judul hilang:\n%s", got)
	}
	if !strings.Contains(got, "`FT – ?`\n```Leeds     : 4\nNewcastle : 1```") {
		t.Fatalf("kartu skor tidak sejajar:\n%s", got)
	}
}

func TestFormatKartu(t *testing.T) {
	// now = 17 Okt 2026 10:00 WIB. Kickoff 13:30 UTC = 20:30 WIB.
	now := time.Date(2026, 10, 17, 10, 0, 0, 0, testWIB)
	ko := func(day, h, mi int) time.Time { return time.Date(2026, 10, day, h, mi, 0, 0, time.UTC) }

	ft := formatKartu(external.OfMatch{Home: "Racing de Santander", Away: "Valencia",
		HomeScore: "2", AwayScore: "2", Status: external.OfFinished,
		Kickoff: time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)}, now, testWIB)
	wantFT := "`FT – 12/10/2026`\n```Racing Santander : 2\nValencia         : 2```"
	if ft != wantFT {
		t.Fatalf("FT:\ndapat %q\nmau  %q", ft, wantFT)
	}

	up := formatKartu(external.OfMatch{Home: "Everton", Away: "Chelsea",
		Status: external.OfUpcoming, Kickoff: ko(17, 13, 30)}, now, testWIB)
	if !strings.HasPrefix(up, "`20:30 – Hari ini`") {
		t.Fatalf("upcoming hari ini: %q", up)
	}
	if !strings.Contains(up, "Everton : -") || !strings.Contains(up, "Chelsea : -") {
		t.Fatalf("skor upcoming harus -:\n%s", up)
	}

	besok := formatKartu(external.OfMatch{Home: "Everton", Away: "Chelsea",
		Status: external.OfUpcoming, Kickoff: ko(18, 13, 30)}, now, testWIB)
	if !strings.HasPrefix(besok, "`20:30 – Besok`") {
		t.Fatalf("besok: %q", besok)
	}

	lusa := formatKartu(external.OfMatch{Home: "Everton", Away: "Chelsea",
		Status: external.OfUpcoming, Kickoff: ko(19, 13, 30)}, now, testWIB)
	if !strings.HasPrefix(lusa, "`20:30 – 19/10/2026`") {
		t.Fatalf("lusa: %q", lusa)
	}

	live := formatKartu(external.OfMatch{Home: "Arsenal", Away: "Fulham",
		HomeScore: "1", AwayScore: "0", Status: external.OfLive, LiveLabel: "70'",
		Kickoff: time.Date(2026, 10, 17, 14, 0, 0, 0, time.UTC)}, now, testWIB)
	if !strings.HasPrefix(live, "`70' – 17/10/2026`") {
		t.Fatalf("live: %q", live)
	}

	ht := formatKartu(external.OfMatch{Home: "A", Away: "B",
		HomeScore: "0", AwayScore: "0", Status: external.OfLive, LiveLabel: "Half time",
		Kickoff: time.Date(2026, 10, 17, 14, 0, 0, 0, time.UTC)}, now, testWIB)
	if !strings.HasPrefix(ht, "`HT – 17/10/2026`") {
		t.Fatalf("HT: %q", ht)
	}
}

func TestPadKananRune(t *testing.T) {
	if got := padKanan("Alavés", 8); got != "Alavés  " {
		t.Fatalf("dapat %q", got)
	}
	// é = 1 rune: "Alavés"(6) + 2 spasi = 8.
	if runeLen("Alavés") != 6 {
		t.Fatalf("runeLen salah: %d", runeLen("Alavés"))
	}
}
