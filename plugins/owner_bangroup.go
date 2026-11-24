package plugins

import (
	"context"

	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterBanGroup)
}

func RegisterBanGroup(r *Registry) {
	r.Register(Command{
		Name:        "bangroup",
		Description: "Ban grup ini",
		Category:    CategoryOwner,
		Handler:     handleBanGroup,
	})
}

func bangroupUsage() string {
	return Help(HelpSpec{
		Title:   "Blokir grup",
		Cmd:     "Bangroup",
		Formats: []string{".bangroup"},
		Note:    "Gunakan di dalam grup yang ingin diblokir.",
	})
}

func handleBanGroup(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	if !mctx.IsGroup() {
		return store.ReplyText(ctx, mctx, bangroupUsage())
	}
	if err := store.GroupsFor(mctx.Client).BanGroup(ctx, mctx.Chat.String()); err != nil {
		return store.ReplyText(ctx, mctx, Fail("Gagal blokir grup", err))
	}
	return store.ReplyText(ctx, mctx, "Grup ini diblokir dari bot.")
}
