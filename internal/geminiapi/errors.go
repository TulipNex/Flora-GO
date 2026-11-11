package geminiapi

import "errors"

// Kesalahan yang bisa ditindaklanjuti oleh plugin. Semua error dari paket ini
// membungkus salah satu di bawah supaya pemanggil bisa memilih pesan yang
// tepat tanpa membaca string mentah dari server.
//
// Sejak mekanisme diganti ke mode tamu (tanpa login), tidak ada lagi error
// "cookie ditolak" atau "unggah gagal": kredensial diambil otomatis dari
// HEAD gemini.google.com, dan lampiran/gambar sudah dihapus.
var (
	// ErrGuestCookie: cookie tamu gagal diambil dari HEAD gemini.google.com
	// (jaringan, blokir IP, atau Set-Cookie kosong).
	ErrGuestCookie = errors.New("gagal mendapatkan cookie tamu gemini")
	// ErrMeta: halaman /app tidak memberi parameter yang dibutuhkan
	// (build label `bl` atau `f.sid`); biasanya tanda protokol berubah.
	ErrMeta = errors.New("gagal membaca parameter halaman gemini")
	// ErrLimit: kuota/rate limit tamu (HTTP 429 atau kode limit dari Gemini).
	ErrLimit = errors.New("batas penggunaan gemini")
	// ErrStaleConv: server menolak lanjutan percakapan. Mode tamu memang tidak
	// mendukung lanjutan, jadi ini hanya bisa muncul kalau protokolnya berubah.
	ErrStaleConv = errors.New("percakapan gemini tidak berlaku")
	// ErrTicketStale: tiket percakapan berasal dari sesi tamu lain — cookie
	// (`NID`) yang membuatnya sudah berganti. Server pasti menjawab `1097`,
	// jadi permintaan TIDAK dikirim sama sekali dan pemanggil memakai riwayat
	// prompt. Bukan kegagalan jaringan dan tidak boleh memicu jeda tiket:
	// rotasi cookie tiap 30 menit memang membuat tiket lama basi.
	ErrTicketStale = errors.New("tiket percakapan gemini dari sesi tamu lain")
	// ErrProtocol: balasan server tidak berbentuk seperti yang diharapkan.
	ErrProtocol = errors.New("bentuk balasan gemini tidak dikenali")
	// ErrHTTP: status HTTP lain yang tidak punya kategori khusus.
	ErrHTTP = errors.New("permintaan gemini gagal")
	// ErrEmpty: permintaan dianggap sukses tapi tidak ada teks.
	ErrEmpty = errors.New("gemini tidak mengirim jawaban")
)
