package plugins

import (
	"context"
	"fmt"
	"time"

	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterPing)
}

// RegisterPing mendaftarkan command ".ping" -> balas "Pong!" beserta latensinya
// Latensi dihitung dari selisih waktu pesan dikirim (mctx.RawEvt.Info.Timestamp)
// sampai bot selesai memproses dan siap membalas.
// Ini contoh plugin paling sederhana, jadikan template untuk plugin baru.
func RegisterPing(r *Registry) {
	r.Register(Command{
		Name:        "ping",
		Description: "Cek apakah bot masih hidup",
		Category:    CategoryUmum,
		Handler: func(ctx context.Context, mctx *store.MessageContext) error {
			latency := time.Since(mctx.RawEvt.Info.Timestamp).Milliseconds()
			text := fmt.Sprintf("🏓 Pong! Bot aktif.\n⏱️ Latensi: %dms", latency)
			return store.ReplyText(ctx, mctx, text)
		},
	})
}
