package memlog

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"wabot-go/internal/logfmt"
)

// samplerLoop mengambil sampel berkala. Interval dasarnya lambat (60 dtk),
// tapi selama ada operasi berat berjalan sampler turun ke interval burst
// (5 dtk) dan bertahan burstTail setelah operasi terakhir selesai — dengan
// begitu lonjakan singkat tetap terekam tanpa membayar resolusi tinggi
// sepanjang hari. Sinyal kick dari Begin/End membuat operasi yang mulai di
// tengah jeda panjang tetap terpotret (dibatasi minimal selebar burst).
func samplerLoop(ctx context.Context, cfg config) {
	timer := time.NewTimer(0) // sampel pertama langsung saat start
	defer timer.Stop()

	var lastAt time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-kicker:
			// Operasi mulai/selesai. Ambil sampel lebih cepat, tapi jangan
			// pernah lebih rapat dari interval burst supaya burst ramai
			// tidak berubah jadi sampler tanpa jeda.
			if !lastAt.IsZero() && time.Since(lastAt) < cfg.burst {
				continue
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}

		now := time.Now()
		s := collect(now)
		prev, hasPrev := record(s)
		enqueue(job{sample: &s})
		if ev, ok := detectSpike(s, prev, hasPrev); ok {
			enqueue(job{event: &ev})
		}
		lastAt = now

		if len(ActiveOps()) > 0 {
			extendTail(now)
		}
		next := cfg.sample
		if inBurst(now) {
			next = cfg.burst
		}
		timer.Reset(next)
	}
}

// record menyimpan sampel ke ring + puncak, dan mengembalikan sampel
// sebelumnya untuk deteksi lonjakan.
func record(s Sample) (Sample, bool) {
	ringMu.Lock()
	defer ringMu.Unlock()

	prev, hasPrev := last, hasLast
	ring[ringHead] = s
	ringHead = (ringHead + 1) % ringSize
	if ringCount < ringSize {
		ringCount++
	}
	last, hasLast = s, true
	if s.RSS > peakRSS {
		peakRSS = s.RSS
	}
	if s.HeapAlloc > peakHeap {
		peakHeap = s.HeapAlloc
	}
	if s.ChildRSS > peakChild {
		peakChild = s.ChildRSS
	}
	return prev, hasPrev
}

// Peaks mengembalikan puncak RSS, heap alloc, dan RSS anak sejak proses
// start (di memori; hilang saat restart).
func Peaks() (rss, heap, child uint64) {
	ringMu.Lock()
	defer ringMu.Unlock()
	return peakRSS, peakHeap, peakChild
}

// detectSpike membuat satu baris Event kalau kenaikan antar sampel melewati
// ambang. Event ini yang membuat lonjakan bisa dicari tanpa memindai seluruh
// deret sampel.
func detectSpike(cur, prev Sample, hasPrev bool) (Event, bool) {
	if !hasPrev {
		return Event{}, false
	}
	dh := int64(cur.HeapAlloc) - int64(prev.HeapAlloc)
	dr := int64(cur.RSS) - int64(prev.RSS)
	dc := int64(cur.ChildRSS) - int64(prev.ChildRSS)
	if dh < spikeHeapDelta && dr < spikeRSSDelta && dc < spikeChildDelta {
		return Event{}, false
	}
	label := strings.TrimSpace(cur.Ops)
	if label == "" {
		label = "idle"
	}
	span := cur.TS - prev.TS
	if span < 1 {
		span = 1
	}
	return Event{
		TS:    prev.TS,
		EndTS: cur.TS,
		Kind:  "spike",
		Label: label,
		DurMS: span * 1000,
		Alloc: dh,
		HeapD: dh,
		RSSD:  dr,
		OK:    true,
		Note:  fmt.Sprintf("heap %s rss %s anak %s", signedMB(dh), signedMB(dr), signedMB(dc)),
	}, true
}

func signedMB(b int64) string {
	return fmt.Sprintf("%+.1fMB", float64(b)/(1<<20))
}

// writerLoop menulis antrean ke sink (SQLite) di luar goroutine command,
// supaya satu INSERT yang lambat tidak menahan handler. Antrean penuh =
// dibuang (dihitung), bukan diblokir.
func writerLoop(ctx context.Context) {
	prune := time.NewTicker(pruneEvery)
	defer prune.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-prune.C:
			s := currentSink()
			if s == nil {
				continue
			}
			cfgMu.RLock()
			retention := active.retention
			cfgMu.RUnlock()
			pctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			if _, err := s.Prune(pctx, time.Now().Add(-retention).Unix()); err != nil {
				warnThrottled(err)
			}
			cancel()
		case j := <-jobs:
			s := currentSink()
			if s == nil {
				continue
			}
			wctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			var err error
			switch {
			case j.sample != nil:
				err = s.SaveSample(wctx, *j.sample)
			case j.event != nil:
				err = s.SaveEvent(wctx, *j.event)
			}
			cancel()
			if err != nil {
				warnThrottled(err)
			}
		}
	}
}

func enqueue(j job) {
	if jobs == nil {
		return
	}
	select {
	case jobs <- j:
		queued.Add(1)
	default:
		dropped.Add(1)
	}
}

func currentSink() Sink {
	sinkMu.Lock()
	defer sinkMu.Unlock()
	return sink
}

// warnThrottled mencatat error sink maksimal sekali per 5 menit: DB yang
// sedang sibuk tidak boleh membanjiri log panel.
func warnThrottled(err error) {
	warnMu.Lock()
	if time.Since(warnLast) < 5*time.Minute {
		warnMu.Unlock()
		return
	}
	warnLast = time.Now()
	warnMu.Unlock()
	logfmt.Warn("memlog: gagal menulis sampel/event: %v\n", err)
}

// opsSnapshot merangkum operasi aktif menjadi "kind/name" yang dipisah koma,
// diurutkan supaya nilai tiap sampel stabil dan bisa dibandingkan.
func opsSnapshot() string {
	ops := ActiveOps()
	if len(ops) == 0 {
		return ""
	}
	parts := make([]string, 0, len(ops))
	for _, o := range ops {
		if o.Label == "" {
			parts = append(parts, o.Kind)
			continue
		}
		parts = append(parts, o.Kind+"/"+o.Label)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// ActiveOps mengembalikan operasi yang sedang berjalan (urut waktu mulai).
func ActiveOps() []ActiveOp {
	opsMu.Lock()
	defer opsMu.Unlock()
	if len(opsActive) == 0 {
		return nil
	}
	now := time.Now()
	out := make([]ActiveOp, 0, len(opsActive))
	for _, o := range opsActive {
		out = append(out, ActiveOp{
			Kind:    o.kind,
			Label:   o.name,
			Started: o.start,
			DurMS:   now.Sub(o.start).Milliseconds(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	return out
}
