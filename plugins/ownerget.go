package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"image"
	"io"
	"mime"
	"net"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"wabot-go/internal/messaging"
	"wabot-go/internal/store"

	fhttp "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func init() {
	registerPlugin(RegisterOwnerGet)
}

// Batasan .get — mirror versi acuan (100MB maks, >10MB jadi dokumen),
// plus batas teks balasan supaya tidak melebihi limit pesan WhatsApp.
const (
	getMaxFileSize  = 100 << 20
	getDocThreshold = 10 << 20
	getTimeoutSec   = 60
	maxGetTextChars = 60000
)

// RegisterOwnerGet mendaftarkan command `.get <url> [browser]` (owner):
// fetch HTTP dengan sidik TLS browser (lolos dari server yang memblokir
// fingerprint Go standar), lalu kirim hasilnya sebagai media/dokumen/teks
// sesuai tipe kontennya. Fetch bisa lama (sampai 60 detik) sehingga
// dijalankan async — balasan dikirim saat selesai, event loop tidak
// terblokir.
func RegisterOwnerGet(r *Registry) {
	r.Register(Command{
		Name:        "get",
		Description: "Ambil URL dan kirim isinya",
		Category:    CategoryAlat,
		Handler:     handleGet,
	})
}

func handleGet(ctx context.Context, mctx *store.MessageContext) error {
	if len(mctx.Args) == 0 {
		return store.ReplyText(ctx, mctx, "Format: .get <url> [browser]\nBrowser: chrome, firefox, safari, edge. Contoh: .get https://api.example.com/data")
	}

	targetURL := mctx.Args[0]
	browser := "chrome"
	if len(mctx.Args) > 1 {
		browser = strings.ToLower(mctx.Args[1])
	}
	if !strings.HasPrefix(targetURL, "http://") && !strings.HasPrefix(targetURL, "https://") {
		targetURL = "https://" + targetURL
	}
	parsedURL, err := url.Parse(targetURL)
	if err != nil || parsedURL.Host == "" {
		return store.ReplyText(ctx, mctx, "URL tidak valid.")
	}

	profile, ua, ok := getBrowserProfile(browser)
	if !ok {
		return store.ReplyText(ctx, mctx, "Browser tidak dikenal. Pilih: chrome, firefox, safari, edge.")
	}

	go func() {
		bg := context.Background()
		if rerr := fetchAndSend(bg, mctx, parsedURL, profile, ua, browser); rerr != nil {
			_ = store.ReplyText(bg, mctx, rerr.Error())
		}
	}()
	return nil
}

// getBrowserProfile memetakan nama browser ke profil TLS + User-Agent.
// Edge berbasis Chromium sehingga memakai profil Chrome (tidak ada profil
// Edge khusus di versi tls-client ini).
func getBrowserProfile(browser string) (profiles.ClientProfile, string, bool) {
	switch browser {
	case "chrome", "edge":
		return profiles.Chrome_152, "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/152.0.0.0 Safari/537.36", true
	case "firefox":
		return profiles.Firefox_148, "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:148.0) Gecko/20100101 Firefox/148.0", true
	case "safari":
		return profiles.Safari_16_0, "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.0 Safari/605.1.15", true
	default:
		return profiles.ClientProfile{}, "", false
	}
}

// isPublicGetURL memastikan URL fetch hanya ke host publik. WAJIB karena
// .get boleh dipakai semua user: tanpa ini siapa saja bisa menyuruh server
// mengakses URL internal (metadata cloud, localhost, LAN) alias SSRF.
// IP literal dicek langsung; hostname di-resolve dan SEMUA hasilnya harus
// publik. Redirect ikut dijaga via redirectGuard di bawah.
func isPublicGetURL(ctx context.Context, rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("URL tidak valid.")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("skema URL harus http/https.")
	}
	host := parsed.Host
	if h, _, herr := net.SplitHostPort(host); herr == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if ip := net.ParseIP(host); ip != nil {
		if !isPublicIP(ip) {
			return fmt.Errorf("host internal/private tidak diizinkan.")
		}
		return nil
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(addrs) == 0 {
		return fmt.Errorf("host tidak bisa di-resolve.")
	}
	for _, a := range addrs {
		if !isPublicIP(a.IP) {
			return fmt.Errorf("host mengarah ke alamat internal/private.")
		}
	}
	return nil
}

func isPublicIP(ip net.IP) bool {
	return !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() &&
		!ip.IsLinkLocalMulticast() && !ip.IsUnspecified() && !ip.IsMulticast()
}

// redirectGuard menolak redirect ke host non-publik (Celah SSRF via
// redirect: URL awal lolos validasi tapi redirect ke 169.254.x / localhost).
// Tipe request mengikuti tanda tangan WithCustomRedirectFunc (fhttp, bukan
// net/http standar).
func redirectGuard(req *fhttp.Request, via []*fhttp.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("redirect berlapis terlalu banyak (maks 10)")
	}
	if err := isPublicGetURL(req.Context(), req.URL.String()); err != nil {
		return fmt.Errorf("redirect diblokir: %v", err)
	}
	return nil
}

// fetchAndSend men-fetch URL lalu mengirim hasilnya sesuai tipe konten.
// Mengembalikan error yang SUDAH berupa pesan siap-balas ke user.
func fetchAndSend(ctx context.Context, mctx *store.MessageContext, parsedURL *url.URL, profile profiles.ClientProfile, ua, browser string) error {
	if err := isPublicGetURL(ctx, parsedURL.String()); err != nil {
		return err
	}
	client, err := tls_client.NewHttpClient(tls_client.NewNoopLogger(),
		tls_client.WithTimeoutSeconds(getTimeoutSec),
		tls_client.WithClientProfile(profile),
		tls_client.WithCookieJar(tls_client.NewCookieJar()),
		tls_client.WithCustomRedirectFunc(redirectGuard),
	)
	if err != nil {
		return fmt.Errorf("gagal siapkan client: %v", err)
	}

	req, err := fhttp.NewRequest(fhttp.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return fmt.Errorf("gagal buat request: %v", err)
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "*/*")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request gagal: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("server menjawab HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, getMaxFileSize+1))
	if err != nil {
		return fmt.Errorf("gagal baca response: %v", err)
	}
	if len(body) > getMaxFileSize {
		return fmt.Errorf("file terlalu besar (>100MB).")
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	mediaType, _, _ := mime.ParseMediaType(contentType)
	filename := getFilename(parsedURL, mediaType)

	// File besar (>10MB) langsung jadi dokumen apa pun tipenya.
	if len(body) > getDocThreshold {
		return sendGetDocument(ctx, mctx, body, contentType, filename)
	}

	switch {
	case strings.HasPrefix(mediaType, "image/"):
		return sendGetMedia(ctx, mctx, body, mediaType, whatsmeow.MediaImage, filename, "gambar")
	case strings.HasPrefix(mediaType, "video/"):
		return sendGetMedia(ctx, mctx, body, mediaType, whatsmeow.MediaVideo, filename, "video")
	case strings.HasPrefix(mediaType, "audio/"):
		return sendGetMedia(ctx, mctx, body, mediaType, whatsmeow.MediaAudio, filename, "audio")
	case strings.Contains(mediaType, "json"):
		pretty, perr := prettyJSONBody(body)
		if perr != nil {
			pretty = truncateGetText(string(body))
		}
		return store.ReplyText(ctx, mctx, pretty)
	case strings.Contains(mediaType, "pdf"),
		strings.Contains(mediaType, "zip"),
		strings.Contains(mediaType, "rar"),
		strings.Contains(mediaType, "7z"),
		strings.Contains(mediaType, "msword"),
		strings.Contains(mediaType, "officedocument"),
		strings.Contains(mediaType, "android.package-archive"),
		strings.Contains(mediaType, "octet-stream"):
		return sendGetDocument(ctx, mctx, body, contentType, filename)
	case strings.Contains(mediaType, "html"):
		return store.ReplyText(ctx, mctx, truncateGetText(stripHTMLTags(string(body))))
	case strings.Contains(mediaType, "xml"), strings.Contains(mediaType, "text"):
		return store.ReplyText(ctx, mctx, truncateGetText(string(body)))
	default:
		return sendGetDocument(ctx, mctx, body, contentType, filename)
	}
}

// getFilename menentukan nama file dokumen dari path URL, fallback ke
// "file" + ekstensi tebakan dari tipe konten.
func getFilename(parsedURL *url.URL, mediaType string) string {
	if base := path.Base(parsedURL.Path); base != "" && base != "/" && base != "." {
		return base
	}
	ext := map[string]string{
		"application/pdf": "pdf", "application/zip": "zip",
		"image/jpeg": "jpg", "image/png": "png", "video/mp4": "mp4",
		"audio/mpeg": "mp3", "text/plain": "txt", "text/csv": "csv",
	}[mediaType]
	if ext == "" {
		ext = "bin"
	}
	return "file." + ext
}

// prettyJSONBody merapikan JSON untuk dibaca di chat.
func prettyJSONBody(body []byte) (string, error) {
	var v interface{}
	if err := json.Unmarshal(body, &v); err != nil {
		return "", err
	}
	pretty, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return truncateGetText(string(pretty)), nil
}

var htmlTagRe = regexp.MustCompile(`(?s)<[^>]*>`)

// stripHTMLTags membuang tag HTML + decode entity + padatkan whitespace,
// supaya halaman web terbaca sebagai teks di chat.
func stripHTMLTags(raw string) string {
	text := htmlTagRe.ReplaceAllString(raw, " ")
	text = html.UnescapeString(text)
	return strings.Join(strings.Fields(text), " ")
}

// truncateGetText memotong teks balasan ke batas WA + penanda jujur.
func truncateGetText(text string) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= maxGetTextChars {
		return string(runes)
	}
	return string(runes[:maxGetTextChars]) + fmt.Sprintf("\n...(dipotong, total %d karakter)", len(runes))
}

// sendGetMedia mengupload + mengirim gambar/video/audio hasil fetch.
// Dimensi diisi kalau bisa dibaca (jpeg/png, decoder sudah terdaftar di
// package plugins via sticker.go) — gagal baca dimensi = kirim tanpa
// dimensi, bukan gagal total.
func sendGetMedia(ctx context.Context, mctx *store.MessageContext, data []byte, mimeType string, mediaType whatsmeow.MediaType, filename, logLabel string) error {
	// Normalisasi/gate SEBELUM upload (lihat sendDlBytesVideoAlbum):
	// tolak kosong/HTML, transcode kontainer asing ke MP4.
	if mediaType == whatsmeow.MediaVideo {
		var err error
		data, err = messaging.PrepareVideoBytes(ctx, data)
		if err != nil {
			return fmt.Errorf("gagal siapkan video %s: %v", logLabel, err)
		}
	} else if mediaType == whatsmeow.MediaImage {
		if err := messaging.CheckMediaBytes(data); err != nil {
			return fmt.Errorf("gagal siapkan gambar %s: %v", logLabel, err)
		}
	}
	uploaded, err := mctx.Client.Upload(ctx, data, mediaType)
	if err != nil {
		return fmt.Errorf("gagal upload %s: %v", logLabel, err)
	}

	var msg *waE2E.Message
	switch mediaType {
	case whatsmeow.MediaImage:
		im := &waE2E.ImageMessage{
			URL:               proto.String(uploaded.URL),
			DirectPath:        proto.String(uploaded.DirectPath),
			MediaKey:          uploaded.MediaKey,
			MediaKeyTimestamp: proto.Int64(time.Now().Unix()),
			Mimetype:          proto.String(mimeType),
			FileEncSHA256:     uploaded.FileEncSHA256,
			FileSHA256:        uploaded.FileSHA256,
			FileLength:        proto.Uint64(uploaded.FileLength),
		}
		if cfg, _, cerr := image.DecodeConfig(bytes.NewReader(data)); cerr == nil && cfg.Width > 0 {
			im.Width = proto.Uint32(uint32(cfg.Width))
			im.Height = proto.Uint32(uint32(cfg.Height))
		}
		im.JPEGThumbnail = messaging.InlineThumbForImage(data)
		msg = &waE2E.Message{ImageMessage: im}
	case whatsmeow.MediaVideo:
		thumb := messaging.InlineThumbForVideo(ctx, data)
		vm := &waE2E.VideoMessage{
			URL:               proto.String(uploaded.URL),
			DirectPath:        proto.String(uploaded.DirectPath),
			MediaKey:          uploaded.MediaKey,
			MediaKeyTimestamp: proto.Int64(time.Now().Unix()),
			Mimetype:          proto.String(mimeType),
			FileEncSHA256:     uploaded.FileEncSHA256,
			FileSHA256:        uploaded.FileSHA256,
			FileLength:        proto.Uint64(uploaded.FileLength),
			JPEGThumbnail:     thumb,
		}
		if len(thumb) > 0 {
			if cfg, _, cerr := image.DecodeConfig(bytes.NewReader(thumb)); cerr == nil && cfg.Width > 0 {
				vm.Width = proto.Uint32(uint32(cfg.Width))
				vm.Height = proto.Uint32(uint32(cfg.Height))
			}
		}
		msg = &waE2E.Message{VideoMessage: vm}
	default: // whatsmeow.MediaAudio
		msg = &waE2E.Message{
			AudioMessage: &waE2E.AudioMessage{
				URL:           proto.String(uploaded.URL),
				DirectPath:    proto.String(uploaded.DirectPath),
				MediaKey:      uploaded.MediaKey,
				Mimetype:      proto.String(mimeType),
				FileEncSHA256: uploaded.FileEncSHA256,
				FileSHA256:    uploaded.FileSHA256,
				FileLength:    proto.Uint64(uploaded.FileLength),
				PTT:           proto.Bool(false),
			},
		}
	}

	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, msg)
	if err != nil {
		return fmt.Errorf("gagal kirim %s: %v", logLabel, err)
	}
	store.LogOutgoing(mctx, "get-"+logLabel, filename)
	store.TrackBotMessageID(resp.ID, filename)
	return nil
}

// sendGetDocument mengupload + mengirim hasil fetch sebagai dokumen.
func sendGetDocument(ctx context.Context, mctx *store.MessageContext, data []byte, contentType, filename string) error {
	uploaded, err := mctx.Client.Upload(ctx, data, whatsmeow.MediaDocument)
	if err != nil {
		return fmt.Errorf("gagal upload dokumen: %v", err)
	}

	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, &waE2E.Message{
		DocumentMessage: &waE2E.DocumentMessage{
			URL:           proto.String(uploaded.URL),
			DirectPath:    proto.String(uploaded.DirectPath),
			MediaKey:      uploaded.MediaKey,
			Mimetype:      proto.String(contentType),
			FileEncSHA256: uploaded.FileEncSHA256,
			FileSHA256:    uploaded.FileSHA256,
			FileLength:    proto.Uint64(uploaded.FileLength),
			FileName:      proto.String(filename),
		},
	})
	if err != nil {
		return fmt.Errorf("gagal kirim dokumen: %v", err)
	}
	store.LogOutgoing(mctx, "get-document", filename)
	store.TrackBotMessageID(resp.ID, filename)
	return nil
}
