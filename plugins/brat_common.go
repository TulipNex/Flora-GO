package plugins

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"wabot-go/internal/config"
	"wabot-go/internal/external"
	"wabot-go/internal/logfmt"
	"wabot-go/internal/messaging"
	"wabot-go/internal/store"
)

const (
	bratMaxText = 200
	bratTimeout = 60 * time.Second
)

type bratVariant struct {
	command string
	aliases []string
	kind    string
	desc    string
	title   string
	isVideo bool
}

func handleBrat(ctx context.Context, mctx *store.MessageContext, v bratVariant) error {
	text := bratInputText(mctx)
	if text == "" {
		return store.ReplyText(ctx, mctx, Help(HelpSpec{
			Title:   "Stiker teks brat",
			Cmd:     "Brat",
			Formats: []string{fmt.Sprintf(".%s <teks/reply>", v.command)},
			Contoh:  fmt.Sprintf(".%s apa kabar", v.command),
		}))
	}
	if utf8.RuneCountInString(text) > bratMaxText {
		return store.ReplyText(ctx, mctx, "Teks terlalu panjang, maksimal 200 karakter.")
	}

	go func() {
		bg := context.Background()
		if err := bratGenerate(bg, mctx, text, v); err != nil {
			logfmt.Warn("brat(%s): %v\n", v.kind, err)
			_ = store.ReplyText(bg, mctx, Fail("Gagal bikin brat", err))
		}
	}()
	return nil
}

func bratInputText(mctx *store.MessageContext) string {
	if text := strings.TrimSpace(mctx.RawArgs); text != "" {
		return text
	}

	quoted := store.UnwrapMessage(store.ExtractQuotedMessage(mctx.RawEvt))
	if quoted == nil {
		return ""
	}
	candidates := []string{
		quoted.GetConversation(),
		quoted.GetExtendedTextMessage().GetText(),
		quoted.GetImageMessage().GetCaption(),
		quoted.GetVideoMessage().GetCaption(),
		quoted.GetDocumentMessage().GetCaption(),
	}
	for _, c := range candidates {
		if t := strings.TrimSpace(c); t != "" {
			return t
		}
	}
	return ""
}

func bratGenerate(ctx context.Context, mctx *store.MessageContext, text string, v bratVariant) error {
	ctx, cancel := context.WithTimeout(ctx, bratTimeout)
	defer cancel()

	media, err := fetchBratMedia(ctx, text, v.kind)
	if err != nil {
		return err
	}
	if err := messaging.CheckMediaBytes(media); err != nil {
		return err
	}

	webp, err := convertToStickerWebp(ctx, media, v.isVideo)
	if err != nil {
		return fmt.Errorf("gagal convert ke stiker: %v", err)
	}
	author := config.ResolveStickerAuthor(mctx.SenderPushName())
	webp, err = store.WriteStickerExif(webp, config.ResolveStickerPackname(), author)
	if err != nil {
		return fmt.Errorf("gagal set metadata stiker: %v", err)
	}
	return sendStickerWebp(ctx, mctx, webp, "brat", v.kind)
}

func fetchBratMedia(ctx context.Context, text, kind string) ([]byte, error) {
	return external.FloraFetchBrat(ctx, text, kind)
}
