package plugins

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterServerInfo)
}

// RegisterServerInfo mendaftarkan command `.server` (alias `.serverinfo`,
// `.si`) KHUSUS owner: ringkasan server (hardware, memori sistem, runtime).
// Detail OS ada di `.os`, detail memori proses ada di `.rss` — pembagian
// disengaja supaya tidak ada baris yang tampil dobel antar command.
//
// Semua pembacaan spesifik-Linux di-gate runtime.GOOS: di panel Linux
// lengkap, di laptop Windows field terkait jadi "-" (fail-soft per field,
// command tidak pernah gagal total gara-gara 1 sumber hilang).
func RegisterServerInfo(r *Registry) {
	r.Register(Command{
		Name:        "server",
		Aliases:     []string{"serverinfo", "si"},
		Description: "Ringkasan server (CPU, RAM, runtime)",
		Category:    CategoryOwner,
		Handler:     handleServerInfo,
	})
}

func handleServerInfo(ctx context.Context, mctx *store.MessageContext) error {
	// Info server tak boleh publik: kategori Owner hanya menyembunyikan
	// dari menu, eksekusi digerbang eksplisit di sini.
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}

	cpuModel, cpuClock := linuxCPUInfo()
	memTotal, memFree := linuxMemInfo()

	var memUsed uint64
	if memTotal >= memFree {
		memUsed = memTotal - memFree
	}
	heapLimit := os.Getenv("GOMEMLIMIT")
	if strings.TrimSpace(heapLimit) == "" {
		heapLimit = "-"
	}

	msg := fmt.Sprintf(
		"🖥️ *SERVER INFORMATION*\n"+
			"\n*Hardware*\n"+
			"• CPU : %s\n"+
			"• Core : %d\n"+
			"• Clock : %s\n"+
			"• Architecture : %s\n"+
			"• Endianness : %s\n"+
			"\n*Memory*\n"+
			"• Total : %s\n"+
			"• Used : %s\n"+
			"• Free : %s\n"+
			"\n*Runtime*\n"+
			"• Go : %s\n"+
			"• Engine : gc\n"+
			"• PID : %d\n"+
			"• Heap Limit : %s\n"+
			"• Library : %s\n"+
			"\nDetail: .os | .rss",
		dashStr(cpuModel),
		runtime.NumCPU(),
		dashStr(cpuClock),
		goArchDisplay(),
		goEndianness(),
		gbOrDash(memTotal), gbOrDash(memUsed), gbOrDash(memFree),
		runtime.Version(),
		os.Getpid(),
		heapLimit,
		hypermeowVersion(),
	)
	return store.ReplyText(ctx, mctx, msg)
}

// mbStr memformat byte jadi MB 2 desimal. Dipakai .server dan .rss.
func mbStr(b uint64) string {
	return fmt.Sprintf("%.2f MB", float64(b)/(1<<20))
}

// dashStr mengembalikan "-" untuk string kosong. Dipakai .server dan .os.
func dashStr(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

// hypermeowVersion membaca versi modul hypermeow (github.com/polymorfa/hypermeow,
// fork whatsmeow) dari build info
// (stdlib runtime/debug, tanpa dependensi). Pseudo-version panjang
// (v0.0.0-20250928140511-35f522c88ce3) dipadatkan jadi v0.0.0-20250928 agar
// enak dibaca; "hypermeow" polos bila tak terbaca.
func hypermeowVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range bi.Deps {
			if dep.Path == "github.com/polymorfa/hypermeow" {
				v := dep.Version
				if i := strings.Index(v, "-"); i > 0 && len(v) > i+9 {
					return "hypermeow " + v[:i] + "-" + v[i+1:i+9]
				}
				return "hypermeow " + v
			}
		}
	}
	return "hypermeow"
}

// formatDuration mengubah durasi jadi format "1h 23m 45s" yang lebih enak dibaca
// dibanding format default Go (misal "1h23m45.123s").
// Pindahan dari plugins/info.go yang dihapus — dipakai serverinfo.go,
// osinfo.go (lewat durationOrDash), dan call.go.
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

// gbOrDash memformat byte jadi GB, "-" kalau nol (sumber gagal dibaca).
func gbOrDash(b uint64) string {
	if b == 0 {
		return "-"
	}
	return fmt.Sprintf("%.2f GB", float64(b)/(1<<30))
}

// durationOrDash memformat durasi host, "-" kalau tidak terbaca.
func durationOrDash(d time.Duration, ok bool) string {
	if !ok {
		return "-"
	}
	return formatDuration(d.Round(time.Second))
}

// goArchDisplay memetakan GOARCH ke label umum (amd64->x64).
func goArchDisplay() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x64"
	case "386":
		return "x86"
	default:
		return runtime.GOARCH
	}
}

// goEndianness mengembalikan endianness platform dari peta GOARCH resmi Go
// (setiap port Go endianness-nya tetap — bukan tebakan, deterministik).
func goEndianness() string {
	switch runtime.GOARCH {
	case "amd64", "386", "arm", "arm64", "riscv64", "wasm", "loong64":
		return "LE"
	case "mips", "mips64", "ppc64", "s390x":
		return "BE"
	default:
		return "?"
	}
}

// isLinux true kalau berjalan di Linux (panel). Semua pembaca /proc dan
// syscall di bawah di-gate ini oleh pemanggilnya.
func isLinux() bool {
	return runtime.GOOS == "linux"
}

// linuxCPUInfo membaca model + clock CPU pertama dari /proc/cpuinfo.
// "" bila bukan Linux / gagal dibaca.
func linuxCPUInfo() (model, clock string) {
	if !isLinux() {
		return "", ""
	}
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return "", ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		kv := strings.SplitN(line, ":", 2)
		if len(kv) != 2 {
			continue
		}
		key, val := strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1])
		if model == "" && key == "model name" {
			model = val
		}
		if clock == "" && key == "cpu MHz" {
			if f, ferr := strconv.ParseFloat(val, 64); ferr == nil {
				clock = fmt.Sprintf("%.0f MHz", f)
			} else {
				clock = val + " MHz"
			}
		}
		if model != "" && clock != "" {
			break
		}
	}
	return model, clock
}

// linuxMemInfo membaca total + free RAM (byte). 0,0 bila gagal / bukan
// Linux. Implementasi per-OS dipecah ke file build-tag karena syscall
// Sysinfo/Uname hanya ada di Linux (kode ini juga di-compile di laptop
// Windows — lihat serverinfo_sys_*.go).
func linuxMemInfo() (total, free uint64) {
	if !isLinux() {
		return 0, 0
	}
	return sysMemInfo()
}

// linuxDistro membaca PRETTY_NAME dari /etc/os-release.
func linuxDistro() string {
	if !isLinux() {
		return ""
	}
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			return strings.Trim(strings.TrimSpace(v), `"`)
		}
	}
	return ""
}

// linuxKernelRelease membaca versi kernel. Implementasi syscall ada di
// serverinfo_sys_linux.go (build tag linux); di OS lain selalu "".
func linuxKernelRelease() string {
	if !isLinux() {
		return ""
	}
	return kernelRelease()
}

// linuxUptime membaca uptime host dari /proc/uptime (field pertama, detik).
func linuxUptime() (time.Duration, bool) {
	if !isLinux() {
		return 0, false
	}
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, false
	}
	secs, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || secs < 0 {
		return 0, false
	}
	return time.Duration(secs * float64(time.Second)), true
}

// processRSS membaca memori residen PROSES INI dari /proc/self/statm
// (field ke-2 = jumlah page). Fallback: 0 (ditampilkan "-").
func processRSS() uint64 {
	if !isLinux() {
		return 0
	}
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return 0
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return pages * uint64(os.Getpagesize())
}
