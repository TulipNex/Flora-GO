package plugins

import "testing"

// TestXpvidSendAsDocument mengunci aturan bentuk kiriman .bk: <=100MB video,
// di atasnya dokumen, dan argumen "d" selalu memaksa dokumen.
func TestXpvidSendAsDocument(t *testing.T) {
	const mb = 1 << 20
	cases := []struct {
		name    string
		size    int
		docMode bool
		want    bool
	}{
		{"kecil jadi video", 5 * mb, false, false},
		{"pas 100MB masih video", 100 * mb, false, false},
		{"lebih 1 byte jadi dokumen", 100*mb + 1, false, true},
		{"200MB jadi dokumen", 200 * mb, false, true},
		{"500MB jadi dokumen", 500 * mb, false, true},
		{"mode d memaksa dokumen", 1 * mb, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := xpvidSendAsDocument(c.size, c.docMode); got != c.want {
				t.Fatalf("xpvidSendAsDocument(%d, %v) = %v, mau %v", c.size, c.docMode, got, c.want)
			}
		})
	}
}

// TestXpvidLimits memastikan ambang tidak tertukar: batas terima 500MB dan
// ambang video 100MB.
func TestXpvidLimits(t *testing.T) {
	if xpvidMaxFileSize != 500<<20 {
		t.Fatalf("xpvidMaxFileSize = %d, mau %d", xpvidMaxFileSize, 500<<20)
	}
	if xpvidVideoMaxSize != 100<<20 {
		t.Fatalf("xpvidVideoMaxSize = %d, mau %d", xpvidVideoMaxSize, 100<<20)
	}
}
