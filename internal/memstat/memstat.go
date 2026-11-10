// Package memstat membaca statistik memori proses bot, memori container
// (cgroup), dan proses anak (Chrome/Xvfb/ffmpeg) langsung dari /proc.
//
// Leaf package: hanya stdlib, tidak mengimpor paket repo lain. Ini penting
// karena dipakai internal/memlog, yang pada gilirannya dipanggil dari
// internal/heavy dan internal/external — dua paket yang sengaja dijaga bebas
// dependensi.
//
// Di luar Linux (mis. saat tes di Windows) semua pembacaan /proc gagal dan
// fungsi mengembalikan nilai nol; itulah sebabnya parser di file ini
// dipisah menjadi fungsi murni yang bisa diuji dengan fixture byte.
package memstat

import (
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// maxDescendants membatasi jumlah proses anak yang dipindai. Panel kecil
// tidak mungkin punya ratusan proses; batas ini murni jaring pengaman supaya
// satu container aneh tidak membuat sampler berjalan lama.
const maxDescendants = 128

// maxDepth membatasi kedalaman pohon proses. Chrome menurunkan renderer satu
// tingkat di bawah proses utamanya, jadi 4 sudah lebih dari cukup.
const maxDepth = 4

// Proc adalah satu proses anak beserta RSS-nya.
type Proc struct {
	PID  int
	Name string
	RSS  uint64
}

// Sample adalah potret memori pada satu waktu (semua nilai dalam byte).
type Sample struct {
	RSS         uint64
	Goroutines  int
	CgroupUsed  uint64
	CgroupMax   uint64
	CgroupKnown bool
	Children    []Proc
	ChildRSS    uint64
	ChildTop    string
}

// Self mengambil potret lengkap: RSS proses ini, memori cgroup, dan seluruh
// proses anak (beserta ringkasan teks seperti "chrome 312MB, Xvfb 14MB").
func Self() Sample {
	s := Sample{
		RSS:        SelfRSS(),
		Goroutines: runtime.NumGoroutine(),
	}
	s.CgroupUsed, s.CgroupMax, s.CgroupKnown = Cgroup()
	s.Children = Descendants(os.Getpid())
	s.ChildRSS, s.ChildTop = SummarizeChildren(s.Children, 5)
	return s
}

// SelfRSS membaca RSS proses ini dari /proc/self/statm (field ke-2 = jumlah
// page). 0 bila tidak tersedia (non-Linux).
func SelfRSS() uint64 {
	raw, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	return ParseStatm(raw, uint64(os.Getpagesize()))
}

// ParseStatm mengekstrak RSS dari isi /proc/<pid>/statm.
func ParseStatm(raw []byte, pageSize uint64) uint64 {
	fields := strings.Fields(string(raw))
	if len(fields) < 2 {
		return 0
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return pages * pageSize
}

// Cgroup membaca pemakaian dan batas memori container. known=false bila
// panel tidak memakai cgroup (mis. saat develop di Windows) atau batasnya
// tak terhingga.
func Cgroup() (used, max uint64, known bool) {
	if limit, ok := readUintFile("/sys/fs/cgroup/memory.max"); ok {
		if cur, ok2 := readUintFile("/sys/fs/cgroup/memory.current"); ok2 {
			return cur, limit, true
		}
	}
	// cgroup v1: nilai raksasa (mendekati 2^63) berarti tanpa batas.
	if limit, ok := readUintFile("/sys/fs/cgroup/memory/memory.limit_in_bytes"); ok && limit < 1<<60 {
		if cur, ok2 := readUintFile("/sys/fs/cgroup/memory/memory.usage_in_bytes"); ok2 {
			return cur, limit, true
		}
	}
	return 0, 0, false
}

// Descendants mengembalikan seluruh proses turunan dari root (anak, cucu,
// dst, maksimal maxDepth dan maxDescendants). RSS dibaca per proses.
func Descendants(root int) []Proc {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}

	children := make(map[int][]Proc)
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		raw, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		ppid, name, ok := ParseStat(raw)
		if !ok {
			continue
		}
		children[ppid] = append(children[ppid], Proc{PID: pid, Name: name})
	}

	var out []Proc
	level := []int{root}
	for depth := 0; depth < maxDepth && len(level) > 0; depth++ {
		var next []int
		for _, pid := range level {
			for _, child := range children[pid] {
				child.RSS = ProcessRSS(child.PID)
				out = append(out, child)
				next = append(next, child.PID)
				if len(out) >= maxDescendants {
					return out
				}
			}
		}
		level = next
	}
	return out
}

// ProcessRSS membaca RSS satu proses dari /proc/<pid>/status (VmRSS), dengan
// fallback ke /proc/<pid>/statm.
func ProcessRSS(pid int) uint64 {
	dir := "/proc/" + strconv.Itoa(pid)
	if raw, err := os.ReadFile(dir + "/status"); err == nil {
		if rss, ok := ParseStatusRSS(raw); ok {
			return rss
		}
	}
	if raw, err := os.ReadFile(dir + "/statm"); err == nil {
		return ParseStatm(raw, uint64(os.Getpagesize()))
	}
	return 0
}

// ParseStat membaca pasangan ppid + nama proses dari isi /proc/<pid>/stat.
// Nama proses bisa memuat spasi dan tanda kurung, jadi dipotong berdasarkan
// kurung buka pertama dan kurung tutup terakhir.
func ParseStat(raw []byte) (ppid int, name string, ok bool) {
	s := string(raw)
	open := strings.IndexByte(s, '(')
	closeIdx := strings.LastIndexByte(s, ')')
	if open < 0 || closeIdx <= open {
		return 0, "", false
	}
	name = s[open+1 : closeIdx]
	rest := strings.Fields(s[closeIdx+1:])
	// rest[0] = state, rest[1] = ppid.
	if len(rest) < 2 {
		return 0, "", false
	}
	ppid, err := strconv.Atoi(rest[1])
	if err != nil {
		return 0, "", false
	}
	return ppid, name, true
}

// ParseStatusRSS mengekstrak VmRSS (dalam byte) dari isi /proc/<pid>/status.
func ParseStatusRSS(raw []byte) (uint64, bool) {
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "VmRSS:") {
			continue
		}
		fields := strings.Fields(line)
		// Format: "VmRSS:      1234 kB"
		if len(fields) < 2 {
			return 0, false
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, false
		}
		return kb * 1024, true
	}
	return 0, false
}

// SummarizeChildren menjumlahkan RSS proses anak (digabung per nama, karena
// Chrome menurunkan banyak proses dengan nama sama) dan menyusun label
// ringkas berisi limit nama teratas.
func SummarizeChildren(procs []Proc, limit int) (total uint64, label string) {
	if len(procs) == 0 {
		return 0, ""
	}
	byName := make(map[string]uint64, len(procs))
	for _, p := range procs {
		total += p.RSS
		byName[p.Name] += p.RSS
	}
	type kv struct {
		name string
		rss  uint64
	}
	list := make([]kv, 0, len(byName))
	for name, rss := range byName {
		list = append(list, kv{name, rss})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].rss == list[j].rss {
			return list[i].name < list[j].name
		}
		return list[i].rss > list[j].rss
	})
	if limit <= 0 || limit > len(list) {
		limit = len(list)
	}
	parts := make([]string, 0, limit)
	for _, e := range list[:limit] {
		parts = append(parts, e.name+" "+MB(e.rss))
	}
	return total, strings.Join(parts, ", ")
}

// MB memformat byte menjadi "12.3MB" (satu desimal, cukup untuk label chat).
func MB(b uint64) string {
	return strconv.FormatFloat(float64(b)/(1<<20), 'f', 1, 64) + "MB"
}

// readUintFile membaca satu file berisi angka. Nilai "max" (cgroup v2 tanpa
// batas) dianggap tidak diketahui.
func readUintFile(path string) (uint64, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil || v == 0 {
		return 0, false
	}
	return v, true
}
