package plugins

import (
	"context"
	"fmt"
	"strings"
	"time"

	"wabot-go/internal/external"
	"wabot-go/internal/store"
	"wabot-go/internal/timex"
)

func init() {
	registerPlugin(RegisterJadwal)
}

// jadwalLeagues memetakan liga ke nama + alias Indonesia (alias disamakan
// dengan klasemen) + slug onefootball untuk tab fixtures/results + jumlah
// laga satu round penuh (dipakai aturan "selesai sepenuhnya": UCL fase
// liga = 18, knockout beda judul section sehingga tak perlu hitungan ini).
var jadwalLeagues = []struct {
	code   string
	name   string
	slug   string
	round  int
	alias  []string
}{
	{"eng.1", "Premier League", "premier-league-9", 10, []string{"inggris", "epl", "pl", "premier league", "premier"}},
	{"esp.1", "La Liga", "laliga-10", 10, []string{"spanyol", "laliga", "la liga"}},
	{"ita.1", "Serie A", "serie-a-13", 10, []string{"italia", "italy", "serie a", "seriea"}},
	{"ger.1", "Bundesliga", "bundesliga-1", 9, []string{"jerman", "german", "bundesliga"}},
	{"fra.1", "Ligue 1", "ligue-1-23", 10, []string{"prancis", "france", "ligue 1", "ligue1", "league 1"}},
	{"uefa.champions", "Liga Champions", "uefa-champions-league-5", 18, []string{"ucl", "champions", "liga champions", "liga champion"}},
}

// Zona tampil jam/tanggal mengikuti satu-satunya sumber kebenaran repo,
// internal/timex (env BOT_TIMEZONE, nama IANA) — diteruskan sebagai
// parameter loc agar fungsi format murni & test hermetik.

// RegisterJadwal mendaftarkan `.jadwal` (pekan berjalan) dan `.hasil`
// (pekan selesai penuh) kategori Internet, sumber onefootball.
func RegisterJadwal(r *Registry) {
	r.Register(Command{
		Name:        "jadwal",
		Description: "Jadwal pekan berjalan (.jadwal [liga])",
		Category:    CategoryInternet,
		Handler:     handleJadwal,
	})
	r.Register(Command{
		Name:        "hasil",
		Description: "Hasil pekan selesai (.hasil [liga])",
		Category:    CategoryInternet,
		Handler:     handleHasil,
	})
}

// resolveJadwalLiga memetakan input ke liga; kosong = Premier League
// (konsisten dengan klasemen). false bila tak dikenali.
func resolveJadwalLiga(input string) (name, slug string, round int, ok bool) {
	key := strings.ToLower(strings.TrimSpace(input))
	if key == "" {
		l := jadwalLeagues[0]
		return l.name, l.slug, l.round, true
	}
	for _, l := range jadwalLeagues {
		if key == l.code {
			return l.name, l.slug, l.round, true
		}
		for _, a := range l.alias {
			if key == a {
				return l.name, l.slug, l.round, true
			}
		}
	}
	return "", "", 0, false
}

// jadwalLeagueList menyusun daftar liga untuk input tak dikenali.
func jadwalLeagueList(title string) string {
	var sb strings.Builder
	sb.WriteString(Title(title))
	for _, l := range jadwalLeagues {
		sb.WriteString("\n" + Row(l.name, ".jadwal "+l.alias[0]))
	}
	return sb.String()
}

func handleJadwal(ctx context.Context, mctx *store.MessageContext) error {
	name, slug, _, ok := resolveJadwalLiga(mctx.RawArgs)
	if !ok {
		return store.ReplyText(ctx, mctx, jadwalLeagueList("Liga tidak dikenali. Pilihan:"))
	}
	if err := store.ReplyText(ctx, mctx, "Mengambil jadwal, tunggu sebentar..."); err != nil {
		return err
	}
	go func() {
		bg := context.Background()
		secs, ferr := external.FetchOfSections(bg, slug, "fixtures")
		if ferr != nil {
			_ = store.ReplyText(bg, mctx, ErrMsg("Jadwal", fmt.Sprintf("Gagal ambil jadwal: %v", ferr)))
			return
		}
		_ = store.ReplyText(bg, mctx, formatJadwal(name, secs[0], timex.Zone()))
	}()
	return nil
}

func handleHasil(ctx context.Context, mctx *store.MessageContext) error {
	name, slug, round, ok := resolveJadwalLiga(mctx.RawArgs)
	if !ok {
		return store.ReplyText(ctx, mctx, jadwalLeagueList("Liga tidak dikenali. Pilihan:"))
	}
	if err := store.ReplyText(ctx, mctx, "Mengambil hasil, tunggu sebentar..."); err != nil {
		return err
	}
	go func() {
		bg := context.Background()
		// Jangkar pekan berjalan (fixtures): section results yang round-nya
		// SAMA (judul + overlap link / jumlah tak penuh) dilewati — ini
		// yang menjamin "selesai sepenuhnya".
		fix, ferr := external.FetchOfSections(bg, slug, "fixtures")
		if ferr != nil {
			_ = store.ReplyText(bg, mctx, ErrMsg("Hasil", fmt.Sprintf("Gagal ambil hasil: %v", ferr)))
			return
		}
		res, rerr := external.FetchOfSections(bg, slug, "results")
		if rerr != nil {
			_ = store.ReplyText(bg, mctx, ErrMsg("Hasil", fmt.Sprintf("Gagal ambil hasil: %v", rerr)))
			return
		}
		sec, note := pickHasilSection(fix[0], res, round)
		if sec == nil {
			_ = store.ReplyText(bg, mctx, ErrMsg("Hasil", "Data pekan selesai belum tersedia, coba lagi nanti."))
			return
		}
		_ = store.ReplyText(bg, mctx, formatHasil(name, sec, note, timex.Zone()))
	}()
	return nil
}

// pickHasilSection memilih section hasil yang selesai sepenuhnya + catatan
// kaki bila pekan berjalan sudah ada laga FT. Keputusan "selesai" memakai
// TIGA sinyal (judul saja tidak cukup — semua section UCL berjudul sama
// "League Phase", terverifikasi Okt 2026):
//  1. Judul beda dari pekan berjalan = round lain yang sudah lewat.
//  2. Overlap link dengan fixtures = pekan yang sama tengah berjalan.
//  3. Jumlah kartu < satu round penuh = belum semua main.
// Section lolos hanya bila SEMUA kartunya finished (defensif).
func pickHasilSection(fix external.OfSection, res []external.OfSection, round int) (*external.OfSection, string) {
	note := ""
	cands := res
	if len(res) > 0 && sameRound(res[0], fix, round) {
		// Pekan berjalan: hitung yang sudah FT untuk catatan kaki.
		done := 0
		for _, m := range fix.Matches {
			if m.Status == external.OfFinished {
				done++
			}
		}
		if done > 0 {
			note = fmt.Sprintf("%s masih berjalan (%d/%d selesai).", fix.Title, done, len(fix.Matches))
		}
		cands = res[1:]
	}
	for i := range cands {
		if len(cands[i].Matches) > 0 && allFinished(cands[i].Matches) {
			return &cands[i], note
		}
	}
	return nil, note
}

// sameRound memutuskan apakah section results r adalah round yang SAMA
// dengan pekan berjalan f (artinya belum tentu selesai). Judul sama saja
// tidak cukup (label generik seperti "League Phase" dipakai banyak round)
// — harus diperkuat overlap link ATAU jumlah kartu tak penuh.
func sameRound(r, f external.OfSection, round int) bool {
	if r.Title == "" || f.Title == "" || r.Title != f.Title {
		return false
	}
	if overlapLink(r.Matches, f.Matches) {
		return true
	}
	if round > 0 && len(r.Matches) < round {
		return true
	}
	return false
}

// overlapLink true bila ada satu pun link laga yang sama di kedua list.
func overlapLink(a, b []external.OfMatch) bool {
	set := map[string]bool{}
	for _, m := range a {
		if m.Link != "" {
			set[m.Link] = true
		}
	}
	for _, m := range b {
		if m.Link != "" && set[m.Link] {
			return true
		}
	}
	return false
}

func allFinished(ms []external.OfMatch) bool {
	for _, m := range ms {
		if m.Status != external.OfFinished {
			return false
		}
	}
	return true
}

// formatJadwal merangkai section pekan berjalan sebagai kartu per laga
// (header status + code block skor sejajar), urut kronologis API.
func formatJadwal(league string, sec external.OfSection, loc *time.Location) string {
	var sb strings.Builder
	sb.WriteString(jadwalHead("Jadwal", league, sec.Title))
	for _, m := range sec.Matches {
		sb.WriteString("\n\n" + formatKartu(m, time.Now(), loc))
	}
	return sb.String()
}

// formatHasil merangkai section hasil penuh sebagai kartu per laga +
// catatan kaki (bila ada).
func formatHasil(league string, sec *external.OfSection, note string, loc *time.Location) string {
	var sb strings.Builder
	sb.WriteString(jadwalHead("Hasil", league, sec.Title))
	for _, m := range sec.Matches {
		sb.WriteString("\n\n" + formatKartu(m, time.Now(), loc))
	}
	if strings.TrimSpace(note) != "" {
		sb.WriteString("\n_" + strings.TrimSpace(note) + "_")
	}
	return sb.String()
}

// formatKartu merangkai satu laga: header `STATUS – tanggal` + code block
// dua baris sejajar (padding per laga, hitung rune agar é/ñ aman):
//
//	`FT – 12/10/2026`
//	```Racing Santander : 2
//	Valencia         : 2```
func formatKartu(m external.OfMatch, now time.Time, loc *time.Location) string {
	head := ""
	switch m.Status {
	case external.OfFinished:
		head = "FT – " + kartuTanggal(m.Kickoff, loc)
	case external.OfLive:
		head = liveLabel(m.LiveLabel) + " – " + kartuTanggal(m.Kickoff, loc)
	default:
		head = kartuJam(m.Kickoff, loc) + " – " + relHari(m.Kickoff, now, loc)
	}
	hs, as := m.HomeScore, m.AwayScore
	if strings.TrimSpace(hs) == "" {
		hs = "-"
	}
	if strings.TrimSpace(as) == "" {
		as = "-"
	}
	home, away := shortTeam(m.Home), shortTeam(m.Away)
	w := runeLen(home)
	if r := runeLen(away); r > w {
		w = r
	}
	var sb strings.Builder
	sb.WriteString("`" + head + "`")
	sb.WriteString("\n```" + padKanan(home, w) + " : " + hs)
	sb.WriteString("\n" + padKanan(away, w) + " : " + as + "```")
	return sb.String()
}

// kartuTanggal memformat tanggal kickoff zona loc: "12/10/2026" ("?" bila kosong).
func kartuTanggal(t time.Time, loc *time.Location) string {
	if t.IsZero() {
		return "?"
	}
	return t.In(loc).Format("02/01/2006")
}

// kartuJam memformat jam kickoff zona loc: "20:00" ("--:--" bila kosong).
func kartuJam(t time.Time, loc *time.Location) string {
	if t.IsZero() {
		return "--:--"
	}
	return t.In(loc).Format("15:04")
}

// relHari melabeli tanggal kickoff zona loc relatif ke hari ini: Hari ini /
// Besok / DD/MM/YYYY.
func relHari(t time.Time, now time.Time, loc *time.Location) string {
	if t.IsZero() {
		return "Jadwal menyusul"
	}
	k := t.In(loc)
	n := now.In(loc)
	kd := time.Date(k.Year(), k.Month(), k.Day(), 0, 0, 0, 0, loc)
	nd := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, loc)
	switch kd.Sub(nd) {
	case 0:
		return "Hari ini"
	case 24 * time.Hour:
		return "Besok"
	default:
		return k.Format("02/01/2006")
	}
}

// liveLabel menormalisasi label live apa adanya: half-time → HT,
// kosong → Live, sisanya tampil persis dari sumber.
func liveLabel(tp string) string {
	s := strings.TrimSpace(tp)
	if s == "" {
		return "Live"
	}
	if ls := strings.ToLower(strings.ReplaceAll(s, "_", " ")); ls == "half time" || ls == "ht" {
		return "HT"
	}
	return s
}

// runeLen menghitung panjang rune (é/ñ = 1) untuk padding monospace.
func runeLen(s string) int {
	return len([]rune(s))
}

// padKanan mengganjal spasi hingga lebar w rune.
func padKanan(s string, w int) string {
	if d := w - runeLen(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// jadwalHead membuat judul "Jadwal/Hasil {liga} — {pekan}"; tanpa
// embel-embel bila pekan kosong (defensif).
func jadwalHead(kind, league, pekan string) string {
	if strings.TrimSpace(pekan) == "" {
		return Title(kind + " " + league)
	}
	return Title(kind + " " + league + " — " + pekan)
}

// shortTeam memendekkan nama tim ala onefootball display (tanpa tabel
// pemetaan raksasa): kasus khusus + buang sufiks umum.
func shortTeam(name string) string {
	n := strings.TrimSpace(name)
	switch n {
	case "Manchester United":
		return "Man United"
	case "Manchester City":
		return "Man City"
	case "Tottenham Hotspur":
		return "Tottenham"
	case "West Ham United":
		return "West Ham"
	case "Wolverhampton Wanderers":
		return "Wolves"
	case "Borussia Mönchengladbach":
		return "Gladbach"
	case "Racing de Santander":
		return "Racing Santander"
	}
	if i := strings.Index(n, " & "); i > 0 {
		n = strings.TrimSpace(n[:i])
	}
	for _, suf := range []string{" AFC", " FC", " United", " City", " Town", " Rovers", " Hotspur"} {
		if strings.HasSuffix(n, suf) {
			n = strings.TrimSuffix(n, suf)
			break
		}
	}
	return n
}
