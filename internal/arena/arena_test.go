package arena

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestNormalizeCookie memastikan hasil salin-tempel dari DevTools
// (yang sering membawa awalan "cookie:" dan baris terlipat) dirapikan
// tanpa merusak nilai cookie yang mengandung "=" (base64).
func TestNormalizeCookie(t *testing.T) {
	raw := "Cookie: arena-auth-prod-v1.0=eyJhbGciOi=; cf_clearance=abc123\n" +
		"  __cf_bm=xyz ;\r\n"
	got := normalizeCookie(raw)
	if strings.Contains(got, "Cookie:") {
		t.Errorf("awalan Cookie: tidak dibuang: %q", got)
	}
	if strings.Contains(got, "\n") || strings.Contains(got, "\r") {
		t.Errorf("masih ada newline: %q", got)
	}
	if !strings.Contains(got, "arena-auth-prod-v1.0=eyJhbGciOi=") {
		t.Errorf("nilai cookie rusak: %q", got)
	}
	if !strings.Contains(got, "__cf_bm=xyz") {
		t.Errorf("cookie kedua hilang: %q", got)
	}
}

// TestParseCookies memastikan nilai berisi "=" tidak terpotong: kalau
// terpotong, cookie chunked arena-auth-prod-v1 jadi tidak valid dan server
// membalas 401.
func TestParseCookies(t *testing.T) {
	cookies := parseCookies("a=1; arena-auth-prod-v1.1=abc=def==; empty=")
	if len(cookies) != 3 {
		t.Fatalf("dapat %d cookie, mau 3: %#v", len(cookies), cookies)
	}
	if cookies[1]["value"] != "abc=def==" {
		t.Errorf("nilai terpotong: %v", cookies[1]["value"])
	}
	if cookies[0]["url"] != arenaBase {
		t.Errorf("url cookie salah: %v", cookies[0]["url"])
	}
}

func TestLooksLikeUUID(t *testing.T) {
	cases := map[string]bool{
		"019f42b5-8c52-7793-9be8-de35eecf7ea9": true,
		"019F42B5-8C52-7793-9BE8-DE35EECF7EA9": true,
		"seedream-5.0-pro":                     false,
		"019f42b5-8c52-7793-9be8-de35eecf7ea":  false,
		"019f42b5-8c52-7793-9be8-de35eecf7eaz": false,
		"":                                     false,
	}
	for in, want := range cases {
		if got := looksLikeUUID(in); got != want {
			t.Errorf("looksLikeUUID(%q) = %v, mau %v", in, got, want)
		}
	}
}

// TestParseGenerateResult menguji tiga jalur yang benar-benar terjadi di
// lapangan: sukses (URL gambar), cookie basi (401), dan reCAPTCHA ditolak.
func TestParseGenerateResult(t *testing.T) {
	ok := `{"status":200,"images":["https://messages-prod.r2.cloudflarestorage.com/x.jpeg?X-Amz-Signature=1"],"errors":[],"sample":""}`
	u, err := parseGenerateResult(ok)
	if err != nil || !strings.HasPrefix(u, "https://") {
		t.Fatalf("hasil sukses salah: %q %v", u, err)
	}

	expired := `{"status":401,"images":[],"errors":["User not found"],"sample":""}`
	if _, err := parseGenerateResult(expired); !errors.Is(err, ErrCookieRejected) {
		t.Errorf("401 harus jadi ErrCookieRejected, dapat %v", err)
	}

	captcha := `{"status":403,"images":[],"errors":["recaptcha validation failed"],"sample":""}`
	if _, err := parseGenerateResult(captcha); !errors.Is(err, ErrRecaptcha) {
		t.Errorf("403 recaptcha harus jadi ErrRecaptcha, dapat %v", err)
	}

	modelFailed := `{"status":200,"images":[],"errors":["prompt failed"],"sample":""}`
	if _, err := parseGenerateResult(modelFailed); !errors.Is(err, ErrModelFailed) {
		t.Errorf("prompt failed harus jadi ErrModelFailed, dapat %v", err)
	}

	empty := `{"status":200,"images":[],"errors":[],"sample":"a2:[{\"type\":\"heartbeat\"}]"}`
	if _, err := parseGenerateResult(empty); err == nil {
		t.Error("tanpa gambar harus error, bukan URL kosong")
	}

	if _, err := parseGenerateResult("bukan json"); err == nil {
		t.Error("balasan bukan JSON harus error")
	}
}

// TestBuildGenerateScript memastikan seluruh placeholder tergantikan dan
// prompt di-escape sebagai literal JS (prompt berisi kutip/tag tidak boleh
// merusak skrip).
func TestBuildGenerateScript(t *testing.T) {
	prompt := `kucing "oranye" </script> & <b>tebal</b>`
	script := buildGenerateScript(prompt, "019f42b5-8c52-7793-9be8-de35eecf7ea9", "")

	for _, ph := range []string{"__SITEKEY__", "__ACTION__", "__PROMPT__", "__MODEL__", "__CX__", "__IMAGES__", "__ATTACHMENTS__"} {
		if strings.Contains(script, ph) {
			t.Errorf("placeholder %s belum diganti", ph)
		}
	}
	if !strings.Contains(script, "experimental_attachments: attachments") {
		t.Error("attachments harus berasal dari hasil unggah, bukan literal kosong")
	}
	if strings.Contains(script, "</script>") {
		t.Error("prompt tidak di-escape dengan aman")
	}
	quoted, err := json.Marshal(prompt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, string(quoted)) {
		t.Error("prompt tidak muncul sebagai literal JS ter-escape")
	}
	if !strings.Contains(script, "direct-battle") {
		t.Error("mode direct-battle hilang — server akan menolak")
	}
}

// TestBuildImagesPayload memastikan gambar referensi diubah menjadi payload
// base64 yang aman untuk skrip, dan input yang tidak masuk akal ditolak lebih
// awal (bukan setelah Chrome menyala).
func TestBuildImagesPayload(t *testing.T) {
	jpeg := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00}
	png := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0x00}

	raw, err := buildImagesPayload([][]byte{jpeg})
	if err != nil {
		t.Fatalf("jpeg: %v", err)
	}
	var decoded []arenaImage
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("payload bukan JSON: %v", err)
	}
	if len(decoded) != 1 {
		t.Fatalf("harus 1 gambar, dapat %d", len(decoded))
	}
	if decoded[0].Mime != "image/jpeg" || decoded[0].Name != "ref-1.jpg" {
		t.Errorf("payload jpeg salah: %+v", decoded[0])
	}
	if got, err := base64.StdEncoding.DecodeString(decoded[0].B64); err != nil || len(got) != len(jpeg) {
		t.Errorf("base64 tidak bisa di-decode: %v (%d byte)", err, len(got))
	}

	raw, err = buildImagesPayload([][]byte{png})
	if err != nil {
		t.Fatalf("png: %v", err)
	}
	if !strings.Contains(raw, `"mime":"image/png"`) || !strings.Contains(raw, "ref-1.png") {
		t.Error("png tidak dikenali sebagai image/png")
	}

	if _, err := buildImagesPayload([][]byte{[]byte("bukan gambar")}); err == nil {
		t.Error("byte non-gambar harus ditolak")
	}
	if _, err := buildImagesPayload([][]byte{jpeg, jpeg}); err == nil {
		t.Error("lebih dari satu gambar referensi harus ditolak")
	}
	if raw, err := buildImagesPayload(nil); err != nil || raw != "[]" {
		t.Errorf("tanpa gambar harus jadi array kosong, dapat %q (%v)", raw, err)
	}
}

// TestBuildGenerateScriptWithImages memastikan skrip generate yang memuat gambar referensi
// merangkai fungsi discoverActions dan extractActionId dengan benar tanpa placeholder tersisa.
func TestBuildGenerateScriptWithImages(t *testing.T) {
	jpeg := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00}
	imgPayload, err := buildImagesPayload([][]byte{jpeg})
	if err != nil {
		t.Fatalf("buildImagesPayload: %v", err)
	}

	script := buildGenerateScript("ubah baju jadi hijau", "019f42b5-8c52-7793-9be8-de35eecf7ea9", imgPayload)

	for _, ph := range []string{"__SITEKEY__", "__ACTION__", "__PROMPT__", "__MODEL__", "__CXLINE__", "__IMAGES__"} {
		if strings.Contains(script, ph) {
			t.Errorf("placeholder %s belum diganti", ph)
		}
	}

	// Pastikan fungsi-fungsi baru ada di dalam skrip
	for _, fn := range []string{"uploadOne", "generate-upload-url", "get-signed-url"} {
		if !strings.Contains(script, fn) {
			t.Errorf("fungsi/nama %q harus ada di dalam skrip", fn)
		}
	}
}
