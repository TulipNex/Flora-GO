package plugins

import (
	"context"

	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterUnbanGroup)
}

func RegisterUnbanGroup(r *Registry) {
	r.Register(Command{
		Name:        "unbangroup",
		Description: "Cabut ban grup ini",
		Category:    CategoryOwner,
		Handler:     handleUnbanGroup,
	})
}

func unbangroupUsage() string {
	return Help(HelpSpec{
		Title:   "Buka blokir grup",
		Cmd:     "Unbangroup",
		Formats: []string{".unbangroup"},
		Note:    "Gunakan di dalam grup yang ingin dibuka blokirnya.",
	})
}

func handleUnbanGroup(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	if !mctx.IsGroup() {
		return store.ReplyText(ctx, mctx, unbangroupUsage())
	}
	removed, err := store.GroupsFor(mctx.Client).UnbanGroup(ctx, mctx.Chat.String())
	if err != nil {
		return store.ReplyText(ctx, mctx, Fail("Gagal buka blokir grup", err))
	}
	if !removed {
		return store.ReplyText(ctx, mctx, "Grup ini tidak ada di daftar blokir.")
	}
	return store.ReplyText(ctx, mctx, "Grup ini dibuka blokirnya.")
}
