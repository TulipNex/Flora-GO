package plugins

import (
	"context"
	"fmt"
	"strings"
	"time"

	"wabot-go/internal/external"
	"wabot-go/internal/identity"
	"wabot-go/internal/logfmt"
	"wabot-go/internal/store"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/types"
	"github.com/polymorfa/hypermeow/types/events"
)

// Pencarian Spotify: `.sp <judul>` → daftar bernomor → pilih via
// `.sp <angka>` atau reply angka polos. Port alur downloader-spotify.js
// (minus tombol interactive — teks saja). Track SAJA, tanpa album/playlist
// dan tanpa lirik (keputusan owner).

const (
	spSearchLimit = 5
	spPickTTL     = 5 * time.Minute
)

type spPendingResult struct {
	tracks []external.SpotifyTrack
	query  string
}

var spPending = newPendingStore[*spPendingResult](spPickTTL)

// normPendingJID menormalisasi string JID ke bare (tanpa device/AD), supaya
// flip format pengirim tidak menghilangkan hasil pick yang tersimpan.
func normPendingJID(raw string) string {
	if jid, err := types.ParseJID(raw); err == nil && !jid.IsEmpty() {
		return jid.ToNonAD().String()
	}
	return raw
}

// pendingKey membangun kunci pending pick chat|sender ternormalisasi.
func pendingKey(chat, sender string) string {
	return normPendingJID(chat) + "|" + normPendingJID(sender)
}

// pendingAltSenders mengembalikan bentuk alt sender yang ternormalisasi
// (SenderAlt event + resolve store LID↔PN, di luar bentuk primer) — untuk
// penyimpanan ganda hasil pick, supaya flip format di tengah sesi
// pilih-angka tetap ketemu (pola G7).
func pendingAltSenders(mctx *store.MessageContext) []string {
	primary := ""
	if !mctx.Sender.IsEmpty() {
		primary = mctx.Sender.ToNonAD().String()
	}
	var alt types.JID
	if mctx.RawEvt != nil {
		alt = mctx.RawEvt.Info.SenderAlt
	}
	all := identity.Strings(context.Background(), mctx.Client, mctx.Sender, alt)
	out := make([]string, 0, len(all))
	for _, s := range all {
		if s != primary {
			out = append(out, s)
		}
	}
	return out
}

func spPendingSet(chat, sender string, tracks []external.SpotifyTrack, query string) {
	spPending.Set(chat, sender, &spPendingResult{tracks: tracks, query: query})
}

// spPendingConsume mengambil + menghapus (sekali pakai — anti double-download).
func spPendingConsume(chat, sender string) *spPendingResult {
	v, _ := spPending.Consume(chat, sender)
	return v
}

func spPendingPeek(chat, sender string) (*spPendingResult, time.Time) {
	v, at, _ := spPending.Peek(chat, sender)
	return v, at
}

// isSpotifyURLArg true kalau argumen pertama terlihat seperti URL Spotify
// (jalur lama: langsung download). Selain itu = kata kunci pencarian.
func isSpotifyURLArg(arg string) bool {
	a := strings.ToLower(strings.TrimSpace(arg))
	if strings.HasPrefix(a, "http://") || strings.HasPrefix(a, "https://") {
		return true
	}
	return strings.Contains(a, "open.spotify.com") || strings.Contains(a, "spotify.link")
}

// spotifyPlatform mencari deskriptor platform spotify untuk reuse downloadAndSend.
func spotifyPlatform() *dlPlatform {
	for i := range dlPlatforms {
		if dlPlatforms[i].command == "spotify" {
			p := dlPlatforms[i]
			return &p
		}
	}
	return nil
}

func formatSpotifySearchList(query string, tracks []external.SpotifyTrack) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Hasil pencarian untuk \"%s\" :\n\n", query)
	for i, t := range tracks {
		name := strings.TrimSpace(t.Name)
		if name == "" {
			name = "-"
		}
		artists := strings.TrimSpace(t.Artists)
		if artists == "" {
			artists = "-"
		}
		dur := strings.TrimSpace(t.DurationFmt)
		if dur == "" {
			dur = "-"
		}
		fmt.Fprintf(&sb, "*[%d] %s*\n> Artist : %s\n> Duration : %s\n", i+1, name, artists, dur)
		if i < len(tracks)-1 {
			sb.WriteString("\n")
		}
	}
	sb.WriteString("\n_Ketik nomor urut yang ingin anda unduh._")
	return sb.String()
}

// handleSpotifySearch menjalankan pencarian + menyimpan pending. Async supaya
// event loop tidak terblokir (pola sama dengan handleDownload).
func handleSpotifySearch(ctx context.Context, mctx *store.MessageContext, query string) error {
	query = strings.TrimSpace(query)
	if query == "" {
		return store.ReplyText(ctx, mctx, "Sertakan URL atau judul lagu. Contoh: .sp https://open.spotify.com/... atau .sp about you")
	}
	status, err := sendDlStatus(ctx, mctx, "Mencari lagu, tunggu sebentar...")
	if err != nil {
		return err
	}
	_ = status
	go func() {
		bg := context.Background()
		tracks, serr := external.SearchSpotify(bg, query, spSearchLimit)
		if serr != nil {
			_ = store.ReplyText(bg, mctx, fmt.Sprintf("Lagu tidak ditemukan. Coba kata kunci lain. (%v)", serr))
			return
		}
		spPendingSet(mctx.Chat.String(), mctx.Sender.String(), tracks, query)
		for _, alt := range pendingAltSenders(mctx) {
			spPendingSet(mctx.Chat.String(), alt, tracks, query)
		}
		_ = store.ReplyText(bg, mctx, formatSpotifySearchList(query, tracks))
	}()
	return nil
}

// formatSpotifyCaption membangun caption cover art sebelum audio dikirim:
// *Judul* + quote Artis/Album/Durasi/Rilis (tahun saja, "-" bila kosong).
func formatSpotifyCaption(t *external.SpotifyTrack) string {
	name := strings.TrimSpace(t.Name)
	if name == "" {
		name = "-"
	}
	artists := strings.TrimSpace(t.Artists)
	if artists == "" {
		artists = "-"
	}
	album := strings.TrimSpace(t.Album)
	if album == "" {
		album = "-"
	}
	dur := strings.TrimSpace(t.DurationFmt)
	if dur == "" {
		dur = "-"
	}
	year := strings.TrimSpace(t.ReleaseYear)
	if year == "" {
		year = "-"
	}
	return fmt.Sprintf("*%s*\n> Artis : %s\n> Album : %s\n> Durasi : %s\n> Rilis : %s",
		name, artists, album, dur, year)
}

// resolveSpotifyMeta melengkapi meta untuk cover+caption. Track pending dari
// hasil pencarian sudah membawa semua kecuali tahun rilis — cukup 1 panggilan
// GetSpotifyTrack; gagal = pakai apa adanya (tahun "-"). Untuk URL langsung,
// ID diekstrak dari URL lalu meta penuh diambil; gagal total = nil (pemanggil
// lanjut audio-only diam-diam).
func resolveSpotifyMeta(ctx context.Context, rawURL string, hint *external.SpotifyTrack) *external.SpotifyTrack {
	id := external.SpotifyIDFromURL(rawURL)
	if id == "" && hint != nil {
		id = strings.TrimSpace(hint.ID)
	}
	if id == "" {
		return nil
	}
	meta, err := external.GetSpotifyTrack(ctx, id)
	if err != nil {
		logfmt.Warn("spotify: meta track %s gagal (%v), lanjut tanpa cover.\n", id, err)
		if hint != nil && strings.TrimSpace(hint.ID) != "" {
			cpy := *hint
			return &cpy
		}
		return nil
	}
	return meta
}

// sendSpotifyCover mengunduh cover art + mengirimnya sebagai foto dengan
// caption. Gagal di titik mana pun = skip DIAM-DIAM (log saja), pemanggil
// langsung lanjut ke audio — sesuai keputusan owner.
func sendSpotifyCover(ctx context.Context, mctx *store.MessageContext, meta *external.SpotifyTrack) {
	if meta == nil || strings.TrimSpace(meta.CoverURL) == "" {
		return
	}
	data, err := external.DownloadBytes(ctx, strings.TrimSpace(meta.CoverURL))
	if err != nil || len(data) == 0 {
		logfmt.Warn("spotify: unduh cover gagal (%v)\n", err)
		return
	}
	if serr := sendDlBytesImage(ctx, mctx, data, formatSpotifyCaption(meta)); serr != nil {
		logfmt.Warn("spotify: kirim cover gagal (%v)\n", serr)
	}
}

// downloadSpotifyAndSend adalah jalur khusus Spotify: cover+caption DULU,
// audio KEMUDIAN. Dipakai URL langsung, pilihan pencarian, dan autodownload
// (semuanya bermuara di downloadAndSend). hint diisi dari track pending bila
// ada (hemat fetch), nil untuk URL langsung/autodownload.
func downloadSpotifyAndSend(ctx context.Context, mctx *store.MessageContext, rawURL string, p *dlPlatform, audioOnly bool, status types.MessageID, hint *external.SpotifyTrack) error {
	if meta := resolveSpotifyMeta(ctx, rawURL, hint); meta != nil {
		sendSpotifyCover(ctx, mctx, meta)
	}
	res, err := fetchScraper(ctx, p, rawURL, audioOnly)
	if err != nil {
		logfmt.Warn("downloader: semua scraper gagal (%v)\n", err)
		return err
	}
	return sendScrapeResult(ctx, mctx, res, status)
}

// startSpotifyTrackDownload mengunduh satu track hasil pencarian via jalur
// download lama (nexray → musicfab → universal). Konsumsi dlSem + status
// seperti handleDownload.
func startSpotifyTrackDownload(mctx *store.MessageContext, track external.SpotifyTrack) {
	bg := context.Background()
	p := spotifyPlatform()
	if p == nil {
		_ = store.ReplyText(bg, mctx, "Platform spotify tidak terdaftar.")
		return
	}
	select {
	case dlSem <- struct{}{}:
		defer func() { <-dlSem }()
	default:
		_ = store.ReplyText(bg, mctx, "Antrean download penuh (maks 2 bersamaan), coba lagi sebentar.")
		return
	}
	status, err := sendDlStatus(bg, mctx, "Mengunduh, tunggu sebentar...")
	if err != nil {
		logfmt.Warn("spotify-search: gagal kirim status (%v)\n", err)
		return
	}
	rawURL := "https://open.spotify.com/track/" + track.ID
	if rerr := downloadSpotifyAndSend(bg, mctx, rawURL, p, true, status, &track); rerr != nil {
		logfmt.Warn("spotify: unduh track gagal (%v)\n", rerr)
		_ = store.ReplyText(bg, mctx, "Gagal mengunduh lagu, coba lagi sebentar.")
	}
}

// trySpotifyNumberPick mencoba menafsir arg/angka sebagai pilihan hasil
// pencarian. Mengembalikan (ditangani, pesanError). PesanError non-kosong
// berarti angka di luar range → balas panduan, bukan download.
func trySpotifyNumberPick(mctx *store.MessageContext, numText string) (bool, string) {
	n, ok := parsePlainPickIndex(numText)
	if !ok {
		return false, ""
	}
	pend, _ := spPendingPeek(mctx.Chat.String(), mctx.Sender.String())
	if pend == nil {
		return false, ""
	}
	if n < 1 || n > len(pend.tracks) {
		return true, fmt.Sprintf("Pilih angka 1-%d dari hasil \"%s\".", len(pend.tracks), pend.query)
	}
	track := pend.tracks[n-1]
	// Konsumsi SETELAH valid (pilihan sah) — angka salah tidak menghanguskan daftar.
	spPendingConsume(mctx.Chat.String(), mctx.Sender.String())
	go startSpotifyTrackDownload(mctx, track)
	return true, ""
}

// CheckSpotifyPick mencegat pesan NON-command berupa angka polos saat ada
// hasil pencarian pending dari pengirim yang sama. true = ditangani (setop,
// jangan ke AI). Guard: angka saja + pending aktif + pengirim sama.
func CheckSpotifyPick(client *whatsmeow.Client, evt *events.Message, text string) bool {
	n, ok := parsePlainPickIndex(text)
	if !ok {
		return false
	}
	pend, _ := spPendingPeek(evt.Info.Chat.String(), evt.Info.Sender.String())
	if pend == nil {
		return false
	}
	if n < 1 || n > len(pend.tracks) {
		return false // bukan untuk kita — biar AI/moderasi yang urus
	}
	// Pending lain yang lebih baru menang — biar angkanya ke sana.
	if lrcNewerThanSpotify(evt.Info.Chat.String(), evt.Info.Sender.String()) ||
		shNewerThanSpotify(evt.Info.Chat.String(), evt.Info.Sender.String()) {
		return false
	}
	track := pend.tracks[n-1]
	spPendingConsume(evt.Info.Chat.String(), evt.Info.Sender.String())
	mctx := &store.MessageContext{Client: client, Chat: evt.Info.Chat, Sender: evt.Info.Sender, RawEvt: evt}
	go startSpotifyTrackDownload(mctx, track)
	return true
}
