package reactionwa

import (
	"context"
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

	"wabot-go/internal/logfmt"
)

// browser.go — Chrome sekali pakai untuk memanen token Cloudflare Turnstile
// dari halaman /Free/.
//
// Kenapa harus browser: server memverifikasi captchaToken ke Cloudflare, jadi
// token harus dibuat oleh JS halaman (tidak ada jalur HTTP murni). Halaman
// /Free/ TIDAK butuh login — login Google cukup dilakukan sekali di browser
// owner, hasilnya refresh token yang disimpan bot.
//
// Chrome dinyalakan hanya saat dibutuhkan lalu dimatikan lagi: panel 1 core /
// 1,6 GB tidak sanggup menahan Chrome menganggur. Profil tetap di rwa-profile/
// supaya cookie clearance Cloudflare ikut terbawa (lebih jarang ditantang).
// Satu token Turnstile bisa dipakai beberapa kali dalam masa berlakunya,
// jadi token di-cache sebentar di memori.

const (
	// rwaProfileDir adalah profil Chrome (gitignored, sejajar arena-profile).
	rwaProfileDir = "rwa-profile"
	// captchaTokenTTL: token Turnstile berlaku ±5 menit. Cache 4 menit supaya
	// command beruntun tidak menyalakan Chrome lagi.
	captchaTokenTTL = 4 * time.Minute
	// tokenWaitTimeout: batas menunggu Turnstile selesai (Cloudflare kadang
	// memutar challenge dulu).
	tokenWaitTimeout = 120 * time.Second
	// minFreeMemoryBytes: jangan menyalakan Chrome kalau sisa memori cgroup
	// di bawah ini (panel lain juga memakai Chrome untuk .seedream).
	minFreeMemoryBytes = 350 << 20
)

// Browser membuat token Turnstile. Aman dipakai bersamaan (mutex: satu Chrome
// pada satu waktu).
type Browser struct {
	mu      sync.Mutex
	profile string

	token   string
	tokenAt time.Time
}

func newBrowser() *Browser {
	return &Browser{profile: rwaProfileDir}
}

// Token mengembalikan token Turnstile: dari cache kalau masih segar, selain
// itu menyalakan Chrome dan memanen token baru.
func (b *Browser) Token(ctx context.Context) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.token != "" && time.Since(b.tokenAt) < captchaTokenTTL {
		return b.token, nil
	}
	tok, err := b.mint(ctx)
	if err != nil {
		return "", err
	}
	b.token, b.tokenAt = tok, time.Now()
	return tok, nil
}

// Invalidate membuang token cache (dipakai setelah server menolak captcha).
func (b *Browser) Invalidate() {
	b.mu.Lock()
	b.token = ""
	b.tokenAt = time.Time{}
	b.mu.Unlock()
}

func (b *Browser) mint(ctx context.Context) (string, error) {
	if free, known := cgroupFreeMemory(); known && free < minFreeMemoryBytes {
		return "", fmt.Errorf("%w: sisa memori %d MB, tunggu proses lain selesai", ErrBrowser, free>>20)
	}

	chromePath, err := findChrome()
	if err != nil {
		return "", err
	}
	display, headed, xvfb, err := startDisplay(ctx)
	if err != nil {
		return "", err
	}
	if xvfb != nil {
		defer stopProcess(xvfb)
	}

	prepareProfile(b.profile)
	port, err := freePort()
	if err != nil {
		return "", err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("gagal baca workdir: %w", err)
	}
	profile := filepath.Join(cwd, b.profile)

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
		"--renderer-process-limit=1",
		"--window-size=1280,900",
		"--lang=id-ID",
		"--user-data-dir=" + profile,
		"--remote-debugging-port=" + strconv.Itoa(port),
	}
	if !headed {
		args = append([]string{"--headless=new"}, args...)
	} else {
		args = append(args, headedFlags()...)
	}
	args = append(args, SiteOrigin+"/Free/")

	cmd := exec.Command(chromePath, args...)
	cmd.Dir = cwd
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if display != "" {
		cmd.Env = append(os.Environ(), "DISPLAY="+display)
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("%w: gagal menjalankan Chrome: %v", ErrBrowser, err)
	}
	defer stopProcess(cmd)

	wsURL, err := waitDevTools(ctx, port, 40*time.Second)
	if err != nil {
		return "", err
	}
	c, err := dialCDP(ctx, wsURL)
	if err != nil {
		return "", err
	}
	defer c.close()

	if _, err := c.call(ctx, "Page.enable", nil); err != nil {
		return "", err
	}
	if _, err := c.call(ctx, "Runtime.enable", nil); err != nil {
		return "", err
	}
	return waitTurnstile(ctx, c)
}

// waitTurnstile mem-polling token dari halaman. Widget Turnstile di situs
// otomatis berjalan; execute() dipanggil berkala kalau widgetnya menunggu
// interaksi. Halaman dimuat ulang sekali kalau widget belum muncul.
func waitTurnstile(ctx context.Context, c *cdpClient) (string, error) {
	deadline := time.Now().Add(tokenWaitTimeout)
	started := time.Now()
	nextExecute := started.Add(8 * time.Second)
	reloaded := false
	lastTitle := ""

	for time.Now().Before(deadline) {
		if tok := evalString(ctx, c, turnstileTokenJS); tok != "" {
			return tok, nil
		}
		if title := evalString(ctx, c, "document.title"); title != "" {
			lastTitle = title
		}
		// Muat ulang sekali kalau setelah 20 detik belum ada tanda-tanda
		// widget (Cloudflare kadang menyajikan halaman challenge lebih dulu).
		if !reloaded && time.Since(started) > 20*time.Second {
			has := evalString(ctx, c, "String(typeof window.turnstile)")
			if has != "function" && has != "object" {
				_, _ = c.call(ctx, "Page.navigate", map[string]any{"url": SiteOrigin + "/Free/"})
			}
			reloaded = true
		}
		if time.Now().After(nextExecute) {
			_, _ = c.call(ctx, "Runtime.evaluate", map[string]any{
				"expression":    executeTurnstileJS,
				"returnByValue": true,
			})
			nextExecute = time.Now().Add(10 * time.Second)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(1500 * time.Millisecond):
		}
	}

	url := evalString(ctx, c, "location.href")
	body := evalString(ctx, c, "document.body ? document.body.innerText.slice(0,200) : ''")
	snip := strings.Join(strings.Fields(body), " ")
	if len(snip) > 160 {
		snip = snip[:160] + "…"
	}
	return "", fmt.Errorf("%w: Turnstile tidak selesai dalam %s (judul %q, url %q, isi %q)",
		ErrCaptcha, tokenWaitTimeout, lastTitle, url, snip)
}

func evalString(ctx context.Context, c *cdpClient, expression string) string {
	raw, err := c.call(ctx, "Runtime.evaluate", map[string]any{
		"expression":    expression,
		"returnByValue": true,
	})
	if err != nil {
		return ""
	}
	var out struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return ""
	}
	return out.Result.Value
}

const turnstileTokenJS = `(() => {
  try {
    if (!window.turnstile || typeof window.turnstile.getResponse !== "function") return "";
    const id = window.turnstileWidgetId;
    let r = "";
    try {
      r = (id === undefined || id === null) ? window.turnstile.getResponse() : window.turnstile.getResponse(id);
    } catch (e) {
      r = "";
    }
    return typeof r === "string" ? r : "";
  } catch (e) {
    return "";
  }
})()`

const executeTurnstileJS = `(() => {
  try {
    if (!window.turnstile || typeof window.turnstile.execute !== "function") return "no-turnstile";
    const id = window.turnstileWidgetId;
    if (id === undefined || id === null) {
      window.turnstile.execute();
    } else {
      window.turnstile.execute(id);
    }
    return "ok";
  } catch (e) {
    return "err:" + e.message;
  }
})()`

// waitDevTools menunggu port DevTools hidup lalu mengambil websocket target
// halaman (bukan target lain seperti service worker).
func waitDevTools(ctx context.Context, port int, timeout time.Duration) (string, error) {
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
				URL                  string `json:"url"`
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
	return "", fmt.Errorf("%w: %v", ErrBrowser, lastErr)
}

func findChrome() (string, error) {
	if p := strings.TrimSpace(os.Getenv("RWA_CHROME_PATH")); p != "" {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
		return "", fmt.Errorf("%w: RWA_CHROME_PATH tidak ada (%s)", ErrBrowser, p)
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
	return "", fmt.Errorf("%w: Chrome tidak ditemukan (set RWA_CHROME_PATH)", ErrBrowser)
}

// startDisplay menyiapkan layar untuk Chrome non-headless. Headless sering
// ditolak Turnstile, jadi Chrome dijalankan headed: di panel memakai Xvfb,
// di Windows/macOS memakai desktop yang ada, dan hanya jatuh ke headless bila
// Xvfb tidak tersedia (atau RWA_HEADED=0).
func startDisplay(ctx context.Context) (display string, headed bool, xvfb *exec.Cmd, err error) {
	if os.Getenv("RWA_HEADED") == "0" {
		return "", false, nil, nil
	}
	if d := strings.TrimSpace(os.Getenv("DISPLAY")); d != "" {
		return d, true, nil, nil
	}
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return "", true, nil, nil
	}
	xvfbPath := strings.TrimSpace(os.Getenv("RWA_XVFB_PATH"))
	if xvfbPath == "" {
		xvfbPath = "Xvfb"
	}
	if p, err := exec.LookPath(xvfbPath); err == nil {
		xvfbPath = p
	} else if st, serr := os.Stat(xvfbPath); serr != nil || st.IsDir() {
		logfmt.Warn("reactionwa: Xvfb tidak ada, memakai Chrome headless (Turnstile bisa menolak)\n")
		return "", false, nil, nil
	}
	num, err := freeDisplayNumber()
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
	stopProcess(cmd)
	return "", false, nil, nil
}

func freeDisplayNumber() (int, error) {
	for num := 99; num < 130; num++ {
		if _, err := os.Stat("/tmp/.X11-unix/X" + strconv.Itoa(num)); os.IsNotExist(err) {
			return num, nil
		}
	}
	return 0, fmt.Errorf("tidak ada nomor display bebas")
}

// headedFlags mencegah Chrome menganggap jendelanya tersembunyi (Xvfb tanpa
// window manager) lalu men-throttle renderer, dan mematikan GPU software yang
// boros CPU di panel 1 core.
func headedFlags() []string {
	args := []string{
		"--disable-background-timer-throttling",
		"--disable-renderer-backgrounding",
		"--disable-backgrounding-occluded-windows",
		"--disable-features=CalculateNativeWinOcclusion",
		"--disable-session-crashed-bubble",
		"--hide-crash-restore-bubble",
	}
	if os.Getenv("RWA_HEADED_GPU") != "1" {
		args = append(args, "--disable-gpu")
	}
	return args
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("%w: tidak ada port bebas: %v", ErrBrowser, err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// prepareProfile membuang kunci profil sisa proses yang dimatikan paksa.
func prepareProfile(profile string) {
	if err := os.MkdirAll(profile, 0755); err != nil {
		return
	}
	for _, name := range []string{"SingletonLock", "SingletonSocket", "SingletonCookie", "lockfile", "DevToolsActivePort"} {
		_ = os.Remove(filepath.Join(profile, name))
	}
}

func stopProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if runtime.GOOS == "windows" {
		// Chrome meninggalkan proses anak (crashpad/GPU) yang masih memegang
		// berkas profil; bunuh satu pohon proses.
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

// cgroupFreeMemory membaca sisa memori cgroup (v2 lalu v1). known=false kalau
// panel tidak membatasi memori (mis. laptop).
func cgroupFreeMemory() (int64, bool) {
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
