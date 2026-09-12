package native

import (
	"fmt"
	"runtime"
	"time"

	"wabot-go/internal/ai"
	"wabot-go/internal/config"
)

// startTime dicatat sekali saat package ini di-load (saat bot start),
// dipakai untuk hitung uptime — sama seperti startTime di plugins/info.go,
// tapi dicatat terpisah di sini supaya package internal/ai/tools/native
// tidak perlu bergantung ke internal detail package plugins (yang justru
// mengimpor internal/ai, jadi arahnya harus tetap satu jalur: plugins ->
// internal/ai, TIDAK boleh sebaliknya).
var startTime = time.Now()

func init() {
	ai.RegisterTool(ai.ToolDefinition{
		Name:        "system_info",
		Description: "Cek info teknis server tempat bot berjalan: uptime, versi Go, OS/arch. Gunakan kalau user bertanya tentang status/spesifikasi bot itu sendiri.",
		Parameters: ai.ParameterSchema{
			Type:       "OBJECT",
			Properties: map[string]ai.PropertySchema{},
		},
		Execute: func(actx *ai.AgentContext, args map[string]interface{}) (string, error) {
			uptime := formatDuration(time.Since(startTime).Round(time.Second))

			return fmt.Sprintf(
				"Nama bot: %s\nVersi: %s\nUptime: %s\nBahasa: Go %s\nOS/Arch: %s/%s\nLibrary: whatsmeow",
				config.BotName,
				config.BotVersion,
				uptime,
				runtime.Version(),
				runtime.GOOS,
				runtime.GOARCH,
			), nil
		},
	})
}

// formatDuration mengubah durasi jadi format "1h 23m 45s" — duplikasi kecil
// dari fungsi sama persis di plugins/info.go, sengaja tidak di-share supaya
// package ini tidak perlu impor plugins (lihat catatan startTime di atas).
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
