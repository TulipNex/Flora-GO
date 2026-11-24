package plugins

import (
	"context"

	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterOpen)
}

func RegisterOpen(r *Registry) {
	r.Register(Command{
		Name:        "open",
		Description: "Buka grup, semua member boleh kirim pesan",
		Category:    CategoryGrup,
		Handler:     handleOpen,
	})
}

func handleOpen(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireGroupAdminGate(ctx, mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}

	if err := mctx.Client.SetGroupAnnounce(ctx, mctx.Chat, false); err != nil {
		return store.ReplyText(ctx, mctx, Fail("Gagal membuka grup", err))
	}
	if err := store.GroupsFor(mctx.Client).SetClosed(ctx, mctx.Chat.String(), false); err != nil {
		return store.ReplyText(ctx, mctx, Fail("Grup dibuka, tapi catatan lokal gagal disimpan", err))
	}
	return store.ReplyText(ctx, mctx, "Grup dibuka, semua anggota grup dapat mengirim pesan.")
}
