package plugins

import (
	"context"
	"fmt"
	"strings"

	"wabot-go/internal/store"

	"github.com/polymorfa/hypermeow/types"
)

func init() {
	registerPlugin(RegisterBanList)
}

func RegisterBanList(r *Registry) {
	r.Register(Command{
		Name:        "banlist",
		Description: "Daftar user di-ban",
		Category:    CategoryOwner,
		Handler:     handleBanList,
	})
}

func handleBanList(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	list, err := store.GroupsFor(mctx.Client).ListBannedUsers(ctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, Fail("Gagal baca daftar blokir", err))
	}
	if len(list) == 0 {
		return store.ReplyText(ctx, mctx, "Daftar blokir user kosong.")
	}
	// Tiap baris memakai "@<User>" yang berpasangan dengan MentionedJID
	// (metode yang sama dengan tagall) supaya jadi tag beneran. Baris yang
	// JID-nya tidak bisa di-parse ditulis polos sebagai fallback — daftar
	// tetap tampil lengkap walau satu baris datanya rusak.
	var sb strings.Builder
	sb.WriteString(Title("Daftar blokir user") + "\n\n")
	mentions := make([]types.JID, 0, len(list))
	for _, raw := range list {
		bare, err := types.ParseJID(raw)
		if err != nil || bare.IsEmpty() {
			sb.WriteString(fmt.Sprintf("- %s\n", raw))
			continue
		}
		bare = bare.ToNonAD()
		sb.WriteString(fmt.Sprintf("- @%s\n", bare.User))
		mentions = append(mentions, bare)
	}
	text := strings.TrimRight(sb.String(), "\n")
	if len(mentions) == 0 {
		return store.ReplyText(ctx, mctx, text)
	}
	return store.ReplyTextWithMentions(ctx, mctx, text, mentions)
}
