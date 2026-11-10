// Package reactionwa adalah client "React Channel WhatsApp" milik situs
// auto.reactionwa.online, tanpa apikey.
//
// Alurnya (hasil uji langsung ke server):
//
//	POST securetoken.googleapis.com  -> access token Firebase dari refresh token
//	Chrome membuka /Free/            -> token Cloudflare Turnstile (tanpa login)
//	POST api-v1.reactionwa.online    -> reaksi masuk antrean
//
// Login Google hanya dilakukan sekali oleh owner di browser pribadinya; bot
// menyimpan refresh token di data/rwa-token.txt lalu mencetak access token
// sendiri (endpoint securetoken menolak request tanpa header Origin/Referer
// situs, jadi header itu wajib). Halaman /Free/ sendiri tidak butuh login:
// captcha-nya dibuat di Chrome panel dan dikirim bersama access token.
package reactionwa

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"wabot-go/internal/memlog"
)

const (
	// DefaultAPIURL adalah endpoint proses reaksi (api-v1). Bisa ditimpa
	// lewat RWA_API_URL kalau penyedia pindah domain.
	DefaultAPIURL = "https://api-v1.reactionwa.online"
	// SiteOrigin dipakai sebagai Origin/Referer: API key Firebase dibatasi
	// referer, request tanpa header ini dibalas 403.
	SiteOrigin = "https://auto.reactionwa.online"
	// MaxEmojis sama dengan batas halaman /Free/.
	MaxEmojis = 5

	// tokenFile menyimpan refresh token Firebase (bukan access token).
	tokenFile = "data/rwa-token.txt"

	firebaseAPIKey = "AIzaSyDNbAlgLycK0oRO8I9ja3mPnIccZkOz0M0"
	// secureSalt adalah kunci HMAC yang dipakai JS situs. Server ternyata
	// belum memverifikasi tanda tangan ini, tapi tetap dikirim supaya bentuk
	// request identik dengan aslinya.
	secureSalt = "RW_SECURE_SALT_v9#82d!"

	requestTimeout = 45 * time.Second
)

var defaultTokenURL = "https://securetoken.googleapis.com/v1/token?key=" + firebaseAPIKey

var (
	// ErrNoToken: refresh token belum pernah disimpan.
	ErrNoToken = errors.New("sesi reactionwa belum diatur")
	// ErrTokenRejected: refresh/access token ditolak server (sesi mati).
	ErrTokenRejected = errors.New("sesi reactionwa ditolak")
	// ErrCaptcha: Turnstile tidak lolos / tokennya ditolak server.
	ErrCaptcha = errors.New("verifikasi captcha reactionwa gagal")
	// ErrLimit: jatah reaksi habis atau terlalu sering.
	ErrLimit = errors.New("jatah reaksi reactionwa habis")
	// ErrLink: link bukan link pesan channel.
	ErrLink = errors.New("link channel tidak valid")
	// ErrBrowser: Chrome/Xvfb gagal disiapkan.
	ErrBrowser = errors.New("browser captcha tidak bisa dijalankan")
)

// APIError membawa status HTTP + pesan server untuk penolakan yang tidak
// masuk kategori di atas.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("server menolak permintaan (HTTP %d)", e.Status)
	}
	return e.Message
}

// Account adalah identitas Firebase hasil decode access token.
type Account struct {
	UID       string
	Email     string
	ExpiresAt time.Time
}

// Task adalah ringkasan antrean yang dikembalikan server saat sukses.
type Task struct {
	ID        string `json:"key_id"`
	Status    string `json:"status"`
	Channel   string `json:"saluranURL"`
	Reactions string `json:"reaction_code"`
}

// VIP adalah info paket akun, hanya terisi kalau akun punya paket VIP.
type VIP struct {
	PointRemaining int64  `json:"pointRemaining"`
	PointTotal     int64  `json:"pointTotal"`
	TotalReaction  int64  `json:"totalReaction"`
	PackageName    string `json:"packageName"`
	ExpiresAt      *int64 `json:"expiresAt"`
}

// Result adalah balasan sukses server (reaksi sudah masuk antrean).
type Result struct {
	Message string `json:"message"`
	Task    *Task  `json:"task"`
	VIP     *VIP   `json:"vip"`
}

// CaptchaProvider menghasilkan token Turnstile. Implementasi produksi adalah
// Browser; tes memakai provider palsu.
type CaptchaProvider interface {
	Token(ctx context.Context) (string, error)
	Invalidate()
}

// Client memanggil API reaksi. Aman dipakai berulang: access token di-cache
// sampai mendekati kedaluwarsa.
type Client struct {
	apiURL   string
	tokenURL string
	http     *http.Client
	captcha  CaptchaProvider
	now      func() time.Time

	mu        sync.Mutex
	access    string
	accessExp time.Time
	account   Account
}

var (
	sharedOnce sync.Once
	shared     *Client
)

// Shared mengembalikan client proses-wide (Chrome captcha ikut dipakai
// bersama supaya tidak ada dua instance browser).
func Shared() *Client {
	sharedOnce.Do(func() {
		shared = &Client{
			apiURL:   apiURLFromEnv(),
			tokenURL: defaultTokenURL,
			http:     &http.Client{Timeout: requestTimeout},
			captcha:  newBrowser(),
			now:      time.Now,
		}
	})
	return shared
}

func apiURLFromEnv() string {
	if v := strings.TrimSpace(os.Getenv("RWA_API_URL")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return DefaultAPIURL
}

// NewForTest membuat client tanpa browser (captcha diisi pemanggil).
func newClient(apiURL, tokenURL string, captcha CaptchaProvider, now func() time.Time) *Client {
	return &Client{
		apiURL:   apiURL,
		tokenURL: tokenURL,
		http:     &http.Client{Timeout: requestTimeout},
		captcha:  captcha,
		now:      now,
	}
}

// HasToken melaporkan refresh token sudah tersimpan.
func HasToken() bool {
	_, err := ReadRefreshToken()
	return err == nil
}

// ReadRefreshToken membaca refresh token dari data/rwa-token.txt.
func ReadRefreshToken() (string, error) {
	raw, err := os.ReadFile(tokenFile)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%w: jalankan .rch login <refresh token>", ErrNoToken)
		}
		return "", fmt.Errorf("gagal baca %s: %w", tokenFile, err)
	}
	tok := normalizeToken(string(raw))
	if tok == "" {
		return "", fmt.Errorf("%w: file %s kosong", ErrNoToken, tokenFile)
	}
	return tok, nil
}

// WriteRefreshToken menyimpan refresh token secara atomik (0600).
func WriteRefreshToken(raw string) error {
	tok := normalizeToken(raw)
	if tok == "" {
		return errors.New("refresh token kosong")
	}
	if len(tok) < 20 || strings.ContainsAny(tok, " \t\r\n\"'") {
		return errors.New("refresh token tidak valid (harus satu baris tanpa spasi)")
	}
	if err := os.MkdirAll(filepath.Dir(tokenFile), 0755); err != nil {
		return err
	}
	tmp := tokenFile + ".tmp"
	if err := os.WriteFile(tmp, []byte(tok+"\n"), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, tokenFile)
}

// ClearRefreshToken menghapus sesi tersimpan.
func ClearRefreshToken() error {
	err := os.Remove(tokenFile)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func normalizeToken(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "refresh_token=")
	s = strings.Trim(s, `"'`)
	return strings.TrimSpace(s)
}

// Login menyimpan refresh token baru lalu memastikannya bisa dipakai untuk
// mencetak access token. Token hasil rotasi server (kalau ada) ikut disimpan.
func (c *Client) Login(ctx context.Context, refresh string) (Account, error) {
	if err := WriteRefreshToken(refresh); err != nil {
		return Account{}, err
	}
	c.mu.Lock()
	c.access = ""
	c.accessExp = time.Time{}
	c.mu.Unlock()

	acc, _, err := c.mint(ctx)
	if err != nil {
		_ = ClearRefreshToken()
		return Account{}, err
	}
	return acc, nil
}

// Account mencetak access token (kalau perlu) dan mengembalikan identitasnya.
func (c *Client) Account(ctx context.Context) (Account, error) {
	acc, _, err := c.mint(ctx)
	return acc, err
}

// React mengirim reaksi ke satu pesan WhatsApp Channel.
func (c *Client) React(ctx context.Context, link string, emojis []string) (res *Result, err error) {
	clean, err := NormalizeLink(link)
	if err != nil {
		return nil, err
	}
	if len(emojis) == 0 {
		return nil, errors.New("minimal satu emoji reaksi")
	}
	if len(emojis) > MaxEmojis {
		return nil, fmt.Errorf("maksimal %d emoji per kirim", MaxEmojis)
	}
	if c.captcha == nil {
		return nil, errors.New("captcha belum disiapkan")
	}
	reaction := strings.Join(emojis, ",")

	// Satu baris riwayat memori per permintaan: jalur ini bisa menyalakan
	// Chrome (Turnstile) di panel kecil, jadi biayanya perlu terlihat.
	op := memlog.Begin("reactionwa", "react")
	defer func() { op.End(err == nil, "") }()

	var lastErr error
	// Dua percobaan: token Turnstile berumur pendek, jadi kegagalan captcha
	// dicoba sekali lagi dengan token baru sebelum dilaporkan ke owner.
	for attempt := 0; attempt < 2; attempt++ {
		cap, cerr := c.captcha.Token(ctx)
		if cerr != nil {
			return nil, cerr
		}
		_, token, merr := c.mint(ctx)
		if merr != nil {
			return nil, merr
		}
		res, err = c.post(ctx, token, clean, reaction, cap)
		if err == nil {
			return res, nil
		}
		lastErr = err
		if errors.Is(err, ErrCaptcha) {
			c.captcha.Invalidate()
			continue
		}
		return nil, err
	}
	return nil, lastErr
}

func (c *Client) post(ctx context.Context, token, link, reaction, captcha string) (*Result, error) {
	ts := strconv.FormatInt(c.now().UnixMilli(), 10)
	sig := sign(link, reaction, captcha, ts)

	body, err := json.Marshal(map[string]string{
		"url":          link,
		"reaction":     reaction,
		"captchaToken": captcha,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL, strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("gagal menyiapkan request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("x-req-time", ts)
	req.Header.Set("x-req-sig", sig)
	req.Header.Set("Origin", SiteOrigin)
	req.Header.Set("Referer", SiteOrigin+"/")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tidak bisa menghubungi server: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, fmt.Errorf("gagal membaca balasan: %w", err)
	}

	var out struct {
		Success bool            `json:"success"`
		Status  json.RawMessage `json:"status"`
		Message json.RawMessage `json:"message"`
		Error   json.RawMessage `json:"error"`
		Task    *Task           `json:"task"`
		VIP     *VIP            `json:"vip"`
	}
	_ = json.Unmarshal(raw, &out)
	msg := flexMessage(raw)
	statusText := flexString(out.Status)

	if resp.StatusCode >= 400 {
		return nil, mapAPIError(resp.StatusCode, msg)
	}
	if !out.Success && statusText != "success" && statusText != "true" && !strings.Contains(strings.ToLower(msg), "antrean") {
		return nil, mapAPIError(resp.StatusCode, msg)
	}
	return &Result{Message: msg, Task: out.Task, VIP: out.VIP}, nil
}

func mapAPIError(status int, msg string) error {
	low := strings.ToLower(msg)
	switch {
	case status == http.StatusTooManyRequests,
		strings.Contains(low, "batas"), strings.Contains(low, "limit"),
		strings.Contains(low, "jatah"), strings.Contains(low, "kuota"):
		return fmt.Errorf("%w: %s", ErrLimit, msg)
	case strings.Contains(low, "captcha"):
		return fmt.Errorf("%w: %s", ErrCaptcha, msg)
	case strings.Contains(low, "token otentikasi"), strings.Contains(low, "sesi anonim"):
		return fmt.Errorf("%w: %s", ErrTokenRejected, msg)
	case strings.Contains(low, "url whatsapp channel"):
		return fmt.Errorf("%w: %s", ErrLink, msg)
	}
	return &APIError{Status: status, Message: msg}
}

// mint mencetak access token dari refresh token. Access token yang masih
// berlaku dipakai ulang supaya command beruntun tidak menembak securetoken
// terus-menerus.
func (c *Client) mint(ctx context.Context) (Account, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.access != "" && c.now().Add(2*time.Minute).Before(c.accessExp) {
		return c.account, c.access, nil
	}

	refresh, err := ReadRefreshToken()
	if err != nil {
		return Account{}, "", err
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refresh)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Account{}, "", fmt.Errorf("gagal menyiapkan request token: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", SiteOrigin)
	req.Header.Set("Referer", SiteOrigin+"/")

	resp, err := c.http.Do(req)
	if err != nil {
		return Account{}, "", fmt.Errorf("tidak bisa menghubungi Firebase: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return Account{}, "", fmt.Errorf("gagal membaca balasan Firebase: %w", err)
	}

	var out struct {
		IDToken      string  `json:"id_token"`
		RefreshToken string  `json:"refresh_token"`
		ExpiresIn    flexInt `json:"expires_in"`
		Error        *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &out)
	if resp.StatusCode != http.StatusOK || out.IDToken == "" {
		msg := flexMessage(raw)
		if out.Error != nil && out.Error.Message != "" {
			msg = out.Error.Message
		}
		if msg == "" {
			msg = "respons Firebase tidak dikenali"
		}
		if strings.Contains(strings.ToUpper(msg), "INVALID_REFRESH_TOKEN") ||
			strings.Contains(strings.ToUpper(msg), "TOKEN_EXPIRED") ||
			strings.Contains(strings.ToUpper(msg), "USER_DISABLED") ||
			strings.Contains(strings.ToUpper(msg), "INVALID_ID_TOKEN") {
			return Account{}, "", fmt.Errorf("%w: %s", ErrTokenRejected, msg)
		}
		return Account{}, "", fmt.Errorf("gagal mencetak access token: %s", msg)
	}

	acc, err := parseClaims(out.IDToken)
	if err != nil {
		return Account{}, "", err
	}
	if out.RefreshToken != "" && out.RefreshToken != refresh {
		// Firebase kadang merotasi refresh token; simpan yang baru supaya sesi
		// tidak mati pada pemakaian berikutnya.
		if werr := WriteRefreshToken(out.RefreshToken); werr != nil {
			return Account{}, "", fmt.Errorf("gagal menyimpan refresh token baru: %w", werr)
		}
	}

	ttl := time.Duration(int64(out.ExpiresIn)) * time.Second
	if ttl <= 0 {
		ttl = time.Until(acc.ExpiresAt)
	}
	if ttl <= 0 {
		ttl = time.Hour
	}
	c.access = out.IDToken
	c.accessExp = c.now().Add(ttl)
	c.account = acc
	return acc, c.access, nil
}

func parseClaims(idToken string) (Account, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) < 2 {
		return Account{}, errors.New("access token Firebase tidak valid")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Account{}, errors.New("gagal decode access token Firebase")
	}
	var cl struct {
		UID   string `json:"user_id"`
		Sub   string `json:"sub"`
		Email string `json:"email"`
		Exp   int64  `json:"exp"`
	}
	if err := json.Unmarshal(raw, &cl); err != nil {
		return Account{}, errors.New("klaim access token Firebase tidak terbaca")
	}
	uid := cl.UID
	if uid == "" {
		uid = cl.Sub
	}
	acc := Account{UID: uid, Email: cl.Email}
	if cl.Exp > 0 {
		acc.ExpiresAt = time.Unix(cl.Exp, 0)
	}
	return acc, nil
}

func sign(link, reaction, captcha, ts string) string {
	mac := hmac.New(sha256.New, []byte(secureSalt))
	mac.Write([]byte(link + ":" + reaction + ":" + captcha + ":" + ts))
	return hex.EncodeToString(mac.Sum(nil))
}

// flexMessage mengambil pesan manusia dari bentuk balasan yang bervariasi:
// {"error":"..."}, {"message":"..."}, {"error":{"message":"..."}}, dst.
func flexMessage(raw []byte) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return strings.TrimSpace(string(raw))
	}
	if m := pickMessage(v); m != "" {
		return m
	}
	return ""
}

func pickMessage(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case map[string]any:
		for _, key := range []string{"message", "error", "description"} {
			if m, ok := t[key]; ok {
				if s := pickMessage(m); s != "" {
					return s
				}
			}
		}
		if errs, ok := t["errors"].([]any); ok && len(errs) > 0 {
			return pickMessage(errs[0])
		}
	}
	return ""
}

// flexString membaca nilai JSON string maupun number sebagai teks.
func flexString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return strings.TrimSpace(string(raw))
}

// flexInt menerima angka JSON berbentuk number atau string ("3600").
type flexInt int

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return err
	}
	*f = flexInt(n)
	return nil
}

var linkRe = regexp.MustCompile(`(?i)^https?://(www\.)?whatsapp\.com/channel/[^/\s]+/\d+$`)

// NormalizeLink membersihkan query/hash/trailing slash lalu memastikan
// bentuknya link PESAN channel (ada nomor pesan di belakang), bukan link
// profil channel.
func NormalizeLink(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	s = strings.SplitN(s, "?", 2)[0]
	s = strings.SplitN(s, "#", 2)[0]
	s = strings.TrimRight(s, "/")
	if !linkRe.MatchString(s) {
		return "", fmt.Errorf("%w: contoh https://whatsapp.com/channel/xxxx/141", ErrLink)
	}
	return s, nil
}

// SplitEmojis memecah argumen reaksi (dipisah spasi dan/atau koma) dan
// membuang token yang tidak memuat simbol sama sekali. Emoji berurutan tanpa
// pemisah tetap dihitung satu token supaya emoji gabungan (mis. 👍🏽) tidak
// terbelah.
func SplitEmojis(args []string) []string {
	fields := strings.FieldsFunc(strings.Join(args, " "), func(r rune) bool {
		return r == ',' || unicode.IsSpace(r)
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if hasSymbol(f) {
			out = append(out, f)
		}
	}
	return out
}

func hasSymbol(s string) bool {
	for _, r := range s {
		if r > unicode.MaxASCII {
			return true
		}
	}
	return false
}
