package messaging

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

// solidPNG membuat PNG w×h warna solid untuk uji normalisasi.
func solidPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: 20, G: 120, B: 200, A: 0xFF})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode PNG: %v", err)
	}
	return buf.Bytes()
}

// TestHeaderJPEGNormalizes: PNG besar → JPEG baseline, sisi terpanjang ≤ cap,
// dan byte hasilnya benar-benar JPEG (bukan PNG).
func TestHeaderJPEGNormalizes(t *testing.T) {
	got := HeaderJPEG(solidPNG(t, 100, 50), 64, 80)
	if len(got) == 0 {
		t.Fatal("HeaderJPEG nil")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("hasil bukan gambar: %v", err)
	}
	if format != "jpeg" {
		t.Errorf("format = %q, mau jpeg", format)
	}
	if cfg.Width != 64 || cfg.Height != 32 {
		t.Errorf("dimensi = %dx%d, mau 64x32", cfg.Width, cfg.Height)
	}
}

// TestHeaderJPEGKeepsSmall: gambar di bawah cap tidak diperbesar.
func TestHeaderJPEGKeepsSmall(t *testing.T) {
	got := HeaderJPEG(solidPNG(t, 40, 24), 64, 80)
	cfg, _, err := image.DecodeConfig(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cfg.Width != 40 || cfg.Height != 24 {
		t.Errorf("dimensi = %dx%d, mau 40x24 (tidak diperbesar)", cfg.Width, cfg.Height)
	}
}

// TestHeaderJPEGAcceptsJPEG memastikan input JPEG juga dinormalkan ulang jadi
// baseline (progressive JPEG dari CDN ikut di-re-encode).
func TestHeaderJPEGAcceptsJPEG(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 800, 600))
	var in bytes.Buffer
	if err := jpeg.Encode(&in, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode sumber: %v", err)
	}
	got := HeaderJPEG(in.Bytes(), 640, 85)
	cfg, format, err := image.DecodeConfig(bytes.NewReader(got))
	if err != nil || format != "jpeg" {
		t.Fatalf("decode hasil: %v / %q", err, format)
	}
	if cfg.Width != 640 || cfg.Height != 480 {
		t.Errorf("dimensi = %dx%d, mau 640x480", cfg.Width, cfg.Height)
	}
}

// TestHeaderJPEGRejectsJunk: byte tak dikenal → nil (pemanggil fail-soft).
func TestHeaderJPEGRejectsJunk(t *testing.T) {
	if got := HeaderJPEG([]byte("<html>bukan gambar</html>"), 640, 85); got != nil {
		t.Errorf("byte sampah seharusnya nil, dapat %d byte", len(got))
	}
	if got := HeaderJPEG(nil, 640, 85); got != nil {
		t.Errorf("nil seharusnya nil")
	}
}
