package plugins

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"time"

	"wabot-go/internal/store"
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
			uptime := formatDuration(time.Since(pingStart).Round(time.Second))
			goVer := strings.TrimPrefix(runtime.Version(), "go")
			text := Block("Pong",
				Row("Status", "Aktif"),
				Row("Latensi", fmt.Sprintf("%dms", latency)),
				Row("Uptime", uptime),
				Row("Runtime", "Golang "+goVer),
			)
			return store.ReplyText(ctx, mctx, text)
		},
	})
}
