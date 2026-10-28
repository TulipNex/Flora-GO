package reactionwa

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNormalizeLink(t *testing.T) {
	cases := map[string]string{
		"https://whatsapp.com/channel/abc123/141":            "https://whatsapp.com/channel/abc123/141",
		"https://www.whatsapp.com/channel/abc123/141/?x=1#y": "https://www.whatsapp.com/channel/abc123/141",
		"  https://whatsapp.com/channel/abc123/141/  ":       "https://whatsapp.com/channel/abc123/141",
	}
	for in, want := range cases {
		got, err := NormalizeLink(in)
		if err != nil || got != want {
			t.Errorf("NormalizeLink(%q) = (%q, %v), mau %q", in, got, err, want)
		}
	}
	for _, bad := range []string{
		"",
		"https://whatsapp.com/channel/abc123",
		"https://example.com/channel/abc/1",
		"https://whatsapp.com/channel/abc/abc",
	} {
		if _, err := NormalizeLink(bad); !errors.Is(err, ErrLink) {
			t.Errorf("NormalizeLink(%q) = %v, mau ErrLink", bad, err)
		}
	}
}

func TestSplitEmojis(t *testing.T) {
	got := SplitEmojis([]string{"😭,🔥", "❤️", "halo"})
	want := []string{"😭", "🔥", "❤️"}
	if len(got) != len(want) {
		t.Fatalf("SplitEmojis = %v, mau %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("SplitEmojis[%d] = %q, mau %q", i, got[i], want[i])
		}
	}
	if got := SplitEmojis([]string{"👍🏽"}); len(got) != 1 || got[0] != "👍🏽" {
		t.Errorf("emoji gabungan terbelah: %v", got)
	}
}

func TestSignStable(t *testing.T) {
	first := sign("https://whatsapp.com/channel/a/1", "😭", "cap", "1700000000000")
	second := sign("https://whatsapp.com/channel/a/1", "😭", "cap", "1700000000000")
	if first != second || len(first) != 64 {
		t.Fatalf("sign = %q / %q, mau hex 64 karakter yang sama", first, second)
	}
	if other := sign("https://whatsapp.com/channel/a/1", "😭", "cap", "1700000000001"); other == first {
		t.Error("sign tidak berubah saat waktu berubah")
	}
}

// fakeJWT membuat access token Firebase tiruan (header.payload.signature).
func fakeJWT(uid, email string, exp time.Time) string {
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return enc(map[string]string{"alg": "RS256", "typ": "JWT"}) + "." +
		enc(map[string]any{"user_id": uid, "email": email, "exp": exp.Unix()}) + ".sig"
}

type fakeCaptcha struct {
	mu    atomic.Int32
	token string
}

func (f *fakeCaptcha) Token(context.Context) (string, error) {
	f.mu.Add(1)
	return f.token, nil
}

func (f *fakeCaptcha) Invalidate() {}

func TestReactSuccess(t *testing.T) {
	t.Chdir(t.TempDir())
	var gotAuth, gotSig, gotBody string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotSig = r.Header.Get("x-req-sig")
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		b, _ := json.Marshal(body)
		gotBody = string(b)
		if r.Header.Get("Origin") == "" {
			t.Error("Origin wajib dikirim")
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"success":true,"message":"Reaction VIP API masuk antrean.","task":{"key_id":"vip-1","status":"pending"},"vip":{"pointRemaining":9,"packageName":"VIP"}}`))
	}))
	defer api.Close()

	idToken := fakeJWT("uid-1", "owner@example.com", time.Now().Add(time.Hour))
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Referer") == "" {
			t.Error("Referer wajib dikirim ke securetoken")
		}
		_, _ = w.Write([]byte(`{"id_token":"` + idToken + `","refresh_token":"","expires_in":"3600"}`))
	}))
	defer tokenSrv.Close()

	if err := WriteRefreshToken("AMf-refresh-token-1234567890"); err != nil {
		t.Fatal(err)
	}
	cap := &fakeCaptcha{token: "cap-1"}
	c := newClient(api.URL, tokenSrv.URL, cap, time.Now)

	res, err := c.React(context.Background(), "https://whatsapp.com/channel/abc/1", []string{"😭", "🔥"})
	if err != nil {
		t.Fatalf("React: %v", err)
	}
	if res.Task == nil || res.Task.ID != "vip-1" || res.VIP == nil || res.VIP.PointRemaining != 9 {
		t.Errorf("hasil tidak terbaca: %+v", res)
	}
	if gotAuth != "Bearer "+idToken {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if len(gotSig) != 64 {
		t.Errorf("x-req-sig = %q", gotSig)
	}
	if !strings.Contains(gotBody, `"reaction":"😭,🔥"`) || !strings.Contains(gotBody, `"captchaToken":"cap-1"`) {
		t.Errorf("body = %s", gotBody)
	}
	if cap.mu.Load() != 1 {
		t.Errorf("captcha dipanggil %d kali, mau 1", cap.mu.Load())
	}
}

func TestCaptchaRetry(t *testing.T) {
	t.Chdir(t.TempDir())
	var calls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"Verifikasi CAPTCHA gagal. Silakan coba lagi."}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"success":true,"message":"masuk antrean"}`))
	}))
	defer api.Close()

	idToken := fakeJWT("uid", "", time.Now().Add(time.Hour))
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id_token":"` + idToken + `","expires_in":"3600"}`))
	}))
	defer tokenSrv.Close()

	if err := WriteRefreshToken("AMf-refresh-token-1234567890"); err != nil {
		t.Fatal(err)
	}
	cap := &fakeCaptcha{token: "cap"}
	c := newClient(api.URL, tokenSrv.URL, cap, time.Now)
	if _, err := c.React(context.Background(), "https://whatsapp.com/channel/a/1", []string{"😭"}); err != nil {
		t.Fatalf("React: %v", err)
	}
	if calls.Load() != 2 || cap.mu.Load() != 2 {
		t.Errorf("calls=%d captcha=%d, mau 2/2", calls.Load(), cap.mu.Load())
	}
}

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"captcha", 403, `{"error":"Verifikasi CAPTCHA gagal."}`, ErrCaptcha},
		{"captcha wajib", 403, `{"error":"Verifikasi keamanan (CAPTCHA) wajib diselesaikan."}`, ErrCaptcha},
		{"token", 403, `{"error":"Forbidden: Token otentikasi tidak valid atau sudah kedaluwarsa."}`, ErrTokenRejected},
		{"anonim", 403, `{"error":"Forbidden: Sesi anonim dilarang."}`, ErrTokenRejected},
		{"url", 400, `{"error":"URL WhatsApp Channel tidak valid."}`, ErrLink},
		{"limit", 429, `{"error":"Batas harian tercapai"}`, ErrLimit},
		{"lain", 500, `{"error":"kesalahan server"}`, &APIError{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := mapAPIError(tc.status, flexMessage([]byte(tc.body)))
			if tc.name == "lain" {
				var apiErr *APIError
				if !errors.As(err, &apiErr) {
					t.Fatalf("err = %v, mau APIError", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Errorf("err = %v, mau %v", err, tc.want)
			}
		})
	}
}

func TestMintRotatesRefreshToken(t *testing.T) {
	t.Chdir(t.TempDir())
	idToken := fakeJWT("uid", "a@b", time.Now().Add(time.Hour))
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id_token":"` + idToken + `","refresh_token":"AMf-rotated-token-0987654321","expires_in":3600}`))
	}))
	defer tokenSrv.Close()

	if err := WriteRefreshToken("AMf-lama-token-1234567890"); err != nil {
		t.Fatal(err)
	}
	c := newClient("http://127.0.0.1:1", tokenSrv.URL, &fakeCaptcha{}, time.Now)
	acc, err := c.Account(context.Background())
	if err != nil {
		t.Fatalf("Account: %v", err)
	}
	if acc.Email != "a@b" || acc.UID != "uid" {
		t.Errorf("akun = %+v", acc)
	}
	got, err := ReadRefreshToken()
	if err != nil {
		t.Fatal(err)
	}
	if got != "AMf-rotated-token-0987654321" {
		t.Errorf("refresh token tersimpan = %q, mau hasil rotasi", got)
	}
}

func TestLoginRejectsBadToken(t *testing.T) {
	t.Chdir(t.TempDir())
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"message":"INVALID_REFRESH_TOKEN"}}`))
	}))
	defer tokenSrv.Close()

	c := newClient("http://127.0.0.1:1", tokenSrv.URL, &fakeCaptcha{}, time.Now)
	_, err := c.Login(context.Background(), "AMf-token-palsu-1234567890")
	if !errors.Is(err, ErrTokenRejected) {
		t.Fatalf("Login = %v, mau ErrTokenRejected", err)
	}
	if HasToken() {
		t.Error("token tidak boleh tersimpan kalau ditolak")
	}
}

func TestNoToken(t *testing.T) {
	t.Chdir(t.TempDir())
	c := newClient("http://127.0.0.1:1", "http://127.0.0.1:1", &fakeCaptcha{token: "x"}, time.Now)
	_, err := c.React(context.Background(), "https://whatsapp.com/channel/a/1", []string{"😭"})
	if !errors.Is(err, ErrNoToken) {
		t.Fatalf("React tanpa token = %v, mau ErrNoToken", err)
	}
}
