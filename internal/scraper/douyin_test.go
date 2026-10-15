package scraper

import "testing"

func TestDouyinFindURL(t *testing.T) {
	cases := map[string]string{
		"cek https://vt.tiktok.com/AbC123/ mantap": "https://vt.tiktok.com/AbC123/",
		"https://www.tiktok.com/@user/video/123456": "https://www.tiktok.com/@user/video/123456",
		"cuma keyword elaina edit":                  "",
	}
	for in, want := range cases {
		if got := DouyinFindURL(in); got != want {
			t.Fatalf("input %q: dapat %q, mau %q", in, got, want)
		}
	}
}

func TestFormatDouyinCount(t *testing.T) {
	cases := map[int64]string{0: "0", 12: "12", 1234: "1,234", 1234567: "1,234,567"}
	for in, want := range cases {
		if got := FormatDouyinCount(in); got != want {
			t.Fatalf("%d: dapat %q, mau %q", in, got, want)
		}
	}
}

func TestFormatDouyinDuration(t *testing.T) {
	cases := map[int64]string{0: "00:00", 65: "01:05", 600: "10:00", -3: "00:00"}
	for in, want := range cases {
		if got := FormatDouyinDuration(in); got != want {
			t.Fatalf("%d: dapat %q, mau %q", in, got, want)
		}
	}
}

func TestDouyinAuthor(t *testing.T) {
	if got := douyinAuthor(map[string]any{"nickname": "Udin"}); got != "Udin" {
		t.Fatalf("nickname: %q", got)
	}
	if got := douyinAuthor("SongArtist"); got != "SongArtist" {
		t.Fatalf("string: %q", got)
	}
	if got := douyinAuthor(nil); got != "-" {
		t.Fatalf("nil: %q", got)
	}
}
