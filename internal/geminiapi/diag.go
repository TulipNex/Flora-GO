package geminiapi

import (
	"context"
	"strings"
	"time"
)

// Stage adalah hasil satu tahap diagnostik. Detail sengaja tidak pernah memuat
// nilai cookie — hanya status/ringkasan yang aman ditampilkan di chat.
type Stage struct {
	Name   string
	OK     bool
	Detail string
	Err    error
}

// Diag merangkum pemeriksaan jalur tamu: ambil cookie, baca parameter halaman,
// lalu satu percakapan sungguhan. Biayanya satu kuota chat.
type Diag struct {
	Cookie  Stage
	Meta    Stage
	Chat    Stage
	Latency time.Duration
	Answer  string
	Dump    string
	// Shape adalah bentuk permintaan yang dipakai saat chat ("lengkap" = 80
	// slot seperti browser, "minimal" = 3 slot).
	Shape string
	// Ticket adalah tiket percakapan yang dikembalikan server. Kosong berarti
	// server tidak memberi id lanjutan, sehingga bot memakai riwayat prompt.
	Ticket Resume
}

// Diag menjalankan pemeriksaan berurutan dan berhenti di tahap pertama yang
// gagal. Tembolok kredensial dilewati supaya hasilnya mencerminkan keadaan
// jaringan/IP saat ini, bukan sisa permintaan sebelumnya.
func (c *Client) Diag(ctx context.Context) *Diag {
	d := &Diag{}
	// Kredensial yang sedang dipakai disimpan dulu dan dikembalikan setelah
	// diagnosa. Diag memang harus memakai cookie baru (itu inti pemeriksaannya),
	// tapi cookie baru = sesi tamu baru (`NID` lain), sehingga tiket percakapan
	// yang sedang hidup akan jadi asing. Tanpa pemulihan ini `.gemini diag`
	// membunuh percakapan yang sedang berjalan — persis bug Nov 2026: owner
	// menjalankan `.gemini diag`, giliran berikutnya langsung `1097`.
	// Tembolok yang kosong (proses baru) tidak dipulihkan: tidak ada tiket yang
	// perlu dijaga, dan cookie hasil diag justru menghemat satu HEAD.
	if prev := c.takeCredentials(); prev.cookie != "" {
		defer c.restoreCredentials(prev)
	}
	c.forget()

	cookie, err := c.fetchGuestCookie(ctx)
	if err != nil {
		d.Cookie = Stage{Name: "cookie", Err: err, Detail: err.Error()}
		return d
	}
	d.Cookie = Stage{Name: "cookie", OK: true, Detail: "cookie tamu diterima"}

	bl, sid, err := c.fetchMeta(ctx, cookie)
	if err != nil {
		d.Meta = Stage{Name: "meta", Err: err, Detail: err.Error()}
		return d
	}
	d.Meta = Stage{Name: "meta", OK: true, Detail: "bl=" + bl + ", f.sid=" + sid}

	start := time.Now()
	// Bentuk permintaan dicatat sebelum kirim: Diag selalu memakai tiket kosong,
	// tapi bentuknya menentukan apakah server bisa memberi tiket balik.
	full := c.fullPayloadActive()
	res, err := c.send(ctx, cookie, bl, sid, "Balas satu kata: siap", Resume{}, c.uid)
	d.Latency = time.Since(start)
	d.Shape = "minimal"
	if full {
		d.Shape = "lengkap"
	}
	if err != nil {
		d.Chat = Stage{Name: "chat", Err: err, Detail: err.Error()}
		d.Dump = c.LastDumpPath()
		return d
	}
	d.Chat = Stage{Name: "chat", OK: true, Detail: "jawaban diterima"}
	d.Answer = truncateRunes(res.Text, 200)
	if full {
		// Bentuk minimal tidak membawa tiket, jadi id balasan tidak berguna.
		d.Ticket = res.Resume()
	}
	d.Dump = c.LastDumpPath()
	return d
}

// truncateRunes memotong teks tanpa memecah karakter multibyte.
func truncateRunes(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}
