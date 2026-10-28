package geminiapi

import "errors"

// Kesalahan yang bisa ditindaklanjuti oleh plugin. Semua error dari paket ini
// membungkus salah satu di bawah supaya pemanggil bisa memilih pesan yang
// tepat tanpa membaca string mentah dari server.
var (
	// ErrNoCookie: file cookie belum ada atau tidak memuat __Secure-1PSID.
	ErrNoCookie = errors.New("cookie gemini belum ada")
	// ErrCookieRejected: Google menolak sesi (401/403 atau token at hilang).
	ErrCookieRejected = errors.New("cookie gemini ditolak")
	// ErrLimit: kuota/rate limit akun (429 atau pesan limit dari Gemini).
	ErrLimit = errors.New("batas penggunaan gemini")
	// ErrUpload: unggah lampiran gagal.
	ErrUpload = errors.New("gagal unggah lampiran ke gemini")
	// ErrShape: balasan server tidak berbentuk seperti yang diharapkan
	// (biasanya tanda protokol web berubah).
	ErrShape = errors.New("bentuk balasan gemini tidak dikenali")
	// ErrHTTP: status HTTP lain yang tidak punya kategori khusus.
	ErrHTTP = errors.New("permintaan gemini gagal")
	// ErrEmpty: permintaan dianggap sukses tapi tidak ada teks/gambar.
	ErrEmpty = errors.New("gemini tidak mengembalikan jawaban")
	// ErrStaleConv: id percakapan tidak berlaku lagi (BardErrorInfo 1096);
	// pemanggil harus membuang pointer sesi lalu mencoba sekali lagi.
	ErrStaleConv = errors.New("percakapan gemini sudah tidak berlaku")
)
