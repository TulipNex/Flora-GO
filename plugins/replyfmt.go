package plugins

import (
	"fmt"
	"strings"
)

// replyfmt.go — fondasi format respon standar bot.
//
// Gaya baku (FORMAT_RESPON_BOT.md): bantuan memakai judul deskriptif +
// sub-label command + "Format :" + "Contoh :" + fence parameter (dirakit
// Help), data ringkas di dalam fence (Code), hasil/error berupa teks biasa
// tanpa judul (Fail/Sentence). Row/Opt/ErrMsg dipertahankan untuk command
// yang belum dimigrasi, bukan bentuk tujuan.

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

// Code membungkus baris jadi blok monospace sesuai aturan 5 di
// FORMAT_RESPON_BOT.md: pembuka menempel di depan baris pertama dan
// penutup menempel di belakang baris terakhir.
func Code(lines ...string) string {
	if len(lines) == 0 {
		return ""
	}
	return "```" + strings.Join(lines, "\n") + "```"
}

// HelpSpec adalah isi satu balasan bantuan (FORMAT_RESPON_BOT.md template A/B).
// Semua field opsional kecuali Title: yang kosong tidak ikut dirangkai, supaya
// command yang tidak punya parameter tidak meninggalkan fence kosong.
type HelpSpec struct {
	Title   string   // judul deskriptif, mis. "Keluarkan anggota grup"
	Status  string   // opsional: fence "Status : aktif" tepat di bawah judul
	Cmd     string   // sub-label nama command, mis. "Kick"
	Formats []string // bentuk pemakaian; 1 bentuk ditulis sebaris, >1 jadi daftar
	Contoh  string   // satu contoh nyata di baris berikutnya
	Params  []string // isi fence parameter (sudah sejajar), mis. "@user : tag target"
	Note    string   // catatan ">" di akhir
}

// Help merangkai balasan bantuan baku:
//
//	*Judul*
//
//	`Cmd`
//	Format : ...
//	Contoh :
//	...
//
//	```parameter```
//
//	> catatan
func Help(s HelpSpec) string {
	parts := make([]string, 0, 5)
	if s.Title != "" {
		parts = append(parts, Title(s.Title))
	}
	if s.Status != "" {
		parts = append(parts, Code("Status : "+s.Status))
	}

	var body strings.Builder
	if s.Cmd != "" {
		body.WriteString("`" + s.Cmd + "`")
	}
	switch len(s.Formats) {
	case 0:
	case 1:
		body.WriteString("\nFormat : " + s.Formats[0])
	default:
		body.WriteString("\nFormat :")
		for _, f := range s.Formats {
			body.WriteString("\n- " + f)
		}
	}
	if s.Contoh != "" {
		body.WriteString("\nContoh : " + s.Contoh)
	}
	if body.Len() > 0 {
		parts = append(parts, body.String())
	}

	if len(s.Params) > 0 {
		parts = append(parts, Code(s.Params...))
	}
	if s.Note != "" {
		parts = append(parts, Hint(s.Note))
	}
	return strings.Join(parts, "\n\n")
}

// Fail merangkai error teks biasa: "<awalan>: <sebab>." Titik di akhir sebab
// dipangkas dulu supaya kalimat tidak berakhir dengan titik ganda.
func Fail(prefix string, err error) string {
	return prefix + ": " + strings.TrimRight(err.Error(), ". ") + "."
}

// Sentence merapikan pesan error jadi satu kalimat: huruf kapital di awal dan
// titik di akhir (tanpa titik ganda). Dipakai untuk error validasi yang
// datang dari helper dan belum berbentuk kalimat.
func Sentence(msg string) string {
	msg = strings.TrimRight(strings.TrimSpace(msg), ". ")
	if msg == "" {
		return msg
	}
	r := []rune(msg)
	if r[0] >= 'a' && r[0] <= 'z' {
		r[0] = r[0] - ('a' - 'A')
	}
	return string(r) + "."
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

// Guard standar command grup versi lama (judul = nama command + "Status:").
// Bukan bentuk tujuan: command baru mengirim teks biasa (lihat Fail) dan
// memakai Help untuk bantuan. Sisa pemakai tinggal welcome.go; sisanya
// menunggu migrasi bertahap.
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
