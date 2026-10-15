package messaging

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"strings"
	"time"

	"wabot-go/internal/store"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

// sendurl.go — helper "kirim-via-URL" ala Baileys sendFile: fetch URL +
// upload + kirim dalam satu panggilan. Protokol WA tetap butuh upload
// bytes (tidak ada kirim-URL murni) — bedanya langkah fetch disembunyikan
// di sini, bukan di tiap plugin. Cap + timeout + UA browser standar repo.

// sendURLMaxBytes batas fetch per file (keputusan owner, sama dengan
// batas kirim media bot 100MB).
const sendURLMaxBytes = 100 << 20

const sendURLTimeout = 90 * time.Second

const sendURLUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36"

// FetchBytes mengunduh URL menjadi bytes (maks maxBytes, error bila lebih).
// Kembalian: data + content-type server (boleh kosong).
func FetchBytes(ctx context.Context, url string, maxBytes int64) ([]byte, string, error) {
	tctx, cancel := context.WithTimeout(ctx, sendURLTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(tctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", sendURLUA)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(data)) > maxBytes {
		return nil, "", fmt.Errorf("file %.1fMB melebihi batas %.0fMB", float64(len(data))/(1<<20), float64(maxBytes)/(1<<20))
	}
	return data, resp.Header.Get("Content-Type"), nil
}

// uploadBytes mengupload bytes sebagai tipe media WA.
func uploadBytes(ctx context.Context, mctx *store.MessageContext, data []byte, kind whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
	return mctx.Client.Upload(ctx, data, kind)
}

// SendURLAsImage mengunduh URL gambar lalu mengirimnya + caption.
func SendURLAsImage(ctx context.Context, mctx *store.MessageContext, url, caption string) error {
	data, _, err := FetchBytes(ctx, url, sendURLMaxBytes)
	if err != nil {
		return fmt.Errorf("unduh gambar: %w", err)
	}
	if err := CheckMediaBytes(data); err != nil {
		return err
	}
	uploaded, err := uploadBytes(ctx, mctx, data, whatsmeow.MediaImage)
	if err != nil {
		return err
	}
	mime := "image/jpeg"
	if len(data) > 3 && string(data[1:4]) == "PNG" {
		mime = "image/png"
	}
	im := &waE2E.ImageMessage{
		URL:            proto.String(uploaded.URL),
		DirectPath:     proto.String(uploaded.DirectPath),
		MediaKey:       uploaded.MediaKey,
		Mimetype:       proto.String(mime),
		FileEncSHA256:  uploaded.FileEncSHA256,
		FileSHA256:     uploaded.FileSHA256,
		FileLength:     proto.Uint64(uploaded.FileLength),
		Caption:        proto.String(caption),
		JPEGThumbnail:  InlineThumbForImage(data),
	}
	if cfg, _, cerr := image.DecodeConfig(bytes.NewReader(data)); cerr == nil && cfg.Width > 0 {
		im.Width = proto.Uint32(uint32(cfg.Width))
		im.Height = proto.Uint32(uint32(cfg.Height))
	}
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, &waE2E.Message{ImageMessage: im})
	if err != nil {
		return err
	}
	store.LogOutgoing(mctx, "url-image", caption)
	store.TrackBotMessageID(resp.ID, caption)
	return nil
}

// SendURLAsVideo mengunduh URL video lalu mengirimnya + caption + nama file.
// thumb (boleh kosong = tanpa preview) adalah bytes gambar cover yang
// dijadikan JPEGThumbnail mini — cara murah (tanpa ffmpeg) agar bubble
// video tampil preview, bukan kotak abu-abu.
// Tanpa transcode (pengirim URL dianggap sudah ramah-putar, cermin Baileys);
// untuk video asing yang butuh normalisasi pakai jalur downloader.
func SendURLAsVideo(ctx context.Context, mctx *store.MessageContext, url, caption, filename string, thumb []byte) error {
	data, _, err := FetchBytes(ctx, url, sendURLMaxBytes)
	if err != nil {
		return fmt.Errorf("unduh video: %w", err)
	}
	if err := CheckMediaBytes(data); err != nil {
		return err
	}
	if strings.TrimSpace(filename) == "" {
		filename = "video.mp4"
	}
	uploaded, err := uploadBytes(ctx, mctx, data, whatsmeow.MediaVideo)
	if err != nil {
		return err
	}
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, &waE2E.Message{
		VideoMessage: &waE2E.VideoMessage{
			URL:            proto.String(uploaded.URL),
			DirectPath:     proto.String(uploaded.DirectPath),
			MediaKey:       uploaded.MediaKey,
			Mimetype:       proto.String("video/mp4"),
			FileEncSHA256:  uploaded.FileEncSHA256,
			FileSHA256:     uploaded.FileSHA256,
			FileLength:     proto.Uint64(uploaded.FileLength),
			Caption:        proto.String(caption),
			JPEGThumbnail:  InlineThumbForImage(thumb),
		},
	})
	if err != nil {
		return err
	}
	store.LogOutgoing(mctx, "url-video", caption)
	store.TrackBotMessageID(resp.ID, caption)
	return nil
}

// SendURLAsAudio mengunduh URL audio lalu mengirim sebagai file audio
// (bukan voice note) + nama file.
func SendURLAsAudio(ctx context.Context, mctx *store.MessageContext, url, filename string) error {
	data, _, err := FetchBytes(ctx, url, sendURLMaxBytes)
	if err != nil {
		return fmt.Errorf("unduh audio: %w", err)
	}
	if err := CheckMediaBytes(data); err != nil {
		return err
	}
	if strings.TrimSpace(filename) == "" {
		filename = "audio.mp3"
	}
	uploaded, err := uploadBytes(ctx, mctx, data, whatsmeow.MediaAudio)
	if err != nil {
		return err
	}
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, &waE2E.Message{
		AudioMessage: &waE2E.AudioMessage{
			URL:            proto.String(uploaded.URL),
			DirectPath:     proto.String(uploaded.DirectPath),
			MediaKey:       uploaded.MediaKey,
			Mimetype:       proto.String("audio/mpeg"),
			FileEncSHA256:  uploaded.FileEncSHA256,
			FileSHA256:     uploaded.FileSHA256,
			FileLength:     proto.Uint64(uploaded.FileLength),
			PTT:            proto.Bool(false),
		},
	})
	if err != nil {
		return err
	}
	store.LogOutgoing(mctx, "url-audio", filename)
	store.TrackBotMessageID(resp.ID, filename)
	return nil
}
