package external

import (
	"context"
	"testing"
	"time"
)

func TestFloraGatewayIntegration(t *testing.T) {
	if FloraGatewayKey() == "" {
		t.Skip("skipping Flora Gateway integration test: FLORA_GATEWAY_KEY or FLORA_API_KEY is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	// 1. Spotify Search
	t.Run("SpotifySearch", func(t *testing.T) {
		tracks, err := FloraSearchSpotify(ctx, "coldplay", 2)
		if err != nil {
			t.Fatalf("FloraSearchSpotify failed: %v", err)
		}
		if len(tracks) == 0 {
			t.Fatal("expected at least 1 track")
		}
		if tracks[0].Name == "" || tracks[0].ID == "" {
			t.Errorf("invalid track data: %+v", tracks[0])
		}
	})

	// 2. Lyrics
	t.Run("LyricsSearch", func(t *testing.T) {
		songs, err := FloraSearchLyrics(ctx, "yellow coldplay", 1, "all")
		if err != nil {
			t.Fatalf("FloraSearchLyrics failed: %v", err)
		}
		if len(songs) == 0 {
			t.Fatal("expected at least 1 song")
		}
		if songs[0].TrackName == "" {
			t.Errorf("empty track name in song: %+v", songs[0])
		}
	})

	// 3. Football Standings
	t.Run("FootballStandings", func(t *testing.T) {
		table, err := FloraFetchStandings(ctx, "eng.1")
		if err != nil {
			t.Fatalf("FloraFetchStandings failed: %v", err)
		}
		if len(table.Rows) == 0 {
			t.Fatal("expected standings rows")
		}
		if table.Rows[0].Team == "" {
			t.Errorf("empty team name in first row: %+v", table.Rows[0])
		}
	})

	// 4. Football Schedule
	t.Run("FootballSchedule", func(t *testing.T) {
		secs, err := FloraFetchMatches(ctx, "eng.1", "schedule")
		if err != nil {
			t.Fatalf("FloraFetchMatches schedule failed: %v", err)
		}
		if len(secs) == 0 || len(secs[0].Matches) == 0 {
			t.Fatal("expected schedule matches")
		}
		if secs[0].Matches[0].Home == "" {
			t.Errorf("empty home team: %+v", secs[0].Matches[0])
		}
	})

	// 5. Football Results
	t.Run("FootballResults", func(t *testing.T) {
		secs, err := FloraFetchMatches(ctx, "eng.1", "results")
		if err != nil {
			t.Fatalf("FloraFetchMatches results failed: %v", err)
		}
		if len(secs) == 0 || len(secs[0].Matches) == 0 {
			t.Fatal("expected result matches")
		}
		if secs[0].Matches[0].Home == "" {
			t.Errorf("empty home team: %+v", secs[0].Matches[0])
		}
	})

	// 6. PIHPS Sembako (dengan retry karena upstream BI bisa intermittent)
	t.Run("PIHPSSembako", func(t *testing.T) {
		var rows []PihpsRow
		var dates []string
		var prov string
		var provID int
		var err error

		for attempt := 1; attempt <= 3; attempt++ {
			rows, dates, prov, provID, err = FloraFetchSembako(ctx, "jakarta", "-7", "", "")
			if err == nil && len(rows) > 0 {
				break
			}
			time.Sleep(1 * time.Second)
		}
		if err != nil {
			t.Fatalf("FloraFetchSembako failed: %v", err)
		}
		if len(rows) == 0 || len(dates) == 0 {
			t.Fatal("expected sembako rows and dates")
		}
		if prov == "" && provID == 0 {
			t.Error("expected valid province info")
		}
	})

	// 7. Stock Photo
	t.Run("StockPhoto", func(t *testing.T) {
		url, err := FloraFetchStock(ctx, "2583067397", "shutterstock")
		if err != nil {
			t.Fatalf("FloraFetchStock failed: %v", err)
		}
		if url == "" {
			t.Fatal("expected non-empty stock photo download URL")
		}
	})

	// 8. Brat Maker
	t.Run("BratMaker", func(t *testing.T) {
		media, err := FloraFetchBrat(ctx, "halo test", "image")
		if err != nil {
			t.Fatalf("FloraFetchBrat failed: %v", err)
		}
		if len(media) == 0 {
			t.Fatal("expected non-empty brat media")
		}
	})

	// 9. Smeme Maker
	t.Run("SmemeMaker", func(t *testing.T) {
		tinyPNG := []byte{
			0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
			0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
			0xde, 0x00, 0x00, 0x00, 0x0c, 0x49, 0x44, 0x41, 0x54, 0x08, 0xd7, 0x63, 0xf8, 0xcf, 0xc0, 0x00,
			0x00, 0x03, 0x01, 0x01, 0x00, 0x18, 0xdd, 0x8d, 0xb0, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e,
			0x44, 0xae, 0x42, 0x60, 0x82,
		}
		media, err := FloraFetchSmeme(ctx, tinyPNG, "atas", "bawah")
		if err != nil {
			t.Fatalf("FloraFetchSmeme failed: %v", err)
		}
		if len(media) == 0 {
			t.Fatal("expected non-empty smeme media")
		}
	})
}
