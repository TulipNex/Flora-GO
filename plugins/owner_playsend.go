package plugins

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterPlaySend)
}

func RegisterPlaySend(r *Registry) {
	r.Register(Command{
		Name:        "send",
		Description: "Kirim satu bubble dari script #playground (reply script)",
		Category:    CategoryOwner,
		Handler:     handlePlaySend,
	})
}

func handlePlaySend(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	if mctx.RawEvt == nil {
		return store.ReplyText(ctx, mctx, playSendHint())
	}

	quoted := store.UnwrapMessage(store.ExtractQuotedMessage(mctx.RawEvt))
	if quoted == nil {
		return store.ReplyText(ctx, mctx, playSendHint())
	}
	script := store.MessageText(quoted)
	if strings.TrimSpace(script) == "" {
		return store.ReplyText(ctx, mctx, ErrMsg("Playground", "pesan yang di-reply bukan teks script; kirim script sebagai teks biasa dulu."))
	}
	if n := utf8.RuneCountInString(script); n > playMaxScriptRunes {
		return store.ReplyText(ctx, mctx, ErrMsg("Playground", fmt.Sprintf("script %d rune, maks %d.", n, playMaxScriptRunes)))
	}

	spec, errs := playParse(script)
	if len(errs) == 0 {
		errs = playValidate(&spec)
	}
	if len(errs) > 0 {
		_ = store.SendReaction(ctx, mctx, "❌")
		return store.ReplyText(ctx, mctx, playErrorText(errs))
	}

	msg, extra, err := playRender(ctx, mctx.Client, spec)
	if err != nil {
		_ = store.SendReaction(ctx, mctx, "❌")
		return store.ReplyText(ctx, mctx, ErrMsg("Playground", "gagal menyiapkan bubble: "+err.Error()))
	}

	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, msg, extra)
	if err != nil {
		_ = store.SendReaction(ctx, mctx, "❌")
		return store.ReplyText(ctx, mctx, ErrMsg("Playground", fmt.Sprintf("gagal kirim bubble: %v", err)))
	}

	_ = store.SendReaction(ctx, mctx, "✅")
	label := playLabel(spec)
	store.LogOutgoing(mctx, "playground", label)
	store.TrackBotMessageID(resp.ID, "playground "+label)
	return nil
}
