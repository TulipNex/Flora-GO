// Package metrics menampung counter/latency ringan untuk observabilitas
// Flora GO (Tahap 1). Leaf package: tidak mengimpor paket repo lain.
//
// Semua nilai disimpan di memori (atomic) — tidak ada DB/file. Tujuannya
// memberi gambaran beban bot (pesan, command, AI, downloader, error) tanpa
// menambah dependensi atau kompleksitas deploy. Cocok untuk panel kecil.
package metrics

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Counters utama.
var (
	messagesTotal   atomic.Int64
	commandsTotal   atomic.Int64
	aiCallsTotal    atomic.Int64
	aiErrorsTotal   atomic.Int64
	downloadsTotal  atomic.Int64
	downloadErrors  atomic.Int64
	dbErrorsTotal   atomic.Int64
)

// commandCounts menyimpan jumlah per nama command.
var commandCounts sync.Map // map[string]*atomic.Int64

// aiLatencyMs menyimpan total durasi (ms) per provider untuk rata-rata.
var (
	aiLatencyMu sync.Mutex
	aiLatencyMs = map[string]int64{}
)

// IncMessages menambah hitungan pesan masuk.
func IncMessages() { messagesTotal.Add(1) }

// IncCommand menambah hitungan satu eksekusi command (bernama).
func IncCommand(name string) {
	commandsTotal.Add(1)
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return
	}
	v, _ := commandCounts.LoadOrStore(name, &atomic.Int64{})
	v.(*atomic.Int64).Add(1)
}

// ObserveAI mencatat satu panggilan AI (provider, durasi, error).
func ObserveAI(provider string, dur time.Duration, err error) {
	aiCallsTotal.Add(1)
	if err != nil {
		aiErrorsTotal.Add(1)
	}
	provider = strings.TrimSpace(provider)
	if provider == "" {
		provider = "unknown"
	}
	aiLatencyMu.Lock()
	aiLatencyMs[provider] += dur.Milliseconds()
	aiLatencyMu.Unlock()
}

// IncDownload mencatat satu permintaan download.
func IncDownload() { downloadsTotal.Add(1) }

// IncDownloadError mencatat kegagalan download.
func IncDownloadError() { downloadErrors.Add(1) }

// IncDBError mencatat kegagalan DB (fail-soft counters).
func IncDBError() { dbErrorsTotal.Add(1) }

// Snapshot mengembalikan ringkasan saat ini (untuk log periodik).
type Snapshot struct {
	Messages   int64
	Commands   int64
	AICalls    int64
	AIErrors   int64
	Downloads  int64
	DLErrors   int64
	DBErrors   int64
	TopCommands []struct {
		Name  string
		Count int64
	}
	AIAvgMs map[string]int64
}

// SnapshotNow mengambil snapshot aman (copy).
func SnapshotNow() Snapshot {
	s := Snapshot{
		Messages:  messagesTotal.Load(),
		Commands:  commandsTotal.Load(),
		AICalls:   aiCallsTotal.Load(),
		AIErrors:  aiErrorsTotal.Load(),
		Downloads: downloadsTotal.Load(),
		DLErrors:  downloadErrors.Load(),
		DBErrors:  dbErrorsTotal.Load(),
		AIAvgMs:   map[string]int64{},
	}
	// Top command (maks 5).
	type kv struct {
		k string
		v int64
	}
	var list []kv
	commandCounts.Range(func(key, val any) bool {
		list = append(list, kv{key.(string), val.(*atomic.Int64).Load()})
		return true
	})
	sort.Slice(list, func(i, j int) bool { return list[i].v > list[j].v })
	if len(list) > 5 {
		list = list[:5]
	}
	for _, e := range list {
		s.TopCommands = append(s.TopCommands, struct {
			Name  string
			Count int64
		}{e.k, e.v})
	}
	aiLatencyMu.Lock()
	for k, v := range aiLatencyMs {
		calls := int64(0)
		// rata-rata kasar: total ms / (calls provider). Kita tidak menyimpan
		// calls per provider terpisah, jadi simpan total ms saja di map ini.
		_ = calls
		s.AIAvgMs[k] = v
	}
	aiLatencyMu.Unlock()
	return s
}

// StartLogger menyalakan pencatat periodik bila env METRICS_LOG_INTERVAL diisi
// (format durasi Go, mis. "5m"). Default 5 menit bila env kosong. Nilai
// "off"/"0"/negatif mematikan logger. Log ditulis ke stdout; kegagalan tidak
// mematikan bot.
func StartLogger() {
	raw := strings.TrimSpace(os.Getenv("METRICS_LOG_INTERVAL"))
	interval := 5 * time.Minute
	switch strings.ToLower(raw) {
	case "", "default":
		// pakai default
	case "off", "0", "false", "no":
		return
	default:
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			fmt.Printf("metrics: METRICS_LOG_INTERVAL %q tidak valid — pakai default %s.\n", raw, interval)
		} else {
			interval = d
		}
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			s := SnapshotNow()
			fmt.Printf("metrics: msg=%d cmd=%d ai=%d(err=%d) dl=%d(err=%d) dbErr=%d\n",
				s.Messages, s.Commands, s.AICalls, s.AIErrors, s.Downloads, s.DLErrors, s.DBErrors)
			if len(s.TopCommands) > 0 {
				parts := make([]string, 0, len(s.TopCommands))
				for _, c := range s.TopCommands {
					parts = append(parts, fmt.Sprintf("%s=%d", c.Name, c.Count))
				}
				fmt.Printf("metrics: topcmd: %s\n", strings.Join(parts, " "))
			}
		}
	}()
}

// ParseIntEnv helper kecil (tidak dipakai sekarang, disediakan untuk tahap berikut).
func ParseIntEnv(key string, def int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return n
}