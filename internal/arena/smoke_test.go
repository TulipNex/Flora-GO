package arena

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"strings"
	"testing"
	"time"
)

// TestSmokeBrowser adalah uji asap opsional yang benar-benar menyalakan
// Chrome dan membuka arena.ai. Dilewati kecuali ARENA_SMOKE di-set, karena:
//
//   - butuh Chrome terpasang (di panel /usr/bin/google-chrome, di laptop
//     Windows set ARENA_CHROME_PATH),
//   - butuh cookie login asli (data/arena-cookie.txt),
//   - ARENA_SMOKE=2 ikut membuat SATU gambar sungguhan, artinya memakai
//     kuota akun arena.ai.
//
// Pemakaian di panel:
//
//	ARENA_SMOKE=1 go test ./internal/arena/ -run TestSmokeBrowser -v
//	ARENA_SMOKE=2 go test ./internal/arena/ -run TestSmokeBrowser -v
func TestSmokeBrowser(t *testing.T) {
	mode := strings.TrimSpace(os.Getenv("ARENA_SMOKE"))
	if mode == "" || mode == "0" {
		t.Skip("set ARENA_SMOKE=1 (cek wiring) atau 2 (generate sungguhan)")
	}

	// `go test` berjalan di dalam folder paket, sedangkan seluruh path data
	// bot (data/arena-cookie.txt, arena-profile) relatif terhadap workdir bot
	// — di produksi workdir = folder bot. Pindah ke akar repo supaya uji ini
	// memakai file yang sama dengan yang dipakai bot.
	if err := os.Chdir("../.."); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	cookie, err := ReadCookie()
	if err != nil {
		t.Fatalf("cookie: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	// ARENA_SMOKE=3 menguji jalur lengkap yang dipakai plugin: GenerateImage
	// (browser + token + unduh gambar) tanpa menyentuh WhatsApp.
	if mode == "3" {
		data, model, err := GenerateImage(ctx, "apel merah di atas meja kayu")
		if err != nil {
			t.Fatalf("GenerateImage: %v", err)
		}
		if len(data) == 0 || data[0] != 0xFF || data[1] != 0xD8 {
			t.Fatalf("bukan JPEG: %d byte, awal % x", len(data), data[:min(4, len(data))])
		}
		t.Logf("JPEG %d byte, model %s", len(data), model)
		return
	}

	// ARENA_SMOKE=4 menguji jalur image-to-image (yang dipakai .seedream):
	// gambar referensi dibuat di Go, diunggah ke storage arena.ai lewat
	// Server Action, lalu dipakai sebagai attachment.
	if mode == "4" {
		ref := smokeReferenceJPEG(t)
		data, model, err := Generate(ctx, Request{
			Prompt: "ubah latar belakang menjadi biru muda, pertahankan objek utama",
			Images: [][]byte{ref},
			Model:  envString("ARENA_EDIT_MODEL", envString("ARENA_MODEL", defaultModel)),
		})
		if err != nil {
			t.Fatalf("Generate(edit): %v", err)
		}
		if len(data) == 0 || data[0] != 0xFF || data[1] != 0xD8 {
			t.Fatalf("bukan JPEG: %d byte", len(data))
		}
		t.Logf("edit OK: JPEG %d byte, model %s, referensi %d byte", len(data), model, len(ref))
		return
	}

	// ARENA_SMOKE=5 menguji transport CDP untuk payload .seedream: satu
	// Runtime.evaluate berisi base64 ~5 MB yang di-decode di halaman (bentuk
	// persis yang dikirim .seedream). Tidak memakai kuota arena.ai karena tidak
	// ada request generate, tapi tetap menyalakan Chrome + menyuntikkan cookie.
	if mode == "5" {
		b, err := launchBrowser(ctx, cookie, cookieFingerprint(cookie))
		if err != nil {
			t.Fatalf("launch: %v", err)
		}
		defer b.stop()

		raw := bytes.Repeat([]byte{0x9A}, 4<<20)
		payload := base64.StdEncoding.EncodeToString(raw)
		script := `(() => { const b64 = "` + payload + `"; const bin = atob(b64); ` +
			`const u8 = new Uint8Array(bin.length); ` +
			`for (let i = 0; i < bin.length; i++) u8[i] = bin.charCodeAt(i); ` +
			`return JSON.stringify({ bytes: u8.length }); })()`
		started := time.Now()
		out, err := b.evaluateString(ctx, script, false)
		if err != nil {
			t.Fatalf("evaluate payload %d MB gagal: %v", len(payload)>>20, err)
		}
		var got struct {
			Bytes int `json:"bytes"`
		}
		if uerr := json.Unmarshal([]byte(out), &got); uerr != nil || got.Bytes != len(raw) {
			t.Fatalf("balasan aneh: %.120q (err %v)", out, uerr)
		}
		t.Logf("payload %d MB (base64 %d MB) OK dalam %s",
			len(raw)>>20, len(payload)>>20, time.Since(started).Round(time.Millisecond))
		if free, known := cgroupFreeMemory(); known {
			t.Logf("sisa memori cgroup setelah payload: %d MB", free>>20)
		}
		return
	}

	b, err := launchBrowser(ctx, cookie, cookieFingerprint(cookie))
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	defer b.stop()

	ua, err := b.evaluateString(ctx, "navigator.userAgent", false)
	if err != nil {
		t.Fatalf("baca userAgent: %v", err)
	}
	t.Logf("userAgent: %s", ua)

	uaData, err := b.evaluateString(ctx, "JSON.stringify(navigator.userAgentData || null)", false)
	if err == nil {
		t.Logf("userAgentData: %s", uaData)
	}

	// Sidik jari environment yang paling sering dipakai untuk mendeteksi
	// browser otomatis — dicatat supaya perbedaan headless vs headed kelihatan.
	fp, ferr := b.evaluateString(ctx, `JSON.stringify({
      outer: [window.outerWidth, window.outerHeight],
      inner: [window.innerWidth, window.innerHeight],
      screen: [screen.width, screen.height, screen.availWidth, screen.availHeight],
      dpr: devicePixelRatio,
      plugins: navigator.plugins.length,
      cores: navigator.hardwareConcurrency,
      webdriver: navigator.webdriver,
      lang: navigator.language,
      webgl: (() => { try { const c = document.createElement('canvas').getContext('webgl');
        const d = c.getExtension('WEBGL_debug_renderer_info');
        return d ? c.getParameter(d.UNMASKED_RENDERER_WEBGL) : 'n/a'; } catch (e) { return 'err'; } })()
    })`, false)
	if ferr == nil {
		t.Logf("fingerprint: %s", fp)
	}

	cookies, err := b.evaluateString(ctx, "document.cookie.length", false)
	if err != nil {
		t.Fatalf("baca document.cookie: %v", err)
	}
	t.Logf("cookie terbaca di halaman: %s byte", cookies)

	ready, err := b.evaluateString(ctx, `(async () => {
      const t0 = Date.now();
      while (Date.now() - t0 < 20000) {
        if (window.grecaptcha && grecaptcha.enterprise && typeof grecaptcha.enterprise.execute === 'function') return 'siap';
        await new Promise(r => setTimeout(r, 250));
      }
      return 'tidak siap';
    })()`, true)
	if err != nil {
		t.Fatalf("cek grecaptcha: %v", err)
	}
	if ready != "siap" {
		t.Fatalf("grecaptcha %s — halaman arena.ai tidak memuat skrip reCAPTCHA", ready)
	}
	t.Log("grecaptcha.enterprise siap")

	if mode != "2" {
		return
	}

	ids, _, err := resolveModel(ctx, envString("ARENA_MODEL", defaultModel))
	if err != nil {
		t.Fatalf("resolve model: %v", err)
	}
	script := buildGenerateScript("apel merah di atas meja kayu", ids, "")
	raw, err := b.evaluateString(ctx, script, true)
	if err != nil {
		// Simpan skrip yang dieksekusi supaya kesalahan sintaks/escape bisa
		// diperiksa apa adanya (bukan ditebak dari pesan CDP). Ditulis ke
		// wabot-tmp (folder temp bot), bukan /tmp sistem.
		if derr := os.MkdirAll("wabot-tmp", 0o755); derr == nil {
			path := "wabot-tmp/arena-generate.js"
			if werr := os.WriteFile(path, []byte(script), 0o644); werr == nil {
				t.Logf("skrip tersimpan di %s", path)
			}
		}
		t.Fatalf("generate: %v", err)
	}
	var res generateResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("balasan bukan JSON: %.300s", raw)
	}
	t.Logf("status=%d gambar=%d error=%v", res.Status, len(res.Images), res.Errors)
	if len(res.Images) == 0 {
		t.Fatalf("tidak ada gambar. sample: %.300s", res.Sample)
	}
	t.Logf("URL gambar: %s", res.Images[0])
}

// smokeReferenceJPEG membuat gambar uji 512x512 (gradasi oranye + kotak
// putih) tanpa file di disk — cukup untuk membuktikan arena.ai menerima
// attachment data URL dan mengembalikan gambar hasil edit.
func smokeReferenceJPEG(t *testing.T) []byte {
	t.Helper()
	const side = 512
	img := image.NewRGBA(image.Rect(0, 0, side, side))
	for y := 0; y < side; y++ {
		for x := 0; x < side; x++ {
			img.Set(x, y, color.RGBA{R: uint8(200 + x*40/side), G: uint8(90 + y*60/side), B: 20, A: 255})
		}
	}
	for y := 170; y < 340; y++ {
		for x := 170; x < 340; x++ {
			img.Set(x, y, color.RGBA{R: 255, G: 255, B: 255, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode gambar uji: %v", err)
	}
	return buf.Bytes()
}
