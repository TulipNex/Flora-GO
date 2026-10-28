package geminiapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"wabot-go/internal/logfmt"
)

// metaCache adalah tembolok token halaman Gemini (`at`/`bl`/`f.sid`/push id).
// Isinya BUKAN kredensial akun — cookie tetap satu-satunya kunci sesi — tapi
// tetap ditulis 0600 karena token `at` hanya berguna bagi pemegang sesi.
type metaCache struct {
	At     string `json:"at"`
	Bl     string `json:"bl"`
	Sid    string `json:"sid"`
	PushID string `json:"push_id"`
	Saved  int64  `json:"saved"`
}

// metaFile menaruh tembolok di folder yang sama dengan berkas cookie, sehingga
// path kustom (tes, atau `.env` lain) ikut tanpa env baru.
func (c *Client) metaFile() string {
	return filepath.Join(filepath.Dir(c.cookiePath), "gemini-meta.json")
}

// saveMetaFile menyimpan token halaman setelah /app berhasil. Kegagalan hanya
// dicatat: tanpa tembolok bot tetap jalan, cuma perlu /app lagi saat restart.
func (c *Client) saveMetaFile() {
	c.metaMu.Lock()
	m := metaCache{At: c.at, Bl: c.bl, Sid: c.sid, PushID: c.pushID, Saved: c.metaAt.Unix()}
	c.metaMu.Unlock()
	if m.At == "" {
		return
	}
	data, err := json.Marshal(m)
	if err != nil {
		return
	}
	path := c.metaFile()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logfmt.Warn("gemini: siapkan folder tembolok meta: %v\n", err)
		return
	}
	tmp, err := os.CreateTemp(dir, ".gemini-meta-*.tmp")
	if err != nil {
		logfmt.Warn("gemini: buat tembolok meta: %v\n", err)
		return
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = err // Windows tidak memetakan izin POSIX.
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		logfmt.Warn("gemini: tulis tembolok meta: %v\n", err)
		return
	}
	if err := tmp.Close(); err != nil {
		logfmt.Warn("gemini: tutup tembolok meta: %v\n", err)
		return
	}
	if err := os.Rename(tmpName, path); err != nil {
		logfmt.Warn("gemini: simpan tembolok meta: %v\n", err)
	}
}

// loadMetaFile memakai tembolok disk bila usianya masih di bawah metaTTL.
// Returning true berarti token siap dipakai dan /app tidak perlu dibuka —
// inilah yang membuat restart tidak menuntut login ulang selama sesi masih
// "hangat".
func (c *Client) loadMetaFile() bool {
	data, err := os.ReadFile(c.metaFile())
	if err != nil {
		return false
	}
	var m metaCache
	if err := json.Unmarshal(data, &m); err != nil || m.At == "" || m.Saved <= 0 {
		return false
	}
	saved := time.Unix(m.Saved, 0)
	age := time.Since(saved)
	if age < 0 || age > metaTTL {
		return false
	}
	c.metaMu.Lock()
	c.at = m.At
	c.bl = m.Bl
	c.sid = m.Sid
	c.pushID = m.PushID
	c.metaAt = saved
	c.metaMu.Unlock()
	return true
}

// forgetMetaFile menghapus tembolok (dipakai saat cookie diganti).
func (c *Client) forgetMetaFile() {
	if err := os.Remove(c.metaFile()); err != nil && !os.IsNotExist(err) {
		logfmt.Warn("gemini: hapus tembolok meta: %v\n", err)
	}
}
