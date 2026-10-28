package textx

import "testing"

// BenchmarkTruncateRunes mengukur pemotongan rune (dipakai caption pesan panjang).
func BenchmarkTruncateRunes(b *testing.B) {
	s := "Halo, ini teks panjang yang akan dipotong rune demi rune. " +
		"Lorem ipsum dolor sit amet, consectetur adipiscing elit. " +
		"Ulangi lagi supaya lebih panjang dari batas."
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _, _ = TruncateRunes(s, 120, true)
	}
}
