package plugins

import (
	"testing"

	"wabot-go/internal/external"
)

// TestResolveMediaKind memastikan sniff byte menang atas tebakan Kind dari
// adapter (faa tidak membedakan foto/video), dan tidak merusak audio maupun
// byte tak dikenal.
func TestResolveMediaKind(t *testing.T) {
	jpeg := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0x01}
	png := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	gif := []byte("GIF89a")
	webp := []byte{'R', 'I', 'F', 'F', 0x24, 0x00, 0x00, 0x00, 'W', 'E', 'B', 'P', 'V', 'P', '8', ' '}
	webm := []byte{0x1A, 0x45, 0xDF, 0xA3, 0x9F, 0x42, 0x86, 0x81, 0x01, 0x42, 0xF7, 0x81, 0x01, 0x42, 0xF2, 0x81}
	// MP4: box size 0x18 (harus == len(data) dan kelipatan 4), brand mp42.
	mp4 := []byte{0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p', 'm', 'p', '4', '2',
		0x00, 0x00, 0x00, 0x00, 'i', 's', 'o', 'm', 'm', 'p', '4', '2'}
	plain := []byte("<html><body>bukan media</body></html>")

	cases := []struct {
		name string
		data []byte
		kind external.MediaKind
		want external.MediaKind
	}{
		{"adapter bilang video, byte JPEG", jpeg, external.KindVideo, external.KindPhoto},
		{"adapter bilang video, byte PNG", png, external.KindVideo, external.KindPhoto},
		{"adapter bilang video, byte GIF", gif, external.KindVideo, external.KindPhoto},
		{"adapter bilang video, byte WebP", webp, external.KindVideo, external.KindPhoto},
		{"adapter bilang foto, byte MP4", mp4, external.KindPhoto, external.KindVideo},
		{"adapter bilang foto, byte WebM", webm, external.KindPhoto, external.KindVideo},
		{"adapter bilang foto, byte JPEG (tetap)", jpeg, external.KindPhoto, external.KindPhoto},
		{"adapter bilang video, byte MP4 (tetap)", mp4, external.KindVideo, external.KindVideo},
		{"audio tidak di-sniff", mp4, external.KindAudio, external.KindAudio},
		{"byte tak dikenal pertahankan tebakan", plain, external.KindVideo, external.KindVideo},
		{"byte kosong pertahankan tebakan", nil, external.KindPhoto, external.KindPhoto},
	}

	for _, c := range cases {
		if got := resolveMediaKind(c.data, c.kind); got != c.want {
			t.Errorf("%s: resolveMediaKind = %q, mau %q", c.name, got, c.want)
		}
	}
}
