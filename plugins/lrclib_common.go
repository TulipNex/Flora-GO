package plugins

import (
	"context"
	"fmt"
	"strings"
	"time"

	"wabot-go/internal/external"
	"wabot-go/internal/store"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/types/events"
)

type lrcMode int

const (
	lrcPlain lrcMode = iota
	lrcSynced
)

const (
	lrcSearchLimit = 5
	lrcPickTTL     = 5 * time.Minute
	lrcMaxRunes    = 60000
)

type lrcPendingResult struct {
	songs []external.LrclibSong
	query string
	mode  lrcMode
}

var lrcPending = newPendingStore[*lrcPendingResult](lrcPickTTL)

func lrcPendingSet(chat, sender string, songs []external.LrclibSong, query string, mode lrcMode) {
	lrcPending.Set(chat, sender, &lrcPendingResult{songs: songs, query: query, mode: mode})
}

func lrcPendingPeek(chat, sender string) (*lrcPendingResult, time.Time) {
	v, at, _ := lrcPending.Peek(chat, sender)
	return v, at
}

func lrcPendingConsume(chat, sender string) *lrcPendingResult {
	v, _ := lrcPending.Consume(chat, sender)
	return v
}

func formatLrcSearchList(query string, songs []external.LrclibSong) string {
	var sb strings.Builder
	sb.WriteString(Title("Lirik") + "\n\n")
	for i, s := range songs {
		artist := strings.TrimSpace(s.ArtistName)
		if artist == "" {
			artist = "-"
		}
		fmt.Fprintf(&sb, "- *%d. %s* • %s\n", i+1, s.Title(), artist)
	}
	sb.WriteString("\n> Ketik nomor untuk memilih.")
	return sb.String()
}

func lrcLyricsFor(song *external.LrclibSong, mode lrcMode) (string, bool) {
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

func formatLrcLyrics(s *external.LrclibSong, mode lrcMode) string {
	title := s.Title()
	artist := strings.TrimSpace(s.ArtistName)
	if artist == "" {
		artist = "-"
	}
	lyrics, _ := lrcLyricsFor(s, mode)
	if r := []rune(lyrics); len(r) > lrcMaxRunes {
		lyrics = string(r[:lrcMaxRunes]) + "\n\n_(lirik dipotong, terlalu panjang)_"
	}
	return fmt.Sprintf("*%s — %s*\n\n%s", title, artist, lyrics)
}

func handleLrclibMode(ctx context.Context, mctx *store.MessageContext, mode lrcMode, cmdName string) error {
	if len(mctx.Args) == 0 {
		return store.ReplyText(ctx, mctx, Help(HelpSpec{
			Title:   "Cari lirik",
			Cmd:     "Lrclib",
			Formats: []string{cmdName + " <judul atau artis>"},
			Contoh:  cmdName + " fujii kaze",
		}))
	}
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
		songs, serr := external.SearchLrclib(bg, query, lrcSearchLimit)
		if serr != nil {
			_ = store.ReplyText(bg, mctx, fmt.Sprintf("Lirik tidak ditemukan untuk pencarian tersebut. (%v)", serr))
			return
		}
		lrcPendingSet(mctx.Chat.String(), mctx.Sender.String(), songs, query, mode)
		for _, alt := range pendingAltSenders(mctx) {
			lrcPendingSet(mctx.Chat.String(), alt, songs, query, mode)
		}
		_ = store.ReplyText(bg, mctx, formatLrcSearchList(query, songs))
	}()
	return nil
}

func sendLrcSong(ctx context.Context, mctx *store.MessageContext, song external.LrclibSong, mode lrcMode) {
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

func tryLrcNumberPick(mctx *store.MessageContext, numText string, mode lrcMode) (bool, string) {
	n, ok := parsePlainPickIndex(numText)
	if !ok {
		return false, ""
	}
	pend, _ := lrcPendingPeek(mctx.Chat.String(), mctx.Sender.String())
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

func lrcNewerThanSpotify(chat, sender string) bool {
	lp, lpAt := lrcPendingPeek(chat, sender)
	sp, spAt := spPendingPeek(chat, sender)
	if lp == nil || sp == nil {
		return false
	}
	return lpAt.After(spAt)
}

func CheckLrcPick(client *whatsmeow.Client, evt *events.Message, text string) bool {
	n, ok := parsePlainPickIndex(text)
	if !ok {
		return false
	}
	pend, pendAt := lrcPendingPeek(evt.Info.Chat.String(), evt.Info.Sender.String())
	if pend == nil {
		return false
	}
	if n < 1 || n > len(pend.songs) {
		return false
	}
	chat, sender := evt.Info.Chat.String(), evt.Info.Sender.String()
	if sp, spAt := spPendingPeek(chat, sender); sp != nil && spAt.After(pendAt) {
		return false
	}
	song := pend.songs[n-1]
	mode := pend.mode
	lrcPendingConsume(evt.Info.Chat.String(), evt.Info.Sender.String())
	mctx := &store.MessageContext{Client: client, Chat: evt.Info.Chat, Sender: evt.Info.Sender, RawEvt: evt}
	go sendLrcSong(context.Background(), mctx, song, mode)
	return true
}
