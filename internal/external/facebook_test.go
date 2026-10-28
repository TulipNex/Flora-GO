package external

import "testing"

// TestFBKindHint memastikan tebakan jenis media dari bentuk URL Facebook
// benar untuk bentuk-bentuk yang pernah bikin masalah (.fb share/r/ dulu
// dianggap foto sehingga tidak pernah menyentuh faa).
func TestFBKindHint(t *testing.T) {
	cases := []struct {
		url  string
		want MediaKind
	}{
		{"https://www.facebook.com/share/r/1Cj7iDXM9E/", KindVideo},
		{"https://www.facebook.com/share/reel/abc123/", KindVideo},
		{"https://www.facebook.com/share/v/16abcDEF/", KindVideo},
		{"https://www.facebook.com/reel/1234567890", KindVideo},
		{"https://www.facebook.com/watch/?v=1234567890", KindVideo},
		{"https://www.facebook.com/Someone/videos/1234567890/", KindVideo},
		{"https://www.facebook.com/Someone/video/1234567890/", KindVideo},
		{"https://m.facebook.com/video.php?v=1234567890", KindVideo},
		{"https://fb.watch/abcdEFGHij/", KindVideo},

		{"https://www.facebook.com/share/p/1AbCdEfGHi/", KindPhoto},
		{"https://www.facebook.com/Someone/posts/1234567890", KindPhoto},
		{"https://www.facebook.com/photo/?fbid=1234567890", KindPhoto},
		{"https://www.facebook.com/Someone/photos/a.1/2/", KindPhoto},
	}

	for _, c := range cases {
		if got := fbKindHint(c.url); got != c.want {
			t.Errorf("fbKindHint(%q) = %q, mau %q", c.url, got, c.want)
		}
	}
}
