package timex

// File ini adalah SATU-SATUNYA sumber kebenaran waktu zona bot di repo
// (menggantikan 3 definisi WITA duplikat yang dulu tersebar di
// plugins/call_voice.go, bridge/reminder.go, native/finance.go — nilai sama,
// variabel terpisah, rawan menyimpang saat diubah satu).
//
// Zona dipilih lewat env BOT_TIMEZONE dengan nama IANA (mis. Asia/Makassar,
// Asia/Jakarta, Asia/Jayapura). Alias lama WIB/WITA/WIT tetap diterima supaya
// .env yang sudah berjalan tidak rusak. Kosong = Asia/Makassar (WITA), nilai
// tak dikenal = fallback Asia/Makassar + warning console, bot tetap jalan.
// .env di-source run.sh SEBELUM binary start, jadi os.Getenv di sini aman
// kapan pun dibaca (nilai dibaca sekali per proses — ubah .env lalu restart).
//
// time/tzdata diimpor supaya database zona ikut ke dalam binary: panel tidak
// selalu punya /usr/share/zoneinfo, dan bot tidak boleh bergantung pada zona
// container (dulu dibatasi time.FixedZone WIB/WITA/WIT karena alasan itu).
//
// Label tampilan (Name) tetap singkat: nama IANA Indonesia dipetakan ke
// WIB/WITA/WIT, zona lain memakai singkatan saat ini (mis. JST) supaya pesan
// seperti "pukul 15:04 WITA" tidak berubah bentuk.
//
// Leaf package: NOL import repo, boleh dipakai plugins, internal/store,
// dan internal/ai/... tanpa dependency cycle.

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // database zona IANA tertanam di binary (~450 KB)
)

const (
	// defaultZone dipakai saat BOT_TIMEZONE kosong atau tidak dikenal.
	defaultZone = "Asia/Makassar"

	labelWIB  = "WIB"
	labelWITA = "WITA"
	labelWIT  = "WIT"
)

// Offset detik dari UTC per zona Indonesia (hanya untuk fallback darurat bila
// database zona gagal dimuat — seharusnya tidak terjadi karena tzdata tertanam).
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
		zoneName, zoneLoc = parseZone(os.Getenv("BOT_TIMEZONE"))
	})
}

// parseZone menerjemahkan nilai BOT_TIMEZONE menjadi label tampilan + lokasi.
// Fungsi murni supaya bisa diuji tanpa menyentuh environment maupun sync.Once.
func parseZone(raw string) (string, *time.Location) {
	value := strings.TrimSpace(raw)
	switch strings.ToUpper(value) {
	case "":
		return labelWITA, indonesiaZone("Asia/Makassar", offsetWITA)
	case "WIB":
		return labelWIB, indonesiaZone("Asia/Jakarta", offsetWIB)
	case "WITA":
		return labelWITA, indonesiaZone("Asia/Makassar", offsetWITA)
	case "WIT":
		return labelWIT, indonesiaZone("Asia/Jayapura", offsetWIT)
	}

	loc, err := time.LoadLocation(value)
	if err != nil {
		fmt.Printf("BOT_TIMEZONE=%q tidak dikenal (pakai nama IANA seperti Asia/Makassar) - fallback %s.\n",
			raw, defaultZone)
		return labelWITA, indonesiaZone(defaultZone, offsetWITA)
	}
	return zoneLabel(loc), loc
}

// indonesiaZone memuat nama zona Indonesia; kalau database zona tidak tersedia
// (praktis mustahil karena tzdata tertanam) dipakai offset tetap.
func indonesiaZone(name string, offset int) *time.Location {
	if loc, err := time.LoadLocation(name); err == nil {
		return loc
	}
	label := zoneLabelName(name)
	if label == "" {
		label = name
	}
	return time.FixedZone(label, offset)
}

// zoneLabel memilih label pendek untuk sebuah lokasi: WIB/WITA/WIT untuk zona
// Indonesia, selain itu singkatan yang sedang berlaku (mis. JST), dan kalau
// singkatannya berbentuk offset (mis. +07) dipakai nama IANA apa adanya.
func zoneLabel(loc *time.Location) string {
	switch loc.String() {
	case "Asia/Jakarta", "Asia/Pontianak":
		return labelWIB
	case "Asia/Makassar":
		return labelWITA
	case "Asia/Jayapura":
		return labelWIT
	}
	abbr := time.Now().In(loc).Format("MST")
	if abbr == "" || strings.HasPrefix(abbr, "+") || strings.HasPrefix(abbr, "-") {
		return loc.String()
	}
	return abbr
}

// zoneLabelName memetakan nama IANA Indonesia ke label pendek; kosong untuk
// zona lain.
func zoneLabelName(name string) string {
	switch name {
	case "Asia/Jakarta", "Asia/Pontianak":
		return labelWIB
	case "Asia/Makassar":
		return labelWITA
	case "Asia/Jayapura":
		return labelWIT
	}
	return ""
}

// Zone mengembalikan lokasi zona aktif sesuai BOT_TIMEZONE.
func Zone() *time.Location {
	initZone()
	return zoneLoc
}

// Name mengembalikan label zona aktif ("WIB"/"WITA"/"WIT", atau singkatan
// zona lain) — untuk label tampil (log, laporan owner, system prompt AI).
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

// FormatDuration mengubah durasi jadi format "1h 23m 45s" yang lebih enak
// dibaca dibanding format default Go (mis. "1h23m45.123s"). Dipusatkan di
// sini (dulu duplikat di plugins/serverinfo.go dan
// internal/ai/tools/native/system.go) supaya hanya ada satu implementasi.
func FormatDuration(d time.Duration) string {
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60

	if h > 0 {
		return fmt.Sprintf("%dh %dm %ds", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}
