package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"wabot-go/internal/external"
	"wabot-go/internal/logfmt"
	"wabot-go/internal/messaging"
	"wabot-go/internal/store"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types/events"
)

func init() {
	registerPlugin(RegisterShazam)
}

// Batas audio Shazam (fingerprint hanya butuh ±12 detik, tapi decode
// butuh file utuh — tolak raksasa sebelum buang waktu/CPU).
const shazamMaxMediaBytes = 100 << 20

// Prefix id tombol kartu hasil. Track ID Spotify ditanam di id tombol supaya
// tap tidak bergantung pending store/TTL (22 karakter, jauh di bawah batas id).
const shazamTapPrefix = "shazam:dl:"

// shazamHeaderSide = sisi maksimum cover yang diunggah sebagai header kartu.
const shazamHeaderSide = 640

func RegisterShazam(r *Registry) {
	r.Register(Command{
		Name:        "shazam",
		Aliases:     []string{"find", "findmusic"},
		Description: "Kenali lagu (.shazam + reply VN/audio)",
		Category:    CategoryInternet,
		Handler:     handleShazam,
	})
}

// handleShazam hanya mendeteksi lagu: reply VN/audio/video/dokumen. Semua
// jalur pencarian teks (.shazam search/track) sudah dihapus.
func handleShazam(ctx context.Context, mctx *store.MessageContext) error {
	if !shazamHasAudio(mctx) {
		return store.ReplyText(ctx, mctx, "Reply VN/audio/video dengan .shazam untuk mengenali lagunya.")
	}
	return shazamRunAsync(ctx, mctx, "Mengenali lagu, tunggu sebentar...", func(bg context.Context) {
		shazamDoRecognize(bg, mctx)
	})
}

func shazamRunAsync(ctx context.Context, mctx *store.MessageContext, status string, fn func(bg context.Context)) error {
	if err := store.ReplyText(ctx, mctx, status); err != nil {
		return err
	}
	go func() { fn(context.Background()) }()
	return nil
}

// shazamTakeAudio mengambil bytes audio (VN/audio/video/dokumen) dari pesan
// sendiri maupun quoted. isVideo true untuk video (ffmpeg ambil trek audio).
func shazamTakeAudio(ctx context.Context, mctx *store.MessageContext) ([]byte, bool, error) {
	msg := store.UnwrapMessage(mctx.RawEvt.Message)
	quoted := store.UnwrapMessage(store.ExtractQuotedMessage(mctx.RawEvt))
	for _, m := range []*waE2E.Message{msg, quoted} {
		if m == nil {
			continue
		}
		if aud := m.GetAudioMessage(); aud != nil {
			data, err := mctx.Client.Download(ctx, aud)
			if err != nil {
				return nil, false, fmt.Errorf("gagal unduh audio: %v", err)
			}
			return shazamCheckAudio(data, false)
		}
		if vid := m.GetVideoMessage(); vid != nil {
			data, err := mctx.Client.Download(ctx, vid)
			if err != nil {
				return nil, false, fmt.Errorf("gagal unduh video: %v", err)
			}
			return shazamCheckAudio(data, true)
		}
		if doc := m.GetDocumentMessage(); doc != nil {
			data, err := mctx.Client.Download(ctx, doc)
			if err != nil {
				return nil, false, fmt.Errorf("gagal unduh dokumen: %v", err)
			}
			return shazamCheckAudio(data, false)
		}
	}
	return nil, false, fmt.Errorf("reply VN/audio/video (atau kirim dengan caption .shazam)")
}

func shazamCheckAudio(data []byte, isVideo bool) ([]byte, bool, error) {
	if len(data) == 0 {
		return nil, false, fmt.Errorf("audio kosong")
	}
	if len(data) > shazamMaxMediaBytes {
		return nil, false, fmt.Errorf("audio %.0f MB melebihi batas 100 MB", float64(len(data))/(1<<20))
	}
	return data, isVideo, nil
}

func shazamHasAudio(mctx *store.MessageContext) bool {
	msg := store.UnwrapMessage(mctx.RawEvt.Message)
	quoted := store.UnwrapMessage(store.ExtractQuotedMessage(mctx.RawEvt))
	for _, m := range []*waE2E.Message{msg, quoted} {
		if m == nil {
			continue
		}
		if m.GetAudioMessage() != nil || m.GetVideoMessage() != nil || m.GetDocumentMessage() != nil {
			return true
		}
	}
	return false
}

func shazamDoRecognize(ctx context.Context, mctx *store.MessageContext) {
	data, isVideo, err := shazamTakeAudio(ctx, mctx)
	if err != nil {
		_ = store.ReplyText(ctx, mctx, err.Error())
		return
	}
	samples, err := external.DecodeAudioPCM16K(ctx, data, isVideo)
	if err != nil {
		_ = store.ReplyText(ctx, mctx, err.Error())
		return
	}
	track, err := external.RecognizeShazam(ctx, samples)
	if err != nil {
		_ = store.ReplyText(ctx, mctx, fmt.Sprintf("Tidak dikenali: %v", err))
		return
	}
	shazamSendTrack(ctx, mctx, track)
}

// shazamDoDownloadUnduh mengunduh URL track via jalur Spotify (cover+caption+
// audio — identik .sp <url>). Dipanggil dari tombol "Unduh".
func shazamDoDownloadUnduh(ctx context.Context, mctx *store.MessageContext, rawURL string) {
	p := spotifyPlatform()
	if p == nil {
		_ = store.ReplyText(ctx, mctx, "Platform spotify tidak terdaftar.")
		return
	}
	select {
	case dlSem <- struct{}{}:
		defer func() { <-dlSem }()
	default:
		_ = store.ReplyText(ctx, mctx, "Antrean download penuh (maks 2 bersamaan), coba lagi sebentar.")
		return
	}
	status, err := sendDlStatus(ctx, mctx, "Mengunduh, tunggu sebentar...")
	if err != nil {
		logfmt.Warn("shazam: gagal kirim status (%v)\n", err)
		return
	}
	if rerr := downloadSpotifyAndSend(ctx, mctx, rawURL, p, true, status, nil); rerr != nil {
		logfmt.Warn("shazam: unduh lagu gagal (%v)\n", rerr)
		_ = store.ReplyText(ctx, mctx, "Gagal mengunduh lagu, coba lagi sebentar.")
	}
}

var (
	shazamSpotifyTrackRe = regexp.MustCompile(`spotify:track:([A-Za-z0-9]+)`)
	shazamTrackIDRe      = regexp.MustCompile(`^[A-Za-z0-9]{1,64}$`)
)

// shazamSpotifySearchURL = halaman pencarian Spotify untuk judul+artis.
func shazamSpotifySearchURL(title, artist string) string {
	query := strings.TrimSpace(strings.TrimSpace(title) + " " + strings.TrimSpace(artist))
	if query == "" {
		return ""
	}
	return "https://open.spotify.com/search/" + url.PathEscape(query)
}

// shazamSpotifyLink menyelesaikan link Spotify yang bisa dipakai.
// Apple kadang memberi URI skema (spotify:search:/spotify:track:), bukan
// URL https — langsung diteruskan hanya bila sudah URL track valid.
// Mengembalikan (url, langsungBisaDiunduh).
func shazamSpotifyLink(ctx context.Context, t *external.ShazamTrack) (string, bool) {
	raw := strings.TrimSpace(t.SpotifyURL)
	if strings.HasPrefix(raw, "https://open.spotify.com/track/") {
		return raw, true
	}
	if m := shazamSpotifyTrackRe.FindStringSubmatch(raw); m != nil {
		return "https://open.spotify.com/track/" + m[1], true
	}
	// spotify:search: / kosong → resolve via pencarian Spotify internal.
	query := strings.TrimSpace(strings.TrimSpace(t.Title) + " " + strings.TrimSpace(t.Artist))
	if query == "" {
		return "", false
	}
	if res, err := external.SearchSpotify(ctx, query, 1); err == nil && len(res) > 0 && strings.TrimSpace(res[0].ID) != "" {
		return "https://open.spotify.com/track/" + strings.TrimSpace(res[0].ID), true
	}
	return shazamSpotifySearchURL(t.Title, t.Artist), false
}

// shazamTrackButton memilih tombol kartu hasil:
//   - track Spotify ada → quick_reply "Unduh" (id membawa track ID),
//   - tidak ada → cta_url "Buka Spotify" ke halaman pencarian.
//
// ok=false bila link kosong (kartu dikirim tanpa tombol).
func shazamTrackButton(link string, direct bool) (messaging.NFButton, bool) {
	if direct {
		id := strings.TrimPrefix(link, "https://open.spotify.com/track/")
		if link == "https://open.spotify.com/track/"+id && shazamTrackIDRe.MatchString(id) {
			return messaging.QuickReply(shazamTapPrefix+id, "Unduh"), true
		}
		// Link "langsung" tapi bentuknya rusak: jangan kirim tombol mati.
		return messaging.NFButton{}, false
	}
	if strings.TrimSpace(link) == "" {
		return messaging.NFButton{}, false
	}
	return messaging.CTAURL("Buka Spotify", link), true
}

// shazamTrackCaption membangun isi kartu hasil (tanpa link Spotify — link
// sudah diwakili tombol).
func shazamTrackCaption(t *external.ShazamTrack) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "*%s*\n", firstNonEmptyShazam(t.Title))
	fmt.Fprintf(&sb, "> Artis : %s\n", firstNonEmptyShazam(t.Artist))
	fmt.Fprintf(&sb, "> Album : %s\n", firstNonEmptyShazam(t.Album))
	fmt.Fprintf(&sb, "> Tahun : %s\n", firstNonEmptyShazam(t.ReleaseYear))
	if strings.TrimSpace(t.Genres) != "" {
		fmt.Fprintf(&sb, "> Genre : %s\n", t.Genres)
	}
	if t.HasLyrics {
		sb.WriteString("\n_Lirik tersedia — cari via .lrc <judul>_")
	}
	return strings.TrimRight(sb.String(), "\n")
}

func firstNonEmptyShazam(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return "-"
}

// shazamFetchCover mengembalikan cover yang SUDAH dinormalkan (JPEG baseline
// ≤640px). Rantai: cover Apple → cover Spotify (oEmbed) → nil (kartu tanpa
// gambar). Cover gagal bukan alasan menggagalkan kartu.
func shazamFetchCover(ctx context.Context, coverURL, spotifyID string) []byte {
	if u := strings.TrimSpace(coverURL); u != "" {
		if b := shazamCoverBytes(ctx, "apple", u); len(b) > 0 {
			return b
		}
	}
	if id := strings.TrimSpace(spotifyID); id != "" && shazamTrackIDRe.MatchString(id) {
		if u, err := shazamSpotifyOEmbedCover(ctx, id); err != nil {
			logfmt.Warn("shazam: cover spotify oembed gagal (%v)\n", err)
		} else if u != "" {
			if b := shazamCoverBytes(ctx, "spotify", u); len(b) > 0 {
				return b
			}
		}
	}
	logfmt.Warn("shazam: tidak ada cover yang bisa dipakai, kartu tanpa gambar\n")
	return nil
}

// shazamCoverBytes mengunduh cover (default → paksa IPv4 bila gagal) lalu
// menormalkannya. IPv4 dipaksa karena CDN Apple memutus transfer IPv6 di
// tengah body dari jaringan owner (lihat external.DownloadBytesIPv4).
func shazamCoverBytes(ctx context.Context, label, rawURL string) []byte {
	data, err := external.DownloadBytes(ctx, rawURL)
	if err != nil {
		logfmt.Warn("shazam: cover %s gagal (%v), ulang via IPv4\n", label, err)
		data, err = external.DownloadBytesIPv4(ctx, rawURL)
		if err != nil {
			logfmt.Warn("shazam: cover %s gagal juga via IPv4 (%v)\n", label, err)
			return nil
		}
		logfmt.Info("shazam: cover %s via IPv4 berhasil (%d byte)\n", label, len(data))
	}
	norm := messaging.HeaderJPEG(data, shazamHeaderSide, 85)
	if len(norm) == 0 {
		logfmt.Warn("shazam: cover %s tidak bisa dinormalkan (%d byte)\n", label, len(data))
		return nil
	}
	return norm
}

// shazamOEmbedURL = endpoint oEmbed Spotify (publik, tanpa auth) yang
// mengembalikan thumbnail_url cover sebuah track.
func shazamOEmbedURL(trackID string) string {
	return "https://open.spotify.com/oembed?url=" + url.QueryEscape("https://open.spotify.com/track/"+trackID)
}

// shazamSpotifyOEmbedCover mengambil URL cover dari oEmbed Spotify.
func shazamSpotifyOEmbedCover(ctx context.Context, trackID string) (string, error) {
	u := shazamOEmbedURL(trackID)
	data, err := external.DownloadBytes(ctx, u)
	if err != nil {
		if data, err = external.DownloadBytesIPv4(ctx, u); err != nil {
			return "", err
		}
	}
	var r struct {
		ThumbnailURL string `json:"thumbnail_url"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return "", fmt.Errorf("JSON oembed invalid: %w", err)
	}
	return strings.TrimSpace(r.ThumbnailURL), nil
}

// shazamSendTrack mengirim kartu hasil sebagai SATU bubble bertombol dengan
// cover sebagai header gambar (ImageMessage hasil upload). Fail-soft tetap
// satu bubble: header gagal → bubble teks + tombol yang sama; bubble gagal
// total → teks saja.
func shazamSendTrack(ctx context.Context, mctx *store.MessageContext, track *external.ShazamTrack) {
	link, direct := shazamSpotifyLink(ctx, track)
	caption := shazamTrackCaption(track)

	var spotifyID string
	if direct {
		spotifyID = strings.TrimPrefix(link, "https://open.spotify.com/track/")
	}
	cover := shazamFetchCover(ctx, track.Cover, spotifyID)

	if btn, ok := shazamTrackButton(link, direct); ok {
		send := func(t messaging.NFText) error {
			return messaging.SendNativeFlow(ctx, mctx, t, "shazam-track", caption, btn)
		}
		t := messaging.NFText{Body: caption, Footer: "Flora Go"}
		if len(cover) > 0 {
			// Header native_flow harus ImageMessage hasil upload; JPEGThumbnail
			// saja diabaikan klien (header jadi kosong).
			if im, err := messaging.UploadImageMessage(ctx, mctx.Client, cover); err == nil {
				t.HeaderImage = im
			} else {
				logfmt.Warn("shazam: upload cover gagal (%v)\n", err)
			}
		}
		if err := send(t); err == nil {
			return
		} else {
			logfmt.Warn("shazam: kirim bubble tombol gagal (%v)\n", err)
		}
		// Ulang tanpa header gambar: tombol tetap sampai ke user.
		if t.HeaderImage != nil {
			if err := send(messaging.NFText{Body: caption, Footer: "Flora Go"}); err == nil {
				return
			}
		}
	}

	_ = store.ReplyText(ctx, mctx, caption)
}

// CheckShazamButtonTap mencegat tap tombol kartu hasil ("shazam:dl:<trackID>")
// lalu menjalankan unduhan. true = ditangani (setop, jangan ke AI).
func CheckShazamButtonTap(client *whatsmeow.Client, evt *events.Message) bool {
	id, ok := nativeFlowTapID(evt)
	if !ok || !strings.HasPrefix(id, shazamTapPrefix) {
		return false
	}
	trackID := strings.TrimSpace(strings.TrimPrefix(id, shazamTapPrefix))
	ctx := context.Background()
	mctx := &store.MessageContext{Client: client, Chat: evt.Info.Chat, Sender: evt.Info.Sender, RawEvt: evt}
	if !shazamTrackIDRe.MatchString(trackID) {
		_ = store.ReplyText(ctx, mctx, "Tombol unduh tidak valid, kenali lagunya ulang.")
		return true
	}
	go shazamDoDownloadUnduh(ctx, mctx, "https://open.spotify.com/track/"+trackID)
	return true
}
