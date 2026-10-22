package plugins

import (
	"context"
	"strings"
	"testing"

	"wabot-go/internal/external"
	"wabot-go/internal/messaging"

	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types/events"
	"google.golang.org/protobuf/proto"
)

// TestShazamTrackCaptionBersih memastikan kartu hasil tidak lagi memuat link
// Spotify maupun instruksi manual ("unduh", ".sp").
func TestShazamTrackCaptionBersih(t *testing.T) {
	track := &external.ShazamTrack{
		Title:       "Bohemian Rhapsody",
		Artist:      "Queen",
		Album:       "A Night at the Opera",
		ReleaseYear: "1975",
		Genres:      "Rock",
		HasLyrics:   true,
	}
	got := shazamTrackCaption(track)

	for _, bad := range []string{"Spotify", "spotify", "unduh", ".sp ", ".shazam", "open.spotify.com"} {
		if strings.Contains(got, bad) {
			t.Errorf("caption masih memuat %q:\n%s", bad, got)
		}
	}
	for _, want := range []string{"Bohemian Rhapsody", "Queen", "A Night at the Opera", "1975", "Rock", ".lrc"} {
		if !strings.Contains(got, want) {
			t.Errorf("caption kehilangan %q:\n%s", want, got)
		}
	}
}

// TestShazamTrackButton memastikan pilihan tombol: track Spotify → quick_reply
// "Unduh" membawa track ID; tanpa track → cta_url "Buka Spotify".
func TestShazamTrackButton(t *testing.T) {
	btn, ok := shazamTrackButton("https://open.spotify.com/track/4uLU6hMCjMI75M1A2tKUQC", true)
	if !ok {
		t.Fatal("tombol Unduh tidak terbentuk")
	}
	if btn.Name != messaging.NFQuickReply {
		t.Fatalf("nama tombol = %q, mau %q", btn.Name, messaging.NFQuickReply)
	}
	params, _ := btn.Params.(map[string]string)
	if params["display_text"] != "Unduh" {
		t.Errorf("display = %q", params["display_text"])
	}
	if want := shazamTapPrefix + "4uLU6hMCjMI75M1A2tKUQC"; params["id"] != want {
		t.Errorf("id = %q, mau %q", params["id"], want)
	}

	search := shazamSpotifySearchURL("AC/DC", "Back In Black")
	btn, ok = shazamTrackButton(search, false)
	if !ok {
		t.Fatal("tombol Buka Spotify tidak terbentuk")
	}
	if btn.Name != messaging.NFCTAURL {
		t.Fatalf("nama tombol = %q, mau %q", btn.Name, messaging.NFCTAURL)
	}
	params, _ = btn.Params.(map[string]string)
	if params["display_text"] != "Buka Spotify" {
		t.Errorf("display = %q", params["display_text"])
	}
	if params["url"] != search {
		t.Errorf("url = %q, mau %q", params["url"], search)
	}

	if _, ok := shazamTrackButton("", false); ok {
		t.Error("link kosong seharusnya tidak menghasilkan tombol")
	}
	if _, ok := shazamTrackButton("https://open.spotify.com/track/id-aneh!", true); ok {
		t.Error("track ID tidak valid seharusnya tidak menghasilkan tombol Unduh")
	}
}

// TestShazamSpotifySearchURL memastikan query judul+artis ter-escape sebagai
// satu segmen path (spasi jadi %20, garis miring jadi %2F).
func TestShazamSpotifySearchURL(t *testing.T) {
	got := shazamSpotifySearchURL("AC/DC", "Back In Black")
	want := "https://open.spotify.com/search/AC%2FDC%20Back%20In%20Black"
	if got != want {
		t.Errorf("url = %q, mau %q", got, want)
	}
	if got := shazamSpotifySearchURL("", "  "); got != "" {
		t.Errorf("query kosong = %q, mau string kosong", got)
	}
}

// TestNativeFlowTapID memastikan id tombol terbaca dari jalur tap quick_reply
// (TemplateButtonReplyMessage) dan fallback InteractiveResponseMessage.
func TestNativeFlowTapID(t *testing.T) {
	const want = shazamTapPrefix + "4uLU6hMCjMI75M1A2tKUQC"

	evt := &events.Message{Message: &waE2E.Message{
		TemplateButtonReplyMessage: &waE2E.TemplateButtonReplyMessage{
			SelectedID: proto.String(want),
		},
	}}
	if id, ok := nativeFlowTapID(evt); !ok || id != want {
		t.Fatalf("tap quick_reply = (%q,%v), mau (%q,true)", id, ok, want)
	}

	evt = &events.Message{Message: &waE2E.Message{
		InteractiveResponseMessage: &waE2E.InteractiveResponseMessage{
			InteractiveResponseMessage: &waE2E.InteractiveResponseMessage_NativeFlowResponseMessage_{
				NativeFlowResponseMessage: &waE2E.InteractiveResponseMessage_NativeFlowResponseMessage{
					ParamsJSON: proto.String(`{"id":"` + want + `"}`),
				},
			},
		},
	}}
	if id, ok := nativeFlowTapID(evt); !ok || id != want {
		t.Fatalf("tap interactive = (%q,%v), mau (%q,true)", id, ok, want)
	}

	evt = &events.Message{Message: &waE2E.Message{
		TemplateButtonReplyMessage: &waE2E.TemplateButtonReplyMessage{
			SelectedID: proto.String("pdfmerge:gabung"),
		},
	}}
	if id, ok := nativeFlowTapID(evt); !ok || id != "pdfmerge:gabung" {
		t.Fatalf("tap lain = (%q,%v)", id, ok)
	}
}

// TestShazamOEmbedURL memastikan URL track Spotify dibungkus sebagai query
// oEmbed (track ID sudah aman, tapi tetap di-escape sebagai satu nilai query).
func TestShazamOEmbedURL(t *testing.T) {
	got := shazamOEmbedURL("4uLU6hMCjMI75M1A2tKUQC")
	want := "https://open.spotify.com/oembed?url=https%3A%2F%2Fopen.spotify.com%2Ftrack%2F4uLU6hMCjMI75M1A2tKUQC"
	if got != want {
		t.Errorf("url = %q, mau %q", got, want)
	}
}

// TestShazamFetchCoverTanpaSumber: tanpa URL cover dan tanpa track Spotify,
// fungsi harus langsung nil tanpa menyentuh jaringan (kartu tetap terkirim).
func TestShazamFetchCoverTanpaSumber(t *testing.T) {
	if got := shazamFetchCover(context.Background(), "  ", ""); got != nil {
		t.Errorf("tanpa sumber seharusnya nil, dapat %d byte", len(got))
	}
	if got := shazamFetchCover(context.Background(), "", "bukan id valid!"); got != nil {
		t.Errorf("track ID tidak valid seharusnya nil, dapat %d byte", len(got))
	}
}
