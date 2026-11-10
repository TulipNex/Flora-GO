package memlog

import (
	"strings"
	"testing"
	"time"
)

// resetRing mengosongkan ring global supaya tiap tes mulai dari nol.
func resetRing() {
	ringMu.Lock()
	ring = [ringSize]Sample{}
	ringHead, ringCount = 0, 0
	last, hasLast = Sample{}, false
	peakRSS, peakHeap, peakChild = 0, 0, 0
	ringMu.Unlock()
	tailUntilNS.Store(0)
}

// TestConfigFromEnv memastikan nilai env dijepit ke batas aman: salah ketik
// tidak boleh mematikan panel (sampel terlalu rapat) atau menyembunyikan data
// (retensi terlalu pendek).
func TestConfigFromEnv(t *testing.T) {
	t.Setenv("MEMLOG_SAMPLE_SECONDS", "")
	t.Setenv("MEMLOG_BURST_SECONDS", "")
	t.Setenv("MEMLOG_RETENTION_DAYS", "")
	cfg, on := configFromEnv()
	if !on || cfg.sample != 60*time.Second || cfg.burst != 5*time.Second || cfg.retention != 7*24*time.Hour {
		t.Fatalf("default = %+v on=%v", cfg, on)
	}

	t.Setenv("MEMLOG_SAMPLE_SECONDS", "0")
	if _, on := configFromEnv(); on {
		t.Fatal("MEMLOG_SAMPLE_SECONDS=0 seharusnya menonaktifkan")
	}

	t.Setenv("MEMLOG_SAMPLE_SECONDS", "1")
	t.Setenv("MEMLOG_BURST_SECONDS", "900")
	t.Setenv("MEMLOG_RETENTION_DAYS", "999")
	cfg, on = configFromEnv()
	if !on {
		t.Fatal("harus aktif")
	}
	if cfg.sample != 15*time.Second {
		t.Fatalf("sample = %s, mau 15s (minimum)", cfg.sample)
	}
	if cfg.burst != cfg.sample {
		t.Fatalf("burst = %s, harus dijepit ke sample (%s)", cfg.burst, cfg.sample)
	}
	if cfg.retention != 90*24*time.Hour {
		t.Fatalf("retention = %s, mau 90 hari", cfg.retention)
	}
}

// TestRingUrut memastikan ring mengembalikan sampel tertua → terbaru dan tetap
// benar setelah berputar melewati kapasitas.
func TestRingUrut(t *testing.T) {
	resetRing()
	for i := 1; i <= 3; i++ {
		record(Sample{TS: int64(i)})
	}
	got := Ring()
	if len(got) != 3 || got[0].TS != 1 || got[2].TS != 3 {
		t.Fatalf("Ring = %+v", got)
	}
	if s, ok := Latest(); !ok || s.TS != 3 {
		t.Fatalf("Latest = %+v ok=%v", s, ok)
	}

	// Putar sampai melewati ringSize: yang tersisa harus ringSize terakhir.
	resetRing()
	for i := 0; i < ringSize+5; i++ {
		record(Sample{TS: int64(i)})
	}
	got = Ring()
	if len(got) != ringSize {
		t.Fatalf("len(Ring) = %d, mau %d", len(got), ringSize)
	}
	if got[0].TS != 5 || got[len(got)-1].TS != int64(ringSize+4) {
		t.Fatalf("urutan ring salah: awal %d akhir %d", got[0].TS, got[len(got)-1].TS)
	}
}

// TestPeaks memastikan puncak hanya naik, tidak pernah turun.
func TestPeaks(t *testing.T) {
	resetRing()
	record(Sample{RSS: 100, HeapAlloc: 50, ChildRSS: 10})
	record(Sample{RSS: 80, HeapAlloc: 70, ChildRSS: 5})
	rss, heap, child := Peaks()
	if rss != 100 || heap != 70 || child != 10 {
		t.Fatalf("Peaks = %d/%d/%d", rss, heap, child)
	}
}

// TestDetectSpike memastikan ambang lonjakan bekerja dan label diambil dari
// operasi yang sedang berjalan (atau "idle" bila tidak ada).
func TestDetectSpike(t *testing.T) {
	prev := Sample{TS: 100, HeapAlloc: 20 << 20, RSS: 100 << 20}
	cur := Sample{TS: 160, HeapAlloc: 21 << 20, RSS: 101 << 20}
	if _, ok := detectSpike(cur, prev, true); ok {
		t.Fatal("lonjakan kecil tidak boleh dicatat")
	}

	cur.HeapAlloc = prev.HeapAlloc + 9<<20
	cur.Ops = "ffmpeg/transcode"
	ev, ok := detectSpike(cur, prev, true)
	if !ok {
		t.Fatal("lonjakan heap 9MB harus tercatat")
	}
	if ev.Kind != "spike" || ev.Label != "ffmpeg/transcode" || ev.DurMS != 60000 {
		t.Fatalf("event = %+v", ev)
	}

	cur.Ops = ""
	ev, _ = detectSpike(cur, prev, true)
	if ev.Label != "idle" {
		t.Fatalf("label = %q, mau idle", ev.Label)
	}

	if _, ok := detectSpike(cur, prev, false); ok {
		t.Fatal("sampel pertama tidak boleh dibandingkan")
	}
}

// TestOpRegistryCap memastikan registry operasi aktif dibatasi maxActiveOps —
// ini jaring pengaman utama supaya map operasi tidak tumbuh tanpa batas.
func TestOpRegistryCap(t *testing.T) {
	started.Store(true)
	defer started.Store(false)
	resetOps()

	total := maxActiveOps + 3
	ops := make([]*Op, 0, total)
	for i := 0; i < total; i++ {
		ops = append(ops, Begin("download", "contoh"))
	}
	if got := len(ActiveOps()); got != maxActiveOps {
		t.Fatalf("operasi aktif = %d, mau %d", got, maxActiveOps)
	}
	if _, dropped, _ := Stats(); dropped != 0 {
		t.Fatalf("dibuang = %d, mau 0 (registry penuh bukan antrean)", dropped)
	}
	opsMu.Lock()
	od := opsDropped
	opsMu.Unlock()
	if od != 3 {
		t.Fatalf("opsDropped = %d, mau 3", od)
	}

	for _, op := range ops {
		op.End(true, "")
	}
	if got := len(ActiveOps()); got != 0 {
		t.Fatalf("setelah End masih ada %d operasi", got)
	}
}

// resetOps mengosongkan registry dan penghitung drop.
func resetOps() {
	opsMu.Lock()
	opsActive = make(map[int64]*Op)
	opsDropped = 0
	opsMu.Unlock()
}

// TestOpsSnapshot memastikan ringkasan operasi stabil (urut) — nilai yang
// disimpan per sampel harus bisa dibandingkan antar detik.
func TestOpsSnapshot(t *testing.T) {
	started.Store(true)
	defer started.Store(false)
	resetOps()

	b := Begin("arena", "generate")
	a := Begin("ai", "turn")
	if got := opsSnapshot(); got != "ai/turn,arena/generate" {
		t.Fatalf("opsSnapshot = %q", got)
	}
	a.End(true, "")
	if got := opsSnapshot(); got != "arena/generate" {
		t.Fatalf("opsSnapshot = %q", got)
	}
	b.End(false, "gagal")
	if got := opsSnapshot(); got != "" {
		t.Fatalf("opsSnapshot = %q, mau kosong", got)
	}
}

// TestEndNilAman memastikan instrumentasi boleh dipasang tanpa syarat: kalau
// perekaman mati, Begin mengembalikan nil dan End tidak boleh panik.
func TestEndNilAman(t *testing.T) {
	var op *Op
	op.End(true, "")
	if Begin("x", "y") != nil {
		t.Fatal("Begin harus nil saat perekaman mati")
	}
}

// TestBurstEkor memastikan ekor burst diperpanjang oleh sinyal kick dan tidak
// pernah memendek (operasi yang selesai lebih dulu tidak boleh menghapus
// jadwal burst milik operasi lain).
func TestBurstEkor(t *testing.T) {
	tailUntilNS.Store(0)
	now := time.Now()
	if inBurst(now) {
		t.Fatal("awal harus di luar mode burst")
	}
	extendTail(now)
	if !inBurst(now) {
		t.Fatal("setelah extendTail harus masuk mode burst")
	}
	if inBurst(now.Add(burstTail + time.Second)) {
		t.Fatal("ekor burst harus berakhir setelah burstTail")
	}

	long := now.Add(10 * time.Minute)
	extendTail(long)
	extendTail(now) // harus diabaikan: sudah ada tenggat lebih panjang
	if !inBurst(long) {
		t.Fatal("extendTail tidak boleh memendekkan tenggat")
	}
	tailUntilNS.Store(0)
}

// TestSignedMB memastikan format delta selalu memuat tanda.
func TestSignedMB(t *testing.T) {
	if got := signedMB(-3 << 20); got != "-3.0MB" {
		t.Fatalf("signedMB = %q", got)
	}
	if got := signedMB(2 << 20); !strings.HasPrefix(got, "+2.0MB") {
		t.Fatalf("signedMB = %q", got)
	}
}
