package plugins

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"

	_ "image/gif"
	_ "image/png"

	"strings"

	"wabot-go/internal/external"
	"wabot-go/internal/store"

	whatsmeow "github.com/polymorfa/hypermeow"
)

func init() {
	registerPlugin(RegisterSetPP)
}

// Ambang kompresi .setpp, diambil persis dari script referensi (sharp):
// hanya gambar >900KB ATAU sisi >720px yang di-resize (fit-inside 720)
// + JPEG q80; yang kecil lolos tanpa re-encode (hemat CPU + tanpa
// generation loss).
const (
	setppMaxBytes = 900 << 10
	setppMaxSide  = 720
)

func RegisterSetPP(r *Registry) {
	r.Register(Command{
		Name:        "setpp",
		Aliases:     []string{"seticon"},
		Description: "Ganti foto grup (.setpp reply/kirim gambar atau .setpp <url>)",
		Category:    CategoryGrup,
		Handler:     handleSetPP,
	})
}

// handleSetPP mengganti foto grup dari gambar (reply / kirim langsung /
// URL). Adaptasi script referensi (sharp → ffmpeg, conn.query →
// SetGroupPhoto). Perbedaan sadar dari script: output SELALU JPEG karena
// server WA bisa menolak PNG mentah (ErrInvalidImageFormat) — script asli
// lolos PNG kecil apa adanya dan berisiko di titik ini.
//
// Dieksekusi sinkron (detikan, pola .sticker).
func handleSetPP(ctx context.Context, mctx *store.MessageContext) error {
	// Gerbang moderasi (grup + admin/owner), pola .autodownload/antilink.
	if msg := requireModGate(ctx, mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}

	mediaBytes, err := setppSourceBytes(ctx, mctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, err.Error())
	}

	jpegBytes, err := setppPrepareJPEG(ctx, mediaBytes)
	if err != nil {
		return store.ReplyText(ctx, mctx, "Gagal siapkan foto: "+err.Error())
	}

	if _, err := mctx.Client.SetGroupPhoto(ctx, mctx.Chat, jpegBytes); err != nil {
		if errors.Is(err, whatsmeow.ErrInvalidImageFormat) {
			return store.ReplyText(ctx, mctx, "Gambar tidak valid untuk foto grup (pakai JPG/PNG).")
		}
		return store.ReplyText(ctx, mctx, "Gagal ganti foto grup: "+err.Error())
	}

	return store.ReplyText(ctx, mctx, "Foto grup berhasil diganti!")
}

// setppSourceBytes mengambil byte gambar dari 3 jalur: argumen URL,
// kirim gambar langsung dengan caption, atau reply gambar. Video ditolak
// tegas (tak bisa jadi foto profil).
func setppSourceBytes(ctx context.Context, mctx *store.MessageContext) ([]byte, error) {
	// Jalur 1: URL ditempel (pola fetch API pihak ketiga + tameng SSRF
	// milik .get, seperti validasi downloader).
	if len(mctx.Args) > 0 && strings.HasPrefix(mctx.Args[0], "http") {
		rawURL := mctx.Args[0]
		if err := isPublicGetURL(ctx, rawURL); err != nil {
			return nil, fmt.Errorf("%v", err)
		}
		data, err := external.DownloadBytes(ctx, rawURL)
		if err != nil {
			return nil, fmt.Errorf("gagal unduh URL: %v", err)
		}
		return data, nil
	}

	// Jalur 2+3: kirim langsung / reply (reuse extractStickerMedia yang
	// sudah unwrap Ephemeral/ViewOnce + kedua jalur ContextInfo).
	img, vid, err := extractStickerMedia(mctx)
	if err != nil {
		return nil, fmt.Errorf("reply/kirim gambar, atau tempel URL: .setpp <url>")
	}
	if vid != nil {
		return nil, fmt.Errorf("video tidak bisa jadi foto grup, pakai gambar")
	}
	data, err := mctx.Client.Download(ctx, img)
	if err != nil {
		return nil, fmt.Errorf("gagal download gambar: %v", err)
	}
	return data, nil
}

// setppPrepareJPEG menyiapkan byte gambar jadi JPEG siap SetGroupPhoto.
//
// Aturan: input yang SUDAH berupa JPEG kecil (≤900KB, sisi ≤720px) dikirim
// APA ADANYA tanpa re-encode — persis script referensi ("tidak dikompres",
// dan terbukti lolos validator server Sep 2026).
//
// Selain itu gambar dinormalisasi via DUA tahap yang disengaja dipisah:
// ffmpeg HANYA decode+scale ke PNG sementara, lalu encode JPEG final oleh
// STDLIB Go (image/jpeg). Alasan: encoder mjpeg ffmpeg (satu-satunya
// penulis JPEG di pipeline lama) menghasilkan byte yang SELALU ditolak
// server (406) dalam 2 varian berbeda — JFIF APP0 (yuvj) maupun COM-first
// (yuv420p) — sementara JPEG original lolos. Encode stdlib menghasilkan
// JFIF bersih tanpa segmen COM, tanpa dependensi baru, dan dimensi output
// selalu benar karena Go membaca HASIL aktual ffmpeg (kebal soal rotasi
// EXIF/orientasi yang bikin tebak dimensi manual meleset).
func setppPrepareJPEG(ctx context.Context, data []byte) ([]byte, error) {
	width, height := 0, 0
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		width, height = cfg.Width, cfg.Height
	}
	isJPEG := len(data) > 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF

	// Jalur lolos-apa-adanya: JPEG + kecil + dimensi terbaca.
	if isJPEG && len(data) <= setppMaxBytes &&
		width > 0 && width <= setppMaxSide && height > 0 && height <= setppMaxSide {
		return data, nil
	}

	// Jalur Go-native: JPEG/PNG/GIF kecil yang bisa di-decode stdlib
	// langsung di-encode ulang TANPA lewat ffmpeg sama sekali.
	if !needSetppResize(len(data), width, height) {
		img, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("format gambar tak dikenal: %v", err)
		}
		return goEncodeJPEG(img, 90)
	}

	// Jalur ffmpeg: decode + fit-inside 720 ke PNG sementara (tanpa encode
	// JPEG!), lalu Go yang encode final. Berlaku untuk gambar besar SEMUA
	// format (JPEG/PNG/GIF) + webp statis (decoder native ffmpeg cukup
	// untuk frame tunggal).
	pngBytes, err := ffmpegToPNG(ctx, data)
	if err != nil {
		return nil, err
	}
	img, _, err := image.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		return nil, fmt.Errorf("gagal baca hasil scale: %v", err)
	}
	return goEncodeJPEG(img, 80)
}

// needSetppResize true kalau gambar melebihi ambang (atau dimensinya tak
// terbaca — perlakukan sebagai butuh normalisasi, bukan lolos mentah).
func needSetppResize(sizeBytes, width, height int) bool {
	if width == 0 || height == 0 {
		return true
	}
	return sizeBytes > setppMaxBytes || width > setppMaxSide || height > setppMaxSide
}

// jfifApp0 adalah segmen JFIF APP0 standar (18 byte) yang disisipkan
// tepat setelah SOI. Tanpa ini JPEG stdlib Go diawali DQT langsung
// (FF D8 FF DB) — valid menurut spec tapi "tidak biasa"; validator ketat
// lebih akrab dengan bentuk SOI+APP0+DQT ala file kamera.
var jfifApp0 = []byte{0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46, 0x00, 0x01, 0x01, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00}

// goEncodeJPEG meng-encode image.Image jadi JPEG JFIF bersih via stdlib
// (tanpa segmen COM ala encoder mjpeg ffmpeg).
func goEncodeJPEG(img image.Image, quality int) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	raw := buf.Bytes()
	if len(raw) < 2 || raw[0] != 0xFF || raw[1] != 0xD8 {
		return nil, fmt.Errorf("hasil encode bukan JPEG")
	}
	out := make([]byte, 0, len(raw)+len(jfifApp0))
	out = append(out, raw[:2]...)
	out = append(out, jfifApp0...)
	out = append(out, raw[2:]...)
	if len(out) == 0 {
		return nil, fmt.Errorf("hasil encode JPEG kosong")
	}
	return out, nil
}

// ffmpegToPNG men-decode + me-resize gambar ke PNG sementara (fit-inside
// 720; gambar kecil tidak di-upscale). PNG dipilih sebagai format antara
// karena lossless + didukung penuh stdlib — ffmpeg TIDAK menyentuh encode
// JPEG di sini. Output PNG dibaca Go untuk dimensi aktualnya.
func ffmpegToPNG(ctx context.Context, data []byte) ([]byte, error) {
	return runFfmpegConvert(ctx, data, smemeExt(data), "pp.png", nil,
		"-vf", "scale=720:720:force_original_aspect_ratio=decrease",
		"-frames:v", "1")
}
