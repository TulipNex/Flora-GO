package plugins

import (
	"context"
	"strings"

	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterSetDesc)
}

func RegisterSetDesc(r *Registry) {
	r.Register(Command{
		Name:        "setdesc",
		Description: "Ganti deskripsi grup, contoh: .setdesc Deskripsi baru",
		Category:    CategoryGrup,
		Handler:     handleSetDesc,
	})
}

func handleSetDesc(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireGroupAdminGate(ctx, mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	newDesc := strings.TrimSpace(strings.Join(mctx.Args, " "))
	if newDesc == "" {
		return store.ReplyText(ctx, mctx, "Silahkan masukkan deskripsi grup baru.")
	}

	if err := mctx.Client.SetGroupTopic(ctx, mctx.Chat, "", "", newDesc); err != nil {
		return store.ReplyText(ctx, mctx, Fail("Gagal ganti deskripsi grup", err))
	}
	return store.ReplyText(ctx, mctx, "Deskripsi grup diperbarui.")
}
