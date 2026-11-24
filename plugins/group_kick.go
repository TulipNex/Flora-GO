package plugins

import (
	"context"

	"wabot-go/internal/store"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/types"
)

func init() {
	registerPlugin(RegisterKick)
}

func RegisterKick(r *Registry) {
	r.Register(Command{
		Name:        "kick",
		Description: "Keluarkan member dari grup (mention atau reply pesan target)",
		Category:    CategoryGrup,
		Handler:     handleKick,
	})
}

func kickHelp() string {
	return Help(HelpSpec{
		Title:   "Keluarkan anggota grup",
		Cmd:     "Kick",
		Formats: []string{".kick @user", ".kick <reply>"},
		Contoh:  ".kick @gibran",
		Params:  []string{"@user : tag target", "reply : balas pesan target"},
		Note:    "Bot harus admin dan target bukan owner bot.",
	})
}

func handleKick(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireGroupAdminGate(ctx, mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	target, err := resolveTargetJID(mctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, kickHelp())
	}
	if isBotTarget(mctx, target) {
		return store.ReplyText(ctx, mctx, "Tidak bisa mengeluarkan bot sendiri dari grup.")
	}
	if isOwnerTarget(ctx, mctx, target) {
		return store.ReplyText(ctx, mctx, "Tidak bisa mengeluarkan owner bot.")
	}

	infoBefore, _ := mctx.GroupInfo(ctx)

	_, err = mctx.Client.UpdateGroupParticipants(ctx, mctx.Chat, []types.JID{target}, whatsmeow.ParticipantChangeRemove)
	if err != nil {
		return store.ReplyText(ctx, mctx, Fail("Gagal mengeluarkan user", err))
	}
	return replySingleMention(ctx, mctx, infoBefore, target, " telah dikeluarkan dari grup.")
}
