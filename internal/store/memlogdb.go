package store

// memlogdb.go — penyimpanan riwayat memori (data/memlog.db).
//
// File terpisah dari ai.db/group.db supaya telemetri yang dibuang berkala
// tidak menumpuk di DB operasional dan tidak ikut membengkakkan backup.
// Implementasi memlog.Sink ada di sini (bukan di internal/memlog) supaya
// paket memlog tetap leaf dan bisa dipanggil dari internal/heavy/external
// tanpa import cycle.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"wabot-go/internal/memlog"
)

// MemLogDB membungkus koneksi data/memlog.db.
type MemLogDB struct {
	db *sql.DB
}

var memLogDB *MemLogDB

// InitMemLog membuka (atau membuat) data/memlog.db beserta tabelnya.
// Dipanggil sekali di main.go; fail-soft di pemanggil (telemetri tidak boleh
// membunuh bot).
func InitMemLog(path string) error {
	db, err := openSQLite(path)
	if err != nil {
		return fmt.Errorf("gagal membuka memlog.db: %w", err)
	}
	schema := `
	CREATE TABLE IF NOT EXISTS mem_samples (
		ts            INTEGER PRIMARY KEY,
		rss           INTEGER NOT NULL DEFAULT 0,
		heap_alloc    INTEGER NOT NULL DEFAULT 0,
		heap_sys      INTEGER NOT NULL DEFAULT 0,
		heap_idle     INTEGER NOT NULL DEFAULT 0,
		heap_released INTEGER NOT NULL DEFAULT 0,
		heap_objects  INTEGER NOT NULL DEFAULT 0,
		stack_inuse   INTEGER NOT NULL DEFAULT 0,
		gc_cycles     INTEGER NOT NULL DEFAULT 0,
		total_alloc   INTEGER NOT NULL DEFAULT 0,
		goroutines    INTEGER NOT NULL DEFAULT 0,
		cg_used       INTEGER NOT NULL DEFAULT 0,
		cg_max        INTEGER NOT NULL DEFAULT 0,
		child_rss     INTEGER NOT NULL DEFAULT 0,
		child_top     TEXT NOT NULL DEFAULT '',
		ops           TEXT NOT NULL DEFAULT ''
	);
	CREATE TABLE IF NOT EXISTS mem_events (
		id      INTEGER PRIMARY KEY AUTOINCREMENT,
		ts      INTEGER NOT NULL,
		end_ts  INTEGER NOT NULL DEFAULT 0,
		kind    TEXT NOT NULL DEFAULT '',
		label   TEXT NOT NULL DEFAULT '',
		dur_ms  INTEGER NOT NULL DEFAULT 0,
		alloc   INTEGER NOT NULL DEFAULT 0,
		heap_d  INTEGER NOT NULL DEFAULT 0,
		rss_d   INTEGER NOT NULL DEFAULT 0,
		ok      INTEGER NOT NULL DEFAULT 1,
		note    TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX IF NOT EXISTS idx_memev_ts ON mem_events(ts);
	`
	if err := applySchema(db, "memlog.db", schema, nil, memlogSchemaVersion); err != nil {
		_ = db.Close()
		return err
	}
	memLogDB = &MemLogDB{db: db}
	return nil
}

// MemLog mengembalikan handle DB telemetri (nil bila init gagal).
func MemLog() *MemLogDB { return memLogDB }

// CloseMemLog menutup koneksi (dipakai tes).
func CloseMemLog() {
	if memLogDB != nil && memLogDB.db != nil {
		_ = memLogDB.db.Close()
	}
	memLogDB = nil
}

// --- implementasi memlog.Sink ---

// SaveSample menyimpan satu sampel memori (idempoten per detik).
func (m *MemLogDB) SaveSample(ctx context.Context, s memlog.Sample) error {
	if m == nil || m.db == nil {
		return nil
	}
	_, err := m.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO mem_samples
			(ts, rss, heap_alloc, heap_sys, heap_idle, heap_released, heap_objects,
			 stack_inuse, gc_cycles, total_alloc, goroutines, cg_used, cg_max,
			 child_rss, child_top, ops)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		s.TS, s.RSS, s.HeapAlloc, s.HeapSys, s.HeapIdle, s.HeapReleased, s.HeapObjects,
		s.StackInuse, s.GCCycles, s.TotalAlloc, s.Goroutines, s.CgroupUsed, s.CgroupMax,
		s.ChildRSS, s.ChildTop, s.Ops)
	return err
}

// SaveEvent menyimpan satu event operasi (atau spike).
func (m *MemLogDB) SaveEvent(ctx context.Context, e memlog.Event) error {
	if m == nil || m.db == nil {
		return nil
	}
	ok := 0
	if e.OK {
		ok = 1
	}
	_, err := m.db.ExecContext(ctx, `
		INSERT INTO mem_events (ts, end_ts, kind, label, dur_ms, alloc, heap_d, rss_d, ok, note)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		e.TS, e.EndTS, e.Kind, e.Label, e.DurMS, e.Alloc, e.HeapD, e.RSSD, ok, e.Note)
	return err
}

// Prune membuang sampel & event yang lebih tua dari before (unix detik).
func (m *MemLogDB) Prune(ctx context.Context, before int64) (int64, error) {
	if m == nil || m.db == nil {
		return 0, nil
	}
	res, err := m.db.ExecContext(ctx, `DELETE FROM mem_samples WHERE ts < ?`, before)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	res, err = m.db.ExecContext(ctx, `DELETE FROM mem_events WHERE ts < ?`, before)
	if err != nil {
		return n, err
	}
	n2, _ := res.RowsAffected()
	return n + n2, nil
}

// --- query untuk command .memlog ---

// SamplesBetween membaca sampel pada rentang [from, to), urut lama → baru.
func (m *MemLogDB) SamplesBetween(ctx context.Context, from, to int64) ([]memlog.Sample, error) {
	if m == nil || m.db == nil {
		return nil, nil
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT ts, rss, heap_alloc, heap_sys, heap_idle, heap_released, heap_objects,
		       stack_inuse, gc_cycles, total_alloc, goroutines, cg_used, cg_max,
		       child_rss, child_top, ops
		FROM mem_samples WHERE ts >= ? AND ts < ? ORDER BY ts ASC`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []memlog.Sample
	for rows.Next() {
		var s memlog.Sample
		if err := rows.Scan(&s.TS, &s.RSS, &s.HeapAlloc, &s.HeapSys, &s.HeapIdle,
			&s.HeapReleased, &s.HeapObjects, &s.StackInuse, &s.GCCycles, &s.TotalAlloc,
			&s.Goroutines, &s.CgroupUsed, &s.CgroupMax, &s.ChildRSS, &s.ChildTop, &s.Ops); err != nil {
			return nil, err
		}
		s.CgroupKnown = s.CgroupMax > 0
		out = append(out, s)
	}
	return out, rows.Err()
}

// LastSample mengambil satu sampel terakhir yang tersimpan.
func (m *MemLogDB) LastSample(ctx context.Context) (memlog.Sample, bool, error) {
	if m == nil || m.db == nil {
		return memlog.Sample{}, false, nil
	}
	row := m.db.QueryRowContext(ctx, `
		SELECT ts, rss, heap_alloc, heap_sys, heap_idle, heap_released, heap_objects,
		       stack_inuse, gc_cycles, total_alloc, goroutines, cg_used, cg_max,
		       child_rss, child_top, ops
		FROM mem_samples ORDER BY ts DESC LIMIT 1`)
	var s memlog.Sample
	if err := row.Scan(&s.TS, &s.RSS, &s.HeapAlloc, &s.HeapSys, &s.HeapIdle,
		&s.HeapReleased, &s.HeapObjects, &s.StackInuse, &s.GCCycles, &s.TotalAlloc,
		&s.Goroutines, &s.CgroupUsed, &s.CgroupMax, &s.ChildRSS, &s.ChildTop, &s.Ops); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return memlog.Sample{}, false, nil
		}
		return memlog.Sample{}, false, err
	}
	s.CgroupKnown = s.CgroupMax > 0
	return s, true, nil
}

// EventsBetween membaca event pada rentang [from, to), urut lama → baru.
func (m *MemLogDB) EventsBetween(ctx context.Context, from, to int64, limit int) ([]memlog.Event, error) {
	if m == nil || m.db == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 500
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT ts, end_ts, kind, label, dur_ms, alloc, heap_d, rss_d, ok, note
		FROM mem_events WHERE ts >= ? AND ts < ? ORDER BY ts ASC LIMIT ?`, from, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []memlog.Event
	for rows.Next() {
		var e memlog.Event
		var ok int
		if err := rows.Scan(&e.TS, &e.EndTS, &e.Kind, &e.Label, &e.DurMS, &e.Alloc,
			&e.HeapD, &e.RSSD, &ok, &e.Note); err != nil {
			return nil, err
		}
		e.OK = ok != 0
		out = append(out, e)
	}
	return out, rows.Err()
}

// MemLogHour adalah agregat satu jam (dipakai .memlog day).
type MemLogHour struct {
	Hour     int64
	N        int
	RSSMax   uint64
	RSSMin   uint64
	RSSAvg   uint64
	HeapMax  uint64
	ObjMax   uint64
	ChildMax uint64
}

// HourBuckets mengagregasi sampel per jam pada rentang [from, to).
// Bucket memakai batas jam UTC (ts/3600); tampilan mengonversinya ke zona bot,
// jadi untuk zona ber-offset jam bulat (WIB/WITA/WIT) batasnya pas.
func (m *MemLogDB) HourBuckets(ctx context.Context, from, to int64) ([]MemLogHour, error) {
	if m == nil || m.db == nil {
		return nil, nil
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT (ts/3600)*3600 AS bucket, COUNT(*), MAX(rss), MIN(rss), AVG(rss),
		       MAX(heap_alloc), MAX(heap_objects), MAX(child_rss)
		FROM mem_samples WHERE ts >= ? AND ts < ?
		GROUP BY bucket ORDER BY bucket ASC`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []MemLogHour
	for rows.Next() {
		var h MemLogHour
		var avg float64
		if err := rows.Scan(&h.Hour, &h.N, &h.RSSMax, &h.RSSMin, &avg,
			&h.HeapMax, &h.ObjMax, &h.ChildMax); err != nil {
			return nil, err
		}
		if avg > 0 {
			h.RSSAvg = uint64(avg)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// MemLogOp adalah agregat satu jenis operasi (dipakai .memlog top).
type MemLogOp struct {
	Kind       string
	Label      string
	N          int
	AllocTotal int64
	AllocAvg   int64
	AllocMax   int64
	DurAvgMS   int64
	RSSDMax    int64
	LastTS     int64
}

// TopOps mengurutkan jenis operasi berdasarkan total alokasi pada rentang.
func (m *MemLogDB) TopOps(ctx context.Context, from, to int64, limit int) ([]MemLogOp, error) {
	if m == nil || m.db == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 5
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT kind, label, COUNT(*), SUM(alloc), MAX(alloc), AVG(dur_ms), MAX(rss_d), MAX(ts)
		FROM mem_events
		WHERE ts >= ? AND ts < ? AND kind <> 'spike'
		GROUP BY kind, label
		ORDER BY SUM(alloc) DESC LIMIT ?`, from, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []MemLogOp
	for rows.Next() {
		var o MemLogOp
		var sum int64
		var avgDur float64
		if err := rows.Scan(&o.Kind, &o.Label, &o.N, &sum, &o.AllocMax, &avgDur,
			&o.RSSDMax, &o.LastTS); err != nil {
			return nil, err
		}
		o.AllocTotal = sum
		if o.N > 0 {
			o.AllocAvg = sum / int64(o.N)
		}
		o.DurAvgMS = int64(avgDur)
		out = append(out, o)
	}
	return out, rows.Err()
}

// Spikes mengembalikan lonjakan terbesar (|delta heap|) pada rentang.
func (m *MemLogDB) Spikes(ctx context.Context, from, to int64, limit int) ([]memlog.Event, error) {
	if m == nil || m.db == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 5
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT ts, end_ts, kind, label, dur_ms, alloc, heap_d, rss_d, ok, note
		FROM mem_events
		WHERE ts >= ? AND ts < ? AND kind = 'spike'
		ORDER BY ABS(heap_d) + ABS(rss_d) DESC LIMIT ?`, from, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []memlog.Event
	for rows.Next() {
		var e memlog.Event
		var ok int
		if err := rows.Scan(&e.TS, &e.EndTS, &e.Kind, &e.Label, &e.DurMS, &e.Alloc,
			&e.HeapD, &e.RSSD, &ok, &e.Note); err != nil {
			return nil, err
		}
		e.OK = ok != 0
		out = append(out, e)
	}
	return out, rows.Err()
}

// MemLogTrend membandingkan dua jendela 24 jam (median, bukan rata-rata,
// supaya satu spike tidak menggeser kesimpulan).
type MemLogTrend struct {
	NowHeap  uint64
	PrevHeap uint64
	NowObj   uint64
	PrevObj  uint64
	NowRSS   uint64
	PrevRSS  uint64
	NowN     int
	PrevN    int
}

// Trend menghitung tren memori: jendela 24 jam terakhir vs 24 jam sebelumnya.
func (m *MemLogDB) Trend(ctx context.Context, now time.Time) (MemLogTrend, error) {
	var t MemLogTrend
	if m == nil || m.db == nil {
		return t, nil
	}
	nowUnix := now.Unix()
	day := int64(24 * 60 * 60)
	curFrom, curTo := nowUnix-day, nowUnix+1
	prevFrom, prevTo := nowUnix-2*day, nowUnix-day

	var err error
	if t.NowHeap, t.NowN, err = m.median(ctx, "heap_alloc", curFrom, curTo); err != nil {
		return t, err
	}
	if t.PrevHeap, t.PrevN, err = m.median(ctx, "heap_alloc", prevFrom, prevTo); err != nil {
		return t, err
	}
	if t.NowObj, _, err = m.median(ctx, "heap_objects", curFrom, curTo); err != nil {
		return t, err
	}
	if t.PrevObj, _, err = m.median(ctx, "heap_objects", prevFrom, prevTo); err != nil {
		return t, err
	}
	if t.NowRSS, _, err = m.median(ctx, "rss", curFrom, curTo); err != nil {
		return t, err
	}
	if t.PrevRSS, _, err = m.median(ctx, "rss", prevFrom, prevTo); err != nil {
		return t, err
	}
	return t, nil
}

// median menghitung nilai tengah satu kolom pada rentang. Kolom hanya boleh
// berasal dari daftar tetap (bukan input pengguna) — dipilih di Trend.
func (m *MemLogDB) median(ctx context.Context, col string, from, to int64) (uint64, int, error) {
	var n int
	if err := m.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM mem_samples WHERE ts >= ? AND ts < ?`, from, to).Scan(&n); err != nil {
		return 0, 0, err
	}
	if n == 0 {
		return 0, 0, nil
	}
	q := fmt.Sprintf(`SELECT %s FROM mem_samples WHERE ts >= ? AND ts < ? ORDER BY %s LIMIT 1 OFFSET ?`, col, col)
	var v uint64
	if err := m.db.QueryRowContext(ctx, q, from, to, n/2).Scan(&v); err != nil {
		return 0, n, err
	}
	return v, n, nil
}

// SizeBytes melaporkan ukuran file DB telemetri (page_count × page_size).
func (m *MemLogDB) SizeBytes(ctx context.Context) (int64, error) {
	if m == nil || m.db == nil {
		return 0, nil
	}
	var pages, pageSize int64
	if err := m.db.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pages); err != nil {
		return 0, err
	}
	if err := m.db.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize); err != nil {
		return 0, err
	}
	return pages * pageSize, nil
}
