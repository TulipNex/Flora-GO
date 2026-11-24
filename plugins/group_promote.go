package plugins

import (
	"context"

	"wabot-go/internal/store"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/types"
)

func init() {
	registerPlugin(RegisterPromote)
}

func RegisterPromote(r *Registry) {
	r.Register(Command{
		Name:        "promote",
		Description: "Jadikan member sebagai admin grup (mention atau reply pesan target)",
		Category:    CategoryGrup,
		Handler:     handlePromote,
	})
}

func promoteHelp() string {
	return Help(HelpSpec{
		Title:   "Angkat jadi admin",
		Cmd:     "Promote",
		Formats: []string{".promote @user", ".promote <reply>"},
		Contoh:  ".promote @gibran",
		Params:  []string{"@user : tag target", "reply : balas pesan target"},
		Note:    "Bot harus admin dan target bukan bot sendiri.",
	})
}

func handlePromote(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireGroupAdminGate(ctx, mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	target, err := resolveTargetJID(mctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, promoteHelp())
	}
	if isBotTarget(mctx, target) {
		return store.ReplyText(ctx, mctx, "Tidak bisa mengubah status admin bot sendiri.")
	}

	infoBefore, _ := mctx.GroupInfo(ctx)

	_, err = mctx.Client.UpdateGroupParticipants(ctx, mctx.Chat, []types.JID{target}, whatsmeow.ParticipantChangePromote)
	if err != nil {
		return store.ReplyText(ctx, mctx, Fail("Gagal mengangkat admin", err))
	}
	return replySingleMention(ctx, mctx, infoBefore, target, " sekarang jadi admin grup.")
}
