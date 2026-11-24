package plugins

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"wabot-go/internal/config"
	"wabot-go/internal/external"
	"wabot-go/internal/logfmt"
	"wabot-go/internal/messaging"
	"wabot-go/internal/store"

	"github.com/polymorfa/hypermeow/proto/waE2E"
)

func init() {
	registerPlugin(RegisterSmeme)
}

const (
	// smemeMaxText membatasi tiap sisi teks (atas/bawah) supaya teks meme tetap wajar.
	smemeMaxText = 100

	// smemePipelineTimeout membatasi seluruh pipeline jaringan (unduh media WA + render Flora Gateway + convert stiker).
	smemePipelineTimeout = 90 * time.Second
)

// RegisterSmeme mendaftarkan command `.smeme` (alias `.stikermeme`, `.sm`)
// kategori Media: reply gambar + teks `atas | bawah` → stiker meme.
func RegisterSmeme(r *Registry) {
	r.Register(Command{
		Name:        "smeme",
		Aliases:     []string{"stikermeme", "sm"},
		Description: "Bikin stiker meme dari gambar (reply gambar + `.smeme atas | bawah`)",
		Category:    CategoryMedia,
		Handler:     handleSmeme,
	})
}

func smemeHelp() string {
	return Help(HelpSpec{
		Title:   "Stiker meme",
		Cmd:     "Smeme",
		Formats: []string{".smeme <atas>|<bawah>"},
		Contoh:  ".smeme halo|apa kabar",
		Note:    "Reply gambar dulu; salah satu sisi boleh kosong.",
	})
}

// handleSmeme memvalidasi input (teks + media background) lalu memproses
// di goroutine terpisah (pola sama dengan .brat/downloader): pipeline
// jaringan bisa memakan belasan detik, jadi event loop hypermeow tidak
// boleh diblokir.
func handleSmeme(ctx context.Context, mctx *store.MessageContext) error {
	top, bottom, err := smemeInputText(mctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, err.Error())
	}

	// Reuse jalur ContextInfo bersama milik .sticker (sudah unwrap
	// Ephemeral/ViewOnce) — hanya gambar yang diterima, video ditolak
	// tegas karena custom background memegen image-only (GIF/MP4 sebagai
	// background terverifikasi dibalas 415).
	img, vid, merr := extractStickerMedia(mctx)
	if merr != nil {
		return store.ReplyText(ctx, mctx, smemeHelp())
	}
	if vid != nil {
		return store.ReplyText(ctx, mctx, "Video tidak didukung, reply gambar saja.")
	}

	go func() {
		bg := context.Background()
		if err := smemeGenerate(bg, mctx, img, top, bottom); err != nil {
			logfmt.Warn("smeme: %v\n", err)
			_ = store.ReplyText(bg, mctx, Fail("Gagal bikin meme", err))
		}
	}()
	return nil
}

// smemeInputText memecah RawArgs menjadi teks atas dan bawah dengan
// pemisah `|`. Tanpa `|` seluruh teks dianggap teks atas. Minimal satu
// sisi terisi; tiap sisi dibatasi smemeMaxText karakter.
func smemeInputText(mctx *store.MessageContext) (top, bottom string, err error) {
	parts := strings.SplitN(strings.TrimSpace(mctx.RawArgs), "|", 2)
	top = strings.TrimSpace(parts[0])
	if len(parts) == 2 {
		bottom = strings.TrimSpace(parts[1])
	}
	if top == "" && bottom == "" {
		return "", "", fmt.Errorf("%s", smemeHelp())
	}
	if utf8.RuneCountInString(top) > smemeMaxText || utf8.RuneCountInString(bottom) > smemeMaxText {
		return "", "", fmt.Errorf("Tiap sisi teks maks %d karakter.", smemeMaxText)
	}
	return top, bottom, nil
}

// smemeGenerate menjalankan seluruh pipeline: unduh media WA → render via
// Flora Gateway (/maker/smeme) → convert ke stiker WebP → pasang EXIF → kirim.
func smemeGenerate(ctx context.Context, mctx *store.MessageContext, img *waE2E.ImageMessage, top, bottom string) error {
	ctx, cancel := context.WithTimeout(ctx, smemePipelineTimeout)
	defer cancel()

	media, err := mctx.Client.Download(ctx, img)
	if err != nil {
		return fmt.Errorf("gagal unduh gambar: %v", err)
	}
	if err := messaging.CheckMediaBytes(media); err != nil {
		return err
	}

	meme, err := external.FloraFetchSmeme(ctx, media, top, bottom)
	if err != nil {
		return fmt.Errorf("gagal render meme: %w", err)
	}
	if err := messaging.CheckMediaBytes(meme); err != nil {
		return err
	}

	// Pipeline convert stiker yang SAMA dengan .brat/.sticker (ffmpeg pad
	// 512x512 transparan + EXIF pack/author) — reuse, bukan duplikasi.
	webp, err := convertToStickerWebp(ctx, meme, false)
	if err != nil {
		return fmt.Errorf("gagal convert ke stiker: %v", err)
	}
	author := config.ResolveStickerAuthor(mctx.SenderPushName())
	webp, err = store.WriteStickerExif(webp, config.ResolveStickerPackname(), author)
	if err != nil {
		return fmt.Errorf("gagal set metadata stiker: %v", err)
	}

	label := strings.TrimSpace(top + " | " + bottom)
	if runes := []rune(label); len(runes) > 60 {
		label = string(runes[:60])
	}
	return sendStickerWebp(ctx, mctx, webp, "smeme", label)
}

// smemeExt menebak ekstensi dari magic bytes (pola sama dengan
// detectDlImageMIME milik downloader). Dipakai juga oleh seedream_edit dan setpp.
func smemeExt(data []byte) string {
	switch {
	case len(data) > 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return ".jpg"
	case len(data) > 8 && string(data[1:4]) == "PNG":
		return ".png"
	case len(data) > 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return ".webp"
	case len(data) > 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a"):
		return ".gif"
	default:
		return ".jpg"
	}
}
