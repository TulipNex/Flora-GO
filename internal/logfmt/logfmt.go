package logfmt

// Paket leaf untuk format blok log: warna header per kategori + garis
// pembatas antar blok. Dipakai store/log.go (blok MESSAGE) dan
// internal/ai/ailog.go (blok AI_*) — ditaruh di leaf supaya tidak
// dependency cycle (ai mengimpor store, jadi helper tak bisa di store).
//
// Hanya BARIS HEADER yang diwarnai; baris kunci + isi tetap default agar
// copy-paste bersih dan nyaman dibaca. Hormati NO_COLOR (standar): bila
// env NO_COLOR terisi (nilai apa pun), semua warna mati.

import (
	"fmt"
	"os"
	"strings"
	"sync"
)

// Hanya BARIS HEADER yang diwarnai; baris kunci + isi tetap default agar
// copy-paste bersih dan nyaman dibaca. Aturan env (prioritas turun):
//   1. FORCE_COLOR=1/true → warna NYALA (menang atas segalanya).
//   2. NO_COLOR bernilai "hidup" (selain kosong/0/false) → warna MATI.
//   3. Selain itu (termasuk NO_COLOR=0/false/absen) → warna NYALA.
// NO_COLOR=0 diperlakukan sebagai "tidak di-set" (makna akal sehat, sesuai
// kebutuhan owner; panel Pterodactyl men-set NO_COLOR=0 secara default).

var (
	colorOnce    sync.Once
	colorEnabled bool
	colorReason  string
)

// Enabled true bila warna ANSI boleh dipakai.
func Enabled() bool {
	colorOnce.Do(func() {
		colorEnabled, colorReason = resolveColor()
	})
	return colorEnabled
}

// resolveColor menentukan status warna + alasannya (untuk baris status
// startup — kondisi langsung terlihat, tak perlu menebak).
func resolveColor() (bool, string) {
	if isTruthy(os.Getenv("FORCE_COLOR")) {
		return true, "AKTIF (FORCE_COLOR)"
	}
	if nc := os.Getenv("NO_COLOR"); !isFalsy(nc) && nc != "" {
		return false, "NONAKTIF (NO_COLOR=" + nc + ")"
	}
	return true, "AKTIF"
}

// isTruthy true untuk 1/true/yes/on (case-insensitive).
func isTruthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// isFalsy true untuk kosong/0/false/no/off (diperlakukan sebagai tak di-set).
func isFalsy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "0", "false", "no", "off":
		return true
	}
	return false
}

// ColorStatus mengembalikan "AKTIF ..." / "NONAKTIF ..." untuk baris
// status startup.
func ColorStatus() string {
	Enabled()
	return colorReason
}

// tagColor memetakan tag blok AI ke kode warna ANSI.
func tagColor(tag string) string {
	t := strings.ToUpper(tag)
	switch {
	case strings.HasPrefix(t, "[AI_TURN]"):
		return "35" // ungu
	case strings.HasPrefix(t, "[AI_REQ]") || strings.HasPrefix(t, "[AI_RESP]"):
		return "36" // cyan
	case strings.HasPrefix(t, "[AI_TOOL]"):
		return "33" // kuning
	case strings.HasPrefix(t, "[AI_LOOP]") || strings.HasPrefix(t, "[AI_SIDE]"):
		return "90" // abu gelap
	default:
		return ""
	}
}

// Paint mewarnai teks sesuai tag (atau merah bila mengandung GAGAL/ERROR).
// Tanpa warna = teks apa adanya.
func Paint(tag, text string) string {
	if !Enabled() {
		return text
	}
	code := tagColor(tag)
	if strings.Contains(strings.ToUpper(text), "GAGAL") || strings.Contains(strings.ToUpper(tag), "ERROR") {
		code = "31" // merah
	}
	return PaintCode(code, text)
}

// PaintCode mewarnai teks dengan kode ANSI eksplisit ("32"=hijau,
// "34"=biru, ...). Dipakai pemanggil yang tahu kategorinya sendiri
// (mis. blok MESSAGE masuk=32/keluar=34). Kode kosong = polos.
func PaintCode(code, text string) string {
	if !Enabled() || code == "" {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

// dividerWidth adalah lebar garis pembatas (tetap 60 — prediktibel di
// layar sempit panel, bukan full-width dinamis).
const dividerWidth = 60

// Divider mengembalikan garis pembatas redup antar blok (tanpa warna bila
// NO_COLOR). Berada di luar section content sehingga tidak ikut tercopy
// sebagai isi.
func Divider() string {
	line := strings.Repeat("─", dividerWidth)
	if !Enabled() {
		return line
	}
	return "\x1b[90m" + line + "\x1b[0m"
}

// PrintBlock mencetak satu blok: header berwarna + body + pembatas.
func PrintBlock(tag, headerRest, body string) {
	head := tag
	if strings.TrimSpace(headerRest) != "" {
		head += " " + headerRest
	}
	fmt.Println(Paint(tag, head))
	if strings.TrimSpace(body) != "" {
		fmt.Println(body)
	}
	fmt.Println(Divider())
}

// Log 1-baris berwarna pengganti emoji (keputusan owner: tanpa emoji,
// berwarna, tetap 1 baris). Hormat NO_COLOR/FORCE_COLOR via PaintCode.
// Pesan (format+args) sama persis seperti sebelumnya, hanya emoji depan
// yang dibuang di call site. Memakai fmt.Print (bukan Printf) supaya
// go vet tidak memperingatkan format-string non-konstan — argumen sudah
// di-Sprintf dulu di sini.
//
//	Warn  (kuning)  : peringatan/gagal-nonfatal — pengganti "⚠️"
//	Error (merah)   : error/fatal — pengganti "❌"
//	Skip  (abu)     : lewatan sengaja — pengganti "⏭️"
//	Clean (hijau)   : bersih-bersih selesai — pengganti "🧹"
//	Info  (abu)     : info netral — pengganti "ℹ️"
//	Call  (cyan)    : peristiwa telepon — pengganti "📞📴✅"
//	Plain (polos)   : startup/restore/QR — tanpa warna, tanpa emoji
func Warn(format string, args ...any) {
	fmt.Print(PaintCode("33", fmt.Sprintf(format, args...)))
}

func Error(format string, args ...any) {
	fmt.Print(PaintCode("31", fmt.Sprintf(format, args...)))
}

func Skip(format string, args ...any) {
	fmt.Print(PaintCode("90", fmt.Sprintf(format, args...)))
}

func Clean(format string, args ...any) {
	fmt.Print(PaintCode("32", fmt.Sprintf(format, args...)))
}

func Info(format string, args ...any) {
	fmt.Print(PaintCode("90", fmt.Sprintf(format, args...)))
}

func Call(format string, args ...any) {
	fmt.Print(PaintCode("36", fmt.Sprintf(format, args...)))
}

func Plain(format string, args ...any) {
	fmt.Print(fmt.Sprintf(format, args...))
}
