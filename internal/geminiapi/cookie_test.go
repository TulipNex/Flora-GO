package geminiapi

import (
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseCookieHeaderKeepsEqualsInValue(t *testing.T) {
	cs := ParseCookieHeader("  __Secure-1PSID=g.a000.abc==  ; NID=xyz= ; kosong=  ")
	if got := cs.Get("__Secure-1PSID"); got != "g.a000.abc==" {
		t.Fatalf("1PSID = %q", got)
	}
	if got := cs.Get("NID"); got != "xyz=" {
		t.Fatalf("NID = %q", got)
	}
	if cs.Get("kosong") != "" {
		t.Fatal("nilai kosong harus dibuang")
	}
	if !cs.HasSession() {
		t.Fatal("HasSession harus true")
	}
}

func TestCookieHeaderOrderStable(t *testing.T) {
	a := ParseCookieHeader("zzz=1; __Secure-1PSID=s; aaa=2")
	b := ParseCookieHeader("aaa=2; zzz=1; __Secure-1PSID=s")
	if a.Header() != b.Header() {
		t.Fatalf("urutan tidak stabil:\n%s\n%s", a.Header(), b.Header())
	}
	if !strings.HasPrefix(a.Header(), "__Secure-1PSID=s") {
		t.Fatalf("cookie utama tidak di depan: %s", a.Header())
	}
}

func TestCookieSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gemini-cookie.txt")
	cs := ParseCookieHeader("__Secure-1PSID=s1; __Secure-1PSIDTS=t1; NID=n1")
	if err := cs.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// Windows tidak memetakan izin POSIX; pemeriksaan hanya berarti di Unix.
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("izin file terlalu terbuka: %v", info.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Cookie pendamping ikut tersimpan: nilainya mungkin masih diterima
	// Google setelah restart, dan kalau basi bot mengulang tanpa TS.
	if !strings.Contains(string(raw), "__Secure-1PSIDTS") {
		t.Fatalf("cookie pendamping hilang dari berkas: %s", raw)
	}
	loaded, err := LoadCookieFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Get("__Secure-1PSID") != "s1" || loaded.Get("NID") != "n1" {
		t.Fatalf("cookie hilang: %s", loaded.Header())
	}
	if loaded.Get("__Secure-1PSIDTS") != "t1" {
		t.Fatal("cookie pendamping harus ikut termuat")
	}
}

func TestCookieHeaderFilters(t *testing.T) {
	cs := ParseCookieHeader("__Secure-1PSID=s; __Secure-1PSIDTS=t; NID=n")
	if got := cs.HeaderExcept("__Secure-1PSIDTS"); strings.Contains(got, "1PSIDTS") {
		t.Fatalf("HeaderExcept masih memuat TS: %s", got)
	}
	if got := cs.HeaderOnly("__Secure-1PSID"); got != "__Secure-1PSID=s" {
		t.Fatalf("HeaderOnly = %q", got)
	}
	names := strings.Join(cs.Names(), ",")
	if names != "NID,__Secure-1PSID,__Secure-1PSIDTS" {
		t.Fatalf("Names = %q", names)
	}
	cs.Delete("NID")
	if cs.Get("NID") != "" {
		t.Fatal("Delete tidak bekerja")
	}
}

func TestLoadCookieFileRejectsMissingSession(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.txt")
	if err := os.WriteFile(path, []byte("NID=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCookieFile(path); err == nil {
		t.Fatal("harus menolak cookie tanpa __Secure-1PSID")
	}
}

func TestMergeSetCookiesFiltersForeignDomains(t *testing.T) {
	cs := ParseCookieHeader("__Secure-1PSID=s")
	changed := cs.MergeSetCookies([]*http.Cookie{
		{Name: "__Secure-1PSIDTS", Value: "t2", Domain: ".google.com"},
		{Name: "jahat", Value: "x", Domain: ".contoh.com"},
		{Name: "__Secure-1PSID", Value: "s"},
	})
	if !changed {
		t.Fatal("perubahan tidak terdeteksi")
	}
	if cs.Get("__Secure-1PSIDTS") != "t2" {
		t.Fatal("cookie google tidak terserap")
	}
	if cs.Get("jahat") != "" {
		t.Fatal("cookie domain asing ikut tersimpan")
	}
}

func TestCookieSetStringHidesValues(t *testing.T) {
	cs := ParseCookieHeader("__Secure-1PSID=rahasia; NID=lain")
	s := cs.String()
	if strings.Contains(s, "rahasia") || strings.Contains(s, "lain") {
		t.Fatalf("nilai cookie bocor di String(): %s", s)
	}
}
