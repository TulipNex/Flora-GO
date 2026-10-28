package arena

import (
	"testing"
	"time"
)

// TestIdleExpired: idle-stopper tidak boleh mematikan Chrome yang sedang
// melayani permintaan, sekalipun `lastUsed` sudah lama (permintaan .seedream bisa
// berjalan lebih dari batas idle karena harus mengunggah referensi dulu).
// Regresi: tanpa penjaga ini owner melihat "DevTools terputus saat
// Runtime.evaluate" di tengah generate.
func TestIdleExpired(t *testing.T) {
	idle := 150 * time.Second
	now := time.Now()
	b := &browser{lastUsed: now.Add(-10 * time.Minute)}

	if !idleExpired(b, idle, now) {
		t.Error("Chrome yang benar-benar menganggur harus dianggap kedaluwarsa")
	}

	b.beginRequest()
	if idleExpired(b, idle, now) {
		t.Error("Chrome yang sedang dipakai tidak boleh dimatikan idle-stopper")
	}
	b.endRequest()
	if b.busy.Load() != 0 {
		t.Errorf("busy = %d setelah endRequest, mau 0", b.busy.Load())
	}
	// endRequest memperbarui lastUsed, jadi hitungan idle dimulai lagi.
	b.lastUsed = now
	if idleExpired(b, idle, now.Add(140*time.Second)) {
		t.Error("belum lewat batas idle tapi sudah dianggap kedaluwarsa")
	}
	if !idleExpired(b, idle, now.Add(160*time.Second)) {
		t.Error("sudah lewat batas idle tapi belum dianggap kedaluwarsa")
	}

	if idleExpired(nil, idle, now) {
		t.Error("browser nil tidak boleh dianggap kedaluwarsa")
	}
}

// TestBrowserDead: browser tanpa tab / yang sudah di-stop harus dianggap mati
// supaya perintah berikutnya menyalakan Chrome baru.
func TestBrowserDead(t *testing.T) {
	if !(&browser{}).dead() {
		t.Error("browser tanpa page harus dianggap mati")
	}
	live := &browser{page: &cdpClient{pending: map[int64]chan cdpResponse{}}, waitCh: make(chan struct{})}
	if live.dead() {
		t.Error("browser dengan page hidup tidak boleh dianggap mati")
	}
	live.stopped.Store(true)
	if !live.dead() {
		t.Error("browser yang sudah di-stop harus dianggap mati")
	}
}
