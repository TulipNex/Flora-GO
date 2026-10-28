package geminiapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newCookieFile menulis berkas cookie di direktori tes.
func newCookieFile(t *testing.T, header string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gemini-cookie.txt")
	if err := os.WriteFile(path, []byte(header+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// pointAtServer mengarahkan endpoint produksi ke server tiruan selama tes.
func pointAtServer(t *testing.T, srv *httptest.Server) {
	t.Helper()
	oldApp, oldRotate := appURL, rotateEndpoint
	appURL = srv.URL + "/app"
	rotateEndpoint = srv.URL + "/rotate"
	t.Cleanup(func() {
		appURL = oldApp
		rotateEndpoint = oldRotate
	})
}

const appTokenPage = `<html>SNlM0e":"tok"|cfb2h":"boq_gemini-web-uiserver_1"</html>`
const appLoginPage = `<html>login ServiceLogin</html>`

// TestEnsureMetaSelfHealsStaleTS meniru kasus paling umum: /app hanya membuka
// sesi setelah rotasi mencetak TS baru.
func TestEnsureMetaSelfHealsStaleTS(t *testing.T) {
	appCalls, rotateCalls := new(int), new(int)
	ts := "basi"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rotate" {
			*rotateCalls++
			ts = "segar"
			http.SetCookie(w, &http.Cookie{
				Name:   "__Secure-1PSIDTS",
				Value:  ts,
				Domain: ".google.com",
				Path:   "/",
			})
			return
		}
		*appCalls++
		if !strings.Contains(r.Header.Get("Cookie"), "__Secure-1PSIDTS=segar") {
			_, _ = w.Write([]byte(appLoginPage))
			return
		}
		_, _ = w.Write([]byte(appTokenPage))
	}))
	t.Cleanup(srv.Close)
	pointAtServer(t, srv)

	c := New(newCookieFile(t, "__Secure-1PSID=s1; __Secure-1PSIDTS=basi"))
	if err := c.LoadCookies(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := c.ensureMeta(context.Background()); err != nil {
		t.Fatalf("ensureMeta: %v", err)
	}
	if *rotateCalls != 1 || *appCalls != 2 {
		t.Fatalf("rotasi=%d app=%d, ingin 1/2", *rotateCalls, *appCalls)
	}
	c.metaMu.Lock()
	at := c.at
	c.metaMu.Unlock()
	if at != "tok" {
		t.Fatalf("token = %q", at)
	}
	if err := c.SaveError(); err != nil {
		t.Fatalf("save cookie: %v", err)
	}
}

// TestEnsureMetaRetriesWithoutVolatile meniru kebalikannya: TS yang tersimpan
// justru membuat Google menganggap permintaan tidak sah. Rotasi tidak
// menolong, jadi /app harus diulang tanpa cookie volatile.
func TestEnsureMetaRetriesWithoutVolatile(t *testing.T) {
	appCalls, rotateCalls := new(int), new(int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rotate" {
			*rotateCalls++
			return
		}
		*appCalls++
		if strings.Contains(r.Header.Get("Cookie"), "__Secure-1PSIDTS") {
			_, _ = w.Write([]byte(appLoginPage))
			return
		}
		_, _ = w.Write([]byte(appTokenPage))
	}))
	t.Cleanup(srv.Close)
	pointAtServer(t, srv)

	c := New(newCookieFile(t, "__Secure-1PSID=s1; __Secure-1PSIDTS=basi"))
	if err := c.LoadCookies(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := c.ensureMeta(context.Background()); err != nil {
		t.Fatalf("ensureMeta: %v", err)
	}
	if *rotateCalls != 1 || *appCalls != 3 {
		t.Fatalf("rotasi=%d app=%d, ingin 1/3", *rotateCalls, *appCalls)
	}
}

func TestEnsureMetaReportsRejectedWhenStillNoToken(t *testing.T) {
	appCalls, rotateCalls := new(int), new(int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rotate" {
			*rotateCalls++
			http.SetCookie(w, &http.Cookie{
				Name:   "__Secure-1PSIDTS",
				Value:  "segar",
				Domain: ".google.com",
				Path:   "/",
			})
			return
		}
		*appCalls++
		// 1PSID sudah mati: rotasi pun tidak membuka sesi.
		_, _ = w.Write([]byte(appLoginPage))
	}))
	t.Cleanup(srv.Close)
	pointAtServer(t, srv)

	c := New(newCookieFile(t, "__Secure-1PSID=s1; __Secure-1PSIDTS=basi"))
	if err := c.LoadCookies(); err != nil {
		t.Fatalf("load: %v", err)
	}
	err := c.ensureMeta(context.Background())
	if !errors.Is(err, ErrCookieRejected) {
		t.Fatalf("err = %v, ingin ErrCookieRejected", err)
	}
	if *rotateCalls != 1 || *appCalls != 3 {
		t.Fatalf("rotasi=%d app=%d, ingin 1/3", *rotateCalls, *appCalls)
	}
}

func TestDiagFlagsVolatileCookie(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rotate" {
			http.SetCookie(w, &http.Cookie{Name: "NID", Value: "n2", Domain: ".google.com", Path: "/"})
			return
		}
		if strings.Contains(r.Header.Get("Cookie"), "__Secure-1PSIDTS") {
			_, _ = w.Write([]byte(appLoginPage))
			return
		}
		_, _ = w.Write([]byte(appTokenPage))
	}))
	t.Cleanup(srv.Close)
	pointAtServer(t, srv)

	c := New(newCookieFile(t, "__Secure-1PSID=s1; __Secure-1PSIDTS=basi; NID=n1"))
	d := c.Diag(context.Background())
	if !d.FileHasTS || len(d.FileNames) != 3 {
		t.Fatalf("berkas = %v (TS=%v)", d.FileNames, d.FileHasTS)
	}
	if d.RotateErr != nil || d.Rotate.Status != http.StatusOK {
		t.Fatalf("rotasi = %+v err=%v", d.Rotate, d.RotateErr)
	}
	if d.App.HasToken {
		t.Fatalf("cookie lengkap seharusnya ditolak server tiruan: %+v", d.App)
	}
	if !d.AppNoTS.HasToken {
		t.Fatalf("tanpa TS seharusnya membuka sesi: %+v", d.AppNoTS)
	}
}

// TestMetaCacheSurvivesRestart memastikan klien baru (bot restart) memakai
// token halaman dari disk, jadi tidak menyentuh /app sama sekali.
func TestMetaCacheSurvivesRestart(t *testing.T) {
	appCalls := new(int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rotate" {
			return
		}
		*appCalls++
		_, _ = w.Write([]byte(appTokenPage))
	}))
	t.Cleanup(srv.Close)
	pointAtServer(t, srv)

	path := newCookieFile(t, "__Secure-1PSID=s1; __Secure-1PSIDTS=ts")
	first := New(path)
	if err := first.LoadCookies(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := first.ensureMeta(context.Background()); err != nil {
		t.Fatalf("ensureMeta: %v", err)
	}
	if *appCalls != 1 {
		t.Fatalf("app=%d, ingin 1", *appCalls)
	}
	if _, err := os.Stat(first.metaFile()); err != nil {
		t.Fatalf("tembolok meta tidak ditulis: %v", err)
	}

	restarted := New(path)
	if err := restarted.LoadCookies(); err != nil {
		t.Fatalf("load ulang: %v", err)
	}
	if err := restarted.ensureMeta(context.Background()); err != nil {
		t.Fatalf("ensureMeta setelah restart: %v", err)
	}
	if *appCalls != 1 {
		t.Fatalf("restart membuka /app lagi (app=%d)", *appCalls)
	}
	restarted.metaMu.Lock()
	at := restarted.at
	restarted.metaMu.Unlock()
	if at != "tok" {
		t.Fatalf("token dari tembolok = %q", at)
	}
}

// TestPingIgnoresMetaCache: status harus memverifikasi ke server, bukan
// menjawab dari tembolok token.
func TestPingIgnoresMetaCache(t *testing.T) {
	appCalls := new(int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*appCalls++
		_, _ = w.Write([]byte(appTokenPage))
	}))
	t.Cleanup(srv.Close)
	pointAtServer(t, srv)

	path := newCookieFile(t, "__Secure-1PSID=s1")
	first := New(path)
	if err := first.LoadCookies(); err != nil {
		t.Fatal(err)
	}
	if err := first.ensureMeta(context.Background()); err != nil {
		t.Fatalf("ensureMeta: %v", err)
	}
	if *appCalls != 1 {
		t.Fatalf("app=%d, ingin 1", *appCalls)
	}

	c := New(path)
	if err := c.LoadCookies(); err != nil {
		t.Fatal(err)
	}
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if *appCalls != 2 {
		t.Fatalf("ping memakai tembolok (app=%d, ingin 2)", *appCalls)
	}
}

// TestLoginClearsMetaCache: cookie identitas berganti (login ulang) harus
// membuang tembolok token lama.
func TestLoginClearsMetaCache(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gemini-cookie.txt")
	if err := os.WriteFile(path, []byte("__Secure-1PSID=lama\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := New(path)
	if err := c.LoadCookies(); err != nil {
		t.Fatal(err)
	}
	c.metaMu.Lock()
	c.at, c.metaAt = "tok-lama", time.Now()
	c.metaMu.Unlock()
	c.saveMetaFile()

	if err := os.WriteFile(path, []byte("__Secure-1PSID=baru\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := c.LoadCookies(); err != nil {
		t.Fatal(err)
	}
	c.metaMu.Lock()
	at := c.at
	c.metaMu.Unlock()
	if at != "" {
		t.Fatalf("token lama harus dibuang, dapat %q", at)
	}
	if _, err := os.Stat(c.metaFile()); !os.IsNotExist(err) {
		t.Fatalf("tembolok disk harus dihapus, err=%v", err)
	}
}
