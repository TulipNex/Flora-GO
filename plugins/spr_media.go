package plugins

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"time"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"google.golang.org/protobuf/proto"

	"wabot-go/internal/messaging"
	"wabot-go/internal/store"
)

// sprSourceMediaMessage mengembalikan pesan yang MEMUAT media, baik dari pesan
// command itu sendiri (lampiran) maupun dari pesan yang di-reply. Nil bila
// tidak ada media. Wrapper Ephemeral/ViewOnce dibuka lebih dulu.
func sprSourceMediaMessage(mctx *store.MessageContext) *waE2E.Message {
	if mctx == nil || mctx.RawEvt == nil {
		return nil
	}
	cur := store.UnwrapMessage(mctx.RawEvt.Message)
	if sprIsMediaMessage(cur) {
		return cur
	}
	q := store.UnwrapMessage(store.ExtractQuotedMessage(mctx.RawEvt))
	if sprIsMediaMessage(q) {
		return q
	}
	return nil
}

func sprIsMediaMessage(m *waE2E.Message) bool {
	if m == nil {
		return false
	}
	return m.ImageMessage != nil || m.VideoMessage != nil ||
		m.AudioMessage != nil || m.DocumentMessage != nil ||
		m.StickerMessage != nil
}

// sprBuildMediaMessage mengunduh → upload → merakit *waE2E.Message media
// (image/video/audio/document/sticker) dengan caption opsional. Dipakai SPR
// agar bisa mengirim media secara selektif, bukan hanya teks.
func sprBuildMediaMessage(ctx context.Context, mctx *store.MessageContext, src *waE2E.Message, caption string) (*waE2E.Message, error) {
	client := mctx.Client

	switch {
	case src.ImageMessage != nil:
		img := src.ImageMessage
		data, err := client.Download(ctx, img)
		if err != nil {
			return nil, fmt.Errorf("gagal unduh gambar: %v", err)
		}
		if len(data) == 0 {
			return nil, fmt.Errorf("gambar kosong")
		}
		uploaded, err := client.Upload(ctx, data, whatsmeow.MediaImage)
		if err != nil {
			return nil, fmt.Errorf("gagal upload gambar: %v", err)
		}
		mime := img.GetMimetype()
		if mime == "" {
			mime = "image/jpeg"
		}
		out := &waE2E.ImageMessage{
			URL:               proto.String(uploaded.URL),
			DirectPath:        proto.String(uploaded.DirectPath),
			MediaKey:          uploaded.MediaKey,
			MediaKeyTimestamp: proto.Int64(time.Now().Unix()),
			Mimetype:          proto.String(mime),
			FileEncSHA256:     uploaded.FileEncSHA256,
			FileSHA256:        uploaded.FileSHA256,
			FileLength:        proto.Uint64(uploaded.FileLength),
		}
		if caption != "" {
			out.Caption = proto.String(caption)
		}
		if thumb := messaging.InlineThumbForImage(data); len(thumb) > 0 {
			out.JPEGThumbnail = thumb
		}
		if cfg, _, cerr := image.DecodeConfig(bytes.NewReader(data)); cerr == nil && cfg.Width > 0 {
			out.Width = proto.Uint32(uint32(cfg.Width))
			out.Height = proto.Uint32(uint32(cfg.Height))
		}
		return &waE2E.Message{ImageMessage: out}, nil

	case src.VideoMessage != nil:
		vid := src.VideoMessage
		data, err := client.Download(ctx, vid)
		if err != nil {
			return nil, fmt.Errorf("gagal unduh video: %v", err)
		}
		if len(data) == 0 {
			return nil, fmt.Errorf("video kosong")
		}
		uploaded, err := client.Upload(ctx, data, whatsmeow.MediaVideo)
		if err != nil {
			return nil, fmt.Errorf("gagal upload video: %v", err)
		}
		mime := vid.GetMimetype()
		if mime == "" {
			mime = "video/mp4"
		}
		out := &waE2E.VideoMessage{
			URL:               proto.String(uploaded.URL),
			DirectPath:        proto.String(uploaded.DirectPath),
			MediaKey:          uploaded.MediaKey,
			MediaKeyTimestamp: proto.Int64(time.Now().Unix()),
			Mimetype:          proto.String(mime),
			FileEncSHA256:     uploaded.FileEncSHA256,
			FileSHA256:        uploaded.FileSHA256,
			FileLength:        proto.Uint64(uploaded.FileLength),
		}
		if caption != "" {
			out.Caption = proto.String(caption)
		}
		if thumb := messaging.InlineThumbForVideo(ctx, data); len(thumb) > 0 {
			out.JPEGThumbnail = thumb
		}
		return &waE2E.Message{VideoMessage: out}, nil

	case src.AudioMessage != nil:
		aud := src.AudioMessage
		data, err := client.Download(ctx, aud)
		if err != nil {
			return nil, fmt.Errorf("gagal unduh audio: %v", err)
		}
		if len(data) == 0 {
			return nil, fmt.Errorf("audio kosong")
		}
		uploaded, err := client.Upload(ctx, data, whatsmeow.MediaAudio)
		if err != nil {
			return nil, fmt.Errorf("gagal upload audio: %v", err)
		}
		mime := aud.GetMimetype()
		if mime == "" {
			mime = "audio/ogg; codecs=opus"
		}
		out := &waE2E.AudioMessage{
			URL:               proto.String(uploaded.URL),
			DirectPath:        proto.String(uploaded.DirectPath),
			MediaKey:          uploaded.MediaKey,
			MediaKeyTimestamp: proto.Int64(time.Now().Unix()),
			Mimetype:          proto.String(mime),
			FileEncSHA256:     uploaded.FileEncSHA256,
			FileSHA256:        uploaded.FileSHA256,
			FileLength:        proto.Uint64(uploaded.FileLength),
			PTT:               proto.Bool(aud.GetPTT()),
		}
		// Audio tidak mendukung caption di WhatsApp; caption diabaikan.
		return &waE2E.Message{AudioMessage: out}, nil

	case src.DocumentMessage != nil:
		doc := src.DocumentMessage
		data, err := client.Download(ctx, doc)
		if err != nil {
			return nil, fmt.Errorf("gagal unduh dokumen: %v", err)
		}
		if len(data) == 0 {
			return nil, fmt.Errorf("dokumen kosong")
		}
		uploaded, err := client.Upload(ctx, data, whatsmeow.MediaDocument)
		if err != nil {
			return nil, fmt.Errorf("gagal upload dokumen: %v", err)
		}
		mime := doc.GetMimetype()
		if mime == "" {
			mime = "application/octet-stream"
		}
		name := doc.GetFileName()
		if name == "" {
			name = "document"
		}
		out := &waE2E.DocumentMessage{
			URL:               proto.String(uploaded.URL),
			DirectPath:        proto.String(uploaded.DirectPath),
			MediaKey:          uploaded.MediaKey,
			MediaKeyTimestamp: proto.Int64(time.Now().Unix()),
			Mimetype:          proto.String(mime),
			FileEncSHA256:     uploaded.FileEncSHA256,
			FileSHA256:        uploaded.FileSHA256,
			FileLength:        proto.Uint64(uploaded.FileLength),
			FileName:          proto.String(name),
		}
		if caption != "" {
			out.Caption = proto.String(caption)
		}
		return &waE2E.Message{DocumentMessage: out}, nil

	case src.StickerMessage != nil:
		st := src.StickerMessage
		data, err := client.Download(ctx, st)
		if err != nil {
			return nil, fmt.Errorf("gagal unduh stiker: %v", err)
		}
		if len(data) == 0 {
			return nil, fmt.Errorf("stiker kosong")
		}
		uploaded, err := client.Upload(ctx, data, whatsmeow.MediaImage)
		if err != nil {
			return nil, fmt.Errorf("gagal upload stiker: %v", err)
		}
		mime := st.GetMimetype()
		if mime == "" {
			mime = "image/webp"
		}
		out := &waE2E.StickerMessage{
			URL:               proto.String(uploaded.URL),
			DirectPath:        proto.String(uploaded.DirectPath),
			MediaKey:          uploaded.MediaKey,
			MediaKeyTimestamp: proto.Int64(time.Now().Unix()),
			Mimetype:          proto.String(mime),
			FileEncSHA256:     uploaded.FileEncSHA256,
			FileSHA256:        uploaded.FileSHA256,
			FileLength:        proto.Uint64(uploaded.FileLength),
		}
		return &waE2E.Message{StickerMessage: out}, nil
	}

	return nil, fmt.Errorf("media tidak didukung")
}

// sprIsMediaSource true bila pesan sumber memuat media yang bisa dikirim.
func sprIsMediaSource(mctx *store.MessageContext) bool {
	return sprSourceMediaMessage(mctx) != nil
}

