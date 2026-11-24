package plugins

import (
	"context"

	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterBrat)
}

func RegisterBrat(r *Registry) {
	r.Register(Command{
		Name:        "brat",
		Aliases:     []string{"br"},
		Description: "Bikin stiker brat dari teks (.brat <teks> atau reply pesan)",
		Category:    CategoryMedia,
		Handler: func(ctx context.Context, mctx *store.MessageContext) error {
			return handleBrat(ctx, mctx, bratVariant{
				command: "brat",
				aliases: []string{"br"},
				kind:    "image",
				desc:    "Bikin stiker brat dari teks (.brat <teks> atau reply pesan)",
				title:   "Brat",
				isVideo: false,
			})
		},
	})
}
