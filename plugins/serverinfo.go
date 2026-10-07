package plugins

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterServerInfo)
}

// RegisterServerInfo mendaftarkan command `.serverinfo` (alias `.sysinfo`)
// KHUSUS owner: menampilkan info server (hardware, memori, OS, runtime,
// memori proses) dengan format referensi owner. Sumber data semuanya stdlib
// (runtime, /proc, syscall) — tanpa dependensi baru.
//
// Semua pembacaan spesifik-Linux di-gate runtime.GOOS: di panel Linux
// lengkap, di laptop Windows field terkait jadi "-" (fail-soft per field,
// command tidak pernah gagal total gara-gara 1 sumber hilang).
func RegisterServerInfo(r *Registry) {
	r.Register(Command{
		Name:        "serverinfo",
		Aliases:     []string{"sysinfo"},
		Description: "Info server (CPU, RAM, OS, runtime)",
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
	distro := linuxDistro()
	kernel := linuxKernelRelease()
	hostname, _ := os.Hostname()
	hostUptime, hostUptimeOK := linuxUptime()
	rss := processRSS()
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	mb := func(b uint64) string {
		return fmt.Sprintf("%.2f MB", float64(b)/(1<<20))
	}
	dash := func(s string) string {
		if strings.TrimSpace(s) == "" {
			return "-"
		}
		return s
	}

	heapLimit := os.Getenv("GOMEMLIMIT")
	if strings.TrimSpace(heapLimit) == "" {
		heapLimit = "-"
	}
	var memUsed uint64
	if memTotal >= memFree {
		memUsed = memTotal - memFree
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
			"\n*Operating System*\n"+
			"• Distro : %s\n"+
			"• Platform : %s\n"+
			"• Type : %s\n"+
			"• Release : %s\n"+
			"• Hostname : %s\n"+
			"• Uptime : %s\n"+
			"\n*Runtime*\n"+
			"• Go : %s\n"+
			"• Engine : gc\n"+
			"• PID : %d\n"+
			"• Process Uptime : %s\n"+
			"• Heap Limit : %s\n"+
			"\n*Process Memory*\n"+
			"• RSS : %s\n"+
			"• Heap Used : %s\n"+
			"• Heap Total : %s\n"+
			"• External : %s",
		dash(cpuModel),
		runtime.NumCPU(),
		dash(cpuClock),
		goArchDisplay(),
		goEndianness(),
		gbOrDash(memTotal), gbOrDash(memUsed), gbOrDash(memFree),
		dash(distro),
		runtime.GOOS,
		"Linux",
		dash(kernel),
		dash(hostname),
		durationOrDash(hostUptime, hostUptimeOK),
		runtime.Version(),
		os.Getpid(),
		formatDuration(time.Since(startTime).Round(time.Second)),
		heapLimit,
		mb(rss), mb(uint64(mem.HeapAlloc)), mb(uint64(mem.HeapSys)), mb(uint64(mem.OtherSys)+uint64(mem.GCSys)),
	)
	return store.ReplyText(ctx, mctx, msg)
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
