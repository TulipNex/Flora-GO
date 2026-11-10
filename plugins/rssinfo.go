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
	registerPlugin(RegisterRSSInfo)
}

// RegisterRSSInfo mendaftarkan command `.rss` (alias `.mem`, `.memory`, `.process`)
// KHUSUS owner: detail memori proses bot (Go runtime & RSS).
func RegisterRSSInfo(r *Registry) {
	r.Register(Command{
		Name:        "rss",
		Aliases:     []string{"mem", "memory", "process"},
		Description: "Info memori proses bot (Go runtime & RSS)",
		Category:    CategoryOwner,
		Handler:     handleRSSInfo,
	})
}

func handleRSSInfo(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	lastGC := "-"
	if mem.LastGC != 0 {
		lastGC = time.Unix(0, int64(mem.LastGC)).In(timex.Zone()).Format("15:04:05")
	}

	msg := fmt.Sprintf(
		"*PROCESS MEMORY*\n\n"+
			"```%-14s: %s\n"+
			"%-14s: %s\n"+
			"%-14s: %s\n"+
			"%-14s: %s\n"+
			"%-14s: %s\n"+
			"%-14s: %d\n"+
			"%-14s: %s\n"+
			"%-14s: %d\n"+
			"%-14s: %s\n"+
			"%-14s: %s\n"+
			"%-14s: %s```",
		"RSS", mbStr(processRSS()),
		"Heap Alloc", mbStr(uint64(mem.HeapAlloc)),
		"Heap Sys", mbStr(uint64(mem.HeapSys)),
		"Heap Idle", mbStr(uint64(mem.HeapIdle)),
		"Heap Released", mbStr(uint64(mem.HeapReleased)),
		"Heap Objects", mem.HeapObjects,
		"Stack Inuse", mbStr(uint64(mem.StackInuse)),
		"GC cycles", mem.NumGC,
		"Total Alloc", mbStr(uint64(mem.TotalAlloc)),
		"Runtime Sys", mbStr(uint64(mem.StackSys+mem.MSpanSys+mem.MCacheSys+mem.BuckHashSys+mem.GCSys+mem.OtherSys)),
		"Last GC", lastGC,
	)
	return store.ReplyText(ctx, mctx, msg)
}
