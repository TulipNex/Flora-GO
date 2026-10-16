package plugins

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"wabot-go/internal/external"
	"wabot-go/internal/store"
	"wabot-go/internal/tableimage"
)

func init() {
	registerPlugin(RegisterKlasemen)
}

// klasemenLeagues memetakan kode liga ESPN ke nama + alias Indonesia.
// Port LIGA_ALIAS internet-liga.js (minus liga1 — ESPN tak cover).
var klasemenLeagues = []struct {
	code    string
	name    string
	aliases []string
}{
	{"eng.1", "Premier League", []string{"inggris", "epl", "pl", "premier league", "premier"}},
	{"esp.1", "La Liga", []string{"spanyol", "laliga", "la liga"}},
	{"ita.1", "Serie A", []string{"italia", "italy", "serie a", "seriea"}},
	{"ger.1", "Bundesliga", []string{"jerman", "german", "bundesliga"}},
	{"fra.1", "Ligue 1", []string{"prancis", "france", "ligue 1", "ligue1", "league 1"}},
	{"uefa.champions", "Liga Champions", []string{"ucl", "champions", "liga champions", "liga champion"}},
}

func resolveKlasemenLiga(input string) (code, name string) {
	key := strings.ToLower(strings.TrimSpace(input))
	if key == "" {
		return klasemenLeagues[0].code, klasemenLeagues[0].name
	}
	for _, l := range klasemenLeagues {
		if key == l.code {
			return l.code, l.name
		}
		for _, a := range l.aliases {
			if key == a {
				return l.code, l.name
			}
		}
	}
	return "", ""
}

// klasemenZone memetakan deskripsi zona ESPN ke kategori warna. PENTING:
// "playoff" dicek DULU karena teks UCL "Knockout phase playoffs" mengandung
// "knockout" sekaligus "playoffs" — urutan lama bikin rank 9-24 ikut hijau.
func klasemenZone(zone string) tableimage.Zone {
	z := strings.ToLower(zone)
	switch {
	case strings.Contains(z, "playoff"):
		return tableimage.ZonePO
	case strings.Contains(z, "round of 16"),
		strings.Contains(z, "champions league"):
		return tableimage.ZoneUCL
	case strings.Contains(z, "europa"):
		return tableimage.ZoneUEL
	case strings.Contains(z, "eliminat"):
		return tableimage.ZoneElim
	case strings.Contains(z, "relegation"):
		return tableimage.ZoneRele
	default:
		return tableimage.ZoneNone
	}
}

// klasemenMarker memetakan zona ke emoji (dipakai fallback teks).
func klasemenMarker(zone string) string {
	switch klasemenZone(zone) {
	case tableimage.ZoneUCL:
		return "🟢"
	case tableimage.ZoneUEL:
		return "🔵"
	case tableimage.ZonePO:
		return "🟡"
	case tableimage.ZoneRele, tableimage.ZoneElim:
		return "🔴"
	default:
		return " "
	}
}

func RegisterKlasemen(r *Registry) {
	r.Register(Command{
		Name:        "klasemen",
		Aliases:     []string{"liga"},
		Description: "Klasemen liga top Eropa (.klasemen/.liga [liga])",
		Category:    CategoryInternet,
		Handler:     handleKlasemen,
	})
}

// formatKlasemenList menyusun daftar liga (tanpa prefix command, pemisah
// "_atau_") + satu baris contoh. Dipakai saat argumen kosong maupun salah.
func formatKlasemenList(title string) string {
	var sb strings.Builder
	sb.WriteString(title + "\n")
	for _, l := range klasemenLeagues {
		fmt.Fprintf(&sb, "• %s _atau_ %s\n", l.aliases[0], l.name)
	}
	sb.WriteString("\nContoh: .liga inggris")
	return sb.String()
}

func formatKlasemen(t *external.LeagueTable) string {
	var sb strings.Builder
	title := strings.ToUpper(t.League)
	if title == "" {
		title = "LIGA"
	}
	fmt.Fprintf(&sb, "🏆 *KLASEMEN %s*\n", title)
	if t.Season != "" {
		fmt.Fprintf(&sb, "Musim %s\n", t.Season)
	}
	sb.WriteString("────────────────────────────────\n")
	sb.WriteString("No Tim               M  W  D  L  GD  Pts\n")
	sb.WriteString("────────────────────────────────\n")
	for _, r := range t.Rows {
		name := r.Team
		if len([]rune(name)) > 16 {
			name = string([]rune(name)[:16])
		}
		fmt.Fprintf(&sb, "%s%-2d %-16s %2s %2s %2s %2s %3s %4s\n",
			klasemenMarker(r.Zone), r.Pos, name,
			r.Played, r.Won, r.Draw, r.Lost, r.GD, r.Points)
	}
	sb.WriteString("────────────────────────────────\n")
	sb.WriteString("🟢 UCL  🔵 UEL  🟡 Playoff  🔴 Degradasi\n")
	sb.WriteString("_Sumber: ESPN_")
	return sb.String()
}

func handleKlasemen(ctx context.Context, mctx *store.MessageContext) error {
	raw := strings.TrimSpace(strings.Join(mctx.Args, " "))
	if raw == "" {
		return store.ReplyText(ctx, mctx, formatKlasemenList("Pilih liga:"))
	}
	code, _ := resolveKlasemenLiga(raw)
	if code == "" {
		return store.ReplyText(ctx, mctx, formatKlasemenList("Liga tidak dikenali. Pilihan:"))
	}
	if err := store.ReplyText(ctx, mctx, "Mengambil klasemen, tunggu sebentar..."); err != nil {
		return err
	}
	go func() {
		bg := context.Background()
		t, ferr := external.FetchLeagueTable(bg, code)
		if ferr != nil {
			_ = store.ReplyText(bg, mctx, fmt.Sprintf("Gagal ambil klasemen: %v", ferr))
			return
		}
		if png := renderKlasemenPNG(bg, code, t); len(png) > 0 {
			caption := klasemenCaption(t)
			if serr := sendDlBytesImage(bg, mctx, png, caption); serr == nil {
				return
			}
		}
		// Fallback teks bila render/kirim gambar gagal.
		_ = store.ReplyText(bg, mctx, formatKlasemen(t))
	}()
	return nil
}

// klasemenCaption adalah caption gambar tabel (judul + legenda + sumber).
func klasemenCaption(t *external.LeagueTable) string {
	title := strings.ToUpper(strings.TrimSpace(t.League))
	if title == "" {
		title = "LIGA"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "🏆 *KLASEMEN %s*", title)
	if strings.TrimSpace(t.Season) != "" {
		fmt.Fprintf(&sb, "\nMusim %s", strings.TrimSpace(t.Season))
	}
	sb.WriteString("\n_Sumber: ESPN_")
	return sb.String()
}

// klasemenLogo mengambil logo tim (cache 30 hari di wabot-tmp). Gagal =
// nil (baris digambar tanpa logo, bukan error).
func klasemenLogo(ctx context.Context, teamID, logoURL string) []byte {
	if strings.TrimSpace(logoURL) == "" {
		return nil
	}
	base, err := store.BotTempDir()
	if err != nil {
		return nil
	}
	dir := filepath.Join(base, "tableimage-logos")
	_ = os.MkdirAll(dir, 0755)
	key := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return -1
	}, teamID)
	if key == "" {
		return nil
	}
	path := filepath.Join(dir, "logo-"+key+".png")
	if info, serr := os.Stat(path); serr == nil && time.Since(info.ModTime()) < 30*24*time.Hour {
		if data, rerr := os.ReadFile(path); rerr == nil && len(data) > 0 {
			return data
		}
	}
	data, derr := external.DownloadBytes(ctx, logoURL)
	if derr != nil || len(data) == 0 {
		return nil
	}
	_ = os.WriteFile(path, data, 0600)
	return data
}

// renderKlasemenPNG mengunduh logo (paralel, maks 6) lalu me-render tabel.
// Kosong bila render gagal.
func renderKlasemenPNG(ctx context.Context, code string, t *external.LeagueTable) []byte {
	rows := make([]tableimage.StandingRow, len(t.Rows))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	for i, r := range t.Rows {
		wg.Add(1)
		go func(i int, r external.TableRow) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			rows[i] = tableimage.StandingRow{
				Pos: r.Pos, Team: r.Team, M: r.Played, W: r.Won,
				D: r.Draw, L: r.Lost, GF: r.GF, GA: r.GA, GD: r.GD, Pts: r.Points,
				Zone: klasemenZone(r.Zone),
				Logo: klasemenLogo(ctx, r.TeamID, r.LogoURL),
			}
		}(i, r)
	}
	wg.Wait()
	title := strings.ToUpper(strings.TrimSpace(t.League))
	if title == "" {
		title = "LIGA"
	}
	sub := ""
	if strings.TrimSpace(t.Season) != "" {
		sub = "Musim " + strings.TrimSpace(t.Season)
	}
	return tableimage.RenderStandings(ctx, code, "KLASEMEN "+title, sub, rows)
}
