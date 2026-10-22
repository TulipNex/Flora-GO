package messaging

import (
	"bytes"
	"context"
	"image"
	"net/http"
	"strings"
	"time"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

// imageMIME menebak MIME dari magic bytes (webp/gif dikenali net/http, sisanya
// jatuh ke image/jpeg karena bubble gambar bot hampir selalu JPEG).
func imageMIME(data []byte) string {
	if mt := http.DetectContentType(data); strings.HasPrefix(mt, "image/") {
		return mt
	}
	return "image/jpeg"
}

// UploadImageMessage meng-upload byte gambar ke server WhatsApp lalu merakit
// ImageMessage lengkap: URL, DirectPath, MediaKey (+timestamp), sha256,
// FileLength, dimensi, dan thumbnail inline kecil.
//
// Dipakai dua tempat: bubble gambar biasa (downloader) dan header gambar
// InteractiveMessage (kartu tombol). Header native_flow WAJIB memakai
// ImageMessage hasil upload — klien mengabaikan `Header.JPEGThumbnail`
// (hanya bitmap preview, bukan media yang bisa dimuat) sehingga header tampak
// kosong walau body + tombol tetap terkirim.
func UploadImageMessage(ctx context.Context, client *whatsmeow.Client, data []byte) (*waE2E.ImageMessage, error) {
	if err := CheckMediaBytes(data); err != nil {
		return nil, err
	}
	uploaded, err := client.Upload(ctx, data, whatsmeow.MediaImage)
	if err != nil {
		return nil, err
	}
	im := &waE2E.ImageMessage{
		URL:               proto.String(uploaded.URL),
		DirectPath:        proto.String(uploaded.DirectPath),
		MediaKey:          uploaded.MediaKey,
		MediaKeyTimestamp: proto.Int64(time.Now().Unix()),
		Mimetype:          proto.String(imageMIME(data)),
		FileEncSHA256:     uploaded.FileEncSHA256,
		FileSHA256:        uploaded.FileSHA256,
		FileLength:        proto.Uint64(uploaded.FileLength),
		JPEGThumbnail:     InlineThumbForImage(data),
	}
	if cfg, _, cerr := image.DecodeConfig(bytes.NewReader(data)); cerr == nil && cfg.Width > 0 {
		im.Width = proto.Uint32(uint32(cfg.Width))
		im.Height = proto.Uint32(uint32(cfg.Height))
	}
	return im, nil
}
