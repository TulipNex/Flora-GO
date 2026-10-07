package scraper

// Helper Flaticon untuk .flaticon: ekstrak ID + rakit link CDN semua ukuran.
// Tanpa HTTP keluar (CDN deterministik, pola terverifikasi live) — tercepat
// dan bebas blokir. Fungsi search/unduh dihapus (keputusan owner: output
// berupa daftar link, bukan file).

import (
	"fmt"
	"regexp"
	"strings"
)

// flaticonSizes adalah ukuran valid situs (kecil→besar untuk daftar).
var flaticonSizes = []int{16, 24, 32, 64, 128, 256, 512}

// flaticonPageRe mengekstrak ID dari link halaman ikon:
// flaticon.com/free-icon/<slug>_<id> (id digit di akhir).
var flaticonPageRe = regexp.MustCompile(`flaticon\.com/(?:\w+/)*free-icon/[A-Za-z0-9_-]*_(\d+)`)

// FlaticonExtractID mengambil ID angka dari link halaman ikon atau
// string ID mentah ("1234567"). "" bila bukan keduanya.
func FlaticonExtractID(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if m := flaticonPageRe.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return s
}

// FlaticonCdnURL merakit link CDN dari ID + ukuran (validasi ukuran).
// Pola terverifikasi live: cdn-icons-png.flaticon.com/{size}/{4digit}/{id}.png
func FlaticonCdnURL(id string, size int) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", fmt.Errorf("ID ikon kosong")
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return "", fmt.Errorf("ID ikon harus angka")
		}
	}
	ok := false
	for _, s := range flaticonSizes {
		if s == size {
			ok = true
			break
		}
	}
	if !ok {
		return "", fmt.Errorf("ukuran harus salah satu dari 16/24/32/64/128/256/512")
	}
	prefix := id
	if len(prefix) > 4 {
		prefix = prefix[:4]
	}
	return fmt.Sprintf("https://cdn-icons-png.flaticon.com/%d/%s/%s.png", size, prefix, id), nil
}

// FlaticonAllSizes mengembalikan link CDN semua ukuran untuk satu ID.
func FlaticonAllSizes(id string) ([]string, error) {
	urls := make([]string, 0, len(flaticonSizes))
	for _, size := range flaticonSizes {
		u, err := FlaticonCdnURL(id, size)
		if err != nil {
			return nil, err
		}
		urls = append(urls, u)
	}
	return urls, nil
}
