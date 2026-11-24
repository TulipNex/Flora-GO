package plugins

import (
	"context"
	"strings"

	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterMode)
}

func RegisterMode(r *Registry) {
	r.Register(Command{
		Name:        "mode",
		Description: "Mode bot (self/public)",
		Category:    CategoryOwner,
		Handler:     handleMode,
	})
}

func modeUsage(currentMode string) string {
	status := "publik"
	if currentMode == store.BotModeSelf || currentMode == "private" {
		status = "private"
	}
	return Help(HelpSpec{
		Title:   "Mode bot",
		Status:  status,
		Cmd:     "Set",
		Formats: []string{".mode <state>"},
		Contoh:  ".mode private",
		Params: []string{
			"public  : akses umum",
			"private : khusus owner",
		},
	})
}

func handleMode(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	arg := strings.ToLower(strings.TrimSpace(strings.Join(mctx.Args, " ")))
	if arg == "" {
		mode, err := store.GroupsFor(mctx.Client).GetBotMode(ctx)
		if err != nil {
			return store.ReplyText(ctx, mctx, Fail("Gagal baca mode", err))
		}
		return store.ReplyText(ctx, mctx, modeUsage(mode))
	}
	// Normalisasi alias: "private" dipetakan ke BotModeSelf ("self")
	targetMode := arg
	if targetMode == "private" {
		targetMode = store.BotModeSelf
	}
	if err := store.GroupsFor(mctx.Client).SetBotMode(ctx, targetMode); err != nil {
		return store.ReplyText(ctx, mctx, modeUsage(""))
	}
	if targetMode == store.BotModeSelf {
		return store.ReplyText(ctx, mctx, "Mode private aktif.")
	}
	return store.ReplyText(ctx, mctx, "Mode public aktif.")
}
