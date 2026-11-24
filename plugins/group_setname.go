package plugins

import (
	"context"
	"strings"

	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterSetName)
}

func RegisterSetName(r *Registry) {
	r.Register(Command{
		Name:        "setname",
		Description: "Ganti nama grup, contoh: .setname Nama Baru",
		Category:    CategoryGrup,
		Handler:     handleSetName,
	})
}

func setnameHelp() string {
	return Help(HelpSpec{
		Title:   "Ganti nama grup",
		Cmd:     "Setname",
		Formats: []string{".setname <nama baru>"},
		Contoh:  ".setname Grup Utama",
	})
}

func handleSetName(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireGroupAdminGate(ctx, mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	newName := strings.TrimSpace(strings.Join(mctx.Args, " "))
	if newName == "" {
		return store.ReplyText(ctx, mctx, setnameHelp())
	}

	if err := mctx.Client.SetGroupName(ctx, mctx.Chat, newName); err != nil {
		return store.ReplyText(ctx, mctx, Fail("Gagal ganti nama grup", err))
	}
	return store.ReplyText(ctx, mctx, "Nama grup diubah menjadi "+newName+".")
}
