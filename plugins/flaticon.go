package plugins

import (
	"context"
	"fmt"
	"strings"

	"wabot-go/internal/external"
	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterFlaticon)
}

// RegisterFlaticon mendaftarkan command `.flaticon` (alias `.fi`) kategori
// Owner: tampilkan daftar link CDN semua ukuran untuk satu ID ikon.
// Sync murni (rakit string, tanpa HTTP) — tercepat dan bebas blokir.
func RegisterFlaticon(r *Registry) {
	r.Register(Command{
		Name:        "flaticon",
		Aliases:     []string{"fi"},
		Description: "Link CDN ikon (.flaticon <id/link>)",
		Category:    CategoryOwner,
		Handler:     handleFlaticon,
	})
}

func handleFlaticon(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	if len(mctx.Args) == 0 {
		return store.ReplyText(ctx, mctx, "Format: .flaticon <ID/link ikon>\nContoh: .flaticon 4661257")
	}
	id := external.FlaticonExtractID(mctx.Args[0])
	if id == "" {
		return store.ReplyText(ctx, mctx, "ID tidak valid. Contoh: .flaticon 4661257")
	}
	urls, err := external.FlaticonAllSizes(id)
	if err != nil {
		return store.ReplyText(ctx, mctx, err.Error())
	}
	sizes := []int{16, 24, 32, 64, 128, 256, 512}
	var sb strings.Builder
	fmt.Fprintf(&sb, "CDN icon %s:\n", id)
	for i, u := range urls {
		fmt.Fprintf(&sb, "%d: %s\n", sizes[i], u)
	}
	sb.WriteString("Ikon Flaticon wajib atribusi author (non-premium).")
	return store.ReplyText(ctx, mctx, strings.TrimRight(sb.String(), "\n"))
}
