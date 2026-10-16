package plugins

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"wabot-go/internal/external"
	"wabot-go/internal/store"

	whatsmeow "github.com/polymorfa/hypermeow"
)

func init() {
	registerPlugin(RegisterBK)
}

// RegisterBK mendaftarkan command `.bk` (alias `.po`) kategori Media:
// download video dari link embed berpola /(e|d)/{id} di host APAPUN (tanpa
// whitelist domain — keputusan owner). Resolve + kirim reuse engine di
// file ini (port pola bk.js + xpvid.js referensi).
func RegisterBK(r *Registry) {
	r.Register(Command{
		Name:        "bk",
		Aliases:     []string{"po"},
		Description: "Download video embed (.bk <link> [d])",
		Category:    CategoryHidden,
		Hidden:      true,
		Handler:     handleBK,
	})
}

func handleBK(ctx context.Context, mctx *store.MessageContext) error {
	// Command privat owner: tidak ada di menu, tidak untuk user biasa.
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	if len(mctx.Args) == 0 {
		return store.ReplyText(ctx, mctx, "Format: .bk <link> [d]\nTambah d untuk kirim sebagai dokumen.")
	}
	rawURL := mctx.Args[0]
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		rawURL = "https://" + rawURL
	}
	if !external.IsXpvidURL(rawURL) {
		return store.ReplyText(ctx, mctx, "Media tidak ditemukan.")
	}
	docMode := len(mctx.Args) > 1 && strings.EqualFold(strings.TrimSpace(mctx.Args[1]), "d")
	return fetchSendXpvid(ctx, mctx, rawURL, docMode)
}

// fetchSendXpvid menjalankan alur download di goroutine: gate antrean
// bersama downloader → status → resolve → kirim.
// docMode=true (argumen "d") memaksa hasil direct dikirim sebagai dokumen.
func fetchSendXpvid(ctx context.Context, mctx *store.MessageContext, rawURL string, docMode bool) error {
	// Antrean global downloader ikut berlaku (unduh + remux boros
	// bandwidth/CPU; penuh = tolak langsung, bukan antre diam).
	select {
	case dlSem <- struct{}{}:
		defer func() { <-dlSem }()
	default:
		return store.ReplyText(ctx, mctx, "Antrean download penuh (maks 2 bersamaan), coba lagi sebentar.")
	}

	// Pesan status "Mengambil info video..." SENGAJA dibiarkan dan tidak
	// ada pesan penutup (pola downloader) — media/informasi yang terkirim
	// adalah penanda selesainya.
	if _, err := sendDlStatus(ctx, mctx, "Mengambil info video..."); err != nil {
		return err
	}

	go func() {
		bg := context.Background()
		if rerr := resolveAndSendXpvid(bg, mctx, rawURL, docMode); rerr != nil {
			_ = store.ReplyText(bg, mctx, rerr.Error())
		}
	}()
	return nil
}

// resolveAndSendXpvid adalah engine-nya: resolve URL → info reply →
// HLS diremux jadi MP4 dokumen / direct diunduh jadi video (atau dokumen
// bila mode d). Error berupa pesan ramah siap-balas ("media tidak
// ditemukan" untuk tiap kegagalan resolve/unduh).
func resolveAndSendXpvid(ctx context.Context, mctx *store.MessageContext, rawURL string, docMode bool) error {
	info, err := external.FetchXpvid(ctx, rawURL)
	if err != nil {
		return fmt.Errorf("gagal ambil video: %v%v", err, xpvidBlockHint(err))
	}

	// Info dulu (judul + URL sumber), media menyusul — pola bk.js referensi.
	_ = store.ReplyText(ctx, mctx, fmt.Sprintf("%s\n%s", info.Title, info.VideoURL))

	if info.IsHLS {
		return sendXpvidHLS(ctx, mctx, info)
	}

	data, err := external.DownloadBytesWithHeaders(ctx, info.VideoURL, map[string]string{
		"Referer": info.Referer,
	})
	if err != nil {
		return fmt.Errorf("gagal unduh video: %v", err)
	}
	filename := xpvidFilename(info.Title)
	if docMode {
		return sendGetDocument(ctx, mctx, data, "video/mp4", filename)
	}
	return sendGetMedia(ctx, mctx, data, "video/mp4", whatsmeow.MediaVideo, filename, "video")
}

// sendXpvidHLS me-remux stream HLS jadi MP4 tanpa re-encode (copy video,
// AAC audio — pola convertHLS referensi) lalu mengirimnya sebagai dokumen
// (WhatsApp tidak memutar .m3u8). Input berupa URL remote sehingga tidak
// reuse runFfmpegConvert (yang didesain untuk input byte lokal) — exec
// ditulis eksplisit dengan pola temp+stderr+timeout yang sama. Output dicek
// ke cap .get (100MB).
func sendXpvidHLS(ctx context.Context, mctx *store.MessageContext, info *external.XpvidInfo) error {
	baseTmp, err := store.BotTempDir()
	if err != nil {
		return fmt.Errorf("gagal siapkan temp: %v", err)
	}
	dir, err := os.MkdirTemp(baseTmp, "wabot-xpvid-*")
	if err != nil {
		return fmt.Errorf("gagal siapkan temp: %v", err)
	}
	defer os.RemoveAll(dir)
	outPath := filepath.Join(dir, "out.mp4")

	timeoutCtx, cancel := context.WithTimeout(ctx, dlTimeout)
	defer cancel()
	cmd := exec.CommandContext(timeoutCtx, "ffmpeg",
		"-headers", "Referer: "+xpvidRefererDomain(info.VideoURL)+"\r\n",
		"-i", info.VideoURL,
		"-c:v", "copy", "-c:a", "aac", "-movflags", "+faststart",
		outPath,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if timeoutCtx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("konversi melebihi %s, dibatalkan.", dlTimeout)
		}
		return fmt.Errorf("gagal konversi stream: %v", firstDlErrorLine(stderr.Bytes()))
	}

	mp4, err := os.ReadFile(outPath)
	if err != nil || len(mp4) == 0 {
		return fmt.Errorf("hasil konversi kosong")
	}
	if len(mp4) > getMaxFileSize {
		return fmt.Errorf("hasil %.0f MB melebihi batas %d MB.", float64(len(mp4))/(1<<20), getMaxFileSize/(1<<20))
	}
	return sendGetDocument(ctx, mctx, mp4, "video/mp4", xpvidFilename(info.Title))
}

// xpvidBlockHint menambahkan penjelasan saat error mengarah ke pemblokiran
// ISP (teramati: domain satu engine ini masuk daftar Internet Positif
// Kominfo — DNS me-resolve ke aduankonten.id/internetpositif.id dengan
// sertifikat tak valid, sehingga TLS gagal dengan x509 error).
func xpvidBlockHint(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "certificate") || strings.Contains(msg, "tls:") {
		return " (kemungkinan domain diblokir provider/Internet Positif)"
	}
	return ""
}

// xpvidRefererDomain mengambil "https://domain/" untuk header Referer
// saat remux HLS (server CDN menolak tanpa referer yang benar).
func xpvidRefererDomain(videoURL string) string {
	rest := strings.TrimPrefix(strings.TrimPrefix(videoURL, "https://"), "http://")
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	return "https://" + rest + "/"
}

// xpvidFilename membuat nama file dokumen dari judul (tanpa karakter aneh
// di ujung, maks 80 rune, selalu .mp4).
func xpvidFilename(title string) string {
	name := strings.TrimSpace(title)
	name = strings.ReplaceAll(name, "/", "-")
	if runes := []rune(name); len(runes) > 80 {
		name = string(runes[:80])
	}
	name = strings.TrimSuffix(strings.TrimSuffix(name, ".mp4"), ".MP4")
	if name == "" {
		name = "video"
	}
	return name + ".mp4"
}
