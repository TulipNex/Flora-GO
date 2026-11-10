package plugins

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"io"
	"strings"
	"testing"
	"time"

	"wabot-go/internal/memlog"
	"wabot-go/internal/store"
)

// TestMemlogGrowthReason memverifikasi ambang alarm tren: heap DAN objek
// harus naik bersamaan untuk disebut kebocoran; salah satu saja tetap
// dilaporkan dengan sebab yang berbeda (retensi vs objek kecil menumpuk).
func TestMemlogGrowthReason(t *testing.T) {
	cases := []struct {
		name   string
		now    store.MemLogTrend
		want   bool
		substr string
	}{
		{"stabil", mkTrend(100<<20, 10000, 102<<20, 10200), false, ""},
		{"heap+objek naik", mkTrend(100<<20, 10000, 130<<20, 14000), true, "kebocoran"},
		{"heap saja", mkTrend(100<<20, 10000, 130<<20, 10100), true, "retensi"},
		{"objek saja", mkTrend(100<<20, 10000, 102<<20, 14000), true, "objek kecil"},
		// Naik 20% saja tidak boleh memicu alarm (ambang 25% / 20%).
		{"naik tipis", mkTrend(100<<20, 10000, 120<<20, 12000), false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason, growing := memlogGrowthReason(tc.now)
			if growing != tc.want {
				t.Fatalf("growing = %v, mau %v (alasan %q)", growing, tc.want, reason)
			}
			if tc.want && !strings.Contains(reason, tc.substr) {
				t.Fatalf("alasan = %q, mau memuat %q", reason, tc.substr)
			}
		})
	}
}

// mkTrend merakit MemLogTrend dengan pembanding (prev) dan jendela sekarang.
func mkTrend(prevHeap, prevObj, nowHeap, nowObj uint64) store.MemLogTrend {
	return store.MemLogTrend{
		PrevHeap: prevHeap, PrevObj: prevObj, PrevRSS: prevHeap, PrevN: 100,
		NowHeap: nowHeap, NowObj: nowObj, NowRSS: nowHeap, NowN: 100,
	}
}

// TestMemlogTrendText memastikan verdict teks mengikuti arah delta.
func TestMemlogTrendText(t *testing.T) {
	if got := memlogTrendText(store.MemLogTrend{PrevN: 0}); !strings.Contains(got, "belum ada pembanding") {
		t.Fatalf("tanpa pembanding = %q", got)
	}
	if got := memlogTrendText(store.MemLogTrend{PrevN: 5, NowN: 5, PrevHeap: 100 << 20, NowHeap: 100 << 20}); !strings.HasPrefix(got, "STABIL") {
		t.Fatalf("stabil = %q", got)
	}
	up := memlogTrendText(store.MemLogTrend{
		PrevN: 5, NowN: 5, PrevHeap: 100 << 20, NowHeap: 130 << 20,
		PrevObj: 10000, NowObj: 14000,
	})
	if !strings.HasPrefix(up, "NAIK") {
		t.Fatalf("naik = %q", up)
	}
	down := memlogTrendText(store.MemLogTrend{PrevN: 5, NowN: 5, PrevHeap: 200 << 20, NowHeap: 100 << 20})
	if !strings.HasPrefix(down, "TURUN") {
		t.Fatalf("turun = %q", down)
	}
}

// TestMemlogRawWindow memverifikasi parsing argumen jam/menit dan penolakan
// nilai di luar rentang (command tidak boleh diam-diam membaca hari lain).
func TestMemlogRawWindow(t *testing.T) {
	from, to, err := memlogRawWindow([]string{"06"})
	if err != nil {
		t.Fatalf("jam saja: %v", err)
	}
	if to.Sub(from) != time.Hour || from.Hour() != 6 || from.Minute() != 0 {
		t.Fatalf("rentang jam = %s–%s", from, to)
	}

	from, to, err = memlogRawWindow([]string{"06", "12"})
	if err != nil {
		t.Fatalf("jam+menit: %v", err)
	}
	if to.Sub(from) != time.Minute || from.Minute() != 12 {
		t.Fatalf("rentang menit = %s–%s", from, to)
	}

	if _, _, err := memlogRawWindow([]string{"24"}); err == nil {
		t.Fatal("jam 24 harus ditolak")
	}
	if _, _, err := memlogRawWindow([]string{"06", "60"}); err == nil {
		t.Fatal("menit 60 harus ditolak")
	}
	if _, _, err := memlogRawWindow([]string{"abc"}); err == nil {
		t.Fatal("jam bukan angka harus ditolak")
	}
}

// TestParseMemlogWindow memastikan alias rentang yang dipakai dokumentasi
// benar-benar diterima.
func TestParseMemlogWindow(t *testing.T) {
	if d, err := parseMemlogWindow("7d"); err != nil || d != 7*24*time.Hour {
		t.Fatalf("7d = %s, %v", d, err)
	}
	if d, err := parseMemlogWindow("24h"); err != nil || d != 24*time.Hour {
		t.Fatalf("24h = %s, %v", d, err)
	}
	if d, err := parseMemlogWindow("90m"); err != nil || d != 90*time.Minute {
		t.Fatalf("90m = %s, %v", d, err)
	}
	if _, err := parseMemlogWindow("kemarin"); err == nil {
		t.Fatal("rentang asing harus ditolak")
	}
}

// TestBuildMemLogZip memastikan export berisi dua CSV yang bisa dibuka Excel:
// header benar, angka dalam MB, dan baris sesuai jumlah input.
func TestBuildMemLogZip(t *testing.T) {
	samples := []memlog.Sample{
		{TS: 1000, RSS: 60 << 20, HeapAlloc: 10 << 20, HeapObjects: 1234, ChildRSS: 100 << 20, ChildTop: "chrome 100.0MB", Ops: "ffmpeg/transcode"},
		{TS: 1060, RSS: 61 << 20, HeapAlloc: 11 << 20, HeapObjects: 1300},
	}
	events := []memlog.Event{
		{TS: 1000, EndTS: 1010, Kind: "ffmpeg", Label: "transcode", DurMS: 10000, Alloc: 40 << 20, OK: true, Note: "ok"},
	}
	data, err := buildMemLogZip(samples, events)
	if err != nil {
		t.Fatalf("buildMemLogZip: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("zip: %v", err)
	}

	read := func(name string) [][]string {
		for _, f := range zr.File {
			if f.Name != name {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				t.Fatalf("buka %s: %v", name, err)
			}
			defer rc.Close()
			raw, err := io.ReadAll(rc)
			if err != nil {
				t.Fatalf("baca %s: %v", name, err)
			}
			rows, err := csv.NewReader(bytes.NewReader(raw)).ReadAll()
			if err != nil {
				t.Fatalf("parse %s: %v", name, err)
			}
			return rows
		}
		t.Fatalf("%s tidak ada di ZIP", name)
		return nil
	}

	srows := read("samples.csv")
	if len(srows) != 3 {
		t.Fatalf("samples.csv = %d baris, mau 3", len(srows))
	}
	if srows[0][0] != "ts" || srows[0][2] != "rss_mb" {
		t.Fatalf("header samples = %v", srows[0])
	}
	if srows[1][2] != "60.00" {
		t.Fatalf("rss baris pertama = %q, mau 60.00", srows[1][2])
	}

	erows := read("events.csv")
	if len(erows) != 2 {
		t.Fatalf("events.csv = %d baris, mau 2", len(erows))
	}
	if erows[0][3] != "kind" || erows[1][6] != "40.00" {
		t.Fatalf("events = %v", erows[1])
	}
}

// TestMemlogOpName memastikan label operasi tidak pernah kosong/aneh di chat.
func TestMemlogOpName(t *testing.T) {
	if got := memlogOpName("download", "cdn"); got != "download/cdn" {
		t.Fatalf("got %q", got)
	}
	if got := memlogOpName("download", "  "); got != "download" {
		t.Fatalf("got %q", got)
	}
}

// TestCountShort memastikan angka objek besar diringkas di tabel sempit.
func TestCountShort(t *testing.T) {
	if got := countShort(999); got != "999" {
		t.Fatalf("got %q", got)
	}
	if got := countShort(25400); got != "25.4k" {
		t.Fatalf("got %q", got)
	}
}
