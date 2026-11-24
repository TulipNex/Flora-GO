package plugins

import (
	"context"

	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterBye)
}

func RegisterBye(r *Registry) {
	r.Register(Command{
		Name:        "bye",
		Description: "Perpisahan anggota keluar (on/off/set/simulate)",
		Category:    CategoryGrup,
		Handler:     handleByeCmd,
	})
}

func handleByeCmd(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireModGate(ctx, mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	sub, rest := greetingSubcommand(mctx)
	switch sub {
	case "on", "aktif", "enable":
		if err := store.GroupsFor(mctx.Client).SetBye(ctx, mctx.Chat.String(), true); err != nil {
			return store.ReplyText(ctx, mctx, Fail("Gagal menyalakan bye", err))
		}
		return store.ReplyText(ctx, mctx, welcomeHelp(false, true))
	case "off", "mati", "disable":
		if err := store.GroupsFor(mctx.Client).SetBye(ctx, mctx.Chat.String(), false); err != nil {
			return store.ReplyText(ctx, mctx, Fail("Gagal mematikan bye", err))
		}
		return store.ReplyText(ctx, mctx, welcomeHelp(false, false))
	case "set":
		return handleSetGreeting(ctx, mctx, rest, false)
	case "simulate", "simulasi", "test", "tes":
		return handleSimulateGreeting(ctx, mctx, false)
	default:
		gr, err := store.GroupsFor(mctx.Client).GetGreeting(ctx, mctx.Chat.String())
		if err != nil {
			return store.ReplyText(ctx, mctx, Fail("Gagal baca status bye", err))
		}
		return store.ReplyText(ctx, mctx, welcomeHelp(false, gr.ByeOn))
	}
}
