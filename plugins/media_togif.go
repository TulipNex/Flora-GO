package plugins

import (
	"context"

	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterToGif)
}

func RegisterToGif(r *Registry) {
	r.Register(Command{
		Name:        "togif",
		Aliases:     []string{"tovid", "tovideo"},
		Description: "Ubah stiker bergerak jadi GIF (reply stiker animasi)",
		Category:    CategoryMedia,
		Handler:     handleToGif,
	})
}

func handleToGif(ctx context.Context, mctx *store.MessageContext) error {
	sticker, err := toQuotedSticker(mctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, Help(HelpSpec{
			Title:   "Stiker jadi GIF",
			Cmd:     "togif",
			Formats: []string{".togif <reply stiker video>"},
		}))
	}
	mediaBytes, err := mctx.Client.Download(ctx, sticker)
	if err != nil {
		return store.ReplyText(ctx, mctx, Fail("Gagal download stiker", err))
	}
	if !isWebPBytes(mediaBytes) {
		return store.ReplyText(ctx, mctx, "Stiker ini bukan format webp.")
	}
	if !isAnimatedWebP(mediaBytes) {
		return store.ReplyText(ctx, mctx, "Stiker ini tidak bergerak. Untuk stiker diam, pakai .toimg.")
	}
	mp4Bytes, err := convertAnimatedWebPToMP4(ctx, mediaBytes)
	if err != nil {
		return store.ReplyText(ctx, mctx, Fail("Gagal convert ke GIF", err))
	}
	if err := sendGifPlayback(ctx, mctx, mp4Bytes); err != nil {
		return store.ReplyText(ctx, mctx, Fail("Gagal kirim GIF", err))
	}
	return nil
}
