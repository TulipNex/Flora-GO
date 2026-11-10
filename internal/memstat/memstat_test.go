package memstat

import (
	"strings"
	"testing"
)

// TestParseStatm menguji pembacaan RSS dari /proc/self/statm (field ke-2 =
// jumlah page). Fixture diambil dari bentuk asli berkas itu.
func TestParseStatm(t *testing.T) {
	raw := []byte("1000 250 100 5 0 80 0\n")
	if got := ParseStatm(raw, 4096); got != 250*4096 {
		t.Fatalf("ParseStatm = %d, mau %d", got, 250*4096)
	}
	if got := ParseStatm([]byte("rusak"), 4096); got != 0 {
		t.Fatalf("ParseStatm rusak = %d, mau 0", got)
	}
}

// TestParseStat menguji ekstraksi ppid + nama proses. Nama proses Chrome
// memuat spasi dan tanda kurung — kasus yang dulu memecahkan parser naif.
func TestParseStat(t *testing.T) {
	raw := []byte(`4242 (Chrome_ChildIOT (renderer)) S 999 4242 4242 0 -1 4194560`)
	ppid, name, ok := ParseStat(raw)
	if !ok {
		t.Fatal("ParseStat gagal")
	}
	if ppid != 999 {
		t.Fatalf("ppid = %d, mau 999", ppid)
	}
	if name != "Chrome_ChildIOT (renderer)" {
		t.Fatalf("name = %q", name)
	}
	if _, _, ok := ParseStat([]byte("tanpa-kurung")); ok {
		t.Fatal("ParseStat seharusnya gagal tanpa tanda kurung")
	}
}

// TestParseStatusRSS menguji pembacaan VmRSS (kB → byte).
func TestParseStatusRSS(t *testing.T) {
	raw := []byte("Name:\tbot\nVmPeak:\t 999999 kB\nVmRSS:\t  12345 kB\nThreads:\t9\n")
	got, ok := ParseStatusRSS(raw)
	if !ok || got != 12345*1024 {
		t.Fatalf("ParseStatusRSS = %d, ok=%v", got, ok)
	}
	if _, ok := ParseStatusRSS([]byte("Name:\tbot\n")); ok {
		t.Fatal("ParseStatusRSS seharusnya gagal tanpa VmRSS")
	}
}

// TestSummarizeChildren memastikan RSS proses anak dijumlahkan per nama dan
// labelnya hanya memuat sejumlah limit entri teratas.
func TestSummarizeChildren(t *testing.T) {
	procs := []Proc{
		{PID: 1, Name: "chrome", RSS: 100 << 20},
		{PID: 2, Name: "chrome", RSS: 50 << 20},
		{PID: 3, Name: "Xvfb", RSS: 10 << 20},
		{PID: 4, Name: "ffmpeg", RSS: 1 << 20},
	}
	total, label := SummarizeChildren(procs, 2)
	if want := uint64(161 << 20); total != want {
		t.Fatalf("total = %d, mau %d", total, want)
	}
	if !strings.HasPrefix(label, "chrome 150.0MB, Xvfb 10.0MB") {
		t.Fatalf("label = %q", label)
	}
	if total, label := SummarizeChildren(nil, 3); total != 0 || label != "" {
		t.Fatalf("procs kosong = %d/%q", total, label)
	}
}

// TestMB memastikan format satu desimal yang dipakai label chat.
func TestMB(t *testing.T) {
	if got := MB(1 << 20); got != "1.0MB" {
		t.Fatalf("MB = %q", got)
	}
	if got := MB(3 << 19); got != "1.5MB" {
		t.Fatalf("MB = %q", got)
	}
}
