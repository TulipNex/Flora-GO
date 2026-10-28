package arena

import (
	"encoding/base64"
	"testing"
	"time"
)

// TestParseSessionCookie memastikan sesi Supabase yang dipecah ke beberapa
// cookie (arena-auth-prod-v1.0/.1) bisa dibaca kembali, termasuk masa berlaku
// dan keberadaan refresh token — sumber data .seedream cookie tanpa argumen.
func TestParseSessionCookie(t *testing.T) {
	payload := `{"access_token":"tok","refresh_token":"ref","expires_at":4102444800}`
	encoded := "base64-" + base64.RawURLEncoding.EncodeToString([]byte(payload))
	// Pecah jadi dua bagian seperti yang dilakukan @supabase/ssr.
	mid := len(encoded) / 2
	header := "arena-auth-prod-v1.0=" + encoded[:mid] + "; arena-auth-prod-v1.1=" + encoded[mid:] +
		"; __cf_bm=abc; cf_clearance=def"

	s, ok := parseSessionCookie(header)
	if !ok {
		t.Fatal("sesi chuncked tidak terbaca")
	}
	if s.AccessToken != "tok" || s.RefreshToken != "ref" {
		t.Errorf("token salah: %+v", s)
	}
	if got := s.expiresAt(); got.Unix() != 4102444800 {
		t.Errorf("expires_at = %v", got)
	}
}

// TestParseSessionCookieRawJSON memastikan bentuk lama (JSON tanpa awalan
// base64-) tetap terbaca.
func TestParseSessionCookieRawJSON(t *testing.T) {
	header := `arena-auth-prod-v1.0={"access_token":"tok2","refresh_token":"","expires_at":0}`
	s, ok := parseSessionCookie(header)
	if !ok || s.AccessToken != "tok2" {
		t.Fatalf("JSON mentah tidak terbaca: ok=%v %+v", ok, s)
	}
}

// TestParseSessionCookieJWTExp memastikan exp dibaca dari JWT bila expires_at
// tidak ada di cookie.
func TestParseSessionCookieJWTExp(t *testing.T) {
	claims := base64.RawURLEncoding.EncodeToString([]byte(`{"exp":4102444801}`))
	token := "header." + claims + ".sig"
	payload := `{"access_token":"` + token + `","refresh_token":"r"}`
	encoded := "base64-" + base64.RawURLEncoding.EncodeToString([]byte(payload))
	header := "arena-auth-prod-v1=" + encoded

	s, ok := parseSessionCookie(header)
	if !ok {
		t.Fatal("sesi dengan JWT exp tidak terbaca")
	}
	if got := s.expiresAt(); got.Unix() != 4102444801 {
		t.Errorf("exp dari JWT = %v", got)
	}
}

// TestParseSessionCookieRejectsGarbage memastikan nilai rusak tidak dianggap
// sesi sah (supaya .seedream cookie menampilkan "tak terbaca", bukan waktu palsu).
func TestParseSessionCookieRejectsGarbage(t *testing.T) {
	if _, ok := parseSessionCookie("arena-auth-prod-v1.0=bukan-sesi; x=1"); ok {
		t.Error("nilai rusak harus dianggap tidak terbaca")
	}
	if _, ok := parseSessionCookie("tanpa=sesi"); ok {
		t.Error("tanpa cookie arena-auth harus tidak terbaca")
	}
}

func TestCountCookieEntries(t *testing.T) {
	header := "a=1; b=2; rusak; c=3"
	if got := countCookieEntries(header); got != 3 {
		t.Errorf("entri = %d, mau 3", got)
	}
}

// TestCookieStatusExpired memastikan Expired() benar untuk waktu lampau.
func TestCookieStatusExpired(t *testing.T) {
	st := CookieStatus{Parsed: true, ExpiresAt: time.Now().Add(-time.Minute)}
	if !st.Expired() {
		t.Error("token lampau harus dianggap kedaluwarsa")
	}
	st.ExpiresAt = time.Now().Add(time.Hour)
	if st.Expired() {
		t.Error("token yang masih berlaku tidak boleh dianggap kedaluwarsa")
	}
	st.ExpiresAt = time.Time{}
	if st.Expired() {
		t.Error("tanpa masa berlaku tidak boleh dianggap kedaluwarsa")
	}
}
