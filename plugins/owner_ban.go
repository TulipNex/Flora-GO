package plugins

import (
	"context"
	"fmt"

	"wabot-go/internal/store"

	"github.com/polymorfa/hypermeow/types"
)

func init() {
	registerPlugin(RegisterBan)
}

func RegisterBan(r *Registry) {
	r.Register(Command{
		Name:        "ban",
		Description: "Ban user",
		Category:    CategoryOwner,
		Handler:     handleBan,
	})
}

func banUsage() string {
	return Help(HelpSpec{
		Title: "Blokir user",
		Cmd:   "Ban",
		Formats: []string{
			".ban @user",
			".ban <nomor>",
		},
		Contoh: ".ban @gibran",
		Note:   "Owner tidak bisa di-ban.",
	})
}

func handleBan(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	target, err := resolveBanTarget(mctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, banUsage())
	}
	// Owner tidak boleh mem-ban dirinya sendiri — kalau sampai terjadi,
	// satu-satunya yang bisa unban (owner) ikut terkunci di mode self.
	if isJIDOwner(target) {
		return store.ReplyText(ctx, mctx, "Owner tidak bisa di-ban.")
	}
	if err := store.GroupsFor(mctx.Client).BanUser(ctx, target); err != nil {
		return store.ReplyText(ctx, mctx, Fail("Gagal blokir user", err))
	}
	return store.ReplyTextWithMentions(ctx, mctx,
		fmt.Sprintf("@%s diblokir dari bot.", target.User),
		[]types.JID{target})
}
