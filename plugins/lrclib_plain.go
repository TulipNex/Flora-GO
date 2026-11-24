package plugins

import (
	"context"

	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterLrclib)
}

func RegisterLrclib(r *Registry) {
	r.Register(Command{
		Name:        "lrclib",
		Aliases:     []string{"lrc"},
		Description: "Cari lirik plain (.lrclib <judul>)",
		Category:    CategoryInternet,
		Handler:     handleLrclibPlain,
	})
}

func handleLrclibPlain(ctx context.Context, mctx *store.MessageContext) error {
	return handleLrclibMode(ctx, mctx, lrcPlain, ".lrclib")
}
