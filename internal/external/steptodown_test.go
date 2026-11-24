package external

import (
	"context"
	"testing"
)

func TestParseSteptodownImageLink(t *testing.T) {
	html := []byte(`<li class="watch"><a href="images/steptodown.com648538.jpg" class="btn " download >Download Image</a></li>`)
	if got := parseSteptodownImageLink(html); got != "images/steptodown.com648538.jpg" {
		t.Fatalf("dapat %q, mau %q", got, "images/steptodown.com648538.jpg")
	}
	// Varian ekstensi lain tetap dikenali.
	png := []byte(`<a href="images/steptodown.com123.png">x</a>`)
	if got := parseSteptodownImageLink(png); got != "images/steptodown.com123.png" {
		t.Fatalf("png: dapat %q", got)
	}
	for _, bad := range [][]byte{
		[]byte(`<html><body>gambar tidak ditemukan</body></html>`),
		[]byte(``),
		nil,
	} {
		if got := parseSteptodownImageLink(bad); got != "" {
			t.Fatalf("harus kosong, dapat %q", got)
		}
	}
}

func TestStockCache(t *testing.T) {
	if stockCache == nil {
		t.Fatal("stockCache nil")
	}
	ctx := t.Context()
	key := "test-tool:test-input"
	calls := 0
	loader := func(ctx context.Context) ([]byte, error) {
		calls++
		return []byte("test-image-binary-data"), nil
	}

	val1, err := stockCache.GetOrLoad(ctx, key, loader)
	if err != nil || string(val1) != "test-image-binary-data" {
		t.Fatalf("call 1 gagal: %v, got %q", err, val1)
	}
	if calls != 1 {
		t.Fatalf("harus 1 call, got %d", calls)
	}

	// Call kedua harus hit cache (calls tetap 1)
	val2, err := stockCache.GetOrLoad(ctx, key, loader)
	if err != nil || string(val2) != "test-image-binary-data" {
		t.Fatalf("call 2 gagal: %v, got %q", err, val2)
	}
	if calls != 1 {
		t.Fatalf("call 2 harus hit cache tanpa memanggil loader, calls: %d", calls)
	}
}
