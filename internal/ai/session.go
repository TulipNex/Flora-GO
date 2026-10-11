package ai

import (
	"context"
	"sync"
	"time"

	"wabot-go/internal/store"
)

// IdleTimeout adalah lama waktu tanpa interaksi sebelum sesi dianggap
// "selesai": trigger reply/stiker mati, dan penyapu latar meringkas
// riwayatnya (opsi a yang disetujui owner — selaras dengan definisi sesi
// "mulai chat s.d. diam 10 menit"). Fase 2: hardcode 30 menit dulu — bisa
// dipindah ke config kalau nanti perlu diubah tanpa recompile.
const IdleTimeout = 10 * time.Minute

// historyLoadLimit adalah jumlah entri histori terakhir yang di-re-hydrate
// dari SQLite saat sesi belum ada di cache in-memory (misal baru setelah
// bot restart). Dibatasi supaya tidak mengirim histori tak terbatas ke
// provider tiap request (boros token).
const historyLoadLimit = 20

// BuildSessionKey membangun kunci sesi percakapan AI yang membedakan
// konteks private dan grup, supaya histori tidak tercampur antara chat
// private seseorang dengan chat mereka di dalam sebuah grup.
//
//   - Private (isGroup == false): kunci = senderJID saja, perilaku sama
//     seperti sebelumnya (satu histori per orang untuk semua chat private).
//   - Grup (isGroup == true): kunci = "<chatJID>:<senderJID>", sehingga
//     tiap orang tetap punya histori terpisah miliknya sendiri, TAPI
//     terpisah juga dari histori private-nya dan dari histori dia di
//     grup lain. Ini bukan "satu ingatan bersama untuk seluruh grup" —
//     tiap anggota grup tetap dilayani sebagai sesi masing-masing.
//
// Dipakai konsisten oleh plugins/ai.go (command & trigger AI) dan
// internal/handler/handler.go (trigger natural), supaya kunci sesi yang
// dibangun untuk pesan yang sama selalu identik di kedua jalur.
func BuildSessionKey(isGroup bool, chatJID, senderJID string) string {
	if !isGroup {
		return senderJID
	}
	return chatJID + ":" + senderJID
}

type sessionEntry struct {
	history      []Content
	lastActiveAt time.Time
	// summarizing true selama goroutine peringkas sesi ini berjalan —
	// mencegah penyapu memicu ganda untuk sesi yang sama.
	summarizing bool
}

// SessionStore adalah penyimpanan histori percakapan per key.
// Cache in-memory adalah sumber kebenaran SELAMA bot berjalan (cepat,
// tidak perlu query tiap giliran); SQLite (lewat store.AI()) adalah
// tempat persist supaya histori survive restart. Re-hydrate terjadi
// otomatis sekali per key saat pertama kali diakses setelah restart.
type SessionStore struct {
	mu       sync.Mutex
	sessions map[string]*sessionEntry
}

// GlobalSession adalah instance session store yang dipakai bot.
var GlobalSession = &SessionStore{
	sessions: make(map[string]*sessionEntry),
}

// Get mengambil histori aktif untuk key tertentu.
// Kalau sesi sudah idle terlalu lama, histori dianggap kosong
// (reset otomatis, TERMASUK di SQLite) meski entry-nya masih ada di memori.
// Kalau key belum pernah ada di cache (mis. bot baru restart), coba
// re-hydrate dari SQLite dulu sebelum dianggap sesi baru sepenuhnya.
func (s *SessionStore) Get(key string) []Content {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.sessions[key]
	if !ok {
		entry = s.hydrateFromDB(key)
		s.sessions[key] = entry
	}

	if time.Since(entry.lastActiveAt) > IdleTimeout {
		if len(entry.history) > 0 {
			_ = store.AI().ClearHistory(context.Background(), key)
		}
		entry.history = nil
	}

	return append([]Content{}, entry.history...)
}

// hydrateFromDB membaca histori tersimpan dari SQLite untuk key yang
// belum ada di cache. Kegagalan baca DB tidak dianggap fatal — sesi
// dianggap mulai kosong (lebih aman daripada bot berhenti berfungsi).
//
// Fase 6: lastActiveAt sekarang diambil dari CreatedAt entri TERAKHIR
// yang sungguhan tersimpan di SQLite, bukan time.Now() seperti sebelumnya.
// Ini memperbaiki akurasi idle-timeout tepat setelah bot restart — sesi
// yang sudah idle 25 menit sebelum restart tetap dianggap idle 25 menit
// setelah restart, bukan dianggap "baru saja aktif".
func (s *SessionStore) hydrateFromDB(key string) *sessionEntry {
	entries, err := store.AI().LoadHistory(context.Background(), key, historyLoadLimit)
	if err != nil || len(entries) == 0 {
		return &sessionEntry{}
	}

	history := make([]Content, 0, len(entries))
	for _, e := range entries {
		history = append(history, Content{
			Role:  e.Role,
			Parts: []Part{{Text: e.Content}},
		})
	}

	lastActiveAt := entries[len(entries)-1].CreatedAt
	return &sessionEntry{history: history, lastActiveAt: lastActiveAt}
}

// HasActive mengecek apakah sender ini punya sesi yang masih aktif
// (pernah berinteraksi dan belum melewati idle timeout).
// Dipakai oleh trigger detector untuk memutuskan apakah reply/sticker
// tanpa kata kunci boleh memicu AI.
func (s *SessionStore) HasActive(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.sessions[key]
	if !ok {
		return false
	}
	return time.Since(entry.lastActiveAt) <= IdleTimeout
}

// Append menambahkan satu giliran percakapan baru ke cache in-memory
// SEKALIGUS ke SQLite (supaya survive restart), dan memperbarui waktu
// aktif terakhir (reset hitungan idle timeout).
func (s *SessionStore) Append(key string, c Content) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.sessions[key]
	if !ok {
		entry = &sessionEntry{}
		s.sessions[key] = entry
	}
	entry.history = append(entry.history, c)
	entry.lastActiveAt = time.Now()

	text := ""
	for _, p := range c.Parts {
		text += p.Text
	}
	_ = store.AI().AppendHistory(context.Background(), key, c.Role, text)
}

// Reset menghapus sesi sepenuhnya dari cache DAN dari SQLite
// (dipakai command ".agent reset") — termasuk ringkasan FIFO-nya
// (lupa total = ringkasan ikut hilang).
func (s *SessionStore) Reset(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.sessions, key)
	_ = store.AI().ClearHistory(context.Background(), key)
	_ = store.AI().ClearSessionSummaries(context.Background(), key)
}
