package plugins

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"wabot-go/internal/store"
	"wabot-go/internal/timex"
)

func init() {
	registerPlugin(RegisterPing)
}

// pingStart dicatat sekali saat package dimuat (bot start) untuk hitung
// uptime di .pong.
var pingStart = time.Now()

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
			uptime := timex.FormatDuration(time.Since(pingStart).Round(time.Second))
			// Kolom ":" disejajarkan manual: kunci terpanjang "Latensi"/"Runtime"
			// menentukan jarak, jadi Uptime butuh satu spasi tambahan.
			text := Title("Pong") + "\n\n" + Code(
				"Latensi : "+fmt.Sprintf("%dms", latency),
				"Uptime  : "+uptime,
				"Runtime : Golang "+runtime.Version(),
			)
			return store.ReplyText(ctx, mctx, text)
		},
	})
}
