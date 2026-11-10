// Package memlog merekam riwayat memori bot: sampel berkala (heap, RSS,
// cgroup, proses anak) plus satu baris per operasi berat (ffmpeg, unduhan,
// giliran AI, Chrome, backup) berisi berapa byte yang dialokasikan operasi
// itu.
//
// Tujuan: menjawab "kenapa memori naik" dengan data, bukan tebakan. Sampel
// berkala menunjukkan APAKAH naik; event per operasi menunjukkan SIAPA
// penyebabnya; profil heap/goroutine on-demand menunjukkan baris kode mana.
//
// Paket ini sengaja "leaf": hanya stdlib + internal/memstat + logfmt. Itu
// membuat internal/heavy dan internal/external boleh memanggil Begin/End
// tanpa risiko import cycle, dan penulisan DB dilakukan lewat interface Sink
// yang disuntikkan dari main.go (lihat internal/store/memlogdb.go).
//
// Prinsip yang wajib dijaga (pelajaran dari audit memori sebelumnya):
//   - Semua wadah berukuran tetap: ring sample, map operasi aktif (cap 32),
//     antrean tulis (cap 256). Tidak ada map yang tumbuh per user.
//   - Penulisan DB asinkron; sampler tidak boleh memblokir command.
//   - Sampler TIDAK pernah memanggil debug.FreeOSMemory (mendistorsi ukuran).
package memlog

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"wabot-go/internal/logfmt"
	"wabot-go/internal/memstat"
)

const (
	defaultSampleSeconds = 60
	minSampleSeconds     = 15
	defaultBurstSeconds  = 5
	defaultRetentionDays = 7
	maxRetentionDays     = 90

	// ringSize menentukan berapa sampel terakhir disimpan di memori untuk
	// tampilan cepat (.memlog now). Riwayat penuh ada di SQLite.
	ringSize = 240

	// maxActiveOps membatasi registry operasi yang sedang berjalan. Kalau
	// lebih dari ini berjalan bersamaan, entri tertua dibuang (dan dihitung
	// sebagai drop) — map ini TIDAK boleh tumbuh tanpa batas.
	maxActiveOps = 32

	// queueSize membatasi antrean tulis DB. Penuh = sampel/event dibuang,
	// bukan memblokir command.
	queueSize = 256

	burstTail       = 30 * time.Second
	pruneEvery      = 30 * time.Minute
	spikeHeapDelta  = 8 << 20
	spikeChildDelta = 50 << 20
	spikeRSSDelta   = 60 << 20
)

// Sample adalah satu potret memori (semua byte).
type Sample struct {
	TS           int64
	RSS          uint64
	HeapAlloc    uint64
	HeapSys      uint64
	HeapIdle     uint64
	HeapReleased uint64
	HeapObjects  uint64
	StackInuse   uint64
	GCCycles     uint64
	TotalAlloc   uint64
	Goroutines   uint64
	CgroupUsed   uint64
	CgroupMax    uint64
	CgroupKnown  bool
	ChildRSS     uint64
	ChildTop     string
	Ops          string
}

// Event adalah satu operasi yang sudah selesai. TS = waktu mulai,
// EndTS = waktu selesai, sehingga urutan dan tumpang tindih bisa
// direkonstruksi tanpa menyimpan dua baris.
type Event struct {
	TS    int64
	EndTS int64
	Kind  string
	Label string
	DurMS int64
	// Alloc = delta TotalAlloc selama operasi. Ini metrik deterministik
	// (monotonik, kebal GC): "operasi ini mengalokasikan berapa byte".
	Alloc int64
	// HeapD/RSSD = dampak tertahan. Bisa negatif (GC jalan di tengah), dan
	// ditandai "~" di Note kalau GC terbukti berjalan selama operasi.
	HeapD int64
	RSSD  int64
	OK    bool
	Note  string
}

// Sink adalah tujuan penyimpanan (diimplementasikan internal/store).
type Sink interface {
	SaveSample(ctx context.Context, s Sample) error
	SaveEvent(ctx context.Context, e Event) error
	Prune(ctx context.Context, before int64) (int64, error)
}

// ActiveOp adalah operasi yang sedang berjalan (untuk .memlog ops).
type ActiveOp struct {
	Kind    string
	Label   string
	Started time.Time
	DurMS   int64
}

type config struct {
	sample    time.Duration
	burst     time.Duration
	retention time.Duration
}

type job struct {
	sample *Sample
	event  *Event
}

var (
	sinkMu  sync.Mutex
	sink    Sink
	started atomic.Bool

	cfgMu  sync.RWMutex
	active config

	jobs     chan job
	warnMu   sync.Mutex
	warnLast time.Time

	// kicker membangunkan sampler saat operasi berat mulai/selesai, supaya
	// interval burst benar-benar dipakai (tanpa ini, operasi 15 detik yang
	// mulai di tengah jeda 60 detik tidak pernah terpotret).
	kicker chan struct{}

	// tailUntilNS adalah batas akhir mode burst (unix nano). Diperbarui dari
	// Begin/End (goroutine command), dibaca sampler — karena itu atomik, bukan
	// variabel lokal loop: operasi yang selesai sebelum sampler sempat bangun
	// tetap meninggalkan ekor burst.
	tailUntilNS atomic.Int64

	ringMu    sync.Mutex
	ring      [ringSize]Sample
	ringHead  int
	ringCount int
	last      Sample
	hasLast   bool
	peakRSS   uint64
	peakHeap  uint64
	peakChild uint64

	queued  atomic.Uint64
	dropped atomic.Uint64

	opsMu      sync.Mutex
	opsActive  = make(map[int64]*Op)
	opsSeq     int64
	opsDropped uint64
)

// Enabled melaporkan apakah perekaman aktif (sampler sudah dijalankan).
func Enabled() bool { return started.Load() }

// Op adalah satu operasi yang sedang diukur. Nil pointer aman dipakai
// (End pada Op nil tidak melakukan apa pun), sehingga instrumentasi boleh
// dipasang tanpa syarat.
type Op struct {
	id    int64
	kind  string
	name  string
	start time.Time

	totalAlloc  uint64
	heapAlloc   uint64
	heapObjects uint64
	rss         uint64
	gcCycles    uint64

	done atomic.Bool
}

// configFromEnv membaca setelan dari environment. Nilai di luar batas
// dijepit ke batas aman supaya salah ketik tidak mematikan panel.
func configFromEnv() (config, bool) {
	secs := envInt("MEMLOG_SAMPLE_SECONDS", defaultSampleSeconds)
	if secs <= 0 {
		return config{}, false
	}
	if secs < minSampleSeconds {
		secs = minSampleSeconds
	}
	burst := envInt("MEMLOG_BURST_SECONDS", defaultBurstSeconds)
	if burst < 1 {
		burst = 1
	}
	if burst > secs {
		burst = secs
	}
	days := envInt("MEMLOG_RETENTION_DAYS", defaultRetentionDays)
	if days < 1 {
		days = 1
	}
	if days > maxRetentionDays {
		days = maxRetentionDays
	}
	return config{
		sample:    time.Duration(secs) * time.Second,
		burst:     time.Duration(burst) * time.Second,
		retention: time.Duration(days) * 24 * time.Hour,
	}, true
}

func envInt(key string, def int) int {
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

// Start menyalakan sampler + penulis DB. Panggil sekali di main.go setelah
// database siap. Kalau MEMLOG_SAMPLE_SECONDS=0, fitur dimatikan total
// (Begin menjadi no-op).
func Start(ctx context.Context, s Sink) {
	cfg, on := configFromEnv()
	if !on {
		logfmt.Info("memlog: nonaktif (MEMLOG_SAMPLE_SECONDS=0).\n")
		return
	}
	if s == nil {
		logfmt.Warn("memlog: sink nil — perekaman dilewati.\n")
		return
	}
	if !started.CompareAndSwap(false, true) {
		return
	}

	sinkMu.Lock()
	sink = s
	sinkMu.Unlock()

	cfgMu.Lock()
	active = cfg
	cfgMu.Unlock()

	jobs = make(chan job, queueSize)
	kicker = make(chan struct{}, 1)

	go writerLoop(ctx)
	go samplerLoop(ctx, cfg)

	logfmt.Info("memlog: aktif — sampel %s (burst %s saat ada operasi), retensi %s.\n",
		cfg.sample, cfg.burst, cfg.retention)
}

// SnapshotNow mengambil potret memori saat ini tanpa menyimpannya.
func SnapshotNow() Sample {
	return collect(time.Now())
}

// collect merakit satu Sample dari runtime + /proc.
func collect(now time.Time) Sample {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	st := memstat.Self()
	return Sample{
		TS:           now.Unix(),
		RSS:          st.RSS,
		HeapAlloc:    ms.HeapAlloc,
		HeapSys:      ms.HeapSys,
		HeapIdle:     ms.HeapIdle,
		HeapReleased: ms.HeapReleased,
		HeapObjects:  ms.HeapObjects,
		StackInuse:   ms.StackInuse,
		GCCycles:     uint64(ms.NumGC),
		TotalAlloc:   ms.TotalAlloc,
		Goroutines:   uint64(st.Goroutines),
		CgroupUsed:   st.CgroupUsed,
		CgroupMax:    st.CgroupMax,
		CgroupKnown:  st.CgroupKnown,
		ChildRSS:     st.ChildRSS,
		ChildTop:     st.ChildTop,
		Ops:          opsSnapshot(),
	}
}

// Ring mengembalikan sampel terakhir di memori (tertua → terbaru).
func Ring() []Sample {
	ringMu.Lock()
	defer ringMu.Unlock()
	out := make([]Sample, 0, ringCount)
	if ringCount == 0 {
		return out
	}
	start := ringHead - ringCount
	if start < 0 {
		start += ringSize
	}
	for i := 0; i < ringCount; i++ {
		out = append(out, ring[(start+i)%ringSize])
	}
	return out
}

// Latest mengembalikan sampel terakhir yang tercatat di memori.
func Latest() (Sample, bool) {
	ringMu.Lock()
	defer ringMu.Unlock()
	return last, hasLast
}

// Stats melaporkan hitungan antrean tulis (untuk diagnosis .memlog now).
func Stats() (queuedTotal, droppedTotal, opsDroppedTotal uint64) {
	opsMu.Lock()
	od := opsDropped
	opsMu.Unlock()
	return queued.Load(), dropped.Load(), od
}

// --- registry operasi ---

// Begin memulai pengukuran satu operasi. Mengembalikan nil (aman dipakai)
// kalau perekaman mati.
func Begin(kind, label string) *Op {
	if !started.Load() {
		return nil
	}
	now := time.Now()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	o := &Op{
		kind:        kind,
		name:        label,
		start:       now,
		totalAlloc:  ms.TotalAlloc,
		heapAlloc:   ms.HeapAlloc,
		heapObjects: ms.HeapObjects,
		rss:         memstat.SelfRSS(),
		gcCycles:    uint64(ms.NumGC),
	}

	opsMu.Lock()
	opsSeq++
	o.id = opsSeq
	if len(opsActive) >= maxActiveOps {
		// Buang entri tertua (id terkecil = mulai paling awal).
		var oldest int64
		for id := range opsActive {
			if oldest == 0 || id < oldest {
				oldest = id
			}
		}
		delete(opsActive, oldest)
		opsDropped++
	}
	opsActive[o.id] = o
	opsMu.Unlock()
	kick()
	return o
}

// kick membangunkan sampler supaya memotret keadaan sekarang (non-blocking:
// satu sinyal cukup, dan kalau sampler sedang sibuk sinyalnya dibuang).
// Sekaligus memperpanjang ekor burst: operasi boleh selesai sebelum sampler
// bangun, tapi 30 detik sesudahnya tetap disampel rapat.
func kick() {
	extendTail(time.Now())
	if kicker == nil {
		return
	}
	select {
	case kicker <- struct{}{}:
	default:
	}
}

// extendTail memperpanjang mode burst sampai burstTail dari sekarang.
func extendTail(now time.Time) {
	deadline := now.Add(burstTail).UnixNano()
	for {
		cur := tailUntilNS.Load()
		if cur >= deadline || tailUntilNS.CompareAndSwap(cur, deadline) {
			return
		}
	}
}

// inBurst melaporkan apakah sekarang masih di ekor burst.
func inBurst(now time.Time) bool { return now.UnixNano() < tailUntilNS.Load() }

// End menyelesaikan pengukuran dan mengirim satu baris Event ke sink.
// Idempoten: pemanggilan kedua diabaikan (aman untuk defer + explicit call).
func (o *Op) End(ok bool, note string) {
	if o == nil || !o.done.CompareAndSwap(false, true) {
		return
	}

	opsMu.Lock()
	delete(opsActive, o.id)
	opsMu.Unlock()

	now := time.Now()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	ev := Event{
		TS:    o.start.Unix(),
		EndTS: now.Unix(),
		Kind:  o.kind,
		Label: o.name,
		DurMS: now.Sub(o.start).Milliseconds(),
		Alloc: int64(ms.TotalAlloc) - int64(o.totalAlloc),
		HeapD: int64(ms.HeapAlloc) - int64(o.heapAlloc),
		RSSD:  int64(memstat.SelfRSS()) - int64(o.rss),
		OK:    ok,
		Note:  note,
	}
	if uint64(ms.NumGC) != o.gcCycles {
		// GC berjalan selama operasi: delta heap/RSS tidak bisa dipakai
		// sebagai angka pasti, jadi ditandai supaya tidak salah tafsir.
		ev.Note = strings.TrimSpace(ev.Note + " ~gc")
	}
	enqueue(job{event: &ev})
	// Sinyal ke sampler: keadaan SESUDAH operasi (heap tertahan, RSS anak
	// yang belum turun) adalah data yang paling sering ditanyakan owner.
	kick()
}
