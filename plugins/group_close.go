package plugins

import (
	"context"

	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterClose)
}

func RegisterClose(r *Registry) {
	r.Register(Command{
		Name:        "close",
		Description: "Kunci grup, cuma admin yang boleh kirim pesan",
		Category:    CategoryGrup,
		Handler:     handleClose,
	})
}

func handleClose(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireGroupAdminGate(ctx, mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}

	if err := mctx.Client.SetGroupAnnounce(ctx, mctx.Chat, true); err != nil {
		return store.ReplyText(ctx, mctx, Fail("Gagal mengunci grup", err))
	}
	if err := store.GroupsFor(mctx.Client).SetClosed(ctx, mctx.Chat.String(), true); err != nil {
		return store.ReplyText(ctx, mctx, Fail("Grup dikunci, tapi catatan lokal gagal disimpan", err))
	}
	return store.ReplyText(ctx, mctx, "Grup ditutup, hanya admin yang dapat mengirim pesan.")
}
