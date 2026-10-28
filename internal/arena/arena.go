// Package arena adalah adapter ke arena.ai untuk membuat gambar lewat jalur
// chat langsung (satu model per sesi) memakai akun owner.
//
// Kenapa lewat browser, bukan HTTP client biasa: endpoint
// POST /nextjs-api/stream/create-evaluation mewajibkan token reCAPTCHA
// Enterprise v3 yang hanya bisa dibuat oleh JavaScript halaman arena.ai
// sendiri (sitekey 6LeTGMcsAAAAALuIlkVwIxaAuZA8VledA6d3Nnb0, action
// chat_submit). Token kosong ditolak server dengan
// {"error":"recaptcha validation failed"} — sudah diuji langsung. Solver
// captcha pihak ketiga bisa, tapi skornya mentok ~0.3 dan akun berisiko
// di-flag; memakai Chrome panel yang menjalankan JS asli memberi skor
// seperti manusia dan tanpa biaya per gambar.
//
// Alur yang TERBUKTI (diuji dari console browser, HTTP 200 + URL gambar):
//
//	GET  /image/direct            (halaman memuat grecaptcha)
//	JS   grecaptcha.enterprise.execute(...)     -> token
//	JS   POST /nextjs-api/stream/create-evaluation
//	JS   balasan stream: a2:[{"type":"image","image":"https://...jpeg"}]
//	Go   unduh URL tersebut (berlaku 1 jam) lalu kirim ke WhatsApp
//
// Catatan penting soal cookie: sesi login arena.ai disimpan di cookie
// arena-auth-prod-v1 (terbagi menjadi .0 dan .1) plus cf_clearance/__cf_bm.
// Cookie ini kedaluwarsa berkala, jadi disimpan di file (data/arena-cookie.txt)
// dan bisa diperbarui owner lewat command .seedream cookie tanpa deploy ulang.
package arena

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"wabot-go/internal/external"
)

// Error yang bisa dibedakan pemanggil untuk memberi pesan yang benar-benar
// bisa ditindaklanjuti owner (bukan "gagal" generik).
var (
	ErrNoCookie       = errors.New("cookie arena belum ada")
	ErrCookieRejected = errors.New("cookie arena ditolak")
	ErrRecaptcha      = errors.New("reCAPTCHA ditolak arena.ai")
	ErrChromeMissing  = errors.New("Chrome tidak bisa dipakai")
	ErrChromeOld      = errors.New("versi Chrome terlalu tua")
	ErrLowMemory      = errors.New("memori panel tidak cukup")
	ErrBrowser        = errors.New("browser gagal disiapkan")
	ErrModelUnknown   = errors.New("model tidak tersedia")
	ErrModelFailed    = errors.New("model gagal membuat gambar")
	ErrImageInvalid   = errors.New("gambar referensi tidak bisa dipakai")
	ErrUploadRef      = errors.New("gagal mengunggah gambar referensi ke arena.ai")
)

const (
	arenaBase    = "https://arena.ai"
	cookieFile   = "data/arena-cookie.txt"
	defaultModel = "seedream-5.0-pro"
	siteKey      = "6LeTGMcsAAAAALuIlkVwIxaAuZA8VledA6d3Nnb0"
	// recaptchaAction terverifikasi dari bundle JS arena.ai (14.js).
	recaptchaAction = "chat_submit"
	// defaultCx adalah nilai field "cx" (client interaction) yang dipakai
	// browser saat generate berhasil. Bisa ditimpa lewat ARENA_CX kalau
	// suatu saat formatnya berubah dan server menolak nilai lama.
	defaultCx  = "AhRyIWrGSQeLJgxCgFL6"
	catalogTTL = 24 * time.Hour

	// Batas gambar referensi: base64 membengkak ~33%, dan string data URL
	// ikut masuk ke ekspresi JS yang dikirim lewat CDP. 4 MB cukup untuk
	// foto ponsel setelah dinormalisasi, tapi tidak membuat panel 1 core
	// tersedak saat meng-encode.
	maxReferenceBytes = 4 << 20
	maxReferenceCount = 1
)

// Request adalah satu permintaan generate. Images kosong = text-to-image
// (perilaku .seedream); Images terisi = image-to-image / edit foto.
type Request struct {
	Prompt string
	Images [][]byte
	// Model kosong berarti ARENA_MODEL (default seedream-5.0-pro).
	Model string
}

// Catalog model di-cache di memori: namanya jarang berubah, dan satu
// request HTTP tambahan per generate hanya memperlambat di panel 1 core.
var (
	catalogMu   sync.Mutex
	catalogAt   time.Time
	catalogByID map[string]string // publicName/displayName (lowercase) -> UUID
)

// GenerateImage membuat satu gambar dari prompt dan mengembalikan bytes-nya.
// Pembungkus tipis Generate untuk pemanggil text-to-image.
func GenerateImage(ctx context.Context, prompt string) ([]byte, []byte, error) {
	data, model, err := Generate(ctx, Request{Prompt: prompt})
	if err != nil {
		return nil, nil, err
	}
	return data, []byte(model), nil
}

// Generate menjalankan satu permintaan ke arena.ai (text-to-image maupun
// image-to-image bila req.Images terisi) dan mengembalikan byte gambar plus
// nama model yang dipakai. Dipanggil dari goroutine plugin; seluruh proses
// diserialkan karena panel hanya punya 1 core dan Chrome ~300MB.
func Generate(ctx context.Context, req Request) ([]byte, string, error) {
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return nil, "", fmt.Errorf("prompt kosong")
	}
	attachments, err := buildImagesPayload(req.Images)
	if err != nil {
		return nil, "", err
	}

	genMu.Lock()
	defer genMu.Unlock()

	cookie, err := ReadCookie()
	if err != nil {
		return nil, "", err
	}
	modelName := strings.TrimSpace(req.Model)
	if modelName == "" {
		modelName = envString("ARENA_MODEL", defaultModel)
	}
	modelID, resolvedName, err := resolveModel(ctx, modelName)
	if err != nil {
		return nil, "", err
	}
	b, err := getBrowser(ctx, cookie)
	if err != nil {
		return nil, "", err
	}
	// Tandai browser sedang dipakai: idle-stopper tidak boleh mematikannya di
	// tengah jalan (dulu .seedream bisa dibunuh setelah 150 detik karena unggah
	// 4MB + generate membuat satu permintaan lebih panjang dari batas idle).
	b.beginRequest()
	defer b.endRequest()

	script := buildGenerateScript(prompt, modelID, attachments)
	result, err := b.evaluateString(ctx, script, true)
	if err != nil {
		hint := b.failureHint()
		if b.dead() {
			// Tab/Chrome sudah mati: buang supaya perintah berikutnya
			// menyalakan Chrome baru, bukan mencoba koneksi yang sama.
			dropBrowser(b)
		}
		if hint != "" {
			return nil, "", fmt.Errorf("gagal menjalankan permintaan ke arena.ai: %w (%s)", err, hint)
		}
		return nil, "", fmt.Errorf("gagal menjalankan permintaan ke arena.ai: %w", err)
	}

	imageURL, err := parseGenerateResult(result)
	if err != nil {
		return nil, "", err
	}

	data, err := external.DownloadBytes(ctx, imageURL)
	if err != nil {
		return nil, "", fmt.Errorf("gagal unduh hasil gambar: %w", err)
	}

	// Halaman arena.ai memperbarui cookie sesi (Supabase merotasi refresh
	// token) — simpan kembali supaya generate berikutnya tidak memakai
	// token basi.
	b.saveCookies()

	return data, resolvedName, nil
}

// WaitChromeExit disediakan untuk pemanggil yang ingin mematikan Chrome
// secara eksplisit (mis. saat bot shutdown) — idle-stop sudah menangani
// kasus normal.
func Shutdown() { shutdownBrowser() }

// generateResult adalah bentuk balasan skrip JS di halaman.
type generateResult struct {
	Status int      `json:"status"`
	Images []string `json:"images"`
	Errors []string `json:"errors"`
	Sample string   `json:"sample"`
}

// parseGenerateResult menerjemahkan balasan JS menjadi URL gambar atau error
// yang bisa ditindaklanjuti.
func parseGenerateResult(raw string) (string, error) {
	var res generateResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		return "", fmt.Errorf("balasan arena.ai tidak dikenali: %.200s", raw)
	}
	if len(res.Images) > 0 && strings.HasPrefix(res.Images[0], "http") {
		return res.Images[0], nil
	}
	joined := strings.ToLower(strings.Join(res.Errors, " | "))
	switch {
	case res.Status == http.StatusUnauthorized:
		return "", fmt.Errorf("%w: sesi login tidak dikenali, perbarui cookie dengan .seedream cookie", ErrCookieRejected)
	case strings.Contains(joined, "auth:"):
		// Probe /api/me di halaman: sesi anonim. Pesannya sengaja beda dari
		// kegagalan unggah supaya owner tahu yang perlu diganti cookie.
		return "", fmt.Errorf("%w: %s", ErrCookieRejected, strings.Join(res.Errors, ", "))
	case strings.Contains(joined, "recaptcha"):
		return "", fmt.Errorf("%w: perbarui cookie dengan .seedream cookie lalu coba lagi", ErrRecaptcha)
	case strings.Contains(joined, "upload:"):
		// Rantai unggah gambar referensi (Server Action + R2) gagal.
		return "", fmt.Errorf("%w: %s", ErrUploadRef, strings.Join(res.Errors, ", "))
	case strings.Contains(joined, "prompt failed"):
		// Token reCAPTCHA diterima (bukan 401/403), tapi model gagal
		// menghasilkan gambar. Biasanya sementara — coba lagi.
		return "", fmt.Errorf("%w: arena.ai menerima permintaan tetapi model gagal membuat gambar (prompt failed), coba lagi", ErrModelFailed)
	case strings.Contains(joined, "rate") || strings.Contains(joined, "limit"):
		return "", fmt.Errorf("arena.ai membatasi permintaan: %s", strings.Join(res.Errors, ", "))
	case len(res.Errors) > 0:
		return "", fmt.Errorf("arena.ai menolak permintaan: %s (HTTP %d, %.200s)", strings.Join(res.Errors, ", "), res.Status, res.Sample)
	default:
		return "", fmt.Errorf("arena.ai tidak mengembalikan gambar (HTTP %d): %.200s", res.Status, res.Sample)
	}
}

// buildGenerateScript merangkai JS yang dijalankan DI DALAM halaman arena.ai.
// Semuanya berjalan di origin yang sama dengan halaman, sehingga cookie sesi
// ikut otomatis dan token reCAPTCHA sah. imagesJSON adalah literal JSON
// [{name,mime,b64}] (sudah lewat jsQuote) untuk jalur edit foto.
func buildGenerateScript(prompt, modelID, imagesJSON string) string {
	if strings.TrimSpace(imagesJSON) == "" {
		imagesJSON = "[]"
	}
	// ARENA_CX="-" = kirim tanpa field cx (untuk menguji apakah server masih
	// butuh nilai client-interaction itu). Default: nilai yang sudah terbukti.
	cxLine := ""
	if cx := strings.TrimSpace(os.Getenv("ARENA_CX")); cx != "-" {
		if cx == "" {
			cx = defaultCx
		}
		cxLine = "      cx: " + jsQuote(cx) + ","
	}
	r := strings.NewReplacer(
		"__SITEKEY__", jsQuote(siteKey),
		"__ACTION__", jsQuote(recaptchaAction),
		"__PROMPT__", jsQuote(prompt),
		"__MODEL__", jsQuote(modelID),
		"__IMAGES__", imagesJSON,
		"__CXLINE__", cxLine,
	)
	return r.Replace(generateScriptTemplate)
}

// arenaAttachment adalah satu entri experimental_attachments versi UI
// arena.ai (bentuk ini terbaca dari schema Zod bundle-nya:
// {name?, contentType?, url, thumbnailUrl?}).
type arenaAttachment struct {
	Name        string `json:"name"`
	ContentType string `json:"contentType"`
	URL         string `json:"url"`
}

// arenaImage adalah gambar referensi yang dikirim ke skrip halaman. Bytes-nya
// di-base64 di Go, lalu diubah jadi Blob di dalam browser sebelum diunggah.
// Server arena.ai menolak URL sembarang ("Invalid content") — termasuk data
// URL — jadi gambar HARUS lewat storage mereka (R2) via Server Action
// generateUploadUrl + getSignedUrl.
type arenaImage struct {
	Name string `json:"name"`
	Mime string `json:"mime"`
	B64  string `json:"b64"`
}

// buildImagesPayload mengubah byte gambar menjadi literal JSON siap-tempel
// di skrip halaman (base64 + mime + nama berkas).
func buildImagesPayload(images [][]byte) (string, error) {
	if len(images) == 0 {
		return "[]", nil
	}
	if len(images) > maxReferenceCount {
		return "", fmt.Errorf("%w: maksimal %d gambar referensi", ErrImageInvalid, maxReferenceCount)
	}
	out := make([]arenaImage, 0, len(images))
	for i, img := range images {
		if len(img) == 0 {
			return "", fmt.Errorf("%w: gambar referensi kosong", ErrImageInvalid)
		}
		if len(img) > maxReferenceBytes {
			return "", fmt.Errorf("%w: ukuran %d KB melebihi batas %d KB", ErrImageInvalid, len(img)>>10, maxReferenceBytes>>10)
		}
		mime := imageMime(img)
		if mime == "" {
			return "", fmt.Errorf("%w: format gambar tidak dikenali (pakai JPEG/PNG)", ErrImageInvalid)
		}
		ext := ".jpg"
		if mime == "image/png" {
			ext = ".png"
		}
		out = append(out, arenaImage{
			Name: fmt.Sprintf("ref-%d%s", i+1, ext),
			Mime: mime,
			B64:  base64.StdEncoding.EncodeToString(img),
		})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrImageInvalid, err)
	}
	// json.Marshal sudah meng-escape <, >, dan & menjadi \u003c dll, jadi
	// hasilnya aman ditempel sebagai nilai literal JS tanpa jsQuote.
	return string(b), nil
}

// imageMime mendeteksi tipe gambar dari magic byte-nya (bukan dari nama file
// yang tidak kita miliki — media WhatsApp datang sebagai byte mentah).
func imageMime(data []byte) string {
	switch {
	case len(data) > 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return "image/jpeg"
	case len(data) > 8 && string(data[1:4]) == "PNG":
		return "image/png"
	default:
		return ""
	}
}

// jsQuote meng-encode string Go menjadi literal string JS yang aman.
func jsQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// generateScriptTemplate: UUIDv7 dibuat di JS karena server menolak UUIDv4
// ("Invalid UUIDv7 ID format or time discrepancy") — ini bukan pilihan gaya.
const generateScriptTemplate = `(async () => {
  const SITEKEY = __SITEKEY__;
  const ACTION = __ACTION__;
  const uuidv7 = () => {
    const ms = Date.now();
    const b = crypto.getRandomValues(new Uint8Array(16));
    const dv = new DataView(b.buffer);
    dv.setUint16(0, Math.floor(ms / 2 ** 32));
    dv.setUint32(2, ms >>> 0);
    b[6] = (b[6] & 0x0f) | 0x70;
    b[8] = (b[8] & 0x3f) | 0x80;
    const h = [...b].map(x => x.toString(16).padStart(2, '0')).join('');
    return h.slice(0,8)+'-'+h.slice(8,12)+'-'+h.slice(12,16)+'-'+h.slice(16,20)+'-'+h.slice(20);
  };
  const IMAGES = __IMAGES__;
  // --- unggah gambar referensi ------------------------------------------
  // arena.ai menolak attachment dengan URL sembarang ("Invalid content"),
  // termasuk data URL: server hanya menerima URL dari storage mereka (R2).
  // Unggahan memakai Server Action aplikasi sendiri; id action berubah tiap
  // deploy arena.ai, jadi dipanen dari bundle halaman lalu di-cache di
  // window selama halaman hidup.
  const b64Blob = (b64, mime) => {
    const bin = atob(b64);
    const arr = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) arr[i] = bin.charCodeAt(i);
    return new Blob([arr], { type: mime });
  };
  const flatten = (s) => String(s == null ? '' : s).replace(/\s+/g, ' ');
  // Flight/RSC kadang meng-escape "/" dan "&" di dalam string JSON.
  const unescapeFlight = (s) => String(s || '').replace(/\\u0026/g, '&').replace(/\\\//g, '/');
  const pick = (text, key) => {
    const t = unescapeFlight(text);
    const patterns = [
      new RegExp('"' + key + '":"([^"]+)"'),
      new RegExp('\\\\"' + key + '\\\\":\\\\"([^"\\\\]+)')
    ];
    for (const re of patterns) {
      const m = t.match(re);
      if (m) return m[1];
    }
    return '';
  };
  // arena.ai membalas {"success":false,"error":"..."} untuk penolakan.
  const failureOf = (text) => {
    const t = unescapeFlight(flatten(text));
    if (!/"success":false/.test(t)) return '';
    const m = t.match(/"error":"([^"]*)"/);
    return m ? m[1] : 'success:false';
  };
  // Sesi anonim membuat action unggah menjawab "Failed to generate upload
  // url" — pesan itu menyesatkan. /api/me menjawab 401 untuk sesi mati, jadi
  // diperiksa lebih dulu supaya owner mendapat pesan .seedream cookie yang benar.
  const checkSession = async () => {
    let r;
    try {
      r = await fetch('/api/me', { headers: { 'Accept': 'application/json' }, cache: 'no-store' });
    } catch (e) {
      return; // gangguan jaringan: biarkan alur normal yang melapor
    }
    if (r.status === 401 || r.status === 403) {
      throw new Error('auth: sesi arena.ai tidak dikenali (HTTP ' + r.status + ')');
    }
  };
  const chunkSources = async () => {
    const out = [];
    const add = (u) => {
      try {
        const abs = new URL(u, location.href).href;
        if (abs.startsWith(location.origin) && /\.js(\?|$)/.test(abs) && out.indexOf(abs) < 0) out.push(abs);
      } catch (e) {}
    };
    for (const m of document.documentElement.innerHTML.matchAll(/["']([^"']+\.js(?:\?[^"']*)?)["']/g)) add(m[1]);
    for (const s of document.scripts) add(s.src);
    for (const e of performance.getEntriesByType('resource')) add(e.name);
    return out;
  };
  const discoverActions = async (force) => {
    if (!force && window.__arenaActions) return window.__arenaActions;
    const srcs = await chunkSources();
    let up = '', sign = '';
    for (const u of srcs) {
      let t = '';
      try { t = await (await fetch(u, { cache: 'no-store' })).text(); } catch (e) { continue; }
      if (!up) { const m = t.match(/([0-9a-f]{40,})"[^)]{0,240}"generateUploadUrl"/); if (m) up = m[1]; }
      if (!sign) { const m = t.match(/([0-9a-f]{40,})"[^)]{0,240}"getSignedUrl"/); if (m) sign = m[1]; }
      if (up && sign) break;
    }
    if (!up || !sign) throw new Error('upload: id Server Action tidak ditemukan (' + srcs.length + ' chunk dipindai)');
    window.__arenaActions = { up: up, sign: sign };
    return window.__arenaActions;
  };
  const callAction = async (id, args) => {
    // Selalu ke /image/direct: setelah generate, aplikasi bisa memindahkan
    // history ke /c/<uuid> dan action di route itu tidak selalu tersedia.
    const r = await fetch(location.origin + '/image/direct', {
      method: 'POST',
      headers: {
        'Next-Action': id,
        'Content-Type': 'text/plain;charset=UTF-8',
        'Accept': 'text/x-component'
      },
      body: JSON.stringify(args)
    });
    const text = await r.text();
    if (!r.ok) throw new Error('upload: Server Action HTTP ' + r.status + ' ' + flatten(text).slice(0, 160));
    if (/^\s*</.test(text)) throw new Error('upload: balasan bukan RSC (HTML, HTTP ' + r.status + ')');
    return text;
  };
  const uploadOne = async (ids, img) => {
    const meta = await callAction(ids.up, [img.name, img.mime]);
    const uploadUrl = pick(meta, 'uploadUrl');
    const key = pick(meta, 'key');
    if (!uploadUrl || !key) {
      const fail = failureOf(meta);
      throw new Error('upload: uploadUrl/key tidak ada' + (fail ? ' (arena: ' + fail + ')' : '') +
        ', balasan: ' + flatten(meta).slice(0, 240));
    }
    const put = await fetch(uploadUrl, {
      method: 'PUT',
      headers: { 'Content-Type': img.mime },
      body: b64Blob(img.b64, img.mime)
    });
    if (!put.ok) throw new Error('upload: storage menolak berkas HTTP ' + put.status + ' ' + flatten(await put.text()).slice(0, 160));
    const signedText = await callAction(ids.sign, [key.replace(/^\//, '')]);
    const url = pick(signedText, 'url');
    if (!url) {
      const fail = failureOf(signedText);
      throw new Error('upload: signed URL tidak ada' + (fail ? ' (arena: ' + fail + ')' : '') +
        ', balasan: ' + flatten(signedText).slice(0, 240));
    }
    return { name: img.name, contentType: img.mime, url: url };
  };
  const buildAttachments = async () => {
    if (!IMAGES.length) return [];
    await checkSession();
    const run = async (force) => {
      const ids = await discoverActions(force);
      const out = [];
      for (const img of IMAGES) out.push(await uploadOne(ids, img));
      return out;
    };
    try {
      return await run(false);
    } catch (e) {
      // id action bisa basi kalau arena.ai deploy ulang saat halaman masih
      // terbuka: buang cache, panen ulang, coba sekali lagi.
      window.__arenaActions = null;
      return await run(true);
    }
  };
  try {
    const sleep = (ms) => new Promise(r => setTimeout(r, ms));
    const waitCaptcha = async () => {
      // 45 detik, bukan 20: panel 1 core butuh waktu lama mem-parse halaman
      // arena.ai + memuat skrip reCAPTCHA, dan halaman yang baru dimuat ulang
      // (lihat wakeCaptcha di chrome.go) memulai dari nol.
      const started = Date.now();
      while (Date.now() - started < 45000) {
        if (window.grecaptcha && grecaptcha.enterprise && typeof grecaptcha.enterprise.execute === 'function') {
          await new Promise(res => grecaptcha.enterprise.ready(res));
          return;
        }
        await sleep(250);
      }
      throw new Error('grecaptcha tidak siap');
    };
    // Token dibuat sesaat setelah permintaan, jadi tunggu reCAPTCHA siap dan
    // biarkan ia mengumpulkan sinyal dulu — token yang dipanen terlalu cepat
    // cenderung berskor rendah dan ditolak server.
    await waitCaptcha();
    await sleep(1500);
    const attachments = await buildAttachments();
    const freshToken = () => grecaptcha.enterprise.execute(SITEKEY, { action: ACTION });
    const post = async (token) => {
      const body = {
        id: uuidv7(),
        mode: 'direct-battle',
        modelAId: __MODEL__,
        userMessageId: uuidv7(),
        modelAMessageId: uuidv7(),
		userMessage: { content: __PROMPT__, experimental_attachments: attachments, metadata: {} },
        modality: 'image',
        recaptchaV3Token: token,
__CXLINE__
      };
      const r = await fetch('/nextjs-api/stream/create-evaluation', {
        method: 'POST',
        body: JSON.stringify(body)
      });
      const text = await r.text();
      const images = [...text.matchAll(/"type":"image","image":"([^"]+)"/g)].map(m => m[1]);
      const errors = [...text.matchAll(/"error":"([^"]+)"/g)].map(m => m[1]);
      return { status: r.status, images, errors, sample: text.slice(0, 400) };
    };
    let out = await post(await freshToken());
    // Satu percobaan ulang dengan token baru: penolakan reCAPTCHA sering
    // bersifat sesaat (token pertama dibuat saat halaman masih "dingin").
    if (out.status === 403 && /recaptcha/i.test(out.errors.join(' '))) {
      await sleep(2000);
      out = await post(await freshToken());
    }
    return JSON.stringify(out);
  } catch (e) {
    return JSON.stringify({ status: 0, images: [], errors: [String((e && e.message) || e)], sample: '' });
  }
})()`

// ReadCookie membaca cookie arena.ai dari file dan memvalidasi bentuknya.
func ReadCookie() (string, error) {
	raw, err := os.ReadFile(cookieFile)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%w: setel sekali dengan .seedream cookie <cookie>", ErrNoCookie)
		}
		return "", fmt.Errorf("gagal baca %s: %w", cookieFile, err)
	}
	cookie := normalizeCookie(string(raw))
	if cookie == "" {
		return "", fmt.Errorf("%w: file %s kosong", ErrNoCookie, cookieFile)
	}
	if !strings.Contains(cookie, "arena-auth-prod-v1") {
		return "", fmt.Errorf("%w: cookie tidak memuat arena-auth-prod-v1 (belum login?)", ErrNoCookie)
	}
	return cookie, nil
}

// WriteCookie menyimpan cookie baru secara atomik supaya pembacaan di tengah
// penulisan tidak pernah melihat file separuh jadi.
func WriteCookie(cookie string) error {
	cookie = normalizeCookie(cookie)
	if cookie == "" {
		return fmt.Errorf("cookie kosong")
	}
	if !strings.Contains(cookie, "arena-auth-prod-v1") {
		return fmt.Errorf("cookie tidak memuat arena-auth-prod-v1 — pastikan kamu menyalin cookie dari situs arena.ai dalam keadaan login")
	}
	if err := os.MkdirAll(filepath.Dir(cookieFile), 0755); err != nil {
		return err
	}
	tmp := cookieFile + ".tmp"
	if err := os.WriteFile(tmp, []byte(cookie+"\n"), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, cookieFile)
}

// normalizeCookie membersihkan hasil salin-tempel dari DevTools:
// membuang awalan "cookie:", baris kosong, dan spasi/newline berlebih.
func normalizeCookie(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "Cookie:"), "cookie:")
	lines := strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' })
	parts := make([]string, 0, len(lines))
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		parts = append(parts, ln)
	}
	out := strings.Join(parts, " ")
	out = strings.ReplaceAll(out, " ;", ";")
	return strings.TrimSpace(out)
}

// parseCookies mengubah header cookie menjadi objek cookie CDP. Memakai
// bentuk "url" supaya domain/path diisi Chrome sendiri — lebih tahan
// terhadap perbedaan format cookie antar versi.
func parseCookies(header string) []map[string]any {
	out := make([]map[string]any, 0, 16)
	for _, part := range strings.Split(header, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		i := strings.Index(part, "=")
		if i <= 0 {
			continue
		}
		name := strings.TrimSpace(part[:i])
		value := part[i+1:]
		if name == "" {
			continue
		}
		out = append(out, map[string]any{
			"name":  name,
			"value": value,
			"url":   arenaBase,
		})
	}
	return out
}

// cookieFingerprint dipakai untuk mendeteksi cookie yang berubah (lewat
// .seedream cookie) sehingga Chrome dinyalakan ulang dengan cookie baru.
func cookieFingerprint(cookie string) string {
	sum := sha256.Sum256([]byte(cookie))
	return hex.EncodeToString(sum[:])
}

// resolveModel menerjemahkan nama model (mis. "seedream-5.0-pro") menjadi
// UUID yang dipakai API. UUID boleh diberikan langsung lewat ARENA_MODEL.
func resolveModel(ctx context.Context, name string) (id, resolvedName string, err error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = defaultModel
	}
	if looksLikeUUID(name) {
		return name, name, nil
	}
	catalog, err := loadCatalog(ctx)
	if err != nil {
		return "", "", err
	}
	if id, ok := catalog[strings.ToLower(name)]; ok {
		return id, name, nil
	}
	return "", "", fmt.Errorf("%w: %q tidak ada di catalog arena.ai", ErrModelUnknown, name)
}

// loadCatalog mengambil daftar model arena.ai (endpoint publik, tanpa auth)
// dan menyimpannya di memori selama catalogTTL.
func loadCatalog(ctx context.Context) (map[string]string, error) {
	catalogMu.Lock()
	defer catalogMu.Unlock()
	if catalogByID != nil && time.Since(catalogAt) < catalogTTL {
		return catalogByID, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, arenaBase+"/nextjs-api/model-catalog", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("gagal ambil catalog model: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("catalog model menjawab HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("gagal baca catalog model: %w", err)
	}

	var arenas []struct {
		Arena  string `json:"arena"`
		Models []struct {
			ID          string `json:"id"`
			PublicName  string `json:"publicName"`
			DisplayName string `json:"displayName"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &arenas); err != nil {
		return nil, fmt.Errorf("catalog model bukan JSON valid: %w", err)
	}
	index := make(map[string]string, 256)
	for _, a := range arenas {
		for _, m := range a.Models {
			if m.ID == "" {
				continue
			}
			if m.PublicName != "" {
				index[strings.ToLower(m.PublicName)] = m.ID
			}
			if m.DisplayName != "" {
				if _, exists := index[strings.ToLower(m.DisplayName)]; !exists {
					index[strings.ToLower(m.DisplayName)] = m.ID
				}
			}
		}
	}
	if len(index) == 0 {
		return nil, fmt.Errorf("%w: catalog model kosong", ErrModelUnknown)
	}
	catalogByID = index
	catalogAt = time.Now()
	return index, nil
}

// looksLikeUUID mengecek format UUID sederhana (8-4-4-4-12).
func looksLikeUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
				return false
			}
		}
	}
	return true
}

// envString membaca env dengan nilai default.
func envString(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// envDuration membaca env durasi dalam detik.
func envDuration(key string, def time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	if secs, err := time.ParseDuration(raw); err == nil && secs > 0 {
		return secs
	}
	if n, err := time.ParseDuration(raw + "s"); err == nil && n > 0 {
		return n
	}
	return def
}
