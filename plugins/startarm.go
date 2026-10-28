package plugins

import (
	"sync"
	"time"

	"github.com/polymorfa/hypermeow/types"
)

// Jendela kata "mulai" untuk staging PDF/JPG.
//
// Alur lama: kata polos `mulai` adalah filter global — siapa pun yang mengetik
// "mulai" di chat mana pun langsung membuka sesi staging (dan tertelan sebelum
// AI). Sekarang kata itu hanya hidup selama jendela singkat setelah user
// menjalankan command pemilik menu (`.pdfmerge` / `.jpgpdf`); sesudah
// kedaluwarsa pesan `mulai` kembali jadi pesan biasa dan TIDAK ada notifikasi
// apa pun (sesuai keputusan owner).
//
// Jendela disimpan per pengirim+chat dan ditandai tool-nya, sehingga
// `.pdfmerge` → `mulai` membuka sesi PDF, `.jpgpdf` → `mulai` membuka sesi
// JPG. Bila keduanya dijalankan dalam 5 menit, yang terakhir menang.

// Tag tool pemilik jendela.
const (
	startArmPDF = "pdf"
	startArmJPG = "jpg"
)

// startArmTTL adalah umur jendela kata "mulai" sejak command menu terkirim.
const startArmTTL = 5 * time.Minute

// startArm adalah satu jendela aktif.
type startArm struct {
	tool  string
	timer *time.Timer
}

var (
	startArmMu sync.Mutex
	startArms  = map[string]*startArm{}
)

// startArmKey menyusun kunci jendela: pengirim tanpa device + chat. Normalisasi
// lewat ToNonAD supaya pesan dari device yang berbeda tetap satu sesi (selaras
// pdfStageKey), tapi map-nya terpisah dari pdfStages/jpgStages.
func startArmKey(sender, chat types.JID) string {
	return sender.ToNonAD().String() + "\x00" + chat.String()
}

// startArmSet membuka/menyegarkan jendela tool untuk kunci ini.
func startArmSet(key, tool string) {
	startArmSetFor(key, tool, startArmTTL)
}

// startArmSetFor sama seperti startArmSet dengan TTL eksplisit (dipakai tes).
func startArmSetFor(key, tool string, ttl time.Duration) {
	startArmMu.Lock()
	defer startArmMu.Unlock()
	if cur, ok := startArms[key]; ok && cur.timer != nil {
		cur.timer.Stop()
	}
	entry := &startArm{tool: tool}
	// Hapus hanya bila entri masih milik kita: set ulang memasang timer baru
	// dan timer lama tidak boleh membunuh jendela yang lebih segar.
	entry.timer = time.AfterFunc(ttl, func() {
		startArmMu.Lock()
		if cur, ok := startArms[key]; ok && cur == entry {
			delete(startArms, key)
		}
		startArmMu.Unlock()
	})
	startArms[key] = entry
}

// startArmTake mengonsumsi jendela bila tool-nya cocok (sekali pakai).
func startArmTake(key, tool string) bool {
	startArmMu.Lock()
	defer startArmMu.Unlock()
	entry, ok := startArms[key]
	if !ok || entry.tool != tool {
		return false
	}
	if entry.timer != nil {
		entry.timer.Stop()
	}
	delete(startArms, key)
	return true
}

// startArmClear membuang jendela tanpa syarat (mis. saat sesi ternyata sudah
// ada sehingga niat "mulai" dijawab "masih aktif").
func startArmClear(key string) {
	startArmMu.Lock()
	defer startArmMu.Unlock()
	if entry, ok := startArms[key]; ok {
		if entry.timer != nil {
			entry.timer.Stop()
		}
		delete(startArms, key)
	}
}

// startArmAllows memutuskan kata "mulai" boleh dijalankan. Sesi yang sudah ada
// selalu boleh (pemanggil yang menjawab "masih aktif"), dan jendelanya
// dibersihkan karena niat user sudah terpakai. Pembukaan sesi baru menuntut
// jendela tool yang belum basi.
func startArmAllows(key, tool string, sessionExists bool) bool {
	if sessionExists {
		startArmClear(key)
		return true
	}
	return startArmTake(key, tool)
}

// startArmTakeExpired adalah jalur tap tombol "Mulai" pada bubble menu yang
// sudah basi: jendela diambil bila masih hidup (true = lanjut buka sesi);
// bila tidak, jendela disegarkan supaya pemanggil bisa mengirim menu baru.
func startArmTakeOrRearm(key, tool string) bool {
	if startArmTake(key, tool) {
		return true
	}
	startArmSet(key, tool)
	return false
}
