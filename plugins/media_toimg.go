package plugins

import (
	"context"

	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterToImg)
}

func RegisterToImg(r *Registry) {
	r.Register(Command{
		Name:        "toimg",
		Aliases:     []string{"toimage"},
		Description: "Ubah stiker jadi foto (reply stiker)",
		Category:    CategoryMedia,
		Handler:     handleToImg,
	})
}

func handleToImg(ctx context.Context, mctx *store.MessageContext) error {
	sticker, err := toQuotedSticker(mctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, Help(HelpSpec{
			Title:   "Stiker jadi foto",
			Cmd:     "toimg",
			Formats: []string{".toimg <reply stiker>"},
		}))
	}
	mediaBytes, err := mctx.Client.Download(ctx, sticker)
	if err != nil {
		return store.ReplyText(ctx, mctx, Fail("Gagal download stiker", err))
	}
	if !isWebPBytes(mediaBytes) {
		return store.ReplyText(ctx, mctx, "Stiker ini bukan format webp, tidak bisa dijadikan foto.")
	}
	pngBytes, err := runFfmpegConvert(ctx, mediaBytes, ".webp", "out.png", nil,
		"-frames:v", "1")
	if err != nil {
		return store.ReplyText(ctx, mctx, Fail("Gagal convert ke foto", err))
	}
	if err := sendDlBytesImage(ctx, mctx, pngBytes, ""); err != nil {
		return store.ReplyText(ctx, mctx, Fail("Gagal kirim foto", err))
	}
	return nil
}
