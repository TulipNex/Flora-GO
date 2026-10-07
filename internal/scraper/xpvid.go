package scraper

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

// Catatan desain (keputusan owner): TIDAK ada whitelist host — engine
// resolve /(e|d)/{id} generik ini dicoba ke host apa pun. Satu-satunya
// syarat adalah pola path embed; selebihnya kegagalan resolve dilaporkan
// jujur per tahap ("media tidak ditemukan", pola berubah, dst).
// Tameng SSRF (checkPublicURL) tetap berlaku di tiap fetch dan tidak bisa
// dimatikan — itu urusan keamanan, bukan batasan situs.

// Pola URL embed: https://host-apa-saja/e/xxx atau /d/xxx.
// Path huruf kedua tidak dipakai resolver (sama saja), tapi tetap
// divalidasi supaya link salah bentuk ditolak tegas, bukan di-fetch buta.
var xpvidPathRe = regexp.MustCompile(`^/([ed])/([^/?#\s]+)`)

// Pola ekstraksi 3 langkah (persis scraper JS aslinya) + 2 fallback untuk
// varian vidovr.com (langkah-1 tanpa var iframeId/embedToken — URL
// /ip129jk tertanam langsung di videq_iframe.src; langkah-3 bisa berupa
// video langsung yang dikenali dari content-type).
var (
	xpvidIframeIDRe   = regexp.MustCompile(`var iframeId = '([^']+)'`)
	xpvidEmbedTokenRe = regexp.MustCompile(`var embedToken = '([^']+)'`)
	xpvidPlayerPathRe = regexp.MustCompile(`const playerPath\s*=\s*"([^"]+)"`)
	xpvidSourceRe     = regexp.MustCompile(`<source src="(https://[^"]+)"`)
	xpvidTitleRe      = regexp.MustCompile(`<title>([^<]+)</title>`)
	// Fallback langkah-1 (varian vidovr/jwplyr/dll): URL /ip129jk tertanam
	// langsung di halaman dalam bentuk apa pun (videq_iframe.src = '...',
	// var SRC = "...", dst.) — cocokkan pola URL-nya, bukan pembungkusnya,
	// supaya varian berikutnya ikut tertangani tanpa regex baru.
	xpvidIframeSrcRe = regexp.MustCompile(`/ip129jk\?id=([^&'"]+)&t=([^'"]+)`)
)

// XpvidInfo adalah hasil resolve: judul + URL video langsung + apakah HLS
// + Referer halaman asal (wajib dikirim saat unduh media: CDN overfetch
// proteksi hotlink, tanpa Referer yang benar dibalas 403).
type XpvidInfo struct {
	Title    string
	VideoURL string
	IsHLS    bool
	Referer  string
}

// IsXpvidURL true kalau URL berpola link embed /(e|d)/{id} pada host
// apa pun (dipakai validasi awal .bk sebelum fetch).
func IsXpvidURL(rawURL string) bool {
	domain, _, id, ok := ParseXpvidURL(rawURL)
	return ok && domain != "" && id != ""
}

// ParseXpvidURL memecah link embed jadi (domain, path, id). ok=false kalau
// polanya bukan /(e|d)/{id} — host apa pun diterima (tanpa whitelist).
func ParseXpvidURL(rawURL string) (domain, path, id string, ok bool) {
	rest := strings.TrimSpace(rawURL)
	rest = strings.TrimPrefix(strings.TrimPrefix(rest, "https://"), "http://")
	slash := strings.Index(rest, "/")
	if slash < 0 {
		return "", "", "", false
	}
	host := strings.ToLower(rest[:slash])
	if host == "" {
		return "", "", "", false
	}
	m := xpvidPathRe.FindStringSubmatch(rest[slash:])
	if m == nil {
		return "", "", "", false
	}
	return host, m[1], m[2], true
}

// FetchXpvid me-resolve link embed xpvid/vdy jadi URL video langsung.
// Tiap URL yang di-fetch lewat checkPublicURL dulu (tameng SSRF, pola
// downloadBytes) — server tidak boleh disuruh fetch URL internal.
// Error berupa pesan Indonesia siap-balas ke user.
func FetchXpvid(ctx context.Context, rawURL string) (*XpvidInfo, error) {
	domain, path, id, ok := ParseXpvidURL(rawURL)
	if !ok {
		return nil, fmt.Errorf("link tidak valid. Contoh: .get https://xpvid.cc/e/xxx")
	}

	// Langkah 1: halaman embed → iframeId + embedToken. Varian vidovr
	// tidak punya var tersebut — URL /ip129jk tertanam langsung di
	// videq_iframe.src; pakai itu sebagai fallback (langkah 2+3 identik).
	pageURL := "https://" + domain + "/" + path + "/" + id
	h1, err := xpvidGetText(ctx, pageURL, domain)
	if err != nil {
		return nil, fmt.Errorf("gagal buka halaman video: %v", err)
	}
	iframeID := firstSubmatch(xpvidIframeIDRe, h1)
	embedToken := firstSubmatch(xpvidEmbedTokenRe, h1)
	embedTitle := strings.TrimSpace(firstSubmatch(xpvidTitleRe, h1))
	if iframeID == "" || embedToken == "" {
		if m := xpvidIframeSrcRe.FindStringSubmatch(h1); m != nil {
			iframeID, embedToken = m[1], m[2]
		}
	}
	if iframeID == "" || embedToken == "" {
		return nil, fmt.Errorf("pola situs berubah (token tidak ditemukan), lapor ke owner")
	}

	// Langkah 2: endpoint iframe → playerPath.
	iframeURL := "https://" + domain + "/ip129jk?id=" + iframeID + "&t=" + embedToken
	h2, err := xpvidGetTextIframed(ctx, iframeURL, pageURL)
	if err != nil {
		return nil, fmt.Errorf("gagal buka player: %v", err)
	}
	playerPath := firstSubmatch(xpvidPlayerPathRe, h2)
	if playerPath == "" {
		return nil, fmt.Errorf("pola situs berubah (player tidak ditemukan), lapor ke owner")
	}
	playerPath = strings.ReplaceAll(playerPath, `\u0026`, "&")

	// Langkah 3: halaman player → <source> + <title>. Fallback vidovr:
	// playerPath bisa JADI videonya langsung (content-type video/*) —
	// dipakai apa adanya. Judul fallback dari halaman embed bila kosong.
	h3, h3ct, err := xpvidGetTextCT(ctx, playerPath, domain)
	if err != nil {
		return nil, fmt.Errorf("gagal baca sumber video: %v", err)
	}
	videoURL := firstSubmatch(xpvidSourceRe, h3)
	if videoURL == "" && strings.HasPrefix(h3ct, "video/") {
		videoURL = playerPath
	}
	if videoURL == "" {
		return nil, fmt.Errorf("URL video tidak ditemukan (kemungkinan video dihapus/private)")
	}
	title := firstSubmatch(xpvidTitleRe, h3)
	if strings.TrimSpace(title) == "" {
		title = embedTitle
	}
	if strings.TrimSpace(title) == "" {
		title = id
	}

	videoURL = sanitizeXpvidURL(videoURL)

	// Fallback CDN: mp4-05 sering mati → coba mp4-01..04 + meiva via HEAD.
	if host := cdnHost(videoURL); host == "mp4-05" {
		for _, alt := range []string{"mp4-01", "mp4-02", "mp4-03", "mp4-04", "meiva"} {
			altURL := strings.Replace(videoURL, "mp4-05.overfetch.video", alt+".overfetch.video", 1)
			if xpvidHeadOK(ctx, altURL, domain) {
				videoURL = altURL
				break
			}
		}
	}

	return &XpvidInfo{
		Title:    strings.TrimSpace(title),
		VideoURL: videoURL,
		IsHLS:    strings.Contains(strings.ToLower(videoURL), ".m3u8"),
		Referer:  "https://" + domain + "/",
	}, nil
}

// sanitizeXpvidURL merapikan URL hasil regex: trim tepi, entity umum, dan
// spasi → %20. URL berspasi mentah terpotong saat diklik di WhatsApp
// (klien berhenti di spasi pertama) dan rawan gagal di HTTP client —
// versi %20 menunjuk file yang sama di CDN dan dipakai di fetch maupun
// balasan info ke user.
func sanitizeXpvidURL(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, " ", "%20")
	return s
}

// cdnHost mengambil subdomain overfetch ("mp4-05" dari
// https://mp4-05.overfetch.video/...), "" kalau bukan overfetch.
func cdnHost(rawURL string) string {
	rest := strings.TrimPrefix(strings.TrimPrefix(rawURL, "https://"), "http://")
	dot := strings.Index(rest, ".overfetch.video/")
	if dot < 0 {
		return ""
	}
	return rest[:dot]
}

// xpvidGetText GET halaman HTML (embed/player) dengan UA + Referer.
func xpvidGetText(ctx context.Context, pageURL, domain string) (string, error) {
	body, _, err := xpvidGetTextCT(ctx, pageURL, domain)
	return body, err
}

// xpvidGetTextCT varian yang juga mengembalikan content-type respons
// (dipakai langkah-3 mendeteksi video langsung).
func xpvidGetTextCT(ctx context.Context, pageURL, domain string) (string, string, error) {
	if err := checkPublicURL(ctx, pageURL); err != nil {
		return "", "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", scraperUA)
	req.Header.Set("Referer", "https://"+domain+"/")
	resp, err := httpClient.Do(req)
	if err != nil {
		// Detail error asli DISERTAKAN (bukan "tidak merespons" polos):
		// pemanggil (hint blokir di getxpvid.go) butuh melihat apakah ini
		// timeout, DNS, atau TLS — ketiganya menuntut respons berbeda.
		return "", "", fmt.Errorf("tidak merespons (%v)", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	// Jangan unduh badan video raksasa saat sniffing: header cukup untuk
	// klasifikasi video/* (isi tak dipakai pada jalur itu).
	if i := strings.Index(ct, ";"); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	if strings.HasPrefix(strings.ToLower(ct), "video/") {
		return "", ct, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", "", fmt.Errorf("gagal baca halaman")
	}
	return string(body), ct, nil
}

// xpvidGetTextIframed varian langkah-2 dengan header iframe browser
// (Sec-Fetch-*, pola scraper JS aslinya).
func xpvidGetTextIframed(ctx context.Context, iframeURL, referer string) (string, error) {
	if err := checkPublicURL(ctx, iframeURL); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, iframeURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", scraperUA)
	req.Header.Set("Referer", referer)
	req.Header.Set("Sec-Fetch-Dest", "iframe")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("tidak merespons (%v)", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", fmt.Errorf("gagal baca halaman")
	}
	return string(body), nil
}

// xpvidHeadOK mengecek URL CDN alternatif via HEAD (tanpa download).
func xpvidHeadOK(ctx context.Context, rawURL, domain string) bool {
	if err := checkPublicURL(ctx, rawURL); err != nil {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, rawURL, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", scraperUA)
	req.Header.Set("Referer", "https://"+domain+"/")
	resp, err := httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// firstSubmatch mengembalikan grup-1 regex, "" kalau tidak cocok.
func firstSubmatch(re *regexp.Regexp, s string) string {
	if m := re.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}
