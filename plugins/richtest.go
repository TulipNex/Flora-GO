package plugins

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"wabot-go/internal/messaging"
	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterRichTest)
}

// RegisterRichTest mendaftarkan command EKSPERIMEN SEMENTARA ".richtest"
// (owner-only, kategori Owner = tersembunyi): mengirim teks rich dalam 4
// varian untuk menentukan empiris kombinasi mana yang tampil sebagai rich
// di klien (lihat internal/messaging/airich.go). DIHAPUS setelah vonis.
func RegisterRichTest(r *Registry) {
	r.Register(Command{
		Name:        "richtest",
		Description: "EKSPERIMEN SEMENTARA: uji render rich (akan dihapus)",
		Category:    CategoryOwner,
		Handler: func(ctx context.Context, mctx *store.MessageContext) error {
			if msg := requireOwner(mctx); msg != "" {
				return store.ReplyText(ctx, mctx, msg)
			}
			if len(mctx.Args) < 2 {
				return store.ReplyText(ctx, mctx,
					"Pakai: .richtest <1|2|3|4> <teks>\n1=mentah, 2=+viewonce, 3=+biz, 4=+keduanya")
			}
			v, err := strconv.Atoi(strings.TrimSpace(mctx.Args[0]))
			if err != nil || v < 1 || v > 4 {
				return store.ReplyText(ctx, mctx, "Varian harus 1-4.")
			}
			text := strings.TrimSpace(strings.Join(mctx.Args[1:], " "))
			id, err := messaging.NewRichTextBuilder(text).SetTitle("Uji Rich").SendRichText(ctx, mctx, v)
			if err != nil {
				return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal kirim V%d: %v", v, err))
			}
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Terkirim V%d (id %s). Lapor: rich / teks biasa?", v, id))
		},
	})
}
