package plugins

import (
	"context"

	"wabot-go/internal/store"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/types"
)

func init() {
	registerPlugin(RegisterDemote)
}

func RegisterDemote(r *Registry) {
	r.Register(Command{
		Name:        "demote",
		Description: "Turunkan admin grup jadi member biasa (mention atau reply pesan target)",
		Category:    CategoryGrup,
		Handler:     handleDemote,
	})
}

func demoteHelp() string {
	return Help(HelpSpec{
		Title:   "Turunkan jadi member",
		Cmd:     "Demote",
		Formats: []string{".demote @user", ".demote <reply>"},
		Contoh:  ".demote @gibran",
		Params:  []string{"@user : tag target", "reply : balas pesan target"},
		Note:    "Bot harus admin dan target bukan bot sendiri.",
	})
}

func handleDemote(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireGroupAdminGate(ctx, mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	target, err := resolveTargetJID(mctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, demoteHelp())
	}
	if isBotTarget(mctx, target) {
		return store.ReplyText(ctx, mctx, "Tidak bisa mengubah status admin bot sendiri.")
	}

	infoBefore, _ := mctx.GroupInfo(ctx)

	_, err = mctx.Client.UpdateGroupParticipants(ctx, mctx.Chat, []types.JID{target}, whatsmeow.ParticipantChangeDemote)
	if err != nil {
		return store.ReplyText(ctx, mctx, Fail("Gagal menurunkan admin", err))
	}
	return replySingleMention(ctx, mctx, infoBefore, target, " diturunkan jadi member biasa.")
}
