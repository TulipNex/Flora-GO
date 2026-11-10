// Package diag: pelepasan memori OS secara idle (opsional).
//
// Fitur ini MATI secara default. Aktif hanya bila env
// FREE_OS_MEMORY_IDLE_MINUTES diisi dengan angka > 0. Tujuannya: setelah
// periode idle (tidak ada command yang selesai dieksekusi) selama n menit,
// bot memanggil runtime/debug.FreeOSMemory() agar Go melepas halaman heap
// yang ditahan (retention) kembali ke OS. Ini jaring pengaman jangka
// panjang, BUKAN pengganti pelepasan pasca-operasi berat.
package diag

import (
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	idleFreeMu    sync.Mutex
	lastCmdDoneNs atomic.Int64
	lastFreedNs   atomic.Int64
)

// NoteCommandFinished mencatat bahwa sebuah command baru saja selesai
// dieksekusi. Dipanggil dari handler command. Thread-safe.
func NoteCommandFinished() {
	lastCmdDoneNs.Store(time.Now().UnixNano())
}

// FreeOSMemory memaksa Go melepas memori heap yang tidak terpakai kembali
// ke OS. Aman dipanggil dari goroutine mana pun; diserialisasi dengan mutex.
func FreeOSMemory() {
	idleFreeMu.Lock()
	defer idleFreeMu.Unlock()
	debug.FreeOSMemory()
	lastFreedNs.Store(time.Now().UnixNano())
}

// StartIdleMemoryRelease menyalakan goroutine pelepas memori idle bila env
// FREE_OS_MEMORY_IDLE_MINUTES diisi dengan angka menit > 0.
//
// Contoh: FREE_OS_MEMORY_IDLE_MINUTES=10
//
// Goroutine cek tiap 30 detik; free hanya jika:
//   - sudah ada command yang pernah selesai, dan
//   - idle >= n menit, dan
//   - belum pernah free di periode idle yang sama.
func StartIdleMemoryRelease() {
	raw := strings.TrimSpace(os.Getenv("FREE_OS_MEMORY_IDLE_MINUTES"))
	if raw == "" {
		return
	}
	minutes, err := strconv.Atoi(raw)
	if err != nil || minutes <= 0 {
		return
	}
	idle := time.Duration(minutes) * time.Minute
	checkEvery := 30 * time.Second
	if idle < checkEvery {
		checkEvery = idle
		if checkEvery < time.Second {
			checkEvery = time.Second
		}
	}

	go func() {
		ticker := time.NewTicker(checkEvery)
		defer ticker.Stop()
		for range ticker.C {
			now := time.Now()
			lastCmd := lastCmdDoneNs.Load()
			if lastCmd == 0 {
				continue
			}
			if now.Sub(time.Unix(0, lastCmd)) < idle {
				continue
			}
			lastFreed := lastFreedNs.Load()
			if lastFreed != 0 && now.Sub(time.Unix(0, lastFreed)) < idle {
				continue
			}
			FreeOSMemory()
		}
	}()
}
