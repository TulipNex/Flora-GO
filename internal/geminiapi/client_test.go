package geminiapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeGemini meniru tiga endpoint yang dipakai mode tamu: HEAD halaman utama
// (Set-Cookie), GET /app (parameter halaman), POST StreamGenerate.
type fakeGemini struct {
	srv *httptest.Server

	headCalls int32
	appCalls  int32
	chatCalls int32

	mu         sync.Mutex
	chatBody   string
	chatStatus int32
	firstChat  int32 // status khusus permintaan chat pertama (0 = tidak dipakai)
	appBody    string
	setCookies []string

	// lastReq mencatat permintaan chat terakhir untuk diperiksa tes.
	lastQuery  string
	lastForm   string
	lastCook   string
	lastHeader http.Header

	// headCook mencatat header Cookie pada HEAD halaman utama (dipakai tes
	// cookie persetujuan).
	headCook string

	// rotateCookies membuat setiap HEAD mengembalikan NID berbeda, meniru
	// sesi tamu baru (rotasi cookie / proses baru setelah restart).
	rotateCookies bool
}

func newFakeGemini(t *testing.T, answer string) *fakeGemini {
	t.Helper()
	f := &fakeGemini{
		// Penanda tutup stream disertakan supaya parser berhenti seketika
		// (tanpa itu tes menunggu idle 4 dtk per permintaan).
		chatBody:   answer + "\n" + `[["e",4,null,null,131]]` + "\n",
		chatStatus: http.StatusOK,
		appBody:    `{"cfb2h":"boq_gemini-web-uiserver_test","FdrFJe":"12345"}`,
		setCookies: []string{"NID=guest-1; Path=/; Domain=.google.com; HttpOnly"},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodHead && r.URL.Path == "/":
			n := atomic.AddInt32(&f.headCalls, 1)
			f.mu.Lock()
			f.headCook = r.Header.Get("Cookie")
			cookies := append([]string(nil), f.setCookies...)
			if f.rotateCookies {
				cookies = []string{fmt.Sprintf("NID=guest-%d; Path=/", n)}
			}
			f.mu.Unlock()
			for _, c := range cookies {
				w.Header().Add("Set-Cookie", c)
			}
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/app":
			atomic.AddInt32(&f.appCalls, 1)
			f.mu.Lock()
			body := f.appBody
			f.mu.Unlock()
			_, _ = w.Write([]byte(body))
		case r.Method == http.MethodPost && r.URL.Path == "/chat":
			n := atomic.AddInt32(&f.chatCalls, 1)
			// Tutup koneksi setelah balasan supaya parser tidak menunggu idle
			// (server asli juga menutup, hanya kadang lambat).
			w.Header().Set("Connection", "close")
			f.mu.Lock()
			f.lastQuery = r.URL.RawQuery
			f.lastCook = r.Header.Get("Cookie")
			f.lastHeader = r.Header.Clone()
			_ = r.ParseForm()
			f.lastForm = r.PostForm.Get("f.req")
			status, body := f.chatStatus, f.chatBody
			if f.firstChat > 0 && n == 1 {
				status = f.firstChat
			}
			f.mu.Unlock()
			w.WriteHeader(int(status))
			_, _ = w.Write([]byte(body))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	pointAt(t, f.srv)
	return f
}

// pointAt mengarahkan endpoint produksi ke server tiruan selama tes.
func pointAt(t *testing.T, srv *httptest.Server) {
	t.Helper()
	oldRoot, oldApp, oldChat := rootURL, appURL, chatEndpoint
	rootURL = srv.URL + "/"
	appURL = srv.URL + "/app"
	chatEndpoint = srv.URL + "/chat"
	t.Cleanup(func() {
		rootURL, appURL, chatEndpoint = oldRoot, oldApp, oldChat
	})
}

// TestAskGuestFlowCachesCredentials memastikan satu giliran tamu memakai satu
// HEAD + satu GET /app, dan permintaan berikutnya memakai tembolok.
func TestAskGuestFlowCachesCredentials(t *testing.T) {
	f := newFakeGemini(t, buildStreamLine(t, streamInner([]string{"Halo dari Gemini"})))
	c := New()

	ctx := context.Background()
	res, err := c.Ask(ctx, "halo")
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if res.Text != "Halo dari Gemini" {
		t.Fatalf("teks = %q", res.Text)
	}
	if _, err := c.Ask(ctx, "lagi"); err != nil {
		t.Fatalf("ask kedua: %v", err)
	}

	if got := atomic.LoadInt32(&f.headCalls); got != 1 {
		t.Fatalf("HEAD dipanggil %d kali, mau 1", got)
	}
	if got := atomic.LoadInt32(&f.appCalls); got != 1 {
		t.Fatalf("/app dipanggil %d kali, mau 1", got)
	}
	if got := atomic.LoadInt32(&f.chatCalls); got != 2 {
		t.Fatalf("chat dipanggil %d kali, mau 2", got)
	}

	f.mu.Lock()
	query, form, cookie := f.lastQuery, f.lastForm, f.lastCook
	f.mu.Unlock()
	for _, want := range []string{"bl=boq_gemini-web-uiserver_test", "f.sid=12345", "rt=c"} {
		if !strings.Contains(query, want) {
			t.Fatalf("query %q tidak memuat %q", query, want)
		}
	}
	if !strings.Contains(form, "lagi") {
		t.Fatalf("f.req tidak memuat prompt terakhir: %q", form)
	}
	if cookie != "SOCS=CAI; NID=guest-1" {
		t.Fatalf("cookie dikirim = %q", cookie)
	}
}

// TestAskRetriesWithFreshCredentials memastikan cookie yang ditolak (403)
// memicu satu pengambilan kredensial baru, bukan kegagalan langsung.
func TestAskRetriesWithFreshCredentials(t *testing.T) {
	f := newFakeGemini(t, buildStreamLine(t, streamInner([]string{"Pulih"})))
	f.mu.Lock()
	f.firstChat = http.StatusForbidden
	f.mu.Unlock()
	c := New()

	res, err := c.Ask(context.Background(), "halo")
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if res.Text != "Pulih" {
		t.Fatalf("teks = %q", res.Text)
	}
	if got := atomic.LoadInt32(&f.headCalls); got != 2 {
		t.Fatalf("HEAD dipanggil %d kali, mau 2", got)
	}
	if got := atomic.LoadInt32(&f.appCalls); got != 2 {
		t.Fatalf("/app dipanggil %d kali, mau 2", got)
	}
	if got := atomic.LoadInt32(&f.chatCalls); got != 2 {
		t.Fatalf("chat dipanggil %d kali, mau 2", got)
	}
}

// TestAskLimitNotRetried memastikan 429 tidak diulang (menghemat kuota) dan
// dipetakan ke ErrLimit.
func TestAskLimitNotRetried(t *testing.T) {
	f := newFakeGemini(t, "")
	f.mu.Lock()
	f.chatStatus = http.StatusTooManyRequests
	f.mu.Unlock()
	c := New()

	_, err := c.Ask(context.Background(), "halo")
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("error = %v, mau ErrLimit", err)
	}
	if got := atomic.LoadInt32(&f.chatCalls); got != 1 {
		t.Fatalf("chat dipanggil %d kali, mau 1", got)
	}
}

// TestGuestCookieJarKeepsAllCookies mengunci perilaku keranjang cookie: SELURUH
// Set-Cookie dipakai (bukan hanya yang pertama) dan cookie persetujuan selalu
// ikut di depan. Versi lama hanya memakai cookie pertama; di panel itu
// `__Secure-ENID` sehingga `NID` terbuang dan percakapan lanjutan dijawab
// `1097` — bug yang dibuktikan lewat probe panel (Nov 2026).
func TestGuestCookieJarKeepsAllCookies(t *testing.T) {
	f := newFakeGemini(t, buildStreamLine(t, streamInner([]string{"ok"})))
	f.mu.Lock()
	f.setCookies = []string{"NID=pertama; Path=/", "AEC=kedua; Path=/"}
	f.mu.Unlock()

	if _, err := New().Ask(context.Background(), "halo"); err != nil {
		t.Fatalf("ask: %v", err)
	}
	f.mu.Lock()
	cookie := f.lastCook
	f.mu.Unlock()
	if cookie != "SOCS=CAI; NID=pertama; AEC=kedua" {
		t.Fatalf("cookie = %q, mau SOCS=CAI; NID=pertama; AEC=kedua", cookie)
	}
}

// TestGuestCookieSendsConsentOnHead memastikan HEAD halaman utama sudah membawa
// cookie persetujuan. Tanpa itu, IP yang kena dinding persetujuan (panel Eropa)
// tidak diberi `NID` sama sekali.
func TestGuestCookieSendsConsentOnHead(t *testing.T) {
	f := newFakeGemini(t, buildStreamLine(t, streamInner([]string{"ok"})))
	if _, err := New().Ask(context.Background(), "halo"); err != nil {
		t.Fatalf("ask: %v", err)
	}
	f.mu.Lock()
	cookie := f.headCook
	f.mu.Unlock()
	if cookie != consentCookie {
		t.Fatalf("HEAD cookie = %q, mau %q", cookie, consentCookie)
	}
}

// TestGuestCookieKeepsNIDWhenNotFirst meniru urutan cookie panel: `__Secure-ENID`
// lebih dulu, `NID` menyusul. `NID` wajib tetap terkirim saat chat.
func TestGuestCookieKeepsNIDWhenNotFirst(t *testing.T) {
	f := newFakeGemini(t, buildStreamLine(t, streamInner([]string{"ok"})))
	f.mu.Lock()
	f.setCookies = []string{"__Secure-ENID=enid; Path=/", "NID=nid-1; Path=/", "COMPASS=compass; Path=/"}
	f.mu.Unlock()

	c := New()
	if _, err := c.Ask(context.Background(), "halo"); err != nil {
		t.Fatalf("ask: %v", err)
	}
	f.mu.Lock()
	cookie := f.lastCook
	f.mu.Unlock()
	for _, want := range []string{"NID=nid-1", "COMPASS=compass", consentCookie} {
		if !strings.Contains(cookie, want) {
			t.Fatalf("cookie chat %q tidak memuat %q", cookie, want)
		}
	}
	if names := strings.Join(c.CookieNames(), ","); names != "SOCS,__Secure-ENID,NID,COMPASS" {
		t.Fatalf("CookieNames = %q", names)
	}
}

// TestFetchMetaMissingSid memastikan halaman tanpa f.sid dilaporkan ErrMeta
// (protokol berubah), bukan dipakai apa adanya.
func TestFetchMetaMissingSid(t *testing.T) {
	f := newFakeGemini(t, buildStreamLine(t, streamInner([]string{"ok"})))
	f.mu.Lock()
	f.appBody = `{"cfb2h":"boq_x"}`
	f.mu.Unlock()

	_, err := New().Ask(context.Background(), "halo")
	if !errors.Is(err, ErrMeta) {
		t.Fatalf("error = %v, mau ErrMeta", err)
	}
	if got := atomic.LoadInt32(&f.chatCalls); got != 0 {
		t.Fatalf("chat tidak boleh dipanggil, dapat %d", got)
	}
}

// TestFetchMetaMissingBuildLabel menguji fallback pola boq_ saat kunci cfb2h
// tidak ada (nama build pernah berpindah kunci).
func TestFetchMetaMissingBuildLabel(t *testing.T) {
	f := newFakeGemini(t, buildStreamLine(t, streamInner([]string{"ok"})))
	f.mu.Lock()
	f.appBody = `<html>boq_assistant-bard-web-server_20260101.01_p0 ... "FdrFJe":"777"</html>`
	f.mu.Unlock()

	if _, err := New().Ask(context.Background(), "halo"); err != nil {
		t.Fatalf("ask: %v", err)
	}
	f.mu.Lock()
	query := f.lastQuery
	f.mu.Unlock()
	if !strings.Contains(query, "bl=boq_assistant-bard-web-server_20260101.01_p0") {
		t.Fatalf("fallback build label tidak dipakai: %q", query)
	}
}

// TestTransportHeaderTimeouts memastikan batas header dipisah: halaman biasa
// 30 dtk, chat 90 dtk. Menyatukan keduanya pernah membuat POST chat gagal
// dengan `http2: timeout awaiting response headers` padahal kredensial sehat.
func TestTransportHeaderTimeouts(t *testing.T) {
	c := New()
	page, ok := c.http.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport halaman = %T", c.http.Transport)
	}
	chat, ok := c.chat.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport chat = %T", c.chat.Transport)
	}
	if page.ResponseHeaderTimeout != pageHeaderTimeout {
		t.Fatalf("header timeout halaman = %v, mau %v", page.ResponseHeaderTimeout, pageHeaderTimeout)
	}
	if chat.ResponseHeaderTimeout != chatHeaderTimeout {
		t.Fatalf("header timeout chat = %v, mau %v", chat.ResponseHeaderTimeout, chatHeaderTimeout)
	}
	if chatHeaderTimeout <= pageHeaderTimeout {
		t.Fatalf("batas chat (%v) harus lebih panjang dari halaman (%v)", chatHeaderTimeout, pageHeaderTimeout)
	}
	if page == chat {
		t.Fatal("transport halaman dan chat harus terpisah")
	}
}

// TestPingForcesFreshCredentials memastikan Ping tidak memakai tembolok.
func TestPingForcesFreshCredentials(t *testing.T) {
	f := newFakeGemini(t, buildStreamLine(t, streamInner([]string{"ok"})))
	c := New()
	if _, err := c.Ask(context.Background(), "halo"); err != nil {
		t.Fatalf("ask: %v", err)
	}
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if got := atomic.LoadInt32(&f.headCalls); got != 2 {
		t.Fatalf("HEAD dipanggil %d kali, mau 2", got)
	}
}

// TestCookieIdentityUsesNIDOnly mengunci dasar sidik jari sesi tamu: hanya
// nilai `NID` yang menentukan identitas percakapan. Cookie lain atau urutan
// yang berbeda tidak boleh membatalkan tiket, karena tiket hanya berlaku
// selama `NID` yang membuatnya sama (probe Nov 2026).
func TestCookieIdentityUsesNIDOnly(t *testing.T) {
	a := cookieIdentity("SOCS=CAI; NID=abc; COMPASS=x")
	b := cookieIdentity("SOCS=CAI; AEC=y; NID=abc")
	if a == "" || a != b {
		t.Fatalf("sidik jari harus sama walau cookie lain berbeda: %q vs %q", a, b)
	}
	if a == cookieIdentity("SOCS=CAI; NID=lain") {
		t.Fatal("NID berbeda harus menghasilkan sidik jari berbeda")
	}
	if cookieIdentity("") != "" {
		t.Fatal("cookie kosong harus menghasilkan sidik jari kosong")
	}
	if cookieIdentity("SOCS=CAI") == "" {
		t.Fatal("cookie tanpa NID tetap harus punya sidik jari (cadangan)")
	}
}

// TestTicketStaleWhenCookieChanged mengunci penghematan kuota: tiket dari sesi
// tamu lain (cookie/NID berbeda) tidak dikirim sama sekali, karena server pasti
// menjawab `1097`. Tanpa ini setiap rotasi cookie membakar satu permintaan dan
// mencetak baris "tiket percakapan ditolak" yang menyesatkan.
func TestTicketStaleWhenCookieChanged(t *testing.T) {
	f := newFakeGemini(t, buildStreamLine(t, streamInner([]string{"Halo"})))
	c := New()
	ctx := context.Background()
	if _, err := c.Ask(ctx, "halo"); err != nil {
		t.Fatalf("ask: %v", err)
	}
	id := c.CookieID()
	if id == "" {
		t.Fatal("CookieID kosong setelah cookie diambil")
	}

	before := atomic.LoadInt32(&f.chatCalls)
	_, err := c.AskWith(ctx, "lanjut", Resume{ConversationID: "c_lama", CookieID: "sidik-jari-lain"})
	if !errors.Is(err, ErrTicketStale) {
		t.Fatalf("err = %v, mau ErrTicketStale", err)
	}
	if got := atomic.LoadInt32(&f.chatCalls); got != before {
		t.Fatalf("permintaan tetap dikirim (%d → %d), harusnya tidak dikirim", before, got)
	}
	if c.TicketRejected() {
		t.Fatal("tiket basi tidak boleh memicu jeda tiket (bukan penolakan server)")
	}

	// Sidik jari yang cocok tetap dipakai.
	if _, err := c.AskWith(ctx, "lanjut", Resume{ConversationID: "c_baru", CookieID: id}); err != nil {
		t.Fatalf("ask dengan tiket cocok: %v", err)
	}
}

// TestDiagAndPingKeepSessionCookie mengunci bug nyata Nov 2026: `.gemini status`
// memanggil Ping dan `.gemini diag` memanggil Diag, keduanya mengambil cookie
// baru. Cookie baru = sesi tamu baru (NID lain) = tiket percakapan yang sedang
// hidup langsung jadi asing. Owner melihat `1097` tepat setelah menjalankan
// status/diag. Keduanya harus mengembalikan kredensial lama.
func TestDiagAndPingKeepSessionCookie(t *testing.T) {
	f := newFakeGemini(t, buildStreamLine(t, streamInner([]string{"Halo"})))
	f.mu.Lock()
	f.rotateCookies = true
	f.mu.Unlock()

	c := New()
	ctx := context.Background()
	if _, err := c.Ask(ctx, "halo"); err != nil {
		t.Fatalf("ask: %v", err)
	}
	id := c.CookieID()

	if err := c.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if got := c.CookieID(); got != id {
		t.Fatalf("Ping mengganti sesi tamu: %q → %q", id, got)
	}
	if d := c.Diag(ctx); !d.Chat.OK {
		t.Fatalf("diag chat gagal: %+v", d.Chat)
	}
	if got := c.CookieID(); got != id {
		t.Fatalf("Diag mengganti sesi tamu: %q → %q", id, got)
	}
	// Tiket yang dibuat sebelum status/diag harus tetap diterima.
	if _, err := c.AskWith(ctx, "lanjut", Resume{ConversationID: "c_1", CookieID: id}); err != nil {
		t.Fatalf("tiket jadi basi gara-gara status/diag: %v", err)
	}
	if got := atomic.LoadInt32(&f.headCalls); got < 3 {
		t.Fatalf("Ping/Diag harus tetap mengambil cookie baru (HEAD = %d)", got)
	}
}
