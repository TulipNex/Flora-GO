package plugins

import (
	"context"

	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterLrclibSynced)
}

func RegisterLrclibSynced(r *Registry) {
	r.Register(Command{
		Name:        "lrclibs",
		Aliases:     []string{"lrcs"},
		Description: "Cari lirik synced (.lrclibs <judul>)",
		Category:    CategoryInternet,
		Handler:     handleLrclibSynced,
	})
}

func handleLrclibSynced(ctx context.Context, mctx *store.MessageContext) error {
	return handleLrclibMode(ctx, mctx, lrcSynced, ".lrclibs")
}
