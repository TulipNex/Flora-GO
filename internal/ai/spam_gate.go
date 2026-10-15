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
// senderLocks menyimpan satu lock per sender, dibuat on-demand.
// Ini mencegah race condition kalau (secara teori) dua pesan dari sender
// yang sama diproses hampir bersamaan (mis. reply cepat dua kali beruntun
// sebelum balasan pertama selesai) — tanpa lock ini, keduanya bisa saling
// menimpa saat baca-lalu-tulis ke SessionStore.
//
// Lock berbasis channel berkapasitas 1 (bukan sync.Mutex) supaya ada operasi
// non-blocking TrySenderLock: pesan yang datang saat sesi masih diproses
// langsung ditolak diam-diam, tidak antre serial yang menumpuk LLM call +
// bubble balasan berurutan.
type senderLock struct {
	ch chan struct{}
}

var (
	senderLocksMu sync.Mutex
	senderLocks   = make(map[string]*senderLock)
)

func getSenderLock(senderKey string) *senderLock {
	senderLocksMu.Lock()
	defer senderLocksMu.Unlock()

	lock, ok := senderLocks[senderKey]
	if !ok {
		lock = &senderLock{ch: make(chan struct{}, 1)}
		lock.ch <- struct{}{}
		senderLocks[senderKey] = lock
	}
	return lock
}

// WithSenderLock menjalankan fn hanya setelah mendapat lock eksklusif untuk
// senderKey tersebut, memastikan giliran percakapan sender yang sama tidak
// pernah diproses tumpang tindih. Versi blocking (menunggu); dipertahankan
// untuk kompatibilitas — jalur utama HandleAIMessage memakai TrySenderLock.
func WithSenderLock(senderKey string, fn func() (string, error)) (string, error) {
	lock := getSenderLock(senderKey)
	<-lock.ch
	defer func() { lock.ch <- struct{}{} }()
	return fn()
}

// TrySenderLock mencoba mendapat lock eksklusif untuk senderKey TANPA menunggu.
// ok=false berarti sesi ini sedang diproses — pemanggil wajib kembali
// diam-diam (tanpa LLM call, tanpa bubble, tanpa error) supaya burst pesan
// tidak antre serial.
func TrySenderLock(senderKey string, fn func() (string, error)) (result string, ok bool, err error) {
	lock := getSenderLock(senderKey)
	select {
	case <-lock.ch:
		defer func() { lock.ch <- struct{}{} }()
		result, err = fn()
		return result, true, err
	default:
		return "", false, nil
	}
}
