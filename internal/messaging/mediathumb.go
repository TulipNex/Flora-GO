package messaging

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"wabot-go/internal/heavy"
	"wabot-go/internal/logfmt"
	"wabot-go/internal/store"
)

// maxInlineThumbBytes adalah batas byte thumbnail inline. Thumbnail inline
// yang kebesaran bikin klien gagal render — lebih baik tanpa thumbnail
// (placeholder) daripada byte raksasa yang merusak kartu.
const maxInlineThumbBytes = 200 << 10 // 200KB

// tinyInlineDim/Quality membuat thumbnail inline SANGAT kecil (~1-3KB).
// Bukti lapangan (perbandingan log pesan masuk): bot Baileys yang
// preview-nya tampil membawa thumb ~1,5KB, sedangkan thumb 320px/7-26KB
// kita diabaikan klien (placeholder abu-abu) — klien tampaknya membuang
// thumb inline yang kebesaran. Thumb sekecil ini tampil buram saat
// dibesarkan, tapi jauh lebih baik daripada abu-abu polos.
const (
	tinyInlineDim     = 128
	tinyInlineQuality = 60
)

// InlineThumbForImage membuat thumbnail JPEG mini dari byte gambar untuk
// ditempel di field JPEGThumbnail pesan media. Tanpa ini (atau kalau
// kebesaran) daftar chat menampilkan placeholder kamera abu-abu padahal
// file-nya valid. Mengembalikan nil kalau tidak bisa dibuat (fail-soft:
// kirim tanpa thumb). Selalu re-encode via jpeg.Encode stdlib sehingga
// hasilnya baseline JPEG yang aman di-render semua klien (termasuk sumber
// progressive JPEG yang decoder kliennya tidak menanganinya).
func InlineThumbForImage(data []byte) []byte {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	small := img
	if b := img.Bounds(); b.Dx() > tinyInlineDim || b.Dy() > tinyInlineDim {
		small = downscaleBilinear(img, tinyInlineDim)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, small, &jpeg.Options{Quality: tinyInlineQuality}); err != nil {
		return nil
	}
	out := buf.Bytes()
	if len(out) == 0 || len(out) > maxInlineThumbBytes {
		return nil
	}
	return out
}

// mapInlineDim/Quality/Bytes adalah racikan thumbnail KHUSUS preview
// lokasi (.shareloc): bubble peta butuh gambar jauh lebih besar daripada
// kartu link (tinyInlineDim 128px terbukti tampil buram/diabaikan lalu
// diganti peta bawaan klien). Konstanta dipisah supaya tune lokasi tidak
// menggeser kartu link — keduanya punya ambang klien yang berbeda.
const (
	mapInlineDim      = 480
	mapInlineQuality  = 70
	mapInlineMaxBytes = 100 << 10 // 100KB
)

// InlineMapThumb membuat thumbnail JPEG untuk field JPEGThumbnail pesan
// lokasi dari byte gambar bebas (thumb menu / --img). Reuse downscale +
// jpeg.Encode yang sama dengan InlineThumbForImage, hanya beda ukuran.
// Mengembalikan nil kalau tidak bisa dibuat / melebihi cap (fail-soft:
// kirim tanpa preview). CATATAN JUJUR: klien WhatsApp mengganti preview
// ini dengan render petanya sendiri setelah dimuat — thumb besar hanya
// mempertajam tampilan AWAL + daftar chat, bukan mengunci gambar permanen.
func InlineMapThumb(data []byte) []byte {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	big := img
	if b := img.Bounds(); b.Dx() > mapInlineDim || b.Dy() > mapInlineDim {
		big = downscaleBilinear(img, mapInlineDim)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, big, &jpeg.Options{Quality: mapInlineQuality}); err != nil {
		return nil
	}
	out := buf.Bytes()
	if len(out) == 0 || len(out) > mapInlineMaxBytes {
		return nil
	}
	return out
}

// InlineThumbForVideo mengekstrak 1 frame video via ffmpeg lalu mengecilkannya
// jadi thumbnail inline. Butuh ffmpeg di server (sudah wajib untuk .sticker).
// Gagal di titik mana pun = nil (kirim tanpa thumb, bukan gagal total).
func InlineThumbForVideo(ctx context.Context, data []byte) []byte {
	dir, err := os.MkdirTemp(store.MustBotTempDir(), "wabot-vthumb-*")
	if err != nil {
		return nil
	}
	defer os.RemoveAll(dir)

	in := filepath.Join(dir, "in.mp4")
	if err := os.WriteFile(in, data, 0600); err != nil {
		return nil
	}
	out := filepath.Join(dir, "thumb.jpg")
	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// Strategi 1: frame detik ke-1 (lewati frame hitam pembuka).
	// Strategi 2 (fallback): tanpa seek — untuk file pendek (<1 detik,
	// moov tak lazim) di mana -ss 1 melompat melewati EOF.
	// Stderr DITAMPILKAN (dulu dibuang — penyebab "exit status 1" buta).
	strategies := [][]string{
		{"-y", "-ss", "1", "-i", in, "-vframes", "1", "-q:v", "5", out},
		{"-y", "-i", in, "-vframes", "1", "-q:v", "5", out},
	}
	release, acqErr := heavy.AcquireTranscode(ctx)
	if acqErr != nil {
		return nil
	}
	defer release()
	for i, args := range strategies {
		cmd := exec.CommandContext(timeoutCtx, "ffmpeg", args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			logfmt.Warn("mediathumb: ffmpeg frame gagal (strategi %d): %v (%s)\n", i+1, err, lastLine(stderr.String()))
			continue
		}
		thumb, err := os.ReadFile(out)
		if err != nil || len(thumb) == 0 {
			continue
		}
		return InlineThumbForImage(thumb)
	}
	return nil
}
