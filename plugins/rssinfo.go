package plugins

import (
	"context"
	"fmt"
	"runtime"

	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterRSSInfo)
}

// RegisterRSSInfo mendaftarkan command `.rss` KHUSUS owner: detail memori
// proses bot (RSS + heap). Dipisah dari `.server` supaya tidak ada baris
// yang tampil dobel — pembaca /proc dipakai ulang dari serverinfo.go.
func RegisterRSSInfo(r *Registry) {
	r.Register(Command{
		Name:        "rss",
		Description: "Info memori proses bot",
		Category:    CategoryOwner,
		Handler:     handleRSSInfo,
	})
}

func handleRSSInfo(ctx context.Context, mctx *store.MessageContext) error {
	// Info proses tak boleh publik: gate eksplisit seperti .server dan .os.
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	msg := fmt.Sprintf(
		"🧠 *PROCESS MEMORY*\n"+
			"\n• RSS : %s\n"+
			"• Heap Used : %s\n"+
			"• Heap Total : %s\n"+
			"• External : %s",
		mbStr(processRSS()),
		mbStr(uint64(mem.HeapAlloc)),
		mbStr(uint64(mem.HeapSys)),
		mbStr(uint64(mem.OtherSys)+uint64(mem.GCSys)),
	)
	return store.ReplyText(ctx, mctx, msg)
}
