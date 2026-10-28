package geminiapi

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// cookieOrder menjaga urutan cookie saat ditulis ke berkas. Urutan yang
// stabil penting supaya berkas tidak "berubah" hanya karena map diiterasi
// acak — diff palsu menyulitkan pemeriksaan manual, dan fingerprint cookie
// (kalau nanti dipakai) jadi tidak berarti.
var cookieOrder = []string{
	"__Secure-1PSID",
	"__Secure-1PSIDTS",
	"__Secure-3PSID",
	"__Secure-3PSIDTS",
	"SAPISID",
	"__Secure-1PAPISID",
	"__Secure-3PAPISID",
}

// volatileCookies adalah cookie pendamping sesi yang dirotasi Google (≈10
// menit) dan dianggap "volatile": nilainya tetap disimpan ke disk (siapa tahu
// masih diterima setelah restart), tapi tidak pernah dijadikan satu-satunya
// bukti. Bila `/app` menolak sesi, bot mengulang tanpa cookie ini — nilai basi
// bisa membuat permintaan dianggap tidak sah walau `__Secure-1PSID` sehat.
var volatileCookies = []string{
	"__Secure-1PSIDTS",
	"__Secure-3PSIDTS",
}

// volatileCookieNames mengembalikan nama cookie volatile untuk HeaderExcept.
func volatileCookieNames() []string { return volatileCookies }

// CookieSet adalah kumpulan cookie sesi Google yang dipakai untuk memanggil
// antarmuka web Gemini. Nilainya adalah kredensial penuh akun: jangan pernah
// mencetaknya ke log, chat, atau pesan error.
type CookieSet struct {
	mu   sync.Mutex
	vals map[string]string
}

// ParseCookieHeader membaca bentuk header "name=value; name2=value2".
// Nilai yang mengandung "=" (umum untuk cookie Google) tetap utuh karena
// pemisahan hanya pada "=" pertama.
func ParseCookieHeader(header string) *CookieSet {
	cs := &CookieSet{vals: make(map[string]string)}
	for _, part := range strings.Split(header, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if name == "" || value == "" {
			continue
		}
		cs.vals[name] = value
	}
	return cs
}

// LoadCookieFile membaca berkas cookie (satu baris berbentuk header Cookie).
// Baris kosong dan baris berawalan "#" diabaikan.
func LoadCookieFile(path string) (*CookieSet, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s tidak ada", ErrNoCookie, path)
		}
		return nil, fmt.Errorf("gagal baca cookie: %w", err)
	}
	header := firstCookieLine(string(raw))
	cs := ParseCookieHeader(header)
	if !cs.HasSession() {
		return nil, fmt.Errorf("%w: __Secure-1PSID kosong", ErrNoCookie)
	}
	return cs, nil
}

// firstCookieLine mengambil baris pertama yang berisi "=" — toleran terhadap
// BOM/CRLF dan baris komentar.
func firstCookieLine(raw string) string {
	raw = strings.TrimPrefix(raw, "\ufeff")
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return line
	}
	return ""
}

// HasSession true bila cookie kunci (__Secure-1PSID) tersedia.
func (c *CookieSet) HasSession() bool {
	if c == nil {
		return false
	}
	return c.Get("__Secure-1PSID") != ""
}

// Len menghitung jumlah cookie yang tersimpan (dipakai untuk laporan status
// tanpa membocorkan nilainya).
func (c *CookieSet) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.vals)
}

// Get mengambil satu nilai cookie.
func (c *CookieSet) Get(name string) string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.vals[name]
}

// Set menimpa satu nilai cookie.
func (c *CookieSet) Set(name, value string) {
	if c == nil || name == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.vals == nil {
		c.vals = make(map[string]string)
	}
	c.vals[name] = value
}

// Delete membuang satu cookie (dipakai untuk cookie volatile saat memuat).
func (c *CookieSet) Delete(name string) {
	if c == nil || name == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.vals, name)
}

// Names mengembalikan nama cookie terurut (untuk diagnostik/status). Nilai
// tidak pernah ikut supaya tidak bocor ke log atau chat.
func (c *CookieSet) Names() []string {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	names := make([]string, 0, len(c.vals))
	for name := range c.vals {
		names = append(names, name)
	}
	c.mu.Unlock()
	sort.Strings(names)
	return names
}

// Header menyusun header Cookie dengan urutan stabil (lihat cookieOrder),
// lalu sisa nama diurutkan alfabetis.
func (c *CookieSet) Header() string {
	return c.headerFiltered(nil)
}

// HeaderExcept menyusun header tanpa nama tertentu (dipakai diagnostik dan
// penyimpanan: cookie volatile tidak ikut ke disk).
func (c *CookieSet) HeaderExcept(exclude ...string) string {
	skip := make(map[string]bool, len(exclude))
	for _, name := range exclude {
		skip[name] = true
	}
	return c.headerFiltered(func(name string) bool { return !skip[name] })
}

// HeaderOnly menyusun header yang hanya memuat nama tertentu.
func (c *CookieSet) HeaderOnly(include ...string) string {
	keep := make(map[string]bool, len(include))
	for _, name := range include {
		keep[name] = true
	}
	return c.headerFiltered(func(name string) bool { return keep[name] })
}

// headerFiltered menyusun header Cookie; keep nil berarti semua cookie ikut.
func (c *CookieSet) headerFiltered(keep func(string) bool) string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	vals := make(map[string]string, len(c.vals))
	for k, v := range c.vals {
		vals[k] = v
	}
	c.mu.Unlock()

	seen := make(map[string]bool, len(vals))
	parts := make([]string, 0, len(vals))
	for _, name := range cookieOrder {
		if keep != nil && !keep(name) {
			continue
		}
		if v, ok := vals[name]; ok && v != "" {
			parts = append(parts, name+"="+v)
			seen[name] = true
		}
	}
	rest := make([]string, 0, len(vals))
	for name := range vals {
		if seen[name] || vals[name] == "" {
			continue
		}
		if keep != nil && !keep(name) {
			continue
		}
		rest = append(rest, name)
	}
	sort.Strings(rest)
	for _, name := range rest {
		parts = append(parts, name+"="+vals[name])
	}
	return strings.Join(parts, "; ")
}

// MergeSetCookies menyerap Set-Cookie dari respons Google (rotasi
// __Secure-1PSIDTS dan cookie pendamping). Mengembalikan true bila ada
// perubahan, supaya pemanggil tahu kapan harus menulis berkas.
func (c *CookieSet) MergeSetCookies(cookies []*http.Cookie) bool {
	if c == nil || len(cookies) == 0 {
		return false
	}
	changed := false
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.vals == nil {
		c.vals = make(map[string]string)
	}
	for _, ck := range cookies {
		if ck == nil || ck.Name == "" || ck.Value == "" {
			continue
		}
		// Hanya cookie Google yang relevan untuk sesi Gemini.
		if ck.Domain != "" && !isGoogleDomain(ck.Domain) {
			continue
		}
		if c.vals[ck.Name] != ck.Value {
			c.vals[ck.Name] = ck.Value
			changed = true
		}
	}
	return changed
}

func isGoogleDomain(domain string) bool {
	d := strings.TrimPrefix(strings.ToLower(domain), ".")
	return d == "google.com" || strings.HasSuffix(d, ".google.com")
}

// Save menulis cookie kembali ke berkas secara atomik (tulis file sementara
// di direktori yang sama, lalu rename) dengan izin 0600. Semua cookie ikut
// tersimpan — termasuk `__Secure-1PSIDTS` — supaya nilai yang masih diterima
// Google tidak hilang saat bot restart. Kegagalan chmod di Windows
// diabaikan: yang penting berkas tidak berada di repo.
func (c *CookieSet) Save(path string) error {
	if c == nil {
		return fmt.Errorf("%w: cookie kosong", ErrNoCookie)
	}
	header := c.Header()
	if header == "" {
		return fmt.Errorf("%w: cookie kosong", ErrNoCookie)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("gagal siapkan folder cookie: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".gemini-cookie-*.tmp")
	if err != nil {
		return fmt.Errorf("gagal buat file sementara cookie: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(0o600); err != nil {
		// Windows: bukan masalah, lanjut.
		_ = err
	}
	if _, err := tmp.WriteString(header + "\n"); err != nil {
		tmp.Close()
		return fmt.Errorf("gagal tulis cookie: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("gagal sinkron cookie: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("gagal tutup cookie: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("gagal simpan cookie: %w", err)
	}
	return nil
}

// String sengaja tidak menampilkan nilai cookie.
func (c *CookieSet) String() string {
	if c == nil {
		return "cookie(nil)"
	}
	names := make([]string, 0, 8)
	c.mu.Lock()
	for name := range c.vals {
		names = append(names, name)
	}
	c.mu.Unlock()
	sort.Strings(names)
	return "cookie(" + strings.Join(names, ",") + ")"
}
