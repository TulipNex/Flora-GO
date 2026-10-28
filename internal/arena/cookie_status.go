package arena

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// cookie_status.go — membaca status sesi arena.ai yang tersimpan tanpa pernah
// menampilkan isi token. Dipakai .seedream cookie tanpa argumen supaya owner tahu
// kapan perlu mengirim cookie baru (access token ~1 jam, refresh token
// berminggu-minggu).
//
// Bentuk cookie: Supabase SSR menyimpan sesi di cookie arena-auth-prod-v1
// yang dipecah menjadi .0, .1, ... Nilainya base64url dari JSON
// {"access_token":...,"refresh_token":...,"expires_at":...} dengan awalan
// "base64-". Versi lama menyimpan JSON apa adanya.

// CookieStatus ringkasan sesi arena.ai yang tersimpan.
type CookieStatus struct {
	Present    bool
	Entries    int
	Age        time.Duration
	ExpiresAt  time.Time
	HasRefresh bool
	Parsed     bool
}

// Expired true kalau access token sudah lewat masa berlaku.
func (s CookieStatus) Expired() bool {
	return !s.ExpiresAt.IsZero() && time.Now().After(s.ExpiresAt)
}

// CookieStatusOf membaca file cookie dan mengekstrak masa berlaku sesi.
func CookieStatusOf() (CookieStatus, error) {
	info := CookieStatus{}
	st, err := os.Stat(cookieFile)
	if err != nil {
		if os.IsNotExist(err) {
			return info, fmt.Errorf("%w: setel sekali dengan .seedream cookie <cookie>", ErrNoCookie)
		}
		return info, fmt.Errorf("gagal baca %s: %w", cookieFile, err)
	}
	info.Present = true
	info.Age = time.Since(st.ModTime())

	raw, err := os.ReadFile(cookieFile)
	if err != nil {
		return info, fmt.Errorf("gagal baca %s: %w", cookieFile, err)
	}
	header := normalizeCookie(string(raw))
	info.Entries = countCookieEntries(header)

	session, ok := parseSessionCookie(header)
	if !ok {
		return info, nil
	}
	info.Parsed = true
	info.ExpiresAt = session.expiresAt()
	info.HasRefresh = session.RefreshToken != ""
	return info, nil
}

// countCookieEntries menghitung pasangan nama=nilai di header cookie.
func countCookieEntries(header string) int {
	n := 0
	for _, part := range strings.Split(header, ";") {
		if strings.Contains(part, "=") {
			n++
		}
	}
	return n
}

// sessionCookie adalah bagian sesi Supabase yang relevan.
type sessionCookie struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"`
	ExpiresIn    int64  `json:"expires_in"`
}

// expiresAt menentukan waktu kedaluwarsa: field expires_at diutamakan, kalau
// tidak ada dipakai klaim exp di dalam JWT access token.
func (s sessionCookie) expiresAt() time.Time {
	if s.ExpiresAt > 0 {
		return time.Unix(s.ExpiresAt, 0)
	}
	if exp, ok := jwtExpiry(s.AccessToken); ok {
		return exp
	}
	return time.Time{}
}

// jwtExpiry membaca klaim exp dari JWT tanpa verifikasi tanda tangan (tidak
// perlu: kita hanya menampilkan waktu, bukan mempercayai isinya).
func jwtExpiry(token string) (time.Time, bool) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return time.Time{}, false
	}
	payload, err := decodeBase64URL(parts[1])
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == 0 {
		return time.Time{}, false
	}
	return time.Unix(claims.Exp, 0), true
}

// parseSessionCookie menyatukan potongan cookie arena-auth-prod-v1 lalu
// meng-decode sesinya. Best-effort: format bisa berubah, jadi kegagalan
// dianggap "tidak terbaca" (bukan error fatal).
func parseSessionCookie(header string) (sessionCookie, bool) {
	values := make(map[string]string)
	for _, part := range strings.Split(header, ";") {
		part = strings.TrimSpace(part)
		i := strings.Index(part, "=")
		if i <= 0 {
			continue
		}
		values[strings.TrimSpace(part[:i])] = part[i+1:]
	}

	// Kumpulkan potongan berdasarkan indeks numerik di belakang nama.
	type chunk struct {
		idx int
		val string
	}
	var chunks []chunk
	for name, val := range values {
		if !strings.HasPrefix(name, "arena-auth-prod-v1") {
			continue
		}
		idx := 0
		if dot := strings.LastIndex(name, "."); dot >= 0 {
			if n, err := strconv.Atoi(name[dot+1:]); err == nil {
				idx = n
			}
		}
		chunks = append(chunks, chunk{idx: idx, val: val})
	}
	if len(chunks) == 0 {
		return sessionCookie{}, false
	}
	sort.Slice(chunks, func(i, j int) bool { return chunks[i].idx < chunks[j].idx })
	var sb strings.Builder
	for _, c := range chunks {
		sb.WriteString(c.val)
	}
	joined := sb.String()

	for _, candidate := range sessionCandidates(joined) {
		var s sessionCookie
		if err := json.Unmarshal([]byte(candidate), &s); err == nil && s.AccessToken != "" {
			return s, true
		}
	}
	return sessionCookie{}, false
}

// sessionCandidates mengembalikan kemungkinan bentuk JSON sesi dari nilai
// cookie mentah (base64-..., base64url tanpa awalan, atau JSON langsung).
func sessionCandidates(raw string) []string {
	out := []string{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		s = strings.Trim(s, `"`)
		if s != "" {
			out = append(out, s)
		}
	}

	add(raw)
	if v, err := percentDecode(raw); err == nil {
		add(v)
	}
	base := raw
	if strings.HasPrefix(base, "base64-") {
		base = strings.TrimPrefix(base, "base64-")
	}
	if decoded, err := decodeBase64URL(base); err == nil {
		add(string(decoded))
	}
	return out
}

// percentDecode memakai url.QueryUnescape; nilai cookie jarang di-encode,
// tapi sebagian browser/stack melakukannya.
func percentDecode(s string) (string, error) {
	if !strings.Contains(s, "%") {
		return s, nil
	}
	return url.QueryUnescape(s)
}

// decodeBase64URL menerima base64url dengan atau tanpa padding.
func decodeBase64URL(s string) ([]byte, error) {
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.URLEncoding.DecodeString(s)
}
