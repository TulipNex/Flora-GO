package plugins

import (
	"context"
	"fmt"

	"wabot-go/internal/store"

	"github.com/polymorfa/hypermeow/types"
)

func init() {
	registerPlugin(RegisterUnban)
}

func RegisterUnban(r *Registry) {
	r.Register(Command{
		Name:        "unban",
		Description: "Cabut ban user",
		Category:    CategoryOwner,
		Handler:     handleUnban,
	})
}

func unbanUsage() string {
	return Help(HelpSpec{
		Title: "Buka blokir user",
		Cmd:   "Unban",
		Formats: []string{
			".unban @user",
			".unban <nomor>",
		},
		Contoh: ".unban @gibran",
	})
}

func handleUnban(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	target, err := resolveBanTarget(mctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, unbanUsage())
	}
	removed, err := store.GroupsFor(mctx.Client).UnbanUser(ctx, target)
	if err != nil {
		return store.ReplyText(ctx, mctx, Fail("Gagal buka blokir user", err))
	}
	// Sama seperti .ban: "@..." harus berpasangan dengan MentionedJID supaya
	// jadi tag beneran, bukan teks polos.
	if !removed {
		return store.ReplyTextWithMentions(ctx, mctx,
			fmt.Sprintf("@%s tidak ada di daftar blokir.", target.User),
			[]types.JID{target})
	}
	return store.ReplyTextWithMentions(ctx, mctx,
		fmt.Sprintf("@%s dibuka blokirnya.", target.User),
		[]types.JID{target})
}
