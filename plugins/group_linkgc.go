package plugins

import (
	"context"

	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterLinkGC)
}

func RegisterLinkGC(r *Registry) {
	r.Register(Command{
		Name:        "linkgc",
		Description: "Ambil link invite grup saat ini",
		Category:    CategoryGrup,
		Handler:     handleLinkGC,
	})
}

func handleLinkGC(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireGroupAdminGate(ctx, mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}

	code, err := mctx.Client.GetGroupInviteLink(ctx, mctx.Chat, false)
	if err != nil {
		return store.ReplyText(ctx, mctx, Fail("Gagal ambil link invite", err))
	}
	return store.ReplyText(ctx, mctx, code)
}
