package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"wabot-go/internal/memlog"
)

// TestMemLogRoundTrip memverifikasi simpan/baca sampel + event, agregat per
// jam, peringkat operasi, deteksi lonjakan, tren median, dan prune.
func TestMemLogRoundTrip(t *testing.T) {
	if err := InitMemLog(filepath.Join(t.TempDir(), "memlog.db")); err != nil {
		t.Fatalf("InitMemLog: %v", err)
	}
	defer CloseMemLog()

	db := MemLog()
	ctx := context.Background()
	now := time.Now()

	// Dua jam sampel, 1 menit sekali, heap naik perlahan.
	base := now.Add(-2 * time.Hour).Unix()
	for i := 0; i < 120; i++ {
		s := memlog.Sample{
			TS:          base + int64(i*60),
			RSS:         uint64(50<<20) + uint64(i)<<10,
			HeapAlloc:   uint64(10<<20) + uint64(i)<<10,
			HeapObjects: uint64(10000 + i),
			ChildRSS:    uint64(100 << 20),
			ChildTop:    "chrome 100.0MB",
			CgroupUsed:  300 << 20,
			CgroupMax:   1500 << 20,
			Ops:         "ffmpeg/transcode",
		}
		if err := db.SaveSample(ctx, s); err != nil {
			t.Fatalf("SaveSample: %v", err)
		}
	}

	// Sampel duplikat (ts sama) harus idempoten, bukan menambah baris.
	if err := db.SaveSample(ctx, memlog.Sample{TS: base, RSS: 1}); err != nil {
		t.Fatalf("SaveSample duplikat: %v", err)
	}

	samples, err := db.SamplesBetween(ctx, base, base+int64(120*60))
	if err != nil {
		t.Fatalf("SamplesBetween: %v", err)
	}
	if len(samples) != 120 {
		t.Fatalf("sampel = %d, mau 120 (duplikat harus menimpa)", len(samples))
	}
	if !samples[1].CgroupKnown {
		t.Fatal("CgroupKnown harus diturunkan dari cg_max > 0")
	}

	events := []memlog.Event{
		{TS: base + 600, EndTS: base + 610, Kind: "ffmpeg", Label: "transcode", DurMS: 10000, Alloc: 40 << 20, RSSD: 20 << 20, OK: true},
		{TS: base + 700, EndTS: base + 705, Kind: "download", Label: "cdn", DurMS: 5000, Alloc: 60 << 20, OK: true},
		{TS: base + 800, EndTS: base + 820, Kind: "spike", Label: "ffmpeg/transcode", DurMS: 20000, HeapD: 30 << 20, RSSD: 70 << 20, OK: true},
	}
	for _, e := range events {
		if err := db.SaveEvent(ctx, e); err != nil {
			t.Fatalf("SaveEvent: %v", err)
		}
	}

	got, err := db.EventsBetween(ctx, base, base+int64(120*60), 0)
	if err != nil || len(got) != len(events) {
		t.Fatalf("EventsBetween = %d, err=%v", len(got), err)
	}
	if !got[0].OK || got[0].DurMS != 10000 {
		t.Fatalf("event pertama = %+v", got[0])
	}

	hours, err := db.HourBuckets(ctx, base, base+int64(120*60))
	if err != nil {
		t.Fatalf("HourBuckets: %v", err)
	}
	// 120 menit bisa menyentuh 2 atau 3 jam dinding (tergantung menit mulai),
	// jadi yang diuji adalah isinya: semua sampel terhitung dan jam terakhir
	// lebih tinggi dari jam pertama.
	if len(hours) < 2 || len(hours) > 3 {
		t.Fatalf("bucket jam = %d, mau 2-3", len(hours))
	}
	total := 0
	for _, h := range hours {
		total += h.N
	}
	if total != 120 {
		t.Fatalf("total sampel per jam = %d, mau 120", total)
	}
	first, lastHour := hours[0], hours[len(hours)-1]
	if lastHour.HeapMax <= first.HeapMax {
		t.Fatalf("heap jam terakhir harus lebih tinggi: %d vs %d", lastHour.HeapMax, first.HeapMax)
	}

	top, err := db.TopOps(ctx, base, base+int64(120*60), 5)
	if err != nil || len(top) != 2 {
		t.Fatalf("TopOps = %d, err=%v", len(top), err)
	}
	if top[0].Kind != "download" {
		t.Fatalf("peringkat teratas = %+v, mau download (alokasi 60MB)", top[0])
	}
	if top[0].AllocTotal != 60<<20 || top[0].N != 1 {
		t.Fatalf("agregat download = %+v", top[0])
	}

	spikes, err := db.Spikes(ctx, base, base+int64(120*60), 1)
	if err != nil || len(spikes) != 1 {
		t.Fatalf("Spikes = %d, err=%v", len(spikes), err)
	}
	if spikes[0].RSSD != 70<<20 {
		t.Fatalf("spike terbesar = %+v", spikes[0])
	}

	// Tren: jendela 24 jam terakhir vs sebelumnya. Hanya jendela sekarang
	// yang berisi data, jadi PrevN harus 0 (belum ada pembanding).
	trend, err := db.Trend(ctx, now)
	if err != nil {
		t.Fatalf("Trend: %v", err)
	}
	if trend.NowN == 0 || trend.PrevN != 0 {
		t.Fatalf("trend = %+v", trend)
	}
	if trend.NowHeap == 0 || trend.NowHeap < uint64(10<<20) {
		t.Fatalf("median heap = %d", trend.NowHeap)
	}

	if _, ok, err := db.LastSample(ctx); err != nil || !ok {
		t.Fatalf("LastSample ok=%v err=%v", ok, err)
	}

	if size, err := db.SizeBytes(ctx); err != nil || size <= 0 {
		t.Fatalf("SizeBytes = %d, err=%v", size, err)
	}

	// Prune: buang semua yang lebih tua dari 1 jam lalu.
	n, err := db.Prune(ctx, now.Add(-time.Hour).Unix())
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n == 0 {
		t.Fatal("Prune tidak menghapus apa pun")
	}
	left, err := db.SamplesBetween(ctx, base, base+int64(120*60))
	if err != nil {
		t.Fatalf("SamplesBetween setelah prune: %v", err)
	}
	if len(left) >= 120 {
		t.Fatalf("sampel setelah prune = %d, harus berkurang", len(left))
	}
}

// TestMemLogNilAman memastikan handle nil tidak panik: perekaman bisa mati
// (init gagal) tanpa membuat command lain ikut gagal.
func TestMemLogNilAman(t *testing.T) {
	var db *MemLogDB
	ctx := context.Background()
	if err := db.SaveSample(ctx, memlog.Sample{TS: 1}); err != nil {
		t.Fatalf("SaveSample nil: %v", err)
	}
	if err := db.SaveEvent(ctx, memlog.Event{}); err != nil {
		t.Fatalf("SaveEvent nil: %v", err)
	}
	if n, err := db.Prune(ctx, 1); n != 0 || err != nil {
		t.Fatalf("Prune nil = %d, %v", n, err)
	}
	if list, err := db.SamplesBetween(ctx, 0, 1); list != nil || err != nil {
		t.Fatalf("SamplesBetween nil = %v, %v", list, err)
	}
	if t2, err := db.Trend(ctx, time.Now()); err != nil || t2.NowN != 0 {
		t.Fatalf("Trend nil = %+v, %v", t2, err)
	}
}

// TestMemLogStartWiring memverifikasi rantai nyata: memlog.Start menyalakan
// sampler yang langsung mengambil sampel pertama, menaruhnya di antrean, dan
// writerLoop menuliskannya ke SQLite. Tanpa tes ini, kabel Start/Init bisa
// putus tanpa ketahuan sampai owner membuka .memlog dan melihat data kosong.
func TestMemLogStartWiring(t *testing.T) {
	t.Setenv("MEMLOG_SAMPLE_SECONDS", "60")
	if err := InitMemLog(filepath.Join(t.TempDir(), "memlog.db")); err != nil {
		t.Fatalf("InitMemLog: %v", err)
	}
	defer CloseMemLog()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if !memlog.Enabled() {
		memlog.Start(ctx, MemLog())
	}
	if !memlog.Enabled() {
		t.Fatal("memlog tidak aktif setelah Start")
	}

	// Sampel pertama dijadwalkan langsung (timer 0), jadi cukup tunggu
	// sebentar; penulisan DB asinkron butuh satu putaran writerLoop.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if samples, err := MemLog().SamplesBetween(ctx, 0, time.Now().Unix()+2); err == nil && len(samples) > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("tidak ada sampel yang tersimpan setelah Start")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
