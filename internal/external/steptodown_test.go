package external

import "testing"

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
