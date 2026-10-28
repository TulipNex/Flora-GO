package plugins

import "testing"

func TestParseStickerWatermarkArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		pack    string
		author  string
	}{
		{"single", []string{"Mitra"}, "Mitra", ""},
		{"full", []string{"Mitra", "|", "By", "Mitra"}, "Mitra", "By Mitra"},
		{"joined spaces", []string{"Flora", "GO|Mitra", "Wann"}, "Flora GO", "Mitra Wann"},
		{"empty author pipe", []string{"Mitra|"}, "Mitra", ""},
		{"empty pack pipe", []string{"|Tim"}, "Flora GO", "Tim"},
		{"trim spaces", []string{"  Mitra  |  Tim  "}, "Mitra", "Tim"},
		{"no args", []string{}, "Flora GO", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pack, author := parseStickerWatermarkArgs(c.args)
			if pack != c.pack || author != c.author {
				t.Fatalf("got (%q,%q), want (%q,%q)", pack, author, c.pack, c.author)
			}
		})
	}
}