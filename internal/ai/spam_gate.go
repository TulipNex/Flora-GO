package ai

import (
	"sync"
	"time"
)

// MinRequestInterval adalah jeda minimal antar request AI dari sender yang
// sama. Request yang datang lebih cepat dari ini dianggap spam dan diabaikan
// (tidak diproses, tidak membalas apa-apa) — mencegah AI "nyasar" merespons
// pesan beruntun yang sebenarnya satu maksud yang sama, dan menghemat kuota
// API untuk skala pemakaian personal/small-circle.
const MinRequestInterval = 3 * time.Second

type spamGate struct {
	mu          sync.Mutex
	lastRequest map[string]time.Time
}

var globalSpamGate = &spamGate{
	lastRequest: make(map[string]time.Time),
}

// IsSpam mengecek apakah sender ini mengirim request terlalu cepat sejak
// request sebelumnya. Kalau BUKAN spam, otomatis mencatat waktu sekarang
// sebagai request terbaru (jadi pemanggil tidak perlu memanggil fungsi
// pencatatan terpisah).
func IsSpam(senderKey string) bool {
	globalSpamGate.mu.Lock()
	defer globalSpamGate.mu.Unlock()

	last, ok := globalSpamGate.lastRequest[senderKey]
	now := time.Now()
	if ok && now.Sub(last) < MinRequestInterval {
		return true
	}
	globalSpamGate.lastRequest[senderKey] = now
	return false
}

// --- Lock per-sender ---
//
// senderLocks menyimpan satu *sync.Mutex per sender, dibuat on-demand.
// Ini mencegah race condition kalau (secara teori) dua pesan dari sender
// yang sama diproses hampir bersamaan (mis. reply cepat dua kali beruntun
// sebelum balasan pertama selesai) — tanpa lock ini, keduanya bisa saling
// menimpa saat baca-lalu-tulis ke SessionStore.
var (
	senderLocksMu sync.Mutex
	senderLocks   = make(map[string]*sync.Mutex)
)

func getSenderLock(senderKey string) *sync.Mutex {
	senderLocksMu.Lock()
	defer senderLocksMu.Unlock()

	lock, ok := senderLocks[senderKey]
	if !ok {
		lock = &sync.Mutex{}
		senderLocks[senderKey] = lock
	}
	return lock
}

// WithSenderLock menjalankan fn hanya setelah mendapat lock eksklusif untuk
// senderKey tersebut, memastikan giliran percakapan sender yang sama tidak
// pernah diproses tumpang tindih.
func WithSenderLock(senderKey string, fn func() (string, error)) (string, error) {
	lock := getSenderLock(senderKey)
	lock.Lock()
	defer lock.Unlock()
	return fn()
}
