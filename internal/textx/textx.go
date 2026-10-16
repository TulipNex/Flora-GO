// Package textx menampung helper teks kecil lintas paket.
//
// Leaf package: NOL import dari repo sendiri.
package textx

import "strings"

// TruncateRunes memotong s ke paling banyak max rune (aman multi-byte).
// Mengembalikan:
//   - out   : teks hasil (di-TrimSpace dulu bila trim=true)
//   - total : jumlah rune SEBELUM dipotong (untuk pesan "...total N karakter")
//   - cut   : true bila teks memang terpotong
//
// Dipusatkan di sini — sebelumnya pola pemotongan rune ini disalin ulang
// (dengan variasi trim/suffix) di plugins/docread.go, plugins/ownerexec.go,
// plugins/ownerget.go, dan internal/external/omnify.go.
func TruncateRunes(s string, max int, trim bool) (out string, total int, cut bool) {
	if trim {
		s = strings.TrimSpace(s)
	}
	runes := []rune(s)
	total = len(runes)
	if total <= max {
		return string(runes), total, false
	}
	if max < 0 {
		max = 0
	}
	return string(runes[:max]), total, true
}
