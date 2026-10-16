package plugins

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"wabot-go/internal/external"
	"wabot-go/internal/logfmt"
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
const (
	shazamMaxMediaBytes = 100 << 20
	shazamSearchLimit   = 5
	shazamPickTTL       = 5 * time.Minute
)

type shPendingResult struct {
	songs []external.ShazamTrack
	query string
}

var shPending = newPendingStore[*shPendingResult](shazamPickTTL)

func shPendingSet(chat, sender string, songs []external.ShazamTrack, query string) {
	shPending.Set(chat, sender, &shPendingResult{songs: songs, query: query})
}

func shPendingPeek(chat, sender string) (*shPendingResult, time.Time) {
	v, at, _ := shPending.Peek(chat, sender)
	return v, at
}

func shPendingConsume(chat, sender string) *shPendingResult {
	v, _ := shPending.Consume(chat, sender)
	return v
}

// shNewerThanSpotify true bila pending Shazam lebih baru (aturan terbaru menang).
func shNewerThanSpotify(chat, sender string) bool {
	hp, hpAt := shPendingPeek(chat, sender)
	sp, spAt := spPendingPeek(chat, sender)
	if hp == nil || sp == nil {
		return false
	}
	return hpAt.After(spAt)
}

// shNewerThanLrc true bila pending Shazam lebih baru dari pending lirik.
func shNewerThanLrc(chat, sender string) bool {
	hp, hpAt := shPendingPeek(chat, sender)
	lp, lpAt := lrcPendingPeek(chat, sender)
	if hp == nil || lp == nil {
		return false
	}
	return hpAt.After(lpAt)
}

func RegisterShazam(r *Registry) {
	r.Register(Command{
		Name:        "shazam",
		Aliases:     []string{"find", "findmusic"},
		Description: "Kenali lagu (.shazam + reply VN/audio)",
		Category:    CategoryInternet,
		Handler:     handleShazam,
	})
}

func handleShazam(ctx context.Context, mctx *store.MessageContext) error {
	// `.shazam <angka>` saat ada pending = pilih hasil.
	if len(mctx.Args) == 1 {
		if handled, msg := tryShazamNumberPick(mctx, mctx.Args[0]); handled {
			if msg != "" {
				return store.ReplyText(ctx, mctx, msg)
			}
			return nil
		}
	}
	if len(mctx.Args) > 0 {
		switch sub := strings.ToLower(strings.TrimSpace(mctx.Args[0])); sub {
		case "search", "cari":
			query := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(mctx.RawArgs), mctx.Args[0]))
			if query == "" {
				return store.ReplyText(ctx, mctx, "Sertakan judul. Contoh: .shazam search bohemian rhapsody")
			}
			return shazamRunAsync(ctx, mctx, "Mencari lagu, tunggu sebentar...", func(bg context.Context) {
				shazamDoSearch(bg, mctx, query)
			})
		case "track":
			if len(mctx.Args) < 2 || strings.TrimSpace(mctx.Args[1]) == "" {
				return store.ReplyText(ctx, mctx, "Sertakan ID track. Contoh: .shazam track 123456789")
			}
			id := strings.TrimSpace(mctx.Args[1])
			return shazamRunAsync(ctx, mctx, "Mengambil detail lagu, tunggu sebentar...", func(bg context.Context) {
				shazamDoTrack(bg, mctx, id)
			})
		case "unduh", "download", "dl":
			rawURL := shDlPendingConsume(mctx.Chat.String(), mctx.Sender.String())
			if rawURL == "" {
				return store.ReplyText(ctx, mctx, "Tidak ada lagu yang bisa diunduh (hasil Shazam kedaluwarsa >5 menit). Kenali/cari lagunya dulu.")
			}
			go shazamDoDownloadUnduh(context.Background(), mctx, rawURL)
			return nil
		}
	}
	if !shazamHasAudio(mctx) {
		return store.ReplyText(ctx, mctx, "Reply VN/audio/video dengan .shazam untuk mengenali lagunya.\nContoh lain: .shazam search <judul> | .shazam track <id>")
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

func shazamDoSearch(ctx context.Context, mctx *store.MessageContext, query string) {
	songs, err := external.SearchShazam(ctx, query, shazamSearchLimit)
	if err != nil {
		_ = store.ReplyText(ctx, mctx, fmt.Sprintf("Pencarian gagal: %v", err))
		return
	}
	shPendingSet(mctx.Chat.String(), mctx.Sender.String(), songs, query)
	for _, alt := range pendingAltSenders(mctx) {
		shPendingSet(mctx.Chat.String(), alt, songs, query)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Hasil pencarian untuk \"%s\" :\n\n", query)
	for i, s := range songs {
		artist := strings.TrimSpace(s.Artist)
		if artist == "" {
			artist = "-"
		}
		dur := ""
		if s.DurationMs > 0 {
			dur = fmt.Sprintf(" • %d:%02d", s.DurationMs/60000, (s.DurationMs%60000)/1000)
		}
		fmt.Fprintf(&sb, "*[%d] %s*\n> Artis : %s%s\n", i+1, s.Title, artist, dur)
	}
	sb.WriteString("\n_Ketik nomor urut untuk detail._")
	_ = store.ReplyText(ctx, mctx, sb.String())
}

// tryShazamNumberPick menafsir arg/angka sebagai pilihan hasil pencarian.
// Mode pick eksplisit (.shazam N). (ditangani, pesanError).
func tryShazamNumberPick(mctx *store.MessageContext, numText string) (bool, string) {
	n, ok := parsePlainPickIndex(numText)
	if !ok {
		return false, ""
	}
	pend, _ := shPendingPeek(mctx.Chat.String(), mctx.Sender.String())
	if pend == nil {
		return false, ""
	}
	if n < 1 || n > len(pend.songs) {
		return true, fmt.Sprintf("Pilih angka 1-%d dari hasil \"%s\".", len(pend.songs), pend.query)
	}
	song := pend.songs[n-1]
	shPendingConsume(mctx.Chat.String(), mctx.Sender.String())
	go shazamSendSearchPick(context.Background(), mctx, song)
	return true, ""
}

// shazamSendSearchPick mengirim detail lagu pilihan pencarian.
func shazamSendSearchPick(ctx context.Context, mctx *store.MessageContext, song external.ShazamTrack) {
	if strings.TrimSpace(song.ID) != "" {
		shazamDoTrack(ctx, mctx, strings.TrimSpace(song.ID))
		return
	}
	// Tanpa ID Shazam (peta a2st gagal): tampilkan kartu dari data iTunes.
	shazamSendTrack(ctx, mctx, &song)
}

// CheckShazamPick mencegat angka polos saat ada pending pencarian Shazam.
// true = ditangani (setop, jangan ke AI).
func CheckShazamPick(client *whatsmeow.Client, evt *events.Message, text string) bool {
	n, ok := parsePlainPickIndex(text)
	if !ok {
		return false
	}
	chat, sender := evt.Info.Chat.String(), evt.Info.Sender.String()
	pend, pendAt := shPendingPeek(chat, sender)
	if pend == nil {
		return false
	}
	if n < 1 || n > len(pend.songs) {
		return false
	}
	// Pending lain yang lebih baru menang.
	if sp, spAt := spPendingPeek(chat, sender); sp != nil && spAt.After(pendAt) {
		return false
	}
	if lp, lpAt := lrcPendingPeek(chat, sender); lp != nil && lpAt.After(pendAt) {
		return false
	}
	song := pend.songs[n-1]
	shPendingConsume(chat, sender)
	mctx := &store.MessageContext{Client: client, Chat: evt.Info.Chat, Sender: evt.Info.Sender, RawEvt: evt}
	go shazamSendSearchPick(context.Background(), mctx, song)
	return true
}

// shazamDoDownloadUnduh mengunduh URL track pending via jalur Spotify
// (cover+caption+audio — identik .sp <url>).
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

// CheckShazamDownload mencegat kata "unduh" polos saat ada pending hasil
// Shazam dari pengirim yang sama. true = ditangani (setop, jangan ke AI).
func CheckShazamDownload(client *whatsmeow.Client, evt *events.Message, text string) bool {
	if !strings.EqualFold(strings.TrimSpace(text), "unduh") {
		return false
	}
	chat, sender := evt.Info.Chat.String(), evt.Info.Sender.String()
	rawURL := shDlPendingPeek(chat, sender)
	if rawURL == "" {
		return false
	}
	shDlPendingConsume(chat, sender)
	mctx := &store.MessageContext{Client: client, Chat: evt.Info.Chat, Sender: evt.Info.Sender, RawEvt: evt}
	go shazamDoDownloadUnduh(context.Background(), mctx, rawURL)
	return true
}

// shDlPendingPeek melihat pending tanpa menghapus (untuk interceptor).
func shDlPendingPeek(chat, sender string) string {
	v, _, _ := shDlPending.Peek(chat, sender)
	return v
}

func shazamDoTrack(ctx context.Context, mctx *store.MessageContext, id string) {
	track, err := external.GetShazamTrack(ctx, id)
	if err != nil {
		_ = store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal ambil track: %v", err))
		return
	}
	shazamSendTrack(ctx, mctx, track)
}

var shazamSpotifyTrackRe = regexp.MustCompile(`spotify:track:([A-Za-z0-9]+)`)

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
	return "https://open.spotify.com/search/" + url.PathEscape(query), false
}

// shazamTrackCaption membangun kartu hasil: hint, baris kosong, lalu link.
// direct=true berarti link berupa URL track (bisa .sp / unduh langsung).
func shazamTrackCaption(t *external.ShazamTrack, link string, direct bool) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "*%s*\n", firstNonEmptyShazam(t.Title, "-"))
	fmt.Fprintf(&sb, "> Artis : %s\n", firstNonEmptyShazam(t.Artist, "-"))
	fmt.Fprintf(&sb, "> Album : %s\n", firstNonEmptyShazam(t.Album, "-"))
	fmt.Fprintf(&sb, "> Tahun : %s\n", firstNonEmptyShazam(t.ReleaseYear, "-"))
	if strings.TrimSpace(t.Genres) != "" {
		fmt.Fprintf(&sb, "> Genre : %s\n", t.Genres)
	}
	if link == "" {
		if t.HasLyrics {
			sb.WriteString("_Lirik tersedia — cari via .lrc <judul>_")
		}
		return strings.TrimRight(sb.String(), "\n")
	}
	fmt.Fprintf(&sb, "> Spotify : %s\n", link)
	sb.WriteString("\n")
	if direct {
		sb.WriteString("_Ketik \"unduh\" atau via .spotify <url> untuk download lagu_\n")
	} else {
		sb.WriteString("_Buka link, salin URL lagunya, lalu .sp <url>_\n")
	}
	if t.HasLyrics {
		sb.WriteString("_Lirik tersedia — cari via .lrc <judul>_")
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

// shDlPending menyimpan URL track hasil Shazam untuk perintah "unduh".
const shDlPickTTL = 5 * time.Minute

var shDlPending = newPendingStore[string](shDlPickTTL)

func shDlPendingSet(chat, sender, rawURL string) {
	shDlPending.Set(chat, sender, rawURL)
}

func shDlPendingConsume(chat, sender string) string {
	v, _ := shDlPending.Consume(chat, sender)
	return v
}

// shazamSendTrack resolve link, simpan pending unduh (bila langsung),
// lalu kirim cover + caption; gagal cover = teks saja.
func shazamSendTrack(ctx context.Context, mctx *store.MessageContext, track *external.ShazamTrack) {
	link, direct := shazamSpotifyLink(ctx, track)
	if direct && link != "" {
		shDlPendingSet(mctx.Chat.String(), mctx.Sender.String(), link)
		for _, alt := range pendingAltSenders(mctx) {
			shDlPendingSet(mctx.Chat.String(), alt, link)
		}
	}
	caption := shazamTrackCaption(track, link, direct)
	if strings.TrimSpace(track.Cover) != "" {
		if data, err := external.DownloadBytes(ctx, strings.TrimSpace(track.Cover)); err == nil && len(data) > 0 {
			if serr := sendDlBytesImage(ctx, mctx, data, caption); serr == nil {
				return
			} else {
				logfmt.Warn("shazam: kirim cover gagal (%v)\n", serr)
			}
		} else if err != nil {
			logfmt.Warn("shazam: unduh cover gagal (%v)\n", err)
		}
	}
	_ = store.ReplyText(ctx, mctx, caption)
}
