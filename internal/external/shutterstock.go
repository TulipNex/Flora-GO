package external

// Helper Shutterstock untuk command .shutterstock: ekstrak ID aset, lalu
// unduh gambar resolusi tinggi lewat perantara steptodown (lihat steptodown.go).
//
// Kenapa lewat perantara: halaman HTML Shutterstock dilindungi DataDome dan
// membalas 403 untuk fetch server-side (tanpa JS), sedangkan preview CDN
// (image.shutterstock.com) berwatermark + ditempeli bar footer. Endpoint
// shutterstock-downloader milik steptodown mengembalikan gambar BERSIH (tanpa
// watermark & tanpa footer) berukuran ±1600px lebar.

import (
	"context"
	"fmt"
	"strings"
)

// ShutterstockExtractID mengambil ID aset (angka) dari URL halaman
// Shutterstock ATAU dari string ID mentah ("2583067397"). Mengembalikan ""
// bila tidak ada ID yang bisa dipakai.
//
// Contoh URL yang diterima:
//
//	https://www.shutterstock.com/id/image-photo/kendari-...-2583067397?trackingId=...
//	https://www.shutterstock.com/image-vector/icon-set-1234567890
func ShutterstockExtractID(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if strings.Contains(strings.ToLower(s), "shutterstock.com") {
		return shutterIDFromURL(s)
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return s
}

// shutterIDFromURL mengambil rangkaian digit terakhir di URL yang didahului
// pemisah '-' atau '/' (ID aset selalu muncul di belakang, dipisah '-'),
// jadi URL halaman maupun URL file .jpg sama-sama dikenali. Query & fragment
// dibuang lebih dulu.
func shutterIDFromURL(s string) string {
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	for end := len(s); end > 0; {
		if s[end-1] < '0' || s[end-1] > '9' {
			end--
			continue
		}
		start := end
		for start > 0 && s[start-1] >= '0' && s[start-1] <= '9' {
			start--
		}
		if start > 0 {
			if sep := s[start-1]; (sep == '-' || sep == '/') && end-start >= 5 {
				return s[start:end]
			}
		}
		end = start - 1
	}
	return ""
}

// ShutterstockFetch mengunduh byte gambar bersih dari sebuah ID aset
// Shutterstock lewat perantara steptodown. Mengembalikan error bila ID tidak
// dikenali Shutterstock atau jaringan gagal.
func ShutterstockFetch(ctx context.Context, id string) ([]byte, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("ID aset kosong")
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return nil, fmt.Errorf("ID aset harus angka")
		}
	}
	return SteptodownImage(ctx, "shutterstock-downloader", id)
}
