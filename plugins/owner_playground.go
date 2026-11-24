package plugins

import (
	"context"
	"strings"

	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterPlayground)
}

func RegisterPlayground(r *Registry) {
	r.Register(Command{
		Name:        "playground",
		Description: "Referensi script bubble playground (semua elemen)",
		Category:    CategoryOwner,
		Handler:     handlePlayground,
	})
}

func handlePlayground(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	part := ""
	if len(mctx.Args) > 0 {
		part = strings.ToLower(strings.TrimSpace(mctx.Args[0]))
	}
	return store.ReplyText(ctx, mctx, playUsage(part))
}
