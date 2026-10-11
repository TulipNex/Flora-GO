package plugins

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"wabot-go/internal/scraper"
	"wabot-go/internal/store"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types/events"
)

// Pencarian lirik via lrclib.net: `.lrclib`/`.lrc` (plain) dan
// `.lrclibs`/`.lrcs` (synced) → daftar bernomor → pilih via `<cmd> <angka>`
// atau balas angka polos. Port internet-lrc.js (minus tombol interactive —
// teks saja). Versi yang diminta kosong = fallback ke versi satunya.

func init() {
	registerPlugin(RegisterLrclib)
	registerPlugin(RegisterLrclibSynced)
}

// lrcMode adalah versi lirik yang diminta user.
type lrcMode int

const (
	lrcPlain lrcMode = iota
	lrcSynced
)

const (
	lrcSearchLimit = 5
	lrcPickTTL     = 5 * time.Minute
	// lrcMaxRunes batas pengaman bubble WA untuk lirik sangat panjang.
	lrcMaxRunes = 60000
)

type lrcPendingResult struct {
	songs []scraper.LrclibSong
	query string
	mode  lrcMode
	at    time.Time
	exp   time.Time
}

var lrcPending = struct {
	sync.Mutex
	m map[string]*lrcPendingResult
}{m: make(map[string]*lrcPendingResult)}

func lrcPendingKey(chat, sender string) string { return chat + "|" + sender }

func lrcPendingSet(chat, sender string, songs []scraper.LrclibSong, query string, mode lrcMode) {
	lrcPending.Lock()
	defer lrcPending.Unlock()
	now := time.Now()
	for k, v := range lrcPending.m {
		if now.After(v.exp) {
			delete(lrcPending.m, k)
		}
	}
	lrcPending.m[lrcPendingKey(chat, sender)] = &lrcPendingResult{songs: songs, query: query, mode: mode, at: now, exp: now.Add(lrcPickTTL)}
}

func lrcPendingPeek(chat, sender string) *lrcPendingResult {
	lrcPending.Lock()
	defer lrcPending.Unlock()
	v, ok := lrcPending.m[lrcPendingKey(chat, sender)]
	if !ok || time.Now().After(v.exp) {
		return nil
	}
	return v
}

// lrcPendingConsume mengambil + menghapus (sekali pakai).
func lrcPendingConsume(chat, sender string) *lrcPendingResult {
	lrcPending.Lock()
	defer lrcPending.Unlock()
	k := lrcPendingKey(chat, sender)
	v, ok := lrcPending.m[k]
	if !ok {
		return nil
	}
	delete(lrcPending.m, k)
	if time.Now().After(v.exp) {
		return nil
	}
	return v
}

func RegisterLrclib(r *Registry) {
	r.Register(Command{
		Name:        "lrclib",
		Aliases:     []string{"lrc"},
		Description: "Cari lirik plain (.lrclib <judul>)",
		Category:    CategoryAlat,
		Handler:     handleLrclibPlain,
	})
}

// RegisterLrclibSynced mendaftarkan `.lrclibs` (alias `.lrcs`) — varian
// synced (ber-timestamp) dari pencarian lirik.
func RegisterLrclibSynced(r *Registry) {
	r.Register(Command{
		Name:        "lrclibs",
		Aliases:     []string{"lrcs"},
		Description: "Cari lirik synced (.lrclibs <judul>)",
		Category:    CategoryAlat,
		Handler:     handleLrclibSynced,
	})
}

func formatLrcSearchList(query string, songs []scraper.LrclibSong) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Hasil pencarian lirik untuk \"%s\" :\n\n", query)
	for i, s := range songs {
		artist := strings.TrimSpace(s.ArtistName)
		if artist == "" {
			artist = "-"
		}
		fmt.Fprintf(&sb, "*[%d] %s*\n> Artis : %s\n", i+1, s.Title(), artist)
	}
	sb.WriteString("\n_Ketik nomor urut yang ingin anda lihat liriknya._")
	return sb.String()
}

// lrcLyricsFor memilih lirik sesuai mode; versi yang diminta kosong =
// fallback ke versi satunya + catatan. Keduanya kosong = ("", false).
func lrcLyricsFor(song *scraper.LrclibSong, mode lrcMode) (string, bool) {
	plain, synced := song.Plain(), song.Synced()
	switch mode {
	case lrcSynced:
		if synced != "" {
			return synced, true
		}
		if plain != "" {
			return plain + "\n\n_(synced tak tersedia, menampilkan plain)_", true
		}
	default:
		if plain != "" {
			return plain, true
		}
		if synced != "" {
			return synced + "\n\n_(plain tak tersedia, menampilkan synced)_", true
		}
	}
	return "", false
}

func formatLrcLyrics(s *scraper.LrclibSong, mode lrcMode) string {
	title := s.Title()
	artist := strings.TrimSpace(s.ArtistName)
	if artist == "" {
		artist = "-"
	}
	lyrics, _ := lrcLyricsFor(s, mode)
	if r := []rune(lyrics); len(r) > lrcMaxRunes {
		lyrics = string(r[:lrcMaxRunes]) + "\n\n_(lirik dipotong, terlalu panjang)_"
	}
	return fmt.Sprintf("*Judul:* %s\n*Artis:* %s\n\n*Lirik:*\n%s",
		title, artist, lyrics)
}

func handleLrclibPlain(ctx context.Context, mctx *store.MessageContext) error {
	return handleLrclibMode(ctx, mctx, lrcPlain, ".lrc")
}

func handleLrclibSynced(ctx context.Context, mctx *store.MessageContext) error {
	return handleLrclibMode(ctx, mctx, lrcSynced, ".lrcs")
}

func handleLrclibMode(ctx context.Context, mctx *store.MessageContext, mode lrcMode, example string) error {
	if len(mctx.Args) == 0 {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Sertakan judul lagu. Contoh: %s shinunoga e-wa", example))
	}
	// `<cmd> <angka>` saat ada pending = pilih hasil (mode perintah menang).
	if len(mctx.Args) == 1 {
		if handled, msg := tryLrcNumberPick(mctx, mctx.Args[0], mode); handled {
			if msg != "" {
				return store.ReplyText(ctx, mctx, msg)
			}
			return nil
		}
	}
	query := strings.TrimSpace(mctx.RawArgs)
	if query == "" {
		query = strings.Join(mctx.Args, " ")
	}
	if err := store.ReplyText(ctx, mctx, "Mencari lirik, tunggu sebentar..."); err != nil {
		return err
	}
	go func() {
		bg := context.Background()
		songs, serr := scraper.SearchLrclib(bg, query, lrcSearchLimit)
		if serr != nil {
			_ = store.ReplyText(bg, mctx, fmt.Sprintf("Lirik tidak ditemukan untuk pencarian tersebut. (%v)", serr))
			return
		}
		lrcPendingSet(mctx.Chat.String(), mctx.Sender.String(), songs, query, mode)
		_ = store.ReplyText(bg, mctx, formatLrcSearchList(query, songs))
	}()
	return nil
}

// sendLrcSong mengirim lirik satu lagu pilihan sesuai mode.
func sendLrcSong(ctx context.Context, mctx *store.MessageContext, song scraper.LrclibSong, mode lrcMode) {
	if _, ok := lrcLyricsFor(&song, mode); !ok {
		artist := strings.TrimSpace(song.ArtistName)
		if artist == "" {
			artist = "-"
		}
		_ = store.ReplyText(ctx, mctx, fmt.Sprintf("Lagu *%s* oleh *%s* ditemukan, tetapi liriknya belum tersedia di database.", song.Title(), artist))
		return
	}
	_ = store.ReplyText(ctx, mctx, formatLrcLyrics(&song, mode))
}

// tryLrcNumberPick menafsir arg sebagai pilihan hasil pencarian.
// (ditangani, pesanError) — pesanError untuk angka di luar range.
func tryLrcNumberPick(mctx *store.MessageContext, numText string, mode lrcMode) (bool, string) {
	t := strings.TrimSpace(numText)
	if t == "" || strings.Contains(t, " ") {
		return false, ""
	}
	n, err := strconv.Atoi(t)
	if err != nil {
		return false, ""
	}
	pend := lrcPendingPeek(mctx.Chat.String(), mctx.Sender.String())
	if pend == nil {
		return false, ""
	}
	if n < 1 || n > len(pend.songs) {
		return true, fmt.Sprintf("Pilih angka 1-%d dari hasil \"%s\".", len(pend.songs), pend.query)
	}
	song := pend.songs[n-1]
	lrcPendingConsume(mctx.Chat.String(), mctx.Sender.String())
	ctx := context.Background()
	go sendLrcSong(ctx, mctx, song, mode)
	return true, ""
}

// lrcNewerThanSpotify true bila pending lirik lebih baru dari pending
// Spotify (aturan: pencarian terbaru yang menang bila keduanya aktif).
func lrcNewerThanSpotify(chat, sender string) bool {
	lp := lrcPendingPeek(chat, sender)
	sp := spPendingPeek(chat, sender)
	if lp == nil || sp == nil {
		return false
	}
	return lp.at.After(sp.exp.Add(-spPickTTL))
}

// CheckLrcPick mencegat pesan NON-command berupa angka polos saat ada hasil
// pencarian lirik pending dari pengirim yang sama. true = ditangani.
func CheckLrcPick(client *whatsmeow.Client, evt *events.Message, text string) bool {
	t := strings.TrimSpace(text)
	if t == "" || strings.Contains(t, " ") {
		return false
	}
	n, err := strconv.Atoi(t)
	if err != nil {
		return false
	}
	pend := lrcPendingPeek(evt.Info.Chat.String(), evt.Info.Sender.String())
	if pend == nil {
		return false
	}
	if n < 1 || n > len(pend.songs) {
		return false
	}
	// Pending lain yang lebih baru menang — biar angkanya ke sana.
	chat, sender := evt.Info.Chat.String(), evt.Info.Sender.String()
	if sp := spPendingPeek(chat, sender); sp != nil &&
		sp.exp.Add(-spPickTTL).After(pend.at) {
		return false
	}
	if shNewerThanLrc(chat, sender) {
		return false
	}
	song := pend.songs[n-1]
	mode := pend.mode // balas angka polos = ikuti mode perintah search
	lrcPendingConsume(evt.Info.Chat.String(), evt.Info.Sender.String())
	mctx := &store.MessageContext{Client: client, Chat: evt.Info.Chat, Sender: evt.Info.Sender, RawEvt: evt}
	go sendLrcSong(context.Background(), mctx, song, mode)
	return true
}
