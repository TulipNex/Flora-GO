package geminiapi

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// tinyPNG membuat gambar 32x32 dua warna untuk uji unggah tanpa berkas
// eksternal.
func tinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			if x < 16 {
				img.Set(x, y, color.RGBA{R: 220, G: 60, B: 40, A: 255})
			} else {
				img.Set(x, y, color.RGBA{R: 40, G: 90, B: 220, A: 255})
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}
