package geminiapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// chrome.go — jalur unduh terakhir untuk gambar hasil Gemini.
//
// Kenapa perlu browser: URL `/gg-dl/…` hanya mengalihkan ke URL bertanda
// tangan di `work.fife.usercontent.google.com` lalu ke
// `lh3.googleusercontent.com/rd-gg-dl/…`, dan URL terakhir itu menolak
// permintaan HTTP biasa (Go maupun curl) dengan 403 — termasuk saat header
// browser disalin persis, IPv4/IPv6, tanpa cookie. Browser owner sempat
// membuka URL yang sama (walau bisa jadi dari cache), jadi yang membedakan
// kemungkinan besar konteks klien (TLS/HTTP2 Chrome), bukan isi URL. Chrome di
// panel mereproduksi konteks itu; cookie sesi Google sengaja tidak disuntikkan
// karena permintaan gambar di DevTools owner tidak membawa header `Cookie`.
//
// Chrome hanya dinyalakan kalau seluruh rung HTTP gagal, lalu dimatikan lagi:
// panel 1 core / 1,6 GB tidak sanggup menahan Chrome menganggur. Profil di
// `gemini-profile/` (gitignored) hanya menyimpan clearance/keadaan browser.

const (
	// geminiProfileDir adalah profil Chrome (gitignored, sejajar rwa-profile).
	geminiProfileDir = "gemini-profile"
	// geminiChromeMinFreeMemory: jangan menyalakan Chrome kalau sisa memori
	// cgroup di bawah ini (Chrome lain di panel juga memakai memori).
	geminiChromeMinFreeMemory = 500 << 20
	// geminiChromeTimeout membatasi satu unduhan lewat Chrome: peluncuran +
	// navigasi + fetch. Gambar hasil Gemini biasanya < 5 MB.
	geminiChromeTimeout = 75 * time.Second
	// geminiChromeMaxBytes menjaga payload base64 di Runtime.evaluate tetap
	// wajar (CDP read limit 64 MB).
	geminiChromeMaxBytes = 12 << 20
)

// chromeResult adalah hasil satu unduhan lewat Chrome. Hanya status/tipe/ukuran
// dan potongan body yang boleh dilaporkan — URL bertanda tangan tidak pernah
// dikembalikan.
type chromeResult struct {
	Status      int
	ContentType string
	Bytes       []byte
	Snippet     string
	Err         error
}

// fetchMediaViaChrome membuka target di Chrome, lalu mengambil byte gambar
// dari respons jaringan lewat halaman itu sendiri. Cookie sesi sengaja TIDAK
// disuntikkan: permintaan gambar yang berhasil di browser owner tidak membawa
// header Cookie sama sekali (DevTools), jadi konteks yang perlu direproduksi
// adalah TLS/HTTP2 Chrome itu sendiri, bukan sesi Google.
func fetchMediaViaChrome(ctx context.Context, target string) chromeResult {
	res := chromeResult{}
	if free, known := chromeCgroupFree(); known && free < geminiChromeMinFreeMemory {
		res.Err = fmt.Errorf("chrome: sisa memori %d MB, tunggu proses lain selesai", free>>20)
		return res
	}
	chromePath, err := chromeFindBinary()
	if err != nil {
		res.Err = err
		return res
	}
	display, headed, xvfb, err := chromeStartDisplay(ctx)
	if err != nil {
		res.Err = err
		return res
	}
	if xvfb != nil {
		defer chromeStopProcess(xvfb)
	}
	chromePrepareProfile(geminiProfileDir)
	port, err := chromeFreePort()
	if err != nil {
		res.Err = err
		return res
	}
	cwd, err := os.Getwd()
	if err != nil {
		res.Err = fmt.Errorf("chrome: gagal baca workdir: %w", err)
		return res
	}
	profile := filepath.Join(cwd, geminiProfileDir)

	args := []string{
		"--no-sandbox",
		"--disable-dev-shm-usage",
		"--no-first-run",
		"--no-default-browser-check",
		"--no-zygote",
		"--disable-extensions",
		"--disable-background-networking",
		"--disable-sync",
		"--disable-default-apps",
		"--disable-breakpad",
		"--disable-crash-reporter",
		"--disable-blink-features=AutomationControlled",
		"--renderer-process-limit=1",
		"--window-size=1280,900",
		"--lang=id-ID",
		"--user-data-dir=" + profile,
		"--remote-debugging-port=" + strconv.Itoa(port),
		"about:blank",
	}
	if !headed {
		args = append([]string{"--headless=new"}, args...)
	} else {
		args = append(args, chromeHeadedFlags()...)
	}

	cmd := exec.Command(chromePath, args...)
	cmd.Dir = cwd
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if display != "" {
		cmd.Env = append(os.Environ(), "DISPLAY="+display)
	}
	if err := cmd.Start(); err != nil {
		res.Err = fmt.Errorf("chrome: gagal menjalankan: %w", err)
		return res
	}
	defer chromeStopProcess(cmd)

	fctx, cancel := context.WithTimeout(ctx, geminiChromeTimeout)
	defer cancel()

	wsURL, err := chromeWaitDevTools(fctx, port, 40*time.Second)
	if err != nil {
		res.Err = err
		return res
	}
	c, err := chromeDialCDP(fctx, wsURL)
	if err != nil {
		res.Err = err
		return res
	}
	defer c.close()

	if _, err := c.call(fctx, "Page.enable", nil); err != nil {
		res.Err = fmt.Errorf("chrome: %w", err)
		return res
	}
	if _, err := c.call(fctx, "Network.enable", nil); err != nil {
		res.Err = fmt.Errorf("chrome: %w", err)
		return res
	}

	if _, err := c.call(fctx, "Page.navigate", map[string]any{"url": target}); err != nil {
		res.Err = fmt.Errorf("chrome: navigasi gagal: %w", err)
		return res
	}
	chromeWaitDocument(fctx, c, 25*time.Second)

	value, err := chromeEvalFetch(fctx, c)
	if err != nil {
		res.Err = fmt.Errorf("chrome: %w", err)
		return res
	}
	res.Status = value.Status
	res.ContentType = value.Type
	if value.OK {
		data, derr := base64.StdEncoding.DecodeString(value.B64)
		if derr != nil {
			res.Err = fmt.Errorf("chrome: base64 rusak: %w", derr)
			return res
		}
		if len(data) > geminiChromeMaxBytes {
			res.Err = fmt.Errorf("chrome: gambar terlalu besar (%d byte)", len(data))
			return res
		}
		res.Bytes = data
		return res
	}
	body := strings.Join(strings.Fields(value.Body), " ")
	if len(body) > 200 {
		body = body[:200] + "…"
	}
	res.Snippet = body
	if body != "" {
		res.Err = fmt.Errorf("chrome: %s", body)
	} else {
		res.Err = fmt.Errorf("chrome: halaman tidak memuat gambar (status %d)", value.Status)
	}
	return res
}

// chromeFetchJS mengambil gambar yang sudah dimuat halaman. Halaman hasil
// navigasi ke URL gambar adalah dokumen gambar (ada <img>), jadi kanvas di
// origin yang sama bisa membaca pikselnya tanpa permintaan HTTP kedua —
// penting karena permintaan fetch baru punya konteks (Sec-Fetch*) yang bisa
// ditolak CDN. Fetch hanya cadangan kalau kanvas gagal (mis. gambar rusak).
const chromeFetchJS = `(async () => {
  const fail = (status, body) => ({ok:false, status:status||0, type:"", body:(body||"").slice(0,300)});
  const img = document.images && document.images.length ? document.images[0] : null;
  if (!img || !img.src) {
    return fail(0, document.body ? document.body.innerText : "");
  }
  try {
    if (!img.complete) { await img.decode().catch(() => {}); }
    const w = img.naturalWidth || img.width;
    const h = img.naturalHeight || img.height;
    if (w > 0 && h > 0) {
      const max = 2048;
      const k = Math.min(1, max / w, max / h);
      const dw = Math.max(1, Math.round(w * k));
      const dh = Math.max(1, Math.round(h * k));
      const cv = document.createElement("canvas");
      cv.width = dw;
      cv.height = dh;
      const ctx = cv.getContext("2d");
      ctx.drawImage(img, 0, 0, dw, dh);
      const b64 = (cv.toDataURL("image/jpeg", 0.95).split(",")[1]) || "";
      if (b64) { return {ok:true, status:200, type:"image/jpeg", b64:b64}; }
    }
  } catch (e) {}
  try {
    const r = await fetch(img.src, {credentials:"include", cache:"no-store"});
    if (!r.ok) {
      let t = "";
      try { t = await r.text(); } catch (e) {}
      return fail(r.status, t);
    }
    const b = await r.arrayBuffer();
    const u = new Uint8Array(b);
    let s = "";
    const CH = 0x8000;
    for (let i = 0; i < u.length; i += CH) {
      s += String.fromCharCode.apply(null, u.subarray(i, i + CH));
    }
    return {ok:true, status:r.status, type:r.headers.get("content-type")||"", b64:btoa(s)};
  } catch (e) {
    return fail(0, String(e));
  }
})()`

type chromeEvalValue struct {
	OK     bool   `json:"ok"`
	Status int    `json:"status"`
	Type   string `json:"type"`
	B64    string `json:"b64"`
	Body   string `json:"body"`
}

func chromeEvalFetch(ctx context.Context, c *chromeCDP) (chromeEvalValue, error) {
	var out chromeEvalValue
	raw, err := c.call(ctx, "Runtime.evaluate", map[string]any{
		"expression":    chromeFetchJS,
		"awaitPromise":  true,
		"returnByValue": true,
	})
	if err != nil {
		return out, err
	}
	var env struct {
		Result struct {
			Value chromeEvalValue `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return out, fmt.Errorf("baca hasil evaluasi: %w", err)
	}
	return env.Result.Value, nil
}

// chromeWaitDocument menunggu halaman selesai dimuat atau <img> muncul.
func chromeWaitDocument(ctx context.Context, c *chromeCDP, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return
		}
		raw, err := c.call(ctx, "Runtime.evaluate", map[string]any{
			"expression":    `(() => ({ready: document.readyState, img: !!(document.images && document.images.length), text: (document.body ? document.body.innerText : "").slice(0,80)}))()`,
			"returnByValue": true,
		})
		if err == nil {
			var env struct {
				Result struct {
					Value struct {
						Ready string `json:"ready"`
						Img   bool   `json:"img"`
					} `json:"value"`
				} `json:"result"`
			}
			if json.Unmarshal(raw, &env) == nil {
				if env.Result.Value.Img || env.Result.Value.Ready == "complete" {
					return
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// --- Chrome/CDP minimal (pola sama dengan internal/reactionwa) ---

type chromeCDP struct {
	conn *websocket.Conn

	mu       sync.Mutex
	nextID   int64
	pending  map[int64]chan chromeCDPResp
	closed   bool
	closeErr error
}

type chromeCDPResp struct {
	Result json.RawMessage
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
}

func chromeDialCDP(ctx context.Context, wsURL string) (*chromeCDP, error) {
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{})
	if err != nil {
		return nil, fmt.Errorf("chrome: gagal konek DevTools: %w", err)
	}
	conn.SetReadLimit(64 << 20)
	c := &chromeCDP{conn: conn, pending: make(map[int64]chan chromeCDPResp)}
	go c.readLoop()
	return c, nil
}

func (c *chromeCDP) readLoop() {
	for {
		_, data, err := c.conn.Read(context.Background())
		if err != nil {
			c.fail(fmt.Errorf("koneksi DevTools terputus: %w", err))
			return
		}
		var env struct {
			ID     int64           `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &env) != nil || env.ID == 0 {
			continue
		}
		c.mu.Lock()
		ch := c.pending[env.ID]
		delete(c.pending, env.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- chromeCDPResp{Result: env.Result, Error: env.Error}
		}
	}
}

func (c *chromeCDP) fail(err error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	if err != nil {
		c.closeErr = err
	}
	pending := c.pending
	c.pending = make(map[int64]chan chromeCDPResp)
	c.mu.Unlock()
	for _, ch := range pending {
		close(ch)
	}
}

func (c *chromeCDP) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, fmt.Errorf("DevTools sudah tertutup")
	}
	c.nextID++
	id := c.nextID
	ch := make(chan chromeCDPResp, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	payload := map[string]any{"id": id, "method": method}
	if params != nil {
		payload["params"] = params
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", method, err)
	}
	writeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := c.conn.Write(writeCtx, websocket.MessageText, raw); err != nil {
		return nil, fmt.Errorf("kirim %s: %w", method, err)
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp, ok := <-ch:
		if !ok {
			c.mu.Lock()
			reason := c.closeErr
			c.mu.Unlock()
			if reason != nil {
				return nil, fmt.Errorf("DevTools terputus saat %s (%v)", method, reason)
			}
			return nil, fmt.Errorf("DevTools terputus saat %s", method)
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("CDP %s: %s", method, resp.Error.Message)
		}
		return resp.Result, nil
	}
}

func (c *chromeCDP) close() {
	c.fail(fmt.Errorf("ditutup pemanggil"))
	_ = c.conn.Close(websocket.StatusNormalClosure, "")
}

func chromeWaitDevTools(ctx context.Context, port int, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	listURL := fmt.Sprintf("http://127.0.0.1:%d/json/list", port)
	client := &http.Client{Timeout: 3 * time.Second}
	var lastErr error
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
		resp, err := client.Do(req)
		if err == nil {
			var targets []struct {
				Type                 string `json:"type"`
				WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
			}
			derr := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&targets)
			resp.Body.Close()
			if derr == nil {
				for _, t := range targets {
					if t.Type == "page" && t.WebSocketDebuggerURL != "" {
						return t.WebSocketDebuggerURL, nil
					}
				}
				lastErr = fmt.Errorf("belum ada tab halaman")
			} else {
				lastErr = derr
			}
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(400 * time.Millisecond):
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("DevTools tidak merespons")
	}
	return "", fmt.Errorf("chrome: %v", lastErr)
}

func chromeFindBinary() (string, error) {
	if p := strings.TrimSpace(os.Getenv("GEMINI_CHROME_PATH")); p != "" {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
		return "", fmt.Errorf("chrome: GEMINI_CHROME_PATH tidak ada (%s)", p)
	}
	names := []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"}
	if runtime.GOOS == "windows" {
		names = []string{"chrome"}
	}
	for _, name := range names {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	candidates := []string{
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
		"/usr/bin/google-chrome",
		"/usr/bin/chromium",
		"/usr/bin/chromium-browser",
		"/snap/bin/chromium",
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("chrome: binary tidak ditemukan (set GEMINI_CHROME_PATH)")
}

func chromeStartDisplay(ctx context.Context) (display string, headed bool, xvfb *exec.Cmd, err error) {
	if os.Getenv("GEMINI_HEADED") == "0" {
		return "", false, nil, nil
	}
	if d := strings.TrimSpace(os.Getenv("DISPLAY")); d != "" {
		return d, true, nil, nil
	}
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return "", true, nil, nil
	}
	xvfbPath := strings.TrimSpace(os.Getenv("GEMINI_XVFB_PATH"))
	if xvfbPath == "" {
		xvfbPath = "Xvfb"
	}
	if p, err := exec.LookPath(xvfbPath); err == nil {
		xvfbPath = p
	} else if st, serr := os.Stat(xvfbPath); serr != nil || st.IsDir() {
		return "", false, nil, nil
	}
	num, err := chromeFreeDisplay()
	if err != nil {
		return "", false, nil, nil
	}
	disp := ":" + strconv.Itoa(num)
	cmd := exec.Command(xvfbPath, disp, "-screen", "0", "1280x900x24", "-nolisten", "tcp")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return "", false, nil, nil
	}
	socket := "/tmp/.X11-unix/X" + strconv.Itoa(num)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socket); err == nil {
			return disp, true, cmd, nil
		}
		if ctx.Err() != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	chromeStopProcess(cmd)
	return "", false, nil, nil
}

func chromeFreeDisplay() (int, error) {
	for num := 99; num < 130; num++ {
		if _, err := os.Stat("/tmp/.X11-unix/X" + strconv.Itoa(num)); os.IsNotExist(err) {
			return num, nil
		}
	}
	return 0, fmt.Errorf("tidak ada nomor display bebas")
}

// chromeHeadedFlags mencegah Chrome menganggap jendelanya tersembunyi (Xvfb
// tanpa window manager) lalu men-throttle renderer.
func chromeHeadedFlags() []string {
	return []string{
		"--disable-background-timer-throttling",
		"--disable-renderer-backgrounding",
		"--disable-backgrounding-occluded-windows",
		"--disable-features=CalculateNativeWinOcclusion",
		"--disable-session-crashed-bubble",
		"--hide-crash-restore-bubble",
		"--disable-gpu",
	}
}

func chromeFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("chrome: tidak ada port bebas: %w", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func chromePrepareProfile(profile string) {
	if err := os.MkdirAll(profile, 0o755); err != nil {
		return
	}
	for _, name := range []string{"SingletonLock", "SingletonSocket", "SingletonCookie", "lockfile", "DevToolsActivePort"} {
		_ = os.Remove(filepath.Join(profile, name))
	}
}

func chromeStopProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if runtime.GOOS == "windows" {
		_ = exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
	}
	_ = cmd.Process.Kill()
	done := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
}

func chromeCgroupFree() (int64, bool) {
	readInt := func(path string) (int64, bool) {
		b, err := os.ReadFile(path)
		if err != nil {
			return 0, false
		}
		s := strings.TrimSpace(string(b))
		if s == "max" || s == "" {
			return 0, false
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n <= 0 {
			return 0, false
		}
		return n, true
	}
	if limit, ok := readInt("/sys/fs/cgroup/memory.max"); ok {
		if used, ok2 := readInt("/sys/fs/cgroup/memory.current"); ok2 {
			return limit - used, true
		}
	}
	if limit, ok := readInt("/sys/fs/cgroup/memory/memory.limit_in_bytes"); ok {
		if used, ok2 := readInt("/sys/fs/cgroup/memory/memory.usage_in_bytes"); ok2 {
			return limit - used, true
		}
	}
	return 0, false
}
