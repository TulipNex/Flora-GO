package plugins

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"wabot-go/internal/config"
	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterInfo)
}

// startTime dicatat sekali saat package ini di-load (yaitu saat bot start).
var startTime = time.Now()

// RegisterInfo mendaftarkan command ".info" -> menampilkan identitas bot,
// uptime, versi Go, library, dan jumlah command yang terdaftar. Berguna
// untuk cek cepat status bot tanpa perlu buka panel.
func RegisterInfo(r *Registry) {
	r.Register(Command{
		Name:        "info",
		Description: "Menampilkan info & uptime bot",
		Handler: func(ctx context.Context, mctx *store.MessageContext) error {
			uptime := time.Since(startTime).Round(time.Second)

			msg := fmt.Sprintf(
				"🤖 *Info Bot*\n\n"+
					"📛 Nama: %s\n"+
					"🔖 Versi: %s\n"+
					"📝 %s\n\n"+
					"⏱️ Uptime: %s\n"+
					"🧩 Command terdaftar: %d\n"+
					"🐹 Bahasa: Go (Golang) %s\n"+
					"📦 Library: whatsmeow\n"+
					"💻 OS/Arch: %s/%s",
				config.BotName,
				config.BotVersion,
				config.BotDescription,
				formatDuration(uptime),
				len(r.All()),
				runtime.Version(),
				runtime.GOOS,
				runtime.GOARCH,
			)

			return store.ReplyText(ctx, mctx, msg)
		},
	})
}

// formatDuration mengubah durasi jadi format "1h 23m 45s" yang lebih enak dibaca
// dibanding format default Go (misal "1h23m45.123s").
func formatDuration(d time.Duration) string {
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60

	if h > 0 {
		return fmt.Sprintf("%dh %dm %ds", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}
