package plugins

import (
	"fmt"
	"strings"
)

// replyfmt.go — fondasi format respon standar bot (Sesi 1 standardisasi).
//
// Gaya baku: judul bold, baris data "• Kunci : Nilai", seksi dipisah baris
// kosong, error selalu menyebut tahap + kalimat ramah (detail teknis hanya
// bila membantu). Command baru WAJIB merangkai balasannya lewat helper di
// sini; command lama dimigrasi bertahap per batch.

// Title membuat baris judul bold: *Judul*
func Title(t string) string {
	return "*" + t + "*"
}

// Row membuat baris data: • Kunci : Nilai
func Row(key, val string) string {
	return "• " + key + " : " + val
}

// Opt membuat baris opsi dua tingkat: kunci di baris pertama, nilainya
// (contoh pemakaian) di baris kedua dengan awalan "> ". Dipakai blok bantuan
// yang perintahnya panjang, supaya tidak berdesakan di satu baris.
func Opt(key, val string) string {
	return "• " + key + " :\n> " + val
}

// Block merangkai judul + baris-baris isi (baris boleh Row atau teks bebas).
func Block(title string, rows ...string) string {
	var sb strings.Builder
	sb.WriteString(Title(title))
	for _, r := range rows {
		sb.WriteString("\n" + r)
	}
	return sb.String()
}

// Bullet membuat baris daftar: - teks
func Bullet(text string) string {
	return "- " + text
}

// Hint membuat baris keterangan di bawah daftar: > teks
func Hint(text string) string {
	return "> " + text
}

// Section merangkai blok tambahan di dalam pesan yang sudah punya Block
// utama: baris kosong + judul bold + baris isi. Tanpa baris kosong pembuka,
// judul kedua menempel ke baris data terakhir dan blok terbaca sebagai satu
// paragraf.
func Section(title string, rows ...string) string {
	return "\n\n" + Block(title, rows...)
}

// ErrMsg membuat pesan error standar: *Judul*\nStatus: <pesan>
// Pesan harus menyebut TAHAP yang gagal + kalimat ramah; detail teknis
// (err) hanya disertakan bila membantu user bertindak.
func ErrMsg(title, msg string) string {
	return Title(title) + "\nStatus: " + msg
}

// QueueFullMsg adalah teks baku penolakan antrean penuh (sama di semua
// command berantrean: downloader, pdfmerge, pdflocal). Ubah di sini bila
// aturan antrean berubah.
func QueueFullMsg() string {
	return "Antrean penuh (maks 2 bersamaan), coba lagi sebentar."
}

// Guard standar command grup (judul = nama command, Title-Case). Dipakai
// handler yang cek manual (tagall/hidetag); requireGroupAdminGate dan
// requireModGate SENGAJA tidak diubah (string-nya dipakai ulang tool AI).
func ErrNotGroup(cmd string) string {
	return ErrMsg(cmd, "Command ini hanya bisa dipakai di dalam grup.")
}

// ErrNotAdmin untuk pemanggil bukan admin grup.
func ErrNotAdmin(cmd string) string {
	return ErrMsg(cmd, "Hanya admin grup yang boleh pakai command ini.")
}

// ErrAdminCheck untuk kegagalan baca status admin (sertakan err).
func ErrAdminCheck(cmd string, err error) string {
	return ErrMsg(cmd, fmt.Sprintf("Gagal cek status admin: %v", err))
}
