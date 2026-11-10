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
}

// Diag menjalankan pemeriksaan berurutan dan berhenti di tahap pertama yang
// gagal. Tembolok kredensial dilewati supaya hasilnya mencerminkan keadaan
// jaringan/IP saat ini, bukan sisa permintaan sebelumnya.
func (c *Client) Diag(ctx context.Context) *Diag {
	d := &Diag{}
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

	// Simpan kredensial yang baru terbukti sehat supaya permintaan berikutnya
	// tidak mengulang HEAD + GET.
	c.mu.Lock()
	c.cookie, c.cookieAt = cookie, time.Now()
	c.bl, c.sid, c.metaAt = bl, sid, time.Now()
	c.mu.Unlock()

	start := time.Now()
	res, err := c.send(ctx, cookie, bl, sid, "Balas satu kata: siap")
	d.Latency = time.Since(start)
	if err != nil {
		d.Chat = Stage{Name: "chat", Err: err, Detail: err.Error()}
		d.Dump = c.LastDumpPath()
		return d
	}
	d.Chat = Stage{Name: "chat", OK: true, Detail: "jawaban diterima"}
	d.Answer = truncateRunes(res.Text, 200)
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
