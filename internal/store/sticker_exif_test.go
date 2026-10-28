package store

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// tinyWebpB64 adalah webp VP8L 1x1 (dipakai sebagai fixture uji, bukan
// output ffmpeg). Cukup valid untuk parseWebpRiff + WriteStickerExif.
const tinyWebpB64 = "UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA"

func TestWriteStickerExifOmitsPublisherWhenEmpty(t *testing.T) {
	raw, err := base64.StdEncoding.DecodeString(tinyWebpB64)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name    string
		author  string
		wantPub bool
	}{
		{"no author", "", false},
		{"with author", "Tim", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := WriteStickerExif(raw, "Mitra", tc.author)
			if err != nil {
				t.Fatal(err)
			}
			chunks, err := parseWebpRiff(out)
			if err != nil {
				t.Fatal(err)
			}
			var exif []byte
			for _, c := range chunks {
				if c.fourCC == "EXIF" {
					exif = c.data
				}
			}
			if exif == nil {
				t.Fatal("chunk EXIF tidak ditemukan")
			}
			if len(exif) < len(exifHeader) {
				t.Fatalf("chunk EXIF terlalu pendek: %d", len(exif))
			}
			body := exif[len(exifHeader):]
			var payload stickerExifPayload
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatalf("gagal parse JSON EXIF: %v (body=%q)", err, string(body))
			}
			if payload.PackName != "Mitra" {
				t.Fatalf("PackName=%q, want Mitra", payload.PackName)
			}
			hasPub := strings.Contains(string(body), "sticker-pack-publisher")
			if hasPub != tc.wantPub {
				t.Fatalf("keberadaan publisher=%v, want %v (json=%s)", hasPub, tc.wantPub, string(body))
			}
			if tc.wantPub && payload.Publisher != "Tim" {
				t.Fatalf("Publisher=%q, want Tim", payload.Publisher)
			}
		})
	}
}

func TestWriteStickerExifReplacesOldExif(t *testing.T) {
	raw, err := base64.StdEncoding.DecodeString(tinyWebpB64)
	if err != nil {
		t.Fatal(err)
	}
	once, err := WriteStickerExif(raw, "A", "B")
	if err != nil {
		t.Fatal(err)
	}
	twice, err := WriteStickerExif(once, "C", "")
	if err != nil {
		t.Fatal(err)
	}
	chunks, err := parseWebpRiff(twice)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, c := range chunks {
		if c.fourCC == "EXIF" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("jumlah chunk EXIF=%d, want 1", count)
	}
	// pastikan nama terakhir yang menang
	for _, c := range chunks {
		if c.fourCC == "EXIF" && len(c.data) >= len(exifHeader) {
			var p stickerExifPayload
			_ = json.Unmarshal(c.data[len(exifHeader):], &p)
			if p.PackName != "C" {
				t.Fatalf("PackName=%q, want C", p.PackName)
			}
		}
	}
}