package plugins

import (
	"sync"
	"time"
)

// pending.go — satu implementasi peta pilihan-sementara (pending pick) yang
// dipakai ulang oleh .lrc/.lrcs, .shazam, .sp, dan "unduh" Shazam.
//
// Sebelumnya tiap modul menyalin pola mutex + map + TTL (Key/Set/Peek/
// Consume + sapu entri basi) — empat salinan yang rawan menyimpang. Kini
// mekaniknya hidup di sini; modul cukup menyimpan tipe hasilnya masing-masing.

// pendingEntry adalah satu nilai tersimpan plus metadata waktunya.
type pendingEntry[T any] struct {
	val T
	at  time.Time // kapan disimpan (untuk aturan "pending terbaru menang")
	exp time.Time // kapan kedaluwarsa
}

// pendingStore adalah peta pending keyed oleh pendingKey(chat, sender),
// aman untuk akses goroutine ganda.
type pendingStore[T any] struct {
	mu  sync.Mutex
	m   map[string]pendingEntry[T]
	ttl time.Duration
}

// newPendingStore membuat store kosong dengan umur entri ttl.
func newPendingStore[T any](ttl time.Duration) *pendingStore[T] {
	return &pendingStore[T]{m: make(map[string]pendingEntry[T]), ttl: ttl}
}

// Set menyimpan val untuk pasangan chat|sender, sekaligus menyapu entri basi
// secara oportunistik supaya map tidak tumbuh tanpa batas.
func (s *pendingStore[T]) Set(chat, sender string, val T) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for k, e := range s.m {
		if now.After(e.exp) {
			delete(s.m, k)
		}
	}
	s.m[pendingKey(chat, sender)] = pendingEntry[T]{val: val, at: now, exp: now.Add(s.ttl)}
}

// Peek mengembalikan nilai tersimpan + waktu simpannya TANPA menghapus.
// ok=false bila kosong atau sudah basi.
func (s *pendingStore[T]) Peek(chat, sender string) (val T, at time.Time, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, found := s.m[pendingKey(chat, sender)]
	if !found || time.Now().After(e.exp) {
		return val, time.Time{}, false
	}
	return e.val, e.at, true
}

// Consume mengambil + menghapus (sekali pakai). ok=false bila kosong/basi.
func (s *pendingStore[T]) Consume(chat, sender string) (val T, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := pendingKey(chat, sender)
	e, found := s.m[k]
	if !found {
		return val, false
	}
	delete(s.m, k)
	if time.Now().After(e.exp) {
		return val, false
	}
	return e.val, true
}
