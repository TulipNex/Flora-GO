package arena

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"wabot-go/internal/logfmt"
)

// chrome.go — lifecycle Chrome milik panel (bukan unduhan sendiri).
//
// Panel Flora sudah menyediakan Google Chrome 155 di /usr/bin/google-chrome,
// lengkap dengan seluruh library yang dibutuhkan, jadi tidak ada unduhan
// 170MB dan tidak ada risiko mismatch library. Yang dijaga di sini justru
// hal-hal khas panel:
//
//   - Memori container hanya ~1,6GB (cgroup), BUKAN RAM host yang terlihat di
//     `free`. Karena itu Chrome hanya dinyalakan saat dipakai, dimatikan lagi
//     setelah idle, dan peluncuran ditolak kalau sisa memori cgroup tipis —
//     OOM killer panel akan membunuh proses mana pun, termasuk botnya sendiri.
//   - CPU container 1 core, jadi generate diserialkan (lihat genMu di
//     arena.go) dan jumlah proses renderer dibatasi.
//   - `--headless=new` WAJIB (Chrome >= 112): mesin render yang sama dengan
//     Chrome biasa, sehingga skor reCAPTCHA jauh lebih baik daripada headless
//     lama. Versi Chrome diperiksa saat start supaya kegagalan jelas.

const (
	defaultChromePath   = "/usr/bin/google-chrome"
	chromeProfileDir    = "arena-profile"
	minChromeMajor      = 112
	minFreeMemoryBytes  = 500 << 20 // 500MB ruang napas untuk Chrome + renderer
	chromeReadyTimeout  = 30 * time.Second
	chromeShutdownGrace = 3 * time.Second
	// defaultWarmup: jeda setelah halaman arena.ai terbuka sebelum token
	// reCAPTCHA diminta (lihat attach).
	defaultWarmup = 6 * time.Second
)

// browser adalah satu proses Chrome beserta koneksi ke satu tab-nya.
type browser struct {
	cmd       *exec.Cmd
	port      int
	page      *cdpClient
	profile   string
	ephemeral bool // profil sekali-pakai: dibuang saat stop
	headed    bool // berjalan di layar virtual (bukan --headless)
	cookieFP  string
	lastUsed  time.Time
	xvfb      *exec.Cmd
	stopped   atomic.Bool
	busy      atomic.Int32 // jumlah permintaan yang sedang memakai browser

	// waitCh ditutup setelah proses Chrome keluar; exitErr/exitAt diisi
	// sebelum itu oleh goroutine pemantau di startChrome.
	waitCh  chan struct{}
	exitErr error
	exitAt  time.Time
}

var (
	browserMu sync.Mutex // melindungi current
	current   *browser
	genMu     sync.Mutex // satu generate sekaligus (1 core, 1,6GB)
)

// GenerateImage memakai browser yang sudah hidup, atau menyalakan yang baru.
// Cookie dibandingkan lewat fingerprint: kalau owner memperbarui cookie
// lewat .seedream cookie, Chrome dinyalakan ulang supaya cookie baru terpakai tanpa
// perlu restart bot.
func getBrowser(ctx context.Context, cookieHeader string) (*browser, error) {
	fp := cookieFingerprint(cookieHeader)
	browserMu.Lock()
	defer browserMu.Unlock()

	if current != nil {
		if current.alive() && current.cookieFP == fp {
			current.lastUsed = time.Now()
			return current, nil
		}
		current.stop()
		current = nil
	}

	b, err := launchBrowser(ctx, cookieHeader, fp)
	if err != nil {
		return nil, err
	}
	current = b
	go stopWhenIdle(b)
	return b, nil
}

// stopWhenIdle mematikan Chrome setelah ARENA_CHROME_IDLE_SECONDS tanpa
// pemakaian. Default 150 detik: cukup untuk beberapa generate beruntun,
// tapi tidak menahan 300-400MB memori sepanjang hari.
//
// Penting: ticker ini tidak boleh mematikan Chrome yang sedang melayani
// permintaan. Tanpa penjaga `busy`, .seedream (attach + unggah 4MB + generate)
// yang berjalan lebih dari 150 detik akan dibunuh di tengah Runtime.evaluate
// dan owner melihat "DevTools terputus" — bukan kesalahan arena.ai.
func stopWhenIdle(b *browser) {
	idle := envDuration("ARENA_CHROME_IDLE_SECONDS", 150*time.Second)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		browserMu.Lock()
		if current != b {
			browserMu.Unlock()
			return
		}
		if b.stopped.Load() {
			browserMu.Unlock()
			return
		}
		if b.busy.Load() > 0 {
			browserMu.Unlock()
			continue
		}
		if !idleExpired(b, idle, time.Now()) {
			browserMu.Unlock()
			continue
		}
		waited := time.Since(b.lastUsed)
		current = nil
		browserMu.Unlock()
		logfmt.Info("arena: Chrome dimatikan (idle %s)\n", waited.Round(time.Second))
		b.stop()
		return
	}
}

// idleExpired memutuskan apakah Chrome boleh dimatikan karena menganggur.
// Dipisah supaya bisa diuji tanpa menunggu ticker 15 detik: syaratnya tidak
// ada permintaan berjalan DAN sudah lewat batas idle.
func idleExpired(b *browser, idle time.Duration, now time.Time) bool {
	if b == nil || b.busy.Load() > 0 {
		return false
	}
	return now.Sub(b.lastUsed) >= idle
}

// beginRequest menandai browser sedang dipakai. Pemanggil wajib memanggil
// endRequest (biasanya lewat defer).
func (b *browser) beginRequest() {
	b.busy.Add(1)
	browserMu.Lock()
	b.lastUsed = time.Now()
	browserMu.Unlock()
}

func (b *browser) endRequest() {
	b.busy.Add(-1)
	browserMu.Lock()
	b.lastUsed = time.Now()
	browserMu.Unlock()
}

// dead melaporkan Chrome/tab sudah tidak bisa dipakai. Dipakai memilih pesan
// error dan memutuskan apakah browser perlu dibuang supaya perintah berikutnya
// menyalakan Chrome baru.
func (b *browser) dead() bool {
	if b == nil || b.stopped.Load() {
		return true
	}
	if b.page == nil || b.page.dead() {
		return true
	}
	select {
	case <-b.waitCh:
		return true
	default:
		return false
	}
}

// failureHint menjelaskan keadaan Chrome saat sebuah perintah gagal: apakah
// prosesnya mati (dan dengan kode berapa), atau masih hidup. Sisa memori
// cgroup ikut dicatat karena OOM killer panel adalah tersangka utama kalau
// Chrome mati sendiri.
func (b *browser) failureHint() string {
	if b == nil {
		return ""
	}
	reason := ""
	select {
	case <-b.waitCh:
		code := "selesai"
		if b.exitErr != nil {
			code = b.exitErr.Error()
		}
		reason = fmt.Sprintf("Chrome keluar: %s", code)
	default:
		if b.stopped.Load() {
			reason = "Chrome dihentikan saat permintaan berjalan"
		} else {
			reason = "proses Chrome masih hidup"
		}
	}
	if free, known := cgroupFreeMemory(); known {
		reason += fmt.Sprintf(", sisa memori %d MB", free>>20)
	}
	if r := b.page.reason(); r != "" {
		reason += ", " + r
	}
	return reason
}

// dropBrowser membuang browser yang sudah rusak supaya perintah berikutnya
// menyalakan Chrome bersih (bukan mencoba tab mati lagi).
func dropBrowser(b *browser) {
	if b == nil {
		return
	}
	browserMu.Lock()
	if current == b {
		current = nil
	}
	browserMu.Unlock()
	b.stop()
}

// shutdownBrowser dipanggil saat bot berhenti supaya Chrome tidak menjadi
// proses yatim di panel.
func shutdownBrowser() {
	browserMu.Lock()
	b := current
	current = nil
	browserMu.Unlock()
	if b != nil {
		b.stop()
	}
}

// alive mengecek proses masih berjalan DAN tab masih menjawab. Cek murah:
// satu evaluate kecil dengan timeout pendek.
func (b *browser) alive() bool {
	if b.stopped.Load() || b.page == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := b.page.call(ctx, "Runtime.evaluate", map[string]any{"expression": "1"})
	return err == nil
}

// saveCookies menyalin cookie arena.ai yang sedang aktif di Chrome kembali ke
// data/arena-cookie.txt.
//
// Kenapa perlu: cookie sesi arena.ai adalah Supabase (access token ~1 jam +
// refresh token yang dirotasi tiap dipakai). Halaman arena.ai memperbarui
// cookie itu sendiri di profil Chrome, tapi bot menyuntikkan ulang cookie dari
// file setiap kali Chrome menyala — tanpa simpan-balik, hasil rotasi hilang
// dan sesi jadi basi tiap jam. Best-effort: kegagalan di sini tidak boleh
// menggagalkan generate yang sudah berhasil.
func (b *browser) saveCookies() {
	if b == nil || b.page == nil || b.stopped.Load() {
		return
	}
	// Sengaja tidak memakai ctx pemanggil: simpan-balik tetap harus jalan
	// walau deadline generate hampir habis.
	cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	raw, err := b.page.call(cctx, "Storage.getCookies", nil)
	if err != nil {
		return
	}
	var res struct {
		Cookies []struct {
			Name    string  `json:"name"`
			Value   string  `json:"value"`
			Domain  string  `json:"domain"`
			Expires float64 `json:"expires"`
		} `json:"cookies"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return
	}

	// Peta nama->nilai: cookie dengan path berbeda bisa berbagi nama, dan
	// yang dibutuhkan hanya satu nilai per nama untuk header Cookie.
	values := make(map[string]string, len(res.Cookies))
	now := float64(time.Now().Unix())
	for _, c := range res.Cookies {
		if c.Name == "" {
			continue
		}
		if c.Domain != "arena.ai" && !strings.HasSuffix(c.Domain, ".arena.ai") {
			continue
		}
		if c.Expires > 0 && c.Expires < now {
			continue
		}
		values[c.Name] = c.Value
	}
	if len(values) == 0 {
		return
	}

	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+"="+values[name])
	}
	header := normalizeCookie(strings.Join(parts, "; "))
	// Guard: jangan pernah menimpa sesi yang baik dengan set cookie anonim.
	if !strings.Contains(header, "arena-auth-prod-v1") {
		return
	}

	fp := cookieFingerprint(header)
	if cur, err := ReadCookie(); err == nil && cur == header {
		// Tidak berubah: cukup samakan fingerprint supaya Chrome yang sudah
		// hidup tetap dipakai (tidak restart tiap generate).
		b.cookieFP = fp
		return
	}
	if err := WriteCookie(header); err != nil {
		logfmt.Warn("arena: simpan cookie gagal: %v\n", err)
		return
	}
	b.cookieFP = fp
}

// stop menutup tab lalu mematikan proses Chrome.
func (b *browser) stop() {
	if b == nil {
		return
	}
	// CompareAndSwap membuat stop idempoten tanpa data race: idle-stopper dan
	// pemanggil yang menyalakan browser baru bisa memanggilnya bersamaan.
	if !b.stopped.CompareAndSwap(false, true) {
		return
	}
	if b.page != nil {
		// Browser.close mematikan seluruh Chrome dengan rapi; kalau gagal,
		// Kill di bawah tetap jadi jaring pengaman.
		ctx, cancel := context.WithTimeout(context.Background(), chromeShutdownGrace)
		_, _ = b.page.call(ctx, "Browser.close", nil)
		cancel()
		b.page.Close()
	}
	if b.cmd != nil && b.cmd.Process != nil {
		select {
		case <-b.waitCh:
		case <-time.After(chromeShutdownGrace):
			_ = b.cmd.Process.Kill()
			<-b.waitCh
		}
	}
	// Buang profil sekali-pakai supaya tidak menumpuk di panel. Profil tetap
	// sengaja dipertahankan (riwayat reCAPTCHA membantu skor).
	if b.ephemeral && b.profile != "" {
		_ = os.RemoveAll(b.profile)
	}
	stopDisplay(b.xvfb)
}

// launchBrowser menyalakan Chrome, menyuntikkan cookie, lalu membuka
// /image/direct supaya reCAPTCHA Enterprise sudah termuat saat skrip jalan.
//
// Dua lapis profil:
//  1. Profil tetap (arena-profile/profile) — dipakai normal supaya
//     reCAPTCHA menyimpan riwayat/kunjungan antar generate; skornya lebih
//     baik daripada profil yang selalu baru.
//  2. Profil sekali-pakai (arena-profile/run-<ts>-<pid>) — dipakai kalau
//     peluncuran pertama gagal menyiapkan DevTools, mis. karena profil tetap
//     masih dikunci Chrome sisa crash yang tak bisa dibunuh.
func launchBrowser(ctx context.Context, cookieHeader, cookieFP string) (*browser, error) {
	chromePath := envString("ARENA_CHROME_PATH", defaultChromePath)
	if _, err := os.Stat(chromePath); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrChromeMissing, chromePath)
	}
	major, err := chromeMajorVersion(ctx, chromePath)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrChromeMissing, err)
	}
	if major != 0 && major < minChromeMajor {
		return nil, fmt.Errorf("%w: Chrome %d (butuh >= %d untuk --headless=new)", ErrChromeOld, major, minChromeMajor)
	}
	if free, known := cgroupFreeMemory(); known && free < minFreeMemoryBytes {
		return nil, fmt.Errorf("%w: sisa memori %d MB", ErrLowMemory, free>>20)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("gagal baca workdir: %w", err)
	}
	base := filepath.Join(cwd, chromeProfileDir)
	if err := os.MkdirAll(base, 0755); err != nil {
		return nil, fmt.Errorf("gagal siapkan profil Chrome: %w", err)
	}
	cleanupStaleProfiles(base)

	// Urutan percobaan:
	//  1. mode tampilan normal (Xvfb/DISPLAY) + profil tetap,
	//  2. mode tampilan normal + profil sekali-pakai (profil tetap terkunci),
	//  3. headless + profil sekali-pakai (X/GTK di panel tidak lengkap dsb).
	// Semua langkah hanya diulang kalau kegagalannya soal Chrome/DevTools,
	// bukan karena cookie atau jaringan.
	persistent := filepath.Join(base, "profile")
	ephemeral := filepath.Join(base, fmt.Sprintf("run-%d-%d", time.Now().Unix(), os.Getpid()))

	b, err := startChrome(ctx, chromePath, base, persistent, false, true, cookieHeader, cookieFP)
	if err == nil {
		return b, nil
	}
	if !errors.Is(err, ErrBrowser) {
		return nil, err
	}
	logfmt.Warn("arena: Chrome mode tampilan gagal (%v), mencoba profil baru\n", err)
	b, err = startChrome(ctx, chromePath, base, ephemeral, true, true, cookieHeader, cookieFP)
	if err == nil {
		return b, nil
	}
	if !errors.Is(err, ErrBrowser) {
		return nil, err
	}
	logfmt.Warn("arena: Chrome mode tampilan tetap gagal (%v), jatuh ke headless\n", err)
	return startChrome(ctx, chromePath, base, ephemeral, true, false, cookieHeader, cookieFP)
}

// startChrome menjalankan satu proses Chrome dengan profil tertentu lalu
// menyiapkan tab arena.ai. ephemeral=true berarti foldernya dibuang saat stop;
// useDisplay=false memaksa headless (dipakai sebagai upaya terakhir).
func startChrome(ctx context.Context, chromePath, base, profile string, ephemeral, useDisplay bool, cookieHeader, cookieFP string) (*browser, error) {
	prepareProfile(profile)
	port, err := freeLocalPort()
	if err != nil {
		return nil, err
	}
	cwd := filepath.Dir(base)

	args := []string{
		"--no-sandbox",            // wajib di container tanpa user-namespace
		"--disable-dev-shm-usage", // /dev/shm container kecil
		"--no-first-run",
		"--no-default-browser-check",
		"--no-zygote", // satu proses lebih sedikit, hemat memori
		"--disable-extensions",
		"--disable-background-networking",
		"--disable-sync",
		"--disable-default-apps",
		"--renderer-process-limit=1",
		"--window-size=1280,900",
		"--lang=id-ID",
		"--user-data-dir=" + profile,
		"--remote-debugging-port=" + strconv.Itoa(port),
	}
	display := ""
	var xvfbCmd *exec.Cmd
	if useDisplay {
		// Mode headless ditolak reCAPTCHA ("recaptcha validation failed") walau
		// UA, Client Hints, ukuran layar, dan WebGL sudah disamarkan; Chrome
		// headed di mesin yang sama langsung diterima. Karena itu layar virtual
		// (Xvfb) diutamakan bila tersedia — lihat startDisplay.
		display, xvfbCmd, err = startDisplay(ctx)
		if err != nil {
			return nil, err
		}
	}
	if display == "" {
		args = append([]string{"--headless=new"}, args...)
	} else {
		// Layar virtual memungkinkan Chrome berjalan NON-headless di panel.
		args = removeHeadlessFlags(args)
		args = append(args, headedFlags()...)
	}
	cmd := exec.Command(chromePath, args...)
	cmd.Dir = cwd
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if display != "" {
		cmd.Env = append(os.Environ(), "DISPLAY="+display)
	}
	if err := cmd.Start(); err != nil {
		stopDisplay(xvfbCmd)
		return nil, fmt.Errorf("%w: gagal menjalankan Chrome: %v", ErrBrowser, err)
	}
	b := &browser{
		cmd:       cmd,
		port:      port,
		headed:    display != "",
		cookieFP:  cookieFP,
		profile:   profile,
		ephemeral: ephemeral,
		xvfb:      xvfbCmd,
		lastUsed:  time.Now(),
		waitCh:    make(chan struct{}),
	}

	// Satu-satunya pemanggil cmd.Wait: mencatat status keluar Chrome untuk
	// diagnostik dan mencegah proses zombie kalau Chrome mati sendiri (OOM
	// killer panel, crash). stop() hanya menunggu kanal ini.
	go func() {
		b.exitErr = cmd.Wait()
		b.exitAt = time.Now()
		close(b.waitCh)
	}()

	if err := b.attach(ctx, cookieHeader); err != nil {
		b.stop()
		return nil, err
	}
	return b, nil
}

// startDisplay menyiapkan layar untuk Chrome non-headless.
//
// Kenapa perlu: reCAPTCHA menolak token dari Chrome headless (403
// "recaptcha validation failed") sekalipun UA/Client Hints/layar/WebGL sudah
// disamarkan, sementara Chrome headed di mesin yang sama langsung diterima.
// Panel tidak punya layar fisik, jadi dipakai Xvfb (X virtual framebuffer)
// kalau tersedia: Chrome berjalan sebagai browser biasa, bukan headless.
//
// Urutan prioritas:
//  1. ARENA_HEADED=0 → headless (paksa).
//  2. DISPLAY sudah ada  → pakai itu.
//  3. Binary Xvfb ada    → nyalakan layar virtual sendiri.
//  4. Selain itu         → headless (fitur tetap jalan, walau reCAPTCHA
//     kadang menolak; pesannya jelas untuk owner).
func startDisplay(ctx context.Context) (display string, xvfb *exec.Cmd, err error) {
	switch os.Getenv("ARENA_HEADED") {
	case "0":
		return "", nil, nil
	case "1":
		if d := os.Getenv("DISPLAY"); d != "" {
			return d, nil, nil
		}
	}
	if d := os.Getenv("DISPLAY"); d != "" {
		return d, nil, nil
	}
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return "", nil, nil
	}

	xvfbPath := envString("ARENA_XVFB_PATH", "Xvfb")
	if p, lerr := exec.LookPath(xvfbPath); lerr == nil {
		xvfbPath = p
	} else {
		if st, serr := os.Stat(xvfbPath); serr != nil || st.IsDir() {
			return "", nil, nil
		}
	}
	num, derr := freeDisplayNumber()
	if derr != nil {
		return "", nil, nil
	}
	disp := ":" + strconv.Itoa(num)
	cmd := exec.Command(xvfbPath, disp, "-screen", "0", "1280x900x24", "-nolisten", "tcp")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if serr := cmd.Start(); serr != nil {
		return "", nil, nil
	}
	// Tunggu socket X siap (biasanya <1 detik).
	socket := "/tmp/.X11-unix/X" + strconv.Itoa(num)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if _, serr := os.Stat(socket); serr == nil {
			return disp, cmd, nil
		}
		if ctx.Err() != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	stopDisplay(cmd)
	return "", nil, nil
}

// freeDisplayNumber mencari nomor display X yang belum dipakai.
func freeDisplayNumber() (int, error) {
	for num := 99; num < 130; num++ {
		if _, err := os.Stat("/tmp/.X11-unix/X" + strconv.Itoa(num)); os.IsNotExist(err) {
			return num, nil
		}
	}
	return 0, fmt.Errorf("tidak ada nomor display bebas")
}

// stopDisplay mematikan Xvfb milik kita (bukan DISPLAY milik pengguna).
func stopDisplay(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
}

// removeHeadlessFlags membuang flag khusus headless saat Chrome dijalankan di
// layar virtual.
func removeHeadlessFlags(args []string) []string {
	out := args[:0]
	for _, a := range args {
		if a == "--headless=new" || a == "--headless" || a == "--disable-gpu" {
			continue
		}
		out = append(out, a)
	}
	return out
}

// headedFlags menyetel Chrome di layar virtual (Xvfb) supaya halaman tetap
// diproses seperti jendela yang terlihat.
//
// Kenapa perlu: Xvfb tidak punya window manager, jadi Chrome menganggap
// jendelanya ter-occlude/tersembunyi lalu men-throttle timer renderer dan
// compositor. Di panel 1 core efeknya halaman arena.ai berhenti di
// readyState=loading selama-lamanya (gejala di log: "mode tampilan gagal...
// readyState=loading"), padahal di headless halaman yang sama termuat.
//
// --disable-gpu: tanpa itu Chrome membuat proses GPU yang menjalankan
// SwiftShader/llvmpipe untuk layar virtual dan menghabiskan jatah CPU satu
// core. Kalau ternyata WebGL software justru merugikan skor reCAPTCHA, set
// ARENA_HEADED_GPU=1 untuk memakai GPU path lagi.
func headedFlags() []string {
	args := []string{
		"--disable-background-timer-throttling",
		"--disable-renderer-backgrounding",
		"--disable-backgrounding-occluded-windows",
		"--disable-features=CalculateNativeWinOcclusion",
		"--disable-session-crashed-bubble",
		"--hide-crash-restore-bubble",
	}
	if os.Getenv("ARENA_HEADED_GPU") != "1" {
		args = append(args, "--disable-gpu")
	}
	return args
}

// prepareProfile membersihkan kunci profil sisa proses sebelumnya. Chrome
// menolak (atau meneruskan ke instance lain) kalau folder profil masih
// dikunci: di Linux lewat SingletonLock/SingletonSocket/SingletonCookie, di
// Windows lewat berkas "lockfile". Kunci basi = peluncuran baru langsung
// keluar tanpa membuka port DevTools ("DevTools tidak merespons").
func prepareProfile(profile string) {
	if err := os.MkdirAll(profile, 0755); err != nil {
		return
	}
	for _, name := range []string{"SingletonLock", "SingletonSocket", "SingletonCookie", "lockfile"} {
		_ = os.Remove(filepath.Join(profile, name))
	}
}

// cleanupStaleProfiles membuang sisa folder profil sekali-pakai yang lebih tua
// dari satu jam (normalnya dihapus di browser.stop; yang tersisa berarti
// proses sebelumnya mati mendadak). Best-effort: gagal hapus tidak fatal.
func cleanupStaleProfiles(base string) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-time.Hour)
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "run-") {
			continue
		}
		// Nama folder memuat unix timestamp: run-<unix>-<pid>.
		parts := strings.Split(strings.TrimPrefix(e.Name(), "run-"), "-")
		if len(parts) < 2 {
			continue
		}
		ts, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil || time.Unix(ts, 0).After(cutoff) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(base, e.Name()))
	}
}

// applyRealUserAgent menimpa UA headless dengan UA Chrome desktop yang wajar.
// Deteksi "HeadlessChrome" adalah sinyal bot paling murah bagi reCAPTCHA;
// UA-CH (navigator.userAgentData) di Chrome headless baru sudah melaporkan
// "Google Chrome", jadi tanpa override ini UA dan Client Hints saling
// bertentangan — kombinasi yang justru menaikkan risiko.
//
// Gagal di sini tidak fatal: lanjut dengan UA bawaan daripada menggagalkan
// seluruh permintaan.
func (b *browser) applyRealUserAgent(ctx context.Context) {
	if b.page == nil || envString("ARENA_USER_AGENT", "") != "" {
		if b.page != nil {
			_, _ = b.page.call(ctx, "Emulation.setUserAgentOverride", map[string]any{
				"userAgent": envString("ARENA_USER_AGENT", ""),
			})
		}
		return
	}
	raw, err := b.page.call(ctx, "Browser.getVersion", nil)
	if err != nil {
		return
	}
	var ver struct {
		Product string `json:"product"` // contoh: "Chrome/155.0.8059.84"
	}
	if err := json.Unmarshal(raw, &ver); err != nil {
		return
	}
	major := strings.SplitN(strings.TrimPrefix(ver.Product, "Chrome/"), ".", 2)[0]
	if major == "" || major == ver.Product {
		return
	}
	params := map[string]any{
		"userAgent":         desktopUserAgent(major),
		"acceptLanguage":    "id-ID,id;q=0.9,en-US;q=0.8,en;q=0.7",
		"userAgentMetadata": userAgentMetadata(major),
	}
	if _, err := b.page.call(ctx, "Emulation.setUserAgentOverride", params); err != nil {
		// Endpoint lama sebagian Chrome hanya menerima Network.*; coba itu.
		_, _ = b.page.call(ctx, "Network.setUserAgentOverride", params)
	}
}

// applyViewport menyelaraskan ukuran layar dan jendela. Chrome headless
// default melaporkan screen 800x600 sementara jendelanya 1280x900; di desktop
// nyata jendela tidak mungkin lebih besar dari layar, jadi pola itu dipakai
// sebagai penanda browser otomatis.
func (b *browser) applyViewport(ctx context.Context) {
	if b.page == nil {
		return
	}
	_, _ = b.page.call(ctx, "Emulation.setDeviceMetricsOverride", map[string]any{
		"width":             1280,
		"height":            800,
		"deviceScaleFactor": 1,
		"mobile":            false,
		"screenWidth":       1920,
		"screenHeight":      1080,
		"screenOrientation": map[string]any{"angle": 0, "type": "landscapePrimary"},
	})
}

// userAgentMetadata melengkapi override UA dengan Client Hints. Tanpa ini
// navigator.userAgentData jadi kosong (brands=[], platform="") — kombinasi
// yang sama tidak wajarnya dengan UA palsu, jadi justru menaikkan risiko.
func userAgentMetadata(major string) map[string]any {
	full := major + ".0.0.0"
	platform, platformVersion := platformInfo()
	brands := []map[string]string{
		{"brand": "Chromium", "version": major},
		{"brand": "Google Chrome", "version": major},
		{"brand": "Not(A:Brand", "version": "24"},
	}
	fullVersions := []map[string]string{
		{"brand": "Chromium", "version": full},
		{"brand": "Google Chrome", "version": full},
		{"brand": "Not(A:Brand", "version": "24.0.0.0"},
	}
	return map[string]any{
		"brands":          brands,
		"fullVersionList": fullVersions,
		"fullVersion":     full,
		"platform":        platform,
		"platformVersion": platformVersion,
		"architecture":    "x86",
		"bitness":         "64",
		"model":           "",
		"mobile":          false,
		"wow64":           false,
	}
}

// platformInfo mengembalikan nama platform + versinya seperti yang dilaporkan
// Chrome asli di OS ini (dipakai untuk Client Hints).
func platformInfo() (name, version string) {
	switch runtime.GOOS {
	case "windows":
		return "Windows", "10.0.0"
	case "darwin":
		return "macOS", "10.15.7"
	default:
		// /proc/sys/kernel/osrelease berisi mis. "5.15.0-119-generic";
		// Chrome melaporkan angka depannya saja.
		if raw, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
			v := strings.TrimSpace(string(raw))
			if i := strings.IndexAny(v, "-+"); i > 0 {
				v = v[:i]
			}
			if v != "" {
				return "Linux", v
			}
		}
		return "Linux", "6.1.0"
	}
}

// desktopUserAgent merangkai UA Chrome desktop sesuai OS tempat bot berjalan.
// Platform di UA harus cocok dengan navigator.userAgentData.platform yang
// dikirim Chrome (Linux di panel, Windows saat develop di laptop).
func desktopUserAgent(major string) string {
	full := major + ".0.0.0"
	switch runtime.GOOS {
	case "windows":
		return "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" + full + " Safari/537.36"
	case "darwin":
		return "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" + full + " Safari/537.36"
	default:
		return "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" + full + " Safari/537.36"
	}
}

// attach menunggu endpoint DevTools siap, membuat tab, menyuntikkan cookie,
// lalu membuka halaman direct-image arena.ai.
func (b *browser) attach(ctx context.Context, cookieHeader string) error {
	wsURL, err := waitDevToolsTarget(ctx, b.port, chromeReadyTimeout)
	if err != nil {
		return err
	}
	page, err := dialCDP(ctx, wsURL)
	if err != nil {
		return err
	}
	b.page = page

	// Samarkan UA "HeadlessChrome": reCAPTCHA memakai UA sebagai sinyal risiko,
	// dan UA headless (plus ketidakcocokan dengan Client Hints yang justru
	// melaporkan "Google Chrome") adalah salah satu pemicu skor rendah.
	b.applyRealUserAgent(ctx)
	// Layar bawaan headless 800x600 padahal jendela 1280x900 — kombinasi yang
	// mustahil di desktop asli. Samakan ukuran layar/jendela.
	b.applyViewport(ctx)

	if _, err := page.call(ctx, "Network.enable", nil); err != nil {
		return err
	}
	cookies := parseCookies(cookieHeader)
	if len(cookies) > 0 {
		if _, err := page.call(ctx, "Network.setCookies", map[string]any{"cookies": cookies}); err != nil {
			return fmt.Errorf("gagal menyuntikkan cookie: %w", err)
		}
	}
	if _, err := page.call(ctx, "Page.enable", nil); err != nil {
		return err
	}
	navResult, err := page.call(ctx, "Page.navigate", map[string]any{"url": arenaBase + "/image/direct"})
	if err != nil {
		return fmt.Errorf("gagal membuka arena.ai: %w", err)
	}
	// Page.navigate bisa "berhasil" tapi melaporkan kegagalan jaringan di
	// errorText (mis. net::ERR_NAME_NOT_RESOLVED). Tanpa dicek, kegagalan
	// DNS/proxy di panel berubah jadi pesan "halaman tidak selesai dimuat"
	// yang menyesatkan.
	var nav struct {
		ErrorText string `json:"errorText"`
	}
	if uerr := json.Unmarshal(navResult, &nav); uerr == nil && nav.ErrorText != "" {
		return fmt.Errorf("%w: gagal memuat arena.ai (%s)", ErrBrowser, nav.ErrorText)
	}
	if err := b.waitPageReady(ctx); err != nil {
		return err
	}
	// Beri halaman waktu "hangat" sebelum token reCAPTCHA diminta. Ini bukan
	// basa-basi: token yang dipanen ~2 detik setelah halaman dibuka berulang
	// kali ditolak server dengan "recaptcha validation failed", sedangkan
	// setelah halaman terbuka ~5-10 detik tokennya diterima. reCAPTCHA
	// mengumpulkan sinyal selama halaman hidup, jadi skor token pertama
	// bergantung pada jeda ini.
	warmup := envDuration("ARENA_WARMUP_SECONDS", defaultWarmup)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(warmup):
	}
	b.wakeCaptcha(ctx)
	return nil
}

// readyTimeout: batas menunggu DOM. Mode tampilan (Xvfb) diberi waktu lebih
// panjang karena panel 1 core harus merender halaman arena.ai yang berat.
func (b *browser) readyTimeout() time.Duration {
	def := chromeReadyTimeout
	if b.headed {
		def = 45 * time.Second
	}
	return envDuration("ARENA_READY_SECONDS", def)
}

// waitPageReady menunggu DOM selesai di-parse. Cukup "interactive", BUKAN
// "complete": arena.ai mempertahankan koneksi (streaming/analytics) sehingga
// event load sering tidak pernah selesai — diuji di Chrome 155 dan readyState
// berhenti di "interactive" selama-lamanya. Skrip generate tetap menunggu
// grecaptcha secara aktif, jadi itu yang sebenarnya menjadi syarat.
//
// Kalau waktu habis, peluncuran TIDAK digagalkan: menggagalkan di sini membuat
// bot jatuh ke headless, dan headless selalu ditolak reCAPTCHA. Yang dilakukan
// hanya mencatat diagnostik (judul/isi halaman) lalu lanjut — skrip generate
// punya penantian grecaptcha sendiri. Pengecualian: kalau sama sekali tidak ada
// dokumen (URL kosong / about:blank), itu memang kegagalan Chrome.
func (b *browser) waitPageReady(ctx context.Context) error {
	return b.waitReadyFor(ctx, b.readyTimeout())
}

func (b *browser) waitReadyFor(ctx context.Context, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	lastState := "(belum terbaca)"
	lastURL := ""
	// Satu evaluasi untuk dua sinyal: readyState DAN keberadaan grecaptcha.
	// Halaman yang tersangkut di "loading" tetap berguna begitu skrip
	// reCAPTCHA-nya jalan, jadi tidak perlu menunggu sampai batas waktu.
	const probe = `JSON.stringify({r: document.readyState, c: !!(window.grecaptcha && window.grecaptcha.enterprise)})`
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		out, err := b.evaluateString(ctx, probe, false)
		if err == nil {
			var p struct {
				R string `json:"r"`
				C bool   `json:"c"`
			}
			if json.Unmarshal([]byte(out), &p) == nil {
				if p.R == "interactive" || p.R == "complete" {
					return nil
				}
				if p.R != "" {
					lastState = p.R
				}
				if p.C {
					logfmt.Info("arena: readyState=%s tapi grecaptcha sudah ada, lanjut\n", p.R)
					return nil
				}
			}
		}
		if u, uerr := b.evaluateString(ctx, "location.href", false); uerr == nil && u != "" {
			lastURL = u
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}

	diag, blank := b.pageDiagnostics(ctx)
	logfmt.Warn("arena: halaman arena.ai belum siap setelah %s (mode=%s, lanjut): %s\n",
		limit.Round(time.Second), b.modeName(), diag)
	if blank {
		return fmt.Errorf("%w: halaman arena.ai tidak terbentuk (readyState=%s, url=%s)", ErrBrowser, lastState, lastURL)
	}
	return nil
}

// modeName hanya untuk log.
func (b *browser) modeName() string {
	if b.headed {
		return "tampilan"
	}
	return "headless"
}

// pageDiagnostics merangkum keadaan halaman saat readyState tidak mau maju.
// Isinya sengaja judul + potongan teks: itulah yang membedakan halaman yang
// lambat di-parse dari challenge Cloudflare ("Just a moment...").
// blank=true berarti tidak ada dokumen sama sekali.
func (b *browser) pageDiagnostics(ctx context.Context) (string, bool) {
	const expr = `(() => {
  const d = document;
  const body = d.body ? (d.body.innerText || '') : '';
  const res = (performance.getEntriesByType ? performance.getEntriesByType('resource').length : -1);
  return JSON.stringify({
    readyState: d.readyState,
    title: (d.title || '').slice(0, 120),
    url: location.href,
    root: !!d.documentElement,
    body: body.replace(/\s+/g, ' ').slice(0, 200),
    scripts: d.scripts ? d.scripts.length : -1,
    resources: res,
    webdriver: navigator.webdriver === true,
    grecaptcha: !!(window.grecaptcha && window.grecaptcha.enterprise)
  });
})()`
	out, err := b.evaluateString(ctx, expr, false)
	if err != nil || out == "" {
		return "diagnostik gagal: " + errText(err), false
	}
	var d struct {
		ReadyState string `json:"readyState"`
		Title      string `json:"title"`
		URL        string `json:"url"`
		Root       bool   `json:"root"`
		Body       string `json:"body"`
	}
	blank := true
	if json.Unmarshal([]byte(out), &d) == nil {
		blank = !d.Root && d.Title == "" && (d.URL == "" || d.URL == "about:blank")
	}
	return out, blank
}

// errText aman untuk err nil.
func errText(err error) string {
	if err == nil {
		return "tidak ada balasan"
	}
	return err.Error()
}

// wakeCaptcha memastikan grecaptcha sudah terpasang sebelum skrip generate
// dijalankan. Di mode tampilan, kalau belum siap, halaman dimuat ulang sekali:
// halaman yang tersangkut di readyState=loading biasanya baru mengeksekusi
// skrip reCAPTCHA setelah reload. Tidak fatal kalau tetap gagal — skrip
// generate masih menunggu sendiri dan pesannya lebih spesifik.
func (b *browser) wakeCaptcha(ctx context.Context) {
	if b.captchaReady(ctx, 8*time.Second) {
		return
	}
	if !b.headed {
		return
	}
	logfmt.Warn("arena: grecaptcha belum siap di mode tampilan, memuat ulang halaman\n")
	if _, err := b.page.call(ctx, "Page.reload", nil); err != nil {
		return
	}
	_ = b.waitReadyFor(ctx, 15*time.Second)
	select {
	case <-ctx.Done():
		return
	case <-time.After(3 * time.Second):
	}
	if b.captchaReady(ctx, 12*time.Second) {
		logfmt.Info("arena: grecaptcha siap setelah muat ulang\n")
		return
	}
	logfmt.Warn("arena: grecaptcha tetap belum siap setelah muat ulang\n")
}

// captchaReady memeriksa keberadaan grecaptcha.enterprise.execute.
func (b *browser) captchaReady(ctx context.Context, limit time.Duration) bool {
	const expr = `!!(window.grecaptcha && grecaptcha.enterprise && typeof grecaptcha.enterprise.execute === 'function')`
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return false
		}
		if out, err := b.evaluateString(ctx, expr, false); err == nil && out == "true" {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(300 * time.Millisecond):
		}
	}
	return false
}

// evaluateString menjalankan ekspresi JS dan mengembalikan hasil string-nya.
// awaitPromise=true untuk skrip yang mengembalikan Promise (generate).
func (b *browser) evaluateString(ctx context.Context, expression string, awaitPromise bool) (string, error) {
	if b.page == nil {
		return "", fmt.Errorf("%w: tab DevTools tidak tersedia", ErrBrowser)
	}
	raw, err := b.page.call(ctx, "Runtime.evaluate", map[string]any{
		"expression":    expression,
		"awaitPromise":  awaitPromise,
		"returnByValue": true,
		"userGesture":   true,
	})
	if err != nil {
		return "", err
	}
	var res struct {
		Result struct {
			Type  string          `json:"type"`
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", fmt.Errorf("gagal baca hasil JS: %w", err)
	}
	if res.ExceptionDetails != nil {
		desc := res.ExceptionDetails.Text
		if res.ExceptionDetails.Exception.Description != "" {
			desc = res.ExceptionDetails.Exception.Description
		}
		return "", fmt.Errorf("JS error: %s", strings.TrimSpace(desc))
	}
	// Skrip generate & probe mengembalikan string, tapi ekspresi sederhana
	// (mis. document.cookie.length) mengembalikan angka — jangan sampai itu
	// dianggap kegagalan proto.
	if len(res.Result.Value) == 0 {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(res.Result.Value, &s); err == nil {
		return s, nil
	}
	return strings.Trim(string(res.Result.Value), `"`), nil
}

// chromeMajorVersion membaca versi mayor dari `chrome --version`.
//
// Dua kenyataan lapangan: (1) di panel Debian keluarannya "Google Chrome
// 155.0.8059.84"; (2) di Windows, chrome.exe --version TIDAK mencetak versi
// (ia menyambung ke sesi browser yang sudah jalan dan mencetak pesan lain).
// Versi 0 = tidak diketahui, dan pemanggil melewati gerbang versi — lebih
// baik mencoba launch (gagal dengan pesan jelas) daripada menolak Chrome
// yang sebenarnya sehat.
func chromeMajorVersion(ctx context.Context, chromePath string) (int, error) {
	if runtime.GOOS == "windows" {
		// Di Windows, GUI exe seperti msedge.exe/chrome.exe dengan flag --version
		// sering keluar dengan status 0x80000003 atau tanpa output stdout.
		return 0, nil
	}
	vctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(vctx, chromePath, "--version").Output()
	if err != nil {
		return 0, fmt.Errorf("tidak bisa membaca versi: %w", err)
	}
	for _, f := range strings.Fields(string(out)) {
		// Token versi wajib berbentuk angka.angka (mis. 155.0.8059.84).
		if !strings.Contains(f, ".") {
			continue
		}
		major := f[:strings.IndexByte(f, '.')]
		if n, cerr := strconv.Atoi(major); cerr == nil && n > 0 {
			return n, nil
		}
	}
	return 0, nil
}

// freeLocalPort mengambil port bebas di loopback untuk --remote-debugging-port.
func freeLocalPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("gagal cari port debug: %w", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// waitDevToolsTarget menunggu Chrome siap lalu membuat satu tab baru.
// Mengembalikan websocket endpoint tab tersebut (bukan endpoint browser),
// supaya seluruh perintah CDP bisa dipakai tanpa sessionId.
func waitDevToolsTarget(ctx context.Context, port int, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/json/version", nil)
		if err == nil {
			if resp, derr := http.DefaultClient.Do(req); derr == nil {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					if ws, nerr := newPageTarget(ctx, base); nerr == nil {
						return ws, nil
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return "", fmt.Errorf("%w: DevTools tidak merespons di port %d", ErrBrowser, port)
}

// newPageTarget membuat tab kosong lewat endpoint HTTP DevTools (PUT sejak
// Chrome 111) dan mengembalikan websocket tab itu.
func newPageTarget(ctx context.Context, base string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, base+"/json/new?about:blank", nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var target struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&target); err != nil {
		return "", err
	}
	if target.WebSocketDebuggerURL == "" {
		return "", fmt.Errorf("endpoint DevTools tab kosong")
	}
	return target.WebSocketDebuggerURL, nil
}

// cgroupFreeMemory menghitung sisa memori container. known=false kalau
// panel tidak memakai cgroup (mis. saat develop di Windows) — dalam kasus
// itu penjagaan dilewati, bukan ditebak.
func cgroupFreeMemory() (free uint64, known bool) {
	maxRaw, err := os.ReadFile("/sys/fs/cgroup/memory.max")
	if err != nil {
		maxRaw, err = os.ReadFile("/sys/fs/cgroup/memory/memory.limit_in_bytes")
		if err != nil {
			return 0, false
		}
	}
	maxStr := strings.TrimSpace(string(maxRaw))
	if maxStr == "max" {
		return 0, false
	}
	limit, perr := strconv.ParseUint(maxStr, 10, 64)
	if perr != nil || limit >= 1<<60 { // nilai raksasa = tanpa batas (cgroup v1)
		return 0, false
	}
	curRaw, err := os.ReadFile("/sys/fs/cgroup/memory.current")
	if err != nil {
		curRaw, err = os.ReadFile("/sys/fs/cgroup/memory/memory.usage_in_bytes")
		if err != nil {
			return 0, false
		}
	}
	used, perr := strconv.ParseUint(strings.TrimSpace(string(curRaw)), 10, 64)
	if perr != nil {
		return 0, false
	}
	if used >= limit {
		return 0, true
	}
	return limit - used, true
}
