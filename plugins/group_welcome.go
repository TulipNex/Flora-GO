package plugins

import (
	"context"

	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterWelcome)
}

func RegisterWelcome(r *Registry) {
	r.Register(Command{
		Name:        "welcome",
		Description: "Sambutan anggota baru (on/off/set/simulate)",
		Category:    CategoryGrup,
		Handler:     handleWelcomeCmd,
	})
}

func handleWelcomeCmd(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireModGate(ctx, mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	sub, rest := greetingSubcommand(mctx)
	switch sub {
	case "on", "aktif", "enable":
		if err := store.GroupsFor(mctx.Client).SetWelcome(ctx, mctx.Chat.String(), true); err != nil {
			return store.ReplyText(ctx, mctx, Fail("Gagal menyalakan welcome", err))
		}
		return store.ReplyText(ctx, mctx, welcomeHelp(true, true))
	case "off", "mati", "disable":
		if err := store.GroupsFor(mctx.Client).SetWelcome(ctx, mctx.Chat.String(), false); err != nil {
			return store.ReplyText(ctx, mctx, Fail("Gagal mematikan welcome", err))
		}
		return store.ReplyText(ctx, mctx, welcomeHelp(true, false))
	case "set":
		return handleSetGreeting(ctx, mctx, rest, true)
	case "simulate", "simulasi", "test", "tes":
		return handleSimulateGreeting(ctx, mctx, true)
	default:
		gr, err := store.GroupsFor(mctx.Client).GetGreeting(ctx, mctx.Chat.String())
		if err != nil {
			return store.ReplyText(ctx, mctx, Fail("Gagal baca status welcome", err))
		}
		return store.ReplyText(ctx, mctx, welcomeHelp(true, gr.WelcomeOn))
	}
}
