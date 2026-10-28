package plugins

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"os"
	"strings"
	"unicode/utf8"

	"wabot-go/internal/arena"
	"wabot-go/internal/logfmt"
	"wabot-go/internal/store"

	"github.com/polymorfa/hypermeow/proto/waE2E"
)

// seedream_edit.go — jalur edit foto (image-to-image) untuk .seedream.
//
// Tidak ada command terpisah: dispatch di seedream.go memilih jalur ini
// kalau pesan memuat gambar (dilampirkan atau di-reply). Model edit default
// ikut ARENA_MODEL (seedream-5.0-pro, yang menurut catalog arena.ai menerima
// input gambar: inputCapabilities.image.requiresUpload=false), bisa ditimpa
// ARENA_EDIT_MODEL.
//
// Catatan penting: arena.ai hanya menghasilkan rasio 1:1 untuk model gambar,
// jadi foto lanskap/portrait akan dipotong atau diberi bidang oleh model.
// Batas 1 gambar referensi (maxReferenceCount di internal/arena) dan satu
// generate memakai kuota akun arena.ai owner + Chrome ~300MB di panel 1 core,
// karena itu kuota/jeda/backoff captcha dibagi bersama generate teks.

const (
	// seditMaxInputBytes: batas foto mentah dari WhatsApp. Di atas ini
	// ditolak sebelum ffmpeg dipanggil supaya panel tidak kehabisan disk
	// (tmpfs /tmp hanya 100MB di panel).
	seditMaxInputBytes = 20 << 20
	// seditMaxBytes / seditMaxSide: foto yang sudah kecil dan JPEG dipakai
	// apa adanya (tanpa re-encode, jadi kualitas edit maksimal).
	seditMaxBytes = 4 << 20
	seditMaxSide  = 1600
)

// seedreamEdit memvalidasi foto + perintah, lalu menjalankan edit di
// goroutine terpisah (Chrome + reCAPTCHA bisa puluhan detik; event loop
// hypermeow tidak boleh tertahan).
//
// prompt bisa kosong: kalau foto dikirim dengan caption, caption itulah
// perintah editnya (token command dibuang oleh seditCaptionPrompt).
func seedreamEdit(ctx context.Context, mctx *store.MessageContext, img *waE2E.ImageMessage, vid *waE2E.VideoMessage, prompt string) error {
	if img == nil || vid != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Seedream",
			"Hanya foto yang bisa diedit. Kirim atau reply sebuah gambar dengan perintah."))
	}

	if prompt == "" {
		prompt = seditCaptionPrompt(img.GetCaption())
	}
	if prompt == "" {
		return store.ReplyText(ctx, mctx, ErrMsg("Seedream",
			"Perintah editnya belum ada. Contoh: .seedream ubah latar jadi biru muda (reply foto)."))
	}
	if utf8.RuneCountInString(prompt) > seedreamMaxPromptRunes {
		return store.ReplyText(ctx, mctx, ErrMsg("Seedream",
			fmt.Sprintf("Perintah kepanjangan (maks %d karakter).", seedreamMaxPromptRunes)))
	}
	if wait, left, berr := seedreamBudget(); berr != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Seedream", berr.Error()))
	} else if wait > 0 {
		return store.ReplyText(ctx, mctx, ErrMsg("Seedream",
			fmt.Sprintf("Tunggu %d detik lagi. Sisa kuota hari ini: %d.", int(wait.Seconds())+1, left)))
	}

	go func() {
		bg := context.Background()
		tctx, cancel := context.WithTimeout(bg, seedreamTimeout())
		defer cancel()

		// Gagal apa pun sebelum gambar jadi tidak boleh memakan kuota.
		refund := func(err error) {
			seedreamRefund()
			if errors.Is(err, arena.ErrRecaptcha) {
				seedreamBlockCaptcha()
			}
			logfmt.Warn("seedream edit: %v\n", err)
			_ = store.ReplyText(bg, mctx, ErrMsg("Seedream", seditErrorText(err)))
		}

		// Keterangan tunggu tanpa quote (bukan reply); hasil gambar yang
		// nanti me-reply perintah user.
		if err := store.SendText(bg, mctx, seedreamWaitText("edit")); err != nil {
			logfmt.Warn("seedream edit tunggu: %v\n", err)
		}

		raw, err := mctx.Client.Download(tctx, img)
		if err != nil {
			refund(fmt.Errorf("gagal unduh foto: %w", err))
			return
		}
		if len(raw) > seditMaxInputBytes {
			refund(fmt.Errorf("%w: foto %d MB terlalu besar (maks %d MB)",
				arena.ErrImageInvalid, len(raw)>>20, seditMaxInputBytes>>20))
			return
		}
		prepared, err := seditPrepareImage(tctx, raw)
		if err != nil {
			refund(err)
			return
		}

		data, model, err := arena.Generate(tctx, arena.Request{
			Prompt: prompt,
			Images: [][]byte{prepared},
			Model:  arenaEditModelName(),
		})
		if err != nil {
			refund(err)
			return
		}
		if err := seedreamSend(bg, mctx, data, model, "edit", prompt); err != nil {
			logfmt.Warn("seedream edit kirim: %v\n", err)
			_ = store.ReplyText(bg, mctx, ErrMsg("Seedream", err.Error()))
		}
	}()
	return nil
}

// seditCaptionPrompt mengambil perintah dari caption foto. Kalau foto itu
// sendiri yang dikirim dengan caption ".seedream ...", caption masih memuat
// command-nya — buang token command + kata kerja opsional supaya yang
// dikirim ke arena.ai hanya perintah editnya.
func seditCaptionPrompt(caption string) string {
	caption = strings.TrimSpace(caption)
	if !strings.HasPrefix(caption, ".") {
		return caption
	}
	if i := strings.IndexAny(caption, " \t\r\n"); i >= 0 {
		return seedreamStripVerb(caption[i+1:])
	}
	return ""
}

// seditPrepareImage menormalkan foto referensi menjadi JPEG yang bisa
// ditempel sebagai data URL (base64) ke permintaan arena.ai.
//
// Aturan: JPEG kecil (<=4MB, sisi <=1600px) dikirim apa adanya supaya tidak
// ada kehilangan kualitas tambahan. Selain itu dinormalkan: PNG/GIF decode
// stdlib lalu encode JPEG (tanpa ffmpeg), webp/format lain lewat ffmpeg
// (decoder native-nya ada di panel). Latar transparan dijadikan putih karena
// JPEG tidak punya alpha — kalau dibiarkan, area transparan jadi hitam.
func seditPrepareImage(ctx context.Context, data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: foto kosong", arena.ErrImageInvalid)
	}
	isJPEG := len(data) > 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF
	cfg, _, cfgErr := image.DecodeConfig(bytes.NewReader(data))
	if cfgErr == nil && isJPEG && len(data) <= seditMaxBytes &&
		cfg.Width > 0 && cfg.Height > 0 && cfg.Width <= seditMaxSide && cfg.Height <= seditMaxSide {
		return data, nil
	}

	// Jalur stdlib: JPEG/PNG/GIF yang ukurannya masih wajar.
	if cfgErr == nil {
		img, _, derr := image.Decode(bytes.NewReader(data))
		if derr == nil {
			return seditEncodeJPEG(img, 90)
		}
	}

	// Jalur ffmpeg: webp atau gambar besar (di-scale fit-inside 1600).
	pngBytes, err := runFfmpegConvert(ctx, data, smemeExt(data), "ref.png", nil,
		"-vf", fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease", seditMaxSide, seditMaxSide),
		"-frames:v", "1")
	if err != nil {
		return nil, fmt.Errorf("%w: gagal mengonversi foto (%v)", arena.ErrImageInvalid, err)
	}
	img, _, err := image.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		return nil, fmt.Errorf("%w: hasil konversi tidak terbaca (%v)", arena.ErrImageInvalid, err)
	}
	return seditEncodeJPEG(img, 88)
}

// seditEncodeJPEG meng-encode gambar ke JPEG di atas latar putih (alpha
// diratakan) memakai encoder stdlib yang sama dengan .setpp.
func seditEncodeJPEG(img image.Image, quality int) ([]byte, error) {
	bounds := img.Bounds()
	flat := image.NewRGBA(bounds)
	draw.Draw(flat, bounds, &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	draw.Draw(flat, bounds, img, bounds.Min, draw.Over)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, flat, &jpeg.Options{Quality: quality}); err != nil {
		return nil, fmt.Errorf("%w: gagal encode JPEG (%v)", arena.ErrImageInvalid, err)
	}
	return buf.Bytes(), nil
}

// seditErrorText menerjemahkan error adapter ke kalimat yang bisa
// ditindaklanjuti (termasuk membedakan masalah foto vs cookie vs captcha).
func seditErrorText(err error) string {
	switch {
	case errors.Is(err, arena.ErrImageInvalid):
		return "Foto referensi tidak bisa dipakai: " + strings.TrimPrefix(err.Error(), arena.ErrImageInvalid.Error()+": ")
	case errors.Is(err, arena.ErrUploadRef):
		// Detail teknis ikut ditampilkan: command ini owner-only, dan tanpa
		// detail setiap kegagalan unggah butuh tanya-jawab log terpisah.
		detail := strings.TrimPrefix(err.Error(), arena.ErrUploadRef.Error()+": ")
		return "Gagal mengunggah foto ke arena.ai. Detail: " + detail
	case errors.Is(err, arena.ErrNoCookie), errors.Is(err, arena.ErrCookieRejected),
		errors.Is(err, arena.ErrRecaptcha), errors.Is(err, arena.ErrLowMemory),
		errors.Is(err, arena.ErrChromeOld), errors.Is(err, arena.ErrChromeMissing),
		errors.Is(err, arena.ErrBrowser), errors.Is(err, arena.ErrModelUnknown),
		errors.Is(err, arena.ErrModelFailed), errors.Is(err, context.DeadlineExceeded):
		return seedreamErrorText(err)
	default:
		return fmt.Sprintf("Gagal mengedit foto (%v).", err)
	}
}

// arenaEditModelName hanya untuk tampilan/permintaan edit; model edit default
// sama dengan model generate (seedream-5.0-pro), bisa diganti ARENA_EDIT_MODEL.
func arenaEditModelName() string {
	if v := strings.TrimSpace(os.Getenv("ARENA_EDIT_MODEL")); v != "" {
		return v
	}
	return arenaModelName()
}
