package timex

// File ini adalah SATU-SATUNYA sumber kebenaran waktu zona Indonesia di
// repo (menggantikan 3 definisi WITA duplikat yang dulu tersebar di
// plugins/call.go, bridge/reminder.go, native/finance.go — nilai sama,
// variabel terpisah, rawan menyimpang saat diubah satu).
//
// Zona dipilih lewat env BOT_TIMEZONE=WIB/WITA/WIT (default WITA bila
// kosong; nilai tak dikenal = fallback WITA + warning console, bot tetap
// jalan). .env di-source run.sh SEBELUM binary start, jadi os.Getenv di
// sini aman kapan pun dibaca.
//
// Sengaja memakai time.FixedZone (bukan LoadLocation "Asia/...") supaya
// tidak tergantung database tzdata di server panel — WIB/WITA/WIT tanpa
// DST sehingga offset tetap selamanya.
//
// Leaf package: NOL import repo, boleh dipakai plugins, internal/store,
// dan internal/ai/... tanpa dependency cycle.

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// Offset detik dari UTC per zona Indonesia barat-tengah-timur.
const (
	offsetWIB  = 7 * 60 * 60
	offsetWITA = 8 * 60 * 60
	offsetWIT  = 9 * 60 * 60
)

var (
	zoneOnce sync.Once
	zoneLoc  *time.Location
	zoneName string
)

// initZone membaca BOT_TIMEZONE sekali (lazily, thread-safe). Dipanggil
// otomatis oleh Zone()/Name() — tidak di init() supaya urutan import
// tidak relevan dan hasil selalu konsisten dalam satu proses.
func initZone() {
	zoneOnce.Do(func() {
		zoneName = "WITA"
		offset := offsetWITA
		raw := strings.ToUpper(strings.TrimSpace(os.Getenv("BOT_TIMEZONE")))
		switch raw {
		case "", "WITA":
			// Default: perilaku identik sebelum file ini ada.
		case "WIB":
			zoneName, offset = "WIB", offsetWIB
		case "WIT":
			zoneName, offset = "WIT", offsetWIT
		default:
			fmt.Printf("BOT_TIMEZONE=%q tidak dikenal (pakai WIB/WITA/WIT) — fallback WITA.\n", os.Getenv("BOT_TIMEZONE"))
		}
		zoneLoc = time.FixedZone(zoneName, offset)
	})
}

// Zone mengembalikan lokasi zona aktif (WIB/WITA/WIT sesuai env).
func Zone() *time.Location {
	initZone()
	return zoneLoc
}

// Name mengembalikan nama zona aktif ("WIB"/"WITA"/"WIT") — untuk label
// tampil (log, laporan owner, system prompt AI).
func Name() string {
	initZone()
	return zoneName
}

// Now mengembalikan jam dinding zona aktif. Untuk tampilan & batas hari
// (default tanggal, nama file, periode pagi/siang/sore/malam). JANGAN
// dipakai untuk durasi/timer/penyimpanan jadwal — itu zona-agnostik
// (time.Since/Until + unix) dan tidak butuh file ini.
func Now() time.Time {
	return time.Now().In(Zone())
}

// Today mengembalikan tanggal hari ini zona aktif format YYYY-MM-DD
// (format tanggal tool keuangan YYYY-MM-DD).
func Today() string {
	return Now().Format("2006-01-02")
}
