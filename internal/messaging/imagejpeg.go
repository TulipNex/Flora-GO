package messaging

import (
	"bytes"
	"image"
	"image/jpeg"

	// Dekoder tambahan agar gambar dari CDN (PNG/GIF/WebP) bisa dinormalkan.
	_ "image/gif"
	_ "image/png"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// HeaderJPEG menormalkan byte gambar apa pun menjadi JPEG baseline siap
// dikirim sebagai media/header: decode (JPEG/PNG/GIF/WebP) → skala sisi
// terpanjang ≤ maxSide → encode JPEG quality.
//
// Alasan: header gambar InteractiveMessage hanya dirender klien bila byte-nya
// bentuk "biasa" (baseline JPEG, ukuran wajar). Cover dari CDN pihak ketiga
// bisa berupa progressive JPEG/PNG/WebP atau resolusi besar — menormalkan di
// sini membuat semua sumber seragam. Mengembalikan nil bila decode/encode
// gagal; pemanggil WAJIB fail-soft (kirim pesan tanpa gambar).
func HeaderJPEG(data []byte, maxSide, quality int) []byte {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	if maxSide > 0 {
		if b := img.Bounds(); b.Dx() > maxSide || b.Dy() > maxSide {
			w, h := b.Dx(), b.Dy()
			if w >= h {
				h = h * maxSide / w
				w = maxSide
			} else {
				w = w * maxSide / h
				h = maxSide
			}
			if w < 1 {
				w = 1
			}
			if h < 1 {
				h = 1
			}
			dst := image.NewRGBA(image.Rect(0, 0, w, h))
			draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
			img = dst
		}
	}
	if quality <= 0 || quality > 100 {
		quality = 85
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil
	}
	return buf.Bytes()
}
