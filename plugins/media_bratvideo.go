package plugins

import (
	"context"

	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterBratVideo)
}

func RegisterBratVideo(r *Registry) {
	r.Register(Command{
		Name:        "bratvideo",
		Aliases:     []string{"bratvid"},
		Description: "Bikin stiker brat video (.bratvideo <teks> atau reply pesan)",
		Category:    CategoryMedia,
		Handler: func(ctx context.Context, mctx *store.MessageContext) error {
			return handleBrat(ctx, mctx, bratVariant{
				command: "bratvideo",
				aliases: []string{"bratvid"},
				kind:    "video",
				desc:    "Bikin stiker brat video (.bratvideo <teks> atau reply pesan)",
				title:   "Brat Video",
				isVideo: true,
			})
		},
	})
}
