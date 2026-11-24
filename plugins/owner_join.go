package plugins

import (
	"context"
	"fmt"
	"strings"

	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterJoin)
}

func RegisterJoin(r *Registry) {
	r.Register(Command{
		Name:        "join",
		Description: "Masuk ke grup via link invite (.join <link>)",
		Category:    CategoryOwner,
		Handler:     handleJoin,
	})
}

func joinUsage() string {
	return Help(HelpSpec{
		Title:   "Masuk grup",
		Cmd:     "Join",
		Formats: []string{".join <link grup>"},
		Contoh:  ".join https://chat.whatsapp.com/XXXX",
	})
}

func handleJoin(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	raw := strings.TrimSpace(strings.Join(mctx.Args, ""))
	if raw == "" {
		return store.ReplyText(ctx, mctx, joinUsage())
	}

	code := raw
	if m := inviteLinkRe.FindStringSubmatch(raw); len(m) == 2 {
		code = m[1]
	}

	jid, err := mctx.Client.JoinGroupWithLink(ctx, code)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal masuk grup: %v", err))
	}
	return store.ReplyText(ctx, mctx, fmt.Sprintf("Berhasil masuk grup: %s", jid.String()))
}
