package scraper

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// FetchYouTube mengambil video/audio YouTube via savetube-direct (port dari
// script Vamz Spectre untuk yttmp3.com.co — credit dipertahankan di bawah).
// Alur: random-cdn → /v2/info (payload terenkripsi) → pilih format → URL
// googlevideo langsung bila ada, else /download dengan key. audioOnly=true
// memilih MP3 (dipakai .youtubemp3 / .yt --mp3), selain itu MP4 720p.
//
// Credit: Author Vamz Spectre, WhatsApp
// https://whatsapp.com/channel/0029Vb8n36OGE56uvRAq4y3y, base yttmp3.com.co.
// uploadToVamxCdn dari script asli SENGAJA tidak di-port (mirror pihak
// ketiga tak diperlukan — kita unduh langsung).
//
// y2mate SUDAH DIHAPUS (keputusan owner): lebih lambat (polling), dan
// savetube-direct terverifikasi 4/4 live (cdn, info, dekripsi, download).
func FetchYouTube(ctx context.Context, pageURL string, audioOnly bool) (*ScrapeResult, error) {
	const engine = "savetube"
	if extractYouTubeID(pageURL) == "" {
		return nil, fmt.Errorf("%s: ID video tidak dikenali", engine)
	}

	tctx, cancel := context.WithTimeout(ctx, 150*time.Second)
	defer cancel()

	// Client 60 detik per request (bukan 20 detik API): endpoint /download
	// menyiapkan file di server sebelum header keluar — terbukti >20 detik
	// dari panel (timeout menunggu headers, Sep 2026).
	cli60 := &http.Client{Timeout: 60 * time.Second}
	ua := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	call := func(method, url, body string) ([]byte, error) {
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req, err := http.NewRequestWithContext(tctx, method, url, reader)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", ua)
		req.Header.Set("Origin", "https://yttmp3.com.co")
		req.Header.Set("Referer", "https://yttmp3.com.co/")
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := cli60.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		return raw, nil
	}
	refreshCDN := func() string {
		if raw, err := call(http.MethodGet, "https://media.savetube.vip/api/random-cdn", ""); err == nil {
			var rc struct {
				CDN string `json:"cdn"`
			}
			if json.Unmarshal(raw, &rc) == nil && strings.TrimSpace(rc.CDN) != "" {
				return strings.TrimSpace(rc.CDN)
			}
		}
		return "cdn401.savetube.vip"
	}

	// 1. CDN acak (fallback hardcode bila gagal).
	cdn := refreshCDN()
	// downloadWithRetry memanggil /download; bila TIMEOUT, CDN di-refresh
	// (host lambat diganti, bukan diulang) lalu coba sekali lagi.
	downloadWithRetry := func(typ, quality, direct, slug, key string) (string, error) {
		dl, err := savetubeDownload(call, cdn, key, typ, quality, direct, slug)
		if err == nil {
			return dl, nil
		}
		if !isTimeoutErr(err) {
			return "", err
		}
		fmt.Printf("⚠️ savetube: /download timeout di %s, coba CDN lain.\n", cdn)
		cdn = refreshCDN()
		return savetubeDownload(call, cdn, key, typ, quality, direct, slug)
	}

	// 2. Info + dekripsi payload.
	rawInfo, err := call(http.MethodPost, "https://"+cdn+"/v2/info", `{"url":`+jsonString(pageURL)+`}`)
	if err != nil {
		return nil, fmt.Errorf("%s: info: %w", engine, err)
	}
	var infoResp struct {
		Status  bool   `json:"status"`
		Message string `json:"message"`
		Data    string `json:"data"`
	}
	if err := json.Unmarshal(rawInfo, &infoResp); err != nil {
		return nil, fmt.Errorf("%s: info invalid: %w", engine, err)
	}
	if !infoResp.Status || infoResp.Data == "" {
		return nil, fmt.Errorf("%s: %s", engine, firstNonEmpty(infoResp.Message, "info gagal"))
	}
	meta, err := decryptSavetube(infoResp.Data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", engine, err)
	}

	// 3. Pilih format + dapatkan URL unduhan.
	if audioOnly {
		dl, err := downloadWithRetry("audio", "128", "", meta.TitleSlug, meta.Key)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", engine, err)
		}
		return &ScrapeResult{
			Title:  meta.Title,
			Items:  []ScrapeItem{{URL: dl, Thumb: meta.Thumbnail, Kind: KindAudio}},
			Source: "savetube",
		}, nil
	}
	// Video: preferensi 720p; URL googlevideo langsung bila cocok.
	quality := "720"
	direct := ""
	for _, f := range meta.VideoFormats {
		if fmt.Sprint(f.Height) == quality || fmt.Sprint(f.Quality) == quality {
			if strings.TrimSpace(f.URL) != "" {
				direct = f.URL
				break
			}
		}
	}
	dl, err := downloadWithRetry("video", quality, direct, meta.TitleSlug, meta.Key)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", engine, err)
	}
	return &ScrapeResult{
		Title:  meta.Title,
		Items:  []ScrapeItem{{URL: dl, Thumb: meta.Thumbnail, Kind: KindVideo}},
		Source: "savetube",
	}, nil
}

// savetubeMeta adalah hasil dekripsi /v2/info.
type savetubeMeta struct {
	ID           string `json:"id"`
	Key          string `json:"key"`
	Title        string `json:"title"`
	TitleSlug    string `json:"titleSlug"`
	Thumbnail    string `json:"thumbnail"`
	Duration     any    `json:"duration"`
	AudioFormats []struct {
		Quality any    `json:"quality"`
		URL     string `json:"url"`
	} `json:"audio_formats"`
	VideoFormats []struct {
		Height  any    `json:"height"`
		Width   any    `json:"width"`
		URL     string `json:"url"`
		Quality any    `json:"quality"`
	} `json:"video_formats"`
}

// savetubeAESKey adalah kunci AES-128-CBC dari bundle Next.js yttmp3.com.co
// (lihat script asal). Bisa dirotasi pemiliknya sewaktu-waktu — bila
// dekripsi gagal, engine error dan rantai fallback (omnify) jalan.
var savetubeAESKey = []byte{0xC5, 0xD5, 0x8E, 0xF6, 0x7A, 0x75, 0x84, 0xE4, 0xA2, 0x9F, 0x6C, 0x35, 0xBB, 0xC4, 0xEB, 0x12}

// decryptSavetube mendekripsi payload base64 /v2/info: 16 byte pertama = IV,
// sisanya ciphertext AES-128-CBC + unpad PKCS#7.
func decryptSavetube(b64 string) (*savetubeMeta, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(b64, " ", ""))
	if err != nil {
		return nil, fmt.Errorf("base64: %w", err)
	}
	if len(raw) < aes.BlockSize*2 || len(raw)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("payload pendek/rusak")
	}
	block, err := aes.NewCipher(savetubeAESKey)
	if err != nil {
		return nil, err
	}
	plain := make([]byte, len(raw)-aes.BlockSize)
	cipher.NewCBCDecrypter(block, raw[:aes.BlockSize]).CryptBlocks(plain, raw[aes.BlockSize:])
	n := len(plain)
	pad := int(plain[n-1])
	if pad <= 0 || pad > aes.BlockSize || pad > n {
		return nil, fmt.Errorf("padding PKCS#7 invalid")
	}
	for _, v := range plain[n-pad:] {
		if int(v) != pad {
			return nil, fmt.Errorf("padding PKCS#7 invalid")
		}
	}
	var meta savetubeMeta
	if err := json.Unmarshal(bytes.TrimSpace(plain[:n-pad]), &meta); err != nil {
		return nil, fmt.Errorf("JSON meta invalid: %w", err)
	}
	if meta.Key == "" {
		return nil, fmt.Errorf("meta tanpa key")
	}
	return &meta, nil
}

// savetubeDownload mengambil URL unduhan final: URL googlevideo langsung
// bila tersedia (video saja), else POST /download dengan key.
func savetubeDownload(call func(method, url, body string) ([]byte, error), cdn, key, typ, quality, direct, slug string) (string, error) {
	if direct != "" && typ != "audio" {
		title := firstNonEmpty(slug, "video")
		return direct + "&title=" + title + "-ytshorts.savetube.vip", nil
	}
	downloadType := typ
	if typ == "audio" || quality == "128" {
		downloadType = "audio"
	}
	body := `{"downloadType":` + jsonString(downloadType) +
		`,"quality":` + jsonString(quality) +
		`,"key":` + jsonString(key) + `}`
	raw, err := call("POST", "https://"+cdn+"/download", body)
	if err != nil {
		return "", err
	}
	var r struct {
		Status  bool   `json:"status"`
		Message string `json:"message"`
		Data    *struct {
			DownloadURL string `json:"downloadUrl"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", fmt.Errorf("respons invalid: %w", err)
	}
	if !r.Status || r.Data == nil || strings.TrimSpace(r.Data.DownloadURL) == "" {
		return "", fmt.Errorf("%s", firstNonEmpty(r.Message, "download link gagal"))
	}
	return strings.TrimSpace(r.Data.DownloadURL), nil
}

// isTimeoutErr true untuk error timeout jaringan/konteks (layak retry ke
// host lain; error lain seperti HTTP/JSON tidak akan sembuh dengan retry).
func isTimeoutErr(err error) bool {
	if err == nil {
		return false
	}
	var nerr interface{ Timeout() bool }
	if errors.As(err, &nerr) && nerr.Timeout() {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "context deadline exceeded") ||
		strings.Contains(msg, "Client.Timeout")
}

// ytIDRe mengekstrak ID video 11-char dari youtu.be/ID, v=ID, /shorts/ID,
// /live/ID, /embed/ID.
var ytIDRe = regexp.MustCompile(`(?:youtu\.be/|youtube\.com/(?:watch\?.*v=|shorts/|live/|embed/))([A-Za-z0-9_-]{11})`)

func extractYouTubeID(rawURL string) string {
	if m := ytIDRe.FindStringSubmatch(rawURL); m != nil {
		return m[1]
	}
	return ""
}
