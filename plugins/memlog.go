package plugins

// memlog.go — command owner `.memlog`: riwayat memori bot (sampel berkala +
// satu baris per operasi berat) supaya kenaikan beban bisa ditelusuri, bukan
// ditebak. Perekamnya di internal/memlog, penyimpanannya di
// internal/store/memlogdb.go (data/memlog.db).
//
// Pembagian peran:
//   - .memlog now        → potret instan (heap, cgroup, proses anak, ops aktif)
//   - .memlog [24h]      → ringkasan + verdict tren
//   - .memlog day        → agregat per jam (24 baris)
//   - .memlog raw ...    → sampel per menit (60 baris per jam)
//   - .memlog top        → operasi paling boros alokasi
//   - .memlog spike      → lonjakan terbesar + operasi yang berjalan
//   - .memlog export     → ZIP berisi CSV samples+events (untuk Excel)
//   - .memlog heap / goroutine → profil pprof dikirim ke chat (tanpa buka port)

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"wabot-go/internal/config"
	"wabot-go/internal/logfmt"
	"wabot-go/internal/memlog"
	"wabot-go/internal/store"
	"wabot-go/internal/timex"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types"
	"google.golang.org/protobuf/proto"
)

const (
	memlogDefaultWindow = 24 * time.Hour
	memlogMaxRawRows    = 62
	memlogAlertEvery    = 24 * time.Hour
)

func init() {
	registerPlugin(RegisterMemLog)
}

// RegisterMemLog mendaftarkan command `.memlog` (owner-only).
func RegisterMemLog(r *Registry) {
	r.Register(Command{
		Name:        "memlog",
		Description: "Riwayat memori & operasi berat",
		Category:    CategoryOwner,
		Handler:     handleMemLog,
	})
}

func memlogUsage() string {
	return Block("Memlog",
		Opt("now", ".memlog now — potret memori saat ini + proses anak"),
		Opt("ringkas", ".memlog — 24 jam terakhir + verdict tren"),
		Opt("day", ".memlog day — agregat per jam"),
		Opt("raw", ".memlog raw 06 [12] — sampel per menit"),
		Opt("top", ".memlog top [jam] — operasi paling boros alokasi"),
		Opt("spike", ".memlog spike [n] — lonjakan terbesar"),
		Opt("ops", ".memlog ops — operasi yang sedang berjalan"),
		Opt("export", ".memlog export [24h|7d] — CSV samples+events (ZIP)"),
		Opt("profil", ".memlog heap | goroutine — kirim profil pprof"),
		Opt("prune", ".memlog prune [hari] — pangkas riwayat manual"),
	)
}

func handleMemLog(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	args := strings.Fields(mctx.RawArgs)
	if len(args) == 0 {
		return memlogSummary(ctx, mctx, memlogDefaultWindow)
	}

	switch strings.ToLower(args[0]) {
	case "now":
		return memlogNow(ctx, mctx)
	case "day":
		return memlogDay(ctx, mctx, memlogDefaultWindow)
	case "raw":
		return memlogRaw(ctx, mctx, args[1:])
	case "top":
		return memlogTop(ctx, mctx, args[1:])
	case "spike":
		return memlogSpike(ctx, mctx, args[1:])
	case "ops":
		return memlogOps(ctx, mctx)
	case "heap":
		return memlogSendProfile(ctx, mctx, true)
	case "goroutine", "goroutines":
		return memlogSendProfile(ctx, mctx, false)
	case "export":
		return memlogExport(ctx, mctx, args[1:])
	case "prune":
		return memlogPrune(ctx, mctx, args[1:])
	case "help", "?":
		return store.ReplyText(ctx, mctx, memlogUsage())
	default:
		return store.ReplyText(ctx, mctx, ErrMsg("Memlog", "subcommand tidak dikenal.\n"+memlogUsage()))
	}
}

// --- .memlog now ---

func memlogNow(ctx context.Context, mctx *store.MessageContext) error {
	s := memlog.SnapshotNow()
	peakRSS, peakHeap, peakChild := memlog.Peaks()
	queued, dropped, opsDropped := memlog.Stats()

	rows := []string{
		Row("RSS", mbStr(s.RSS)),
		Row("Heap Alloc", mbStr(s.HeapAlloc)),
		Row("Heap Sys", mbStr(s.HeapSys)),
		Row("Heap Idle", mbStr(s.HeapIdle)),
		Row("Heap Released", mbStr(s.HeapReleased)),
		Row("Heap Objects", fmt.Sprintf("%d", s.HeapObjects)),
		Row("Goroutines", fmt.Sprintf("%d", s.Goroutines)),
		Row("GC cycles", fmt.Sprintf("%d", s.GCCycles)),
		Row("Puncak RSS", mbStr(peakRSS)),
		Row("Puncak heap", mbStr(peakHeap)),
	}
	if s.CgroupKnown {
		rows = append(rows, Row("Cgroup", fmt.Sprintf("%s / %s", mbStr(s.CgroupUsed), mbStr(s.CgroupMax))))
	} else {
		rows = append(rows, Row("Cgroup", "tidak terdeteksi"))
	}
	if s.ChildRSS > 0 {
		rows = append(rows, Row("Anak proses", fmt.Sprintf("%s (%s)", mbStr(s.ChildRSS), dashStr(s.ChildTop))))
		if peakChild > 0 {
			rows = append(rows, Row("Puncak anak", mbStr(peakChild)))
		}
	} else {
		rows = append(rows, Row("Anak proses", "tidak ada"))
	}
	rows = append(rows, Row("Sampler", memlogStateText()))
	rows = append(rows, Row("Job tulis", fmt.Sprintf("%d (dibuang %d · ops drop %d)", queued, dropped, opsDropped)))

	if db := store.MemLog(); db != nil {
		if size, err := db.SizeBytes(ctx); err == nil {
			rows = append(rows, Row("DB memlog", humanBytes(size)))
		}
		if last, ok, err := db.LastSample(ctx); err == nil && ok {
			rows = append(rows, Row("Sampel terakhir", time.Unix(last.TS, 0).In(timex.Zone()).Format("15:04:05")))
		}
	}
	return store.ReplyText(ctx, mctx, Block("Memlog Sekarang", rows...))
}

func memlogStateText() string {
	if !memlog.Enabled() {
		return "nonaktif (MEMLOG_SAMPLE_SECONDS=0)"
	}
	return "aktif"
}

// --- .memlog (ringkasan 24 jam) ---

func memlogSummary(ctx context.Context, mctx *store.MessageContext, window time.Duration) error {
	now := time.Now()
	from := now.Add(-window).Unix()
	to := now.Unix() + 1

	db := store.MemLog()
	if db == nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Memlog", "database riwayat tidak tersedia."))
	}

	samples, err := db.SamplesBetween(ctx, from, to)
	if err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Memlog", fmt.Sprintf("gagal membaca sampel: %v", err)))
	}
	if len(samples) == 0 {
		return store.ReplyText(ctx, mctx, ErrMsg("Memlog",
			"belum ada sampel tersimpan. Pastikan sampler aktif (MEMLOG_SAMPLE_SECONDS>0) lalu tunggu 1-2 menit."))
	}

	var rssMin, rssMax, heapMax, objMax, childMax uint64
	for _, s := range samples {
		if rssMin == 0 || s.RSS < rssMin {
			rssMin = s.RSS
		}
		if s.RSS > rssMax {
			rssMax = s.RSS
		}
		if s.HeapAlloc > heapMax {
			heapMax = s.HeapAlloc
		}
		if s.HeapObjects > objMax {
			objMax = s.HeapObjects
		}
		if s.ChildRSS > childMax {
			childMax = s.ChildRSS
		}
	}

	rows := []string{
		Row("Sampel", fmt.Sprintf("%d (%.0f/jam)", len(samples), float64(len(samples))/window.Hours())),
		Row("RSS", fmt.Sprintf("%s – %s (kini %s)", mbStr(rssMin), mbStr(rssMax), mbStr(samples[len(samples)-1].RSS))),
		Row("Heap alloc", fmt.Sprintf("puncak %s · kini %s", mbStr(heapMax), mbStr(samples[len(samples)-1].HeapAlloc))),
		Row("Objek", fmt.Sprintf("puncak %d · kini %d", objMax, samples[len(samples)-1].HeapObjects)),
	}
	if childMax > 0 {
		rows = append(rows, Row("Anak proses", fmt.Sprintf("puncak %s", mbStr(childMax))))
	}

	trend, terr := db.Trend(ctx, now)
	if terr == nil && trend.NowN > 0 {
		rows = append(rows, Row("Tren", memlogTrendText(trend)))
	}

	if top, err := db.TopOps(ctx, from, to, 4); err == nil && len(top) > 0 {
		rows = append(rows, "", "*Top alokasi:*")
		for _, op := range top {
			rows = append(rows, fmt.Sprintf("• %s · %d× · avg %s · total %s",
				memlogOpName(op.Kind, op.Label), op.N,
				humanBytes(op.AllocAvg), humanBytes(op.AllocTotal)))
		}
	}
	if spikes, err := db.Spikes(ctx, from, to, 1); err == nil && len(spikes) > 0 {
		ev := spikes[0]
		rows = append(rows, Row("Spike terbesar",
			fmt.Sprintf("%s · heap %s · %s",
				time.Unix(ev.TS, 0).In(timex.Zone()).Format("02 Jan 15:04"),
				signedMB(ev.HeapD), dashStr(ev.Label))))
	}
	if terr != nil {
		rows = append(rows, Row("Tren", fmt.Sprintf("gagal dihitung: %v", terr)))
	}
	return store.ReplyText(ctx, mctx, Block(fmt.Sprintf("Memlog %s", memlogWindowLabel(window)), rows...))
}

// --- .memlog day ---

func memlogDay(ctx context.Context, mctx *store.MessageContext, window time.Duration) error {
	db := store.MemLog()
	if db == nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Memlog", "database riwayat tidak tersedia."))
	}
	now := time.Now()
	buckets, err := db.HourBuckets(ctx, now.Add(-window).Unix(), now.Unix()+1)
	if err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Memlog", fmt.Sprintf("gagal membaca agregat: %v", err)))
	}
	if len(buckets) == 0 {
		return store.ReplyText(ctx, mctx, ErrMsg("Memlog", "belum ada sampel tersimpan."))
	}

	lines := []string{Title(fmt.Sprintf("Memlog per Jam (%s)", memlogWindowLabel(window))), "```"}
	for _, h := range buckets {
		lines = append(lines, fmt.Sprintf("%s  %s–%s  heap %s  obj %s  anak %s",
			time.Unix(h.Hour, 0).In(timex.Zone()).Format("15:04"),
			mbShort(h.RSSMin), mbShort(h.RSSMax),
			mbShort(h.HeapMax), countShort(h.ObjMax), mbShort(h.ChildMax)))
	}
	lines = append(lines, "```")
	lines = append(lines, Row("Total jam", fmt.Sprintf("%d jam", len(buckets))))
	return store.ReplyText(ctx, mctx, strings.Join(lines, "\n"))
}

// --- .memlog raw ---

func memlogRaw(ctx context.Context, mctx *store.MessageContext, args []string) error {
	db := store.MemLog()
	if db == nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Memlog", "database riwayat tidak tersedia."))
	}
	from, to, err := memlogRawWindow(args)
	if err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Memlog", err.Error()))
	}
	samples, err := db.SamplesBetween(ctx, from.Unix(), to.Unix())
	if err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Memlog", fmt.Sprintf("gagal membaca sampel: %v", err)))
	}
	if len(samples) == 0 {
		return store.ReplyText(ctx, mctx, ErrMsg("Memlog", "tidak ada sampel pada rentang itu."))
	}

	truncated := 0
	if len(samples) > memlogMaxRawRows {
		truncated = len(samples) - memlogMaxRawRows
		samples = samples[len(samples)-memlogMaxRawRows:]
	}
	lines := []string{Title(fmt.Sprintf("Memlog Raw %s–%s",
		from.In(timex.Zone()).Format("15:04"), to.In(timex.Zone()).Format("15:04"))), "```"}
	for _, s := range samples {
		ops := ""
		if s.Ops != "" {
			ops = "  " + s.Ops
		}
		lines = append(lines, fmt.Sprintf("%s  rss %s  heap %s  obj %s%s",
			time.Unix(s.TS, 0).In(timex.Zone()).Format("15:04:05"),
			mbShort(s.RSS), mbShort(s.HeapAlloc), countShort(s.HeapObjects), ops))
	}
	lines = append(lines, "```")
	if truncated > 0 {
		lines = append(lines, Row("Catatan", fmt.Sprintf("%d baris lebih lama dipotong — pakai .memlog export untuk data penuh", truncated)))
	}
	return store.ReplyText(ctx, mctx, strings.Join(lines, "\n"))
}

func memlogRawWindow(args []string) (time.Time, time.Time, error) {
	now := timex.Now()
	if len(args) == 0 {
		return now.Add(-time.Hour), now.Add(time.Minute), nil
	}
	hour, err := strconv.Atoi(strings.TrimSpace(args[0]))
	if err != nil || hour < 0 || hour > 23 {
		return time.Time{}, time.Time{}, fmt.Errorf("jam harus 0-23, contoh: .memlog raw 06")
	}
	minute := 0
	hasMinute := false
	if len(args) > 1 {
		minute, err = strconv.Atoi(strings.TrimSpace(args[1]))
		if err != nil || minute < 0 || minute > 59 {
			return time.Time{}, time.Time{}, fmt.Errorf("menit harus 0-59, contoh: .memlog raw 06 12")
		}
		hasMinute = true
	}
	start := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, timex.Zone())
	if hasMinute {
		return start, start.Add(time.Minute), nil
	}
	return start, start.Add(time.Hour), nil
}

// --- .memlog top ---

func memlogTop(ctx context.Context, mctx *store.MessageContext, args []string) error {
	db := store.MemLog()
	if db == nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Memlog", "database riwayat tidak tersedia."))
	}
	hours := 24
	if len(args) > 0 {
		if n, err := strconv.Atoi(strings.TrimSpace(args[0])); err == nil && n > 0 && n <= 24*30 {
			hours = n
		}
	}
	now := time.Now()
	from := now.Add(-time.Duration(hours) * time.Hour)
	ops, err := db.TopOps(ctx, from.Unix(), now.Unix()+1, 10)
	if err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Memlog", fmt.Sprintf("gagal membaca event: %v", err)))
	}
	if len(ops) == 0 {
		return store.ReplyText(ctx, mctx, ErrMsg("Memlog",
			fmt.Sprintf("belum ada operasi tercatat dalam %d jam terakhir.", hours)))
	}

	lines := []string{Title(fmt.Sprintf("Top Operasi (%d jam)", hours))}
	for _, op := range ops {
		lines = append(lines, fmt.Sprintf("• %s — %d× · avg %s · total %s · rata-rata %s",
			memlogOpName(op.Kind, op.Label), op.N,
			humanBytes(op.AllocAvg), humanBytes(op.AllocTotal),
			time.Duration(op.DurAvgMS)*time.Millisecond))
	}
	lines = append(lines, "", "> alloc = byte yang dialokasikan operasi (kebal GC).")
	return store.ReplyText(ctx, mctx, strings.Join(lines, "\n"))
}

// --- .memlog spike ---

func memlogSpike(ctx context.Context, mctx *store.MessageContext, args []string) error {
	db := store.MemLog()
	if db == nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Memlog", "database riwayat tidak tersedia."))
	}
	limit := 5
	if len(args) > 0 {
		if n, err := strconv.Atoi(strings.TrimSpace(args[0])); err == nil && n > 0 && n <= 30 {
			limit = n
		}
	}
	now := time.Now()
	from := now.Add(-7 * 24 * time.Hour)
	events, err := db.Spikes(ctx, from.Unix(), now.Unix()+1, limit)
	if err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Memlog", fmt.Sprintf("gagal membaca lonjakan: %v", err)))
	}
	if len(events) == 0 {
		return store.ReplyText(ctx, mctx, ErrMsg("Memlog",
			"tidak ada lonjakan di atas ambang (heap +8 MB / RSS +60 MB / anak +50 MB) dalam 7 hari."))
	}

	lines := []string{Title("Lonjakan Terbesar (7 hari)")}
	for _, ev := range events {
		lines = append(lines, fmt.Sprintf("• %s — %s · %s",
			time.Unix(ev.TS, 0).In(timex.Zone()).Format("02 Jan 15:04:05"),
			dashStr(ev.Note), dashStr(ev.Label)))
	}
	return store.ReplyText(ctx, mctx, strings.Join(lines, "\n"))
}

// --- .memlog ops ---

func memlogOps(ctx context.Context, mctx *store.MessageContext) error {
	ops := memlog.ActiveOps()
	queued, dropped, opsDropped := memlog.Stats()

	rows := []string{Row("Sampler", memlogStateText())}
	if len(ops) == 0 {
		rows = append(rows, Row("Operasi aktif", "tidak ada"))
	} else {
		rows = append(rows, Row("Operasi aktif", fmt.Sprintf("%d", len(ops))))
		for _, o := range ops {
			rows = append(rows, fmt.Sprintf("• %s — berjalan %s", memlogOpName(o.Kind, o.Label),
				(time.Duration(o.DurMS)*time.Millisecond).Round(time.Millisecond)))
		}
	}
	rows = append(rows, Row("Job tulis", fmt.Sprintf("%d (dibuang %d)", queued, dropped)))
	rows = append(rows, Row("Operasi dibuang", fmt.Sprintf("%d", opsDropped)))
	return store.ReplyText(ctx, mctx, Block("Operasi Memlog", rows...))
}

// --- profil on-demand ---

func memlogSendProfile(ctx context.Context, mctx *store.MessageContext, heap bool) error {
	dir := store.MustBotTempDir()
	stamp := timex.Now().Format("20060102-150405")
	name := fmt.Sprintf("goroutine-%s.txt", stamp)
	mime := "text/plain"
	if heap {
		name = fmt.Sprintf("heap-%s.pb.gz", stamp)
		mime = "application/octet-stream"
	}
	path := filepath.Join(dir, name)

	var err error
	if heap {
		err = memlog.WriteHeapProfile(path)
	} else {
		err = memlog.WriteGoroutineProfile(path)
	}
	if err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Memlog", fmt.Sprintf("gagal membuat profil: %v", err)))
	}
	data, rerr := os.ReadFile(path)
	_ = os.Remove(path)
	if rerr != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Memlog", fmt.Sprintf("gagal membaca profil: %v", rerr)))
	}

	caption := "*Heap profile*"
	if !heap {
		caption = "*Goroutine dump*"
	}
	caption += fmt.Sprintf("\n%s · %s", name, humanBytes(int64(len(data))))
	if heap {
		caption += "\n\nBaca di laptop:\n> go tool pprof -sample_index=inuse_space " + name
	}
	if err := sendDocBytes(ctx, mctx.Client, mctx.Chat, name, mime, data, caption); err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Memlog", fmt.Sprintf("gagal mengirim profil: %v", err)))
	}
	return nil
}

// --- export CSV ---

func memlogExport(ctx context.Context, mctx *store.MessageContext, args []string) error {
	db := store.MemLog()
	if db == nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Memlog", "database riwayat tidak tersedia."))
	}
	window := memlogDefaultWindow
	if len(args) > 0 {
		parsed, err := parseMemlogWindow(args[0])
		if err != nil {
			return store.ReplyText(ctx, mctx, ErrMsg("Memlog", err.Error()))
		}
		window = parsed
	}
	now := time.Now()
	from, to := now.Add(-window).Unix(), now.Unix()+1

	samples, err := db.SamplesBetween(ctx, from, to)
	if err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Memlog", fmt.Sprintf("gagal membaca sampel: %v", err)))
	}
	if len(samples) == 0 {
		return store.ReplyText(ctx, mctx, ErrMsg("Memlog", "tidak ada data pada rentang itu."))
	}
	events, _ := db.EventsBetween(ctx, from, to, 20000)

	data, err := buildMemLogZip(samples, events)
	if err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Memlog", fmt.Sprintf("gagal merakit ZIP: %v", err)))
	}

	name := fmt.Sprintf("memlog-%s-%s.zip", timex.Now().Format("20060102-1504"), memlogWindowSlug(window))
	caption := fmt.Sprintf("*Export memlog %s*\n%d sampel · %d event · %s\nIsi: samples.csv, events.csv",
		memlogWindowLabel(window), len(samples), len(events), humanBytes(int64(len(data))))
	if err := sendDocBytes(ctx, mctx.Client, mctx.Chat, name, "application/zip", data, caption); err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Memlog", fmt.Sprintf("gagal mengirim export: %v", err)))
	}
	return nil
}

func parseMemlogWindow(raw string) (time.Duration, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "24h", "1d", "hari":
		return 24 * time.Hour, nil
	case "7d", "minggu", "seminggu":
		return 7 * 24 * time.Hour, nil
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 && d <= 30*24*time.Hour {
		return d, nil
	}
	return 0, fmt.Errorf("rentang tidak dikenal: %q (pakai 24h atau 7d)", raw)
}

func memlogWindowSlug(window time.Duration) string {
	switch {
	case window >= 6*24*time.Hour:
		return "7d"
	case window >= 23*time.Hour:
		return "24h"
	default:
		return fmt.Sprintf("%dh", int(window.Hours()))
	}
}

func memlogWindowLabel(window time.Duration) string {
	switch {
	case window >= 6*24*time.Hour:
		return "7 hari"
	case window >= 23*time.Hour:
		return "24 jam"
	default:
		return fmt.Sprintf("%d jam", int(window.Hours()))
	}
}

// buildMemLogZip merakit ZIP berisi samples.csv + events.csv.
func buildMemLogZip(samples []memlog.Sample, events []memlog.Event) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	if err := writeZipCSV(zw, "samples.csv", func(w *csv.Writer) error {
		if err := w.Write([]string{"ts", "waktu", "rss_mb", "heap_alloc_mb", "heap_sys_mb",
			"heap_idle_mb", "heap_released_mb", "heap_objects", "goroutines",
			"cgroup_used_mb", "cgroup_max_mb", "child_rss_mb", "child_top", "ops"}); err != nil {
			return err
		}
		for _, s := range samples {
			row := []string{
				strconv.FormatInt(s.TS, 10),
				time.Unix(s.TS, 0).In(timex.Zone()).Format("2006-01-02 15:04:05"),
				mbNum(s.RSS), mbNum(s.HeapAlloc), mbNum(s.HeapSys),
				mbNum(s.HeapIdle), mbNum(s.HeapReleased),
				strconv.FormatUint(s.HeapObjects, 10), strconv.FormatUint(s.Goroutines, 10),
				mbNum(s.CgroupUsed), mbNum(s.CgroupMax), mbNum(s.ChildRSS),
				s.ChildTop, s.Ops,
			}
			if err := w.Write(row); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}

	if err := writeZipCSV(zw, "events.csv", func(w *csv.Writer) error {
		if err := w.Write([]string{"ts", "waktu", "end", "kind", "label", "dur_ms",
			"alloc_mb", "heap_d_mb", "rss_d_mb", "ok", "note"}); err != nil {
			return err
		}
		for _, e := range events {
			ok := "0"
			if e.OK {
				ok = "1"
			}
			row := []string{
				strconv.FormatInt(e.TS, 10),
				time.Unix(e.TS, 0).In(timex.Zone()).Format("2006-01-02 15:04:05"),
				time.Unix(e.EndTS, 0).In(timex.Zone()).Format("15:04:05"),
				e.Kind, e.Label, strconv.FormatInt(e.DurMS, 10),
				mbNumDelta(e.Alloc), mbNumDelta(e.HeapD), mbNumDelta(e.RSSD),
				ok, e.Note,
			}
			if err := w.Write(row); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}

	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeZipCSV(zw *zip.Writer, name string, fill func(*csv.Writer) error) error {
	f, err := zw.Create(name)
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	if err := fill(w); err != nil {
		return err
	}
	w.Flush()
	return w.Error()
}

// --- .memlog prune ---

func memlogPrune(ctx context.Context, mctx *store.MessageContext, args []string) error {
	db := store.MemLog()
	if db == nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Memlog", "database riwayat tidak tersedia."))
	}
	days := 7
	if len(args) > 0 {
		if n, err := strconv.Atoi(strings.TrimSpace(args[0])); err == nil && n >= 1 && n <= 90 {
			days = n
		}
	}
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour).Unix()
	n, err := db.Prune(ctx, cutoff)
	if err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Memlog", fmt.Sprintf("gagal memangkas: %v", err)))
	}
	return store.ReplyText(ctx, mctx, Block("Memlog Prune",
		Row("Batas", fmt.Sprintf("lebih tua dari %d hari", days)),
		Row("Baris dihapus", fmt.Sprintf("%d", n))))
}

// --- pengingat tren (scheduler) ---

var (
	memlogClientMu  sync.Mutex
	memlogClient    *whatsmeow.Client
	memlogAlertLast time.Time
)

// InitMemLogScheduler memasang client pengirim peringatan tren. Dipanggil
// sekali di main.go; loop-nya memeriksa tren tiap jam dan mengirim DM ke
// owner maksimal sekali per 24 jam.
func InitMemLogScheduler(client *whatsmeow.Client) {
	memlogClientMu.Lock()
	memlogClient = client
	memlogClientMu.Unlock()
	go memlogTrendLoop()
}

func memlogTrendLoop() {
	// Jeda awal: beri waktu sampel terkumpul supaya tidak ada alarm palsu
	// tepat setelah restart (jendela 24 jam masih kosong).
	timer := time.NewTimer(10 * time.Minute)
	<-timer.C
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		memlogCheckTrend()
	}
}

func memlogCheckTrend() {
	if !memlog.Enabled() {
		return
	}
	db := store.MemLog()
	if db == nil {
		return
	}
	now := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	trend, err := db.Trend(ctx, now)
	var top []store.MemLogOp
	if err == nil {
		top, _ = db.TopOps(ctx, now.Add(-24*time.Hour).Unix(), now.Unix()+1, 5)
	}
	cancel()
	if err != nil || trend.PrevN < 12 || trend.NowN < 12 {
		return
	}
	reason, growing := memlogGrowthReason(trend)
	if !growing {
		return
	}
	if time.Since(memlogAlertLast) < memlogAlertEvery {
		return
	}
	memlogAlertLast = time.Now()

	lines := []string{
		"*Tren memori naik*",
		reason,
		fmt.Sprintf("Median heap 24j: %s → %s", mbStr(trend.PrevHeap), mbStr(trend.NowHeap)),
		fmt.Sprintf("Objek: %d → %d", trend.PrevObj, trend.NowObj),
		fmt.Sprintf("RSS: %s → %s", mbStr(trend.PrevRSS), mbStr(trend.NowRSS)),
	}
	if len(top) > 0 {
		lines = append(lines, "", "*Penyumbang alokasi:*")
		for _, op := range top {
			lines = append(lines, fmt.Sprintf("• %s — %d× · total %s",
				memlogOpName(op.Kind, op.Label), op.N, humanBytes(op.AllocTotal)))
		}
	}
	lines = append(lines, "", "Cek `.memlog` lalu `.memlog heap` untuk profil.")
	memlogSendOwners(strings.Join(lines, "\n"))
}

// memlogGrowthReason menilai tren: naik = median heap DAN objek sama-sama
// tumbuh melewati ambang (persentase + absolut). Ambang objek penting karena
// kebocoran map selalu menaikkan jumlah objek, sedangkan retensi heap tidak.
func memlogGrowthReason(t store.MemLogTrend) (string, bool) {
	if t.PrevN == 0 || t.NowN == 0 {
		return "", false
	}
	heapUp := t.NowHeap > t.PrevHeap+15<<20 && float64(t.NowHeap) > float64(t.PrevHeap)*1.25
	objUp := t.NowObj > t.PrevObj+2000 && float64(t.NowObj) > float64(t.PrevObj)*1.2
	switch {
	case heapUp && objUp:
		return "Heap dan jumlah objek naik bersamaan — indikasi kebocoran (map/history tak dibatasi).", true
	case heapUp:
		return "Heap naik, objek stabil — indikasi retensi/puncak operasi besar, bukan kebocoran.", true
	case objUp:
		return "Jumlah objek naik, heap stabil — indikasi objek kecil menumpuk (map/cache).", true
	}
	return "", false
}

func memlogSendOwners(text string) {
	memlogClientMu.Lock()
	client := memlogClient
	memlogClientMu.Unlock()
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, owner := range config.OwnerNumbers {
		owner = strings.TrimSpace(owner)
		if owner == "" {
			continue
		}
		jid, err := types.ParseJID(owner + "@" + types.DefaultUserServer)
		if err != nil || jid.IsEmpty() {
			continue
		}
		_, err = client.SendMessage(ctx, jid, &waE2E.Message{
			ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String(text)},
		})
		if err != nil {
			logfmt.Warn("memlog: gagal kirim peringatan tren ke %s: %v\n", owner, err)
		}
	}
}

// --- helper format ---

func memlogOpName(kind, label string) string {
	if strings.TrimSpace(label) == "" {
		return kind
	}
	return kind + "/" + label
}

// mbShort memformat byte jadi "60.9MB" (tanpa spasi) untuk tabel sempit.
func mbShort(b uint64) string {
	return fmt.Sprintf("%.1fMB", float64(b)/(1<<20))
}

func countShort(n uint64) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return strconv.FormatUint(n, 10)
}

func signedMB(b int64) string {
	return fmt.Sprintf("%+.1fMB", float64(b)/(1<<20))
}

func mbNum(b uint64) string {
	return strconv.FormatFloat(float64(b)/(1<<20), 'f', 2, 64)
}

func mbNumDelta(b int64) string {
	return strconv.FormatFloat(float64(b)/(1<<20), 'f', 2, 64)
}

func memlogTrendText(t store.MemLogTrend) string {
	if t.PrevN == 0 {
		return "belum ada pembanding (data < 48 jam)"
	}
	heapDelta := int64(t.NowHeap) - int64(t.PrevHeap)
	objDelta := int64(t.NowObj) - int64(t.PrevObj)
	verdict := "STABIL"
	if _, growing := memlogGrowthReason(t); growing {
		verdict = "NAIK"
	} else if heapDelta < -(15 << 20) {
		verdict = "TURUN"
	}
	return fmt.Sprintf("%s (heap %s, objek %+d)", verdict, signedMB(heapDelta), objDelta)
}
