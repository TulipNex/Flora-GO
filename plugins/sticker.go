package plugins

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"wabot-go/internal/store"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func init() {
	registerPlugin(RegisterSticker)
}

// RegisterSticker mendaftarkan command ".sticker" -> ubah gambar/video (kirim langsung
// dengan caption .sticker, atau reply ke gambar/video lalu ketik .sticker) jadi stiker
// WhatsApp (webp, statis atau animasi). Butuh ffmpeg terinstall di server tempat bot jalan.
func RegisterSticker(r *Registry) {
	r.Register(Command{
		Name:        "sticker",
		Description: "Ubah gambar/video (kirim langsung atau reply) jadi stiker",
		Handler: func(ctx context.Context, mctx *store.MessageContext) error {
			img, vid, err := extractStickerMedia(mctx)
			if err != nil {
				return store.ReplyText(ctx, mctx, "❌ "+err.Error())
			}

			var mediaBytes []byte
			isVideo := vid != nil

			if img != nil {
				mediaBytes, err = mctx.Client.Download(ctx, img)
			} else {
				mediaBytes, err = mctx.Client.Download(ctx, vid)
			}
			if err != nil {
				return store.ReplyText(ctx, mctx, "❌ Gagal download media: "+err.Error())
			}

			webpBytes, err := convertToStickerWebp(mediaBytes, isVideo)
			if err != nil {
				return store.ReplyText(ctx, mctx, "❌ Gagal convert ke stiker: "+err.Error())
			}

			uploaded, err := mctx.Client.Upload(ctx, webpBytes, whatsmeow.MediaImage)
			if err != nil {
				return store.ReplyText(ctx, mctx, "❌ Gagal upload stiker: "+err.Error())
			}

			_, err = mctx.Client.SendMessage(ctx, mctx.Chat, &waE2E.Message{
				StickerMessage: &waE2E.StickerMessage{
					URL:           proto.String(uploaded.URL),
					DirectPath:    proto.String(uploaded.DirectPath),
					MediaKey:      uploaded.MediaKey,
					Mimetype:      proto.String("image/webp"),
					FileEncSHA256: uploaded.FileEncSHA256,
					FileSHA256:    uploaded.FileSHA256,
					FileLength:    proto.Uint64(uploaded.FileLength),
				},
			})
			if err == nil {
				label := "image->sticker"
				if isVideo {
					label = "video->sticker"
				}
				store.LogOutgoing(mctx, "sticker", label)
			}
			return err
		},
	})
}

// extractStickerMedia mengambil ImageMessage/VideoMessage baik dari pesan langsung
// (kirim media dengan caption .sticker) maupun dari pesan yang di-reply (quoted).
func extractStickerMedia(mctx *store.MessageContext) (*waE2E.ImageMessage, *waE2E.VideoMessage, error) {
	msg := mctx.RawEvt.Message

	if img := msg.GetImageMessage(); img != nil {
		return img, nil, nil
	}
	if vid := msg.GetVideoMessage(); vid != nil {
		return nil, vid, nil
	}

	quoted := msg.GetExtendedTextMessage().GetContextInfo().GetQuotedMessage()
	if quoted != nil {
		if img := quoted.GetImageMessage(); img != nil {
			return img, nil, nil
		}
		if vid := quoted.GetVideoMessage(); vid != nil {
			return nil, vid, nil
		}
	}

	return nil, nil, fmt.Errorf("reply atau kirim gambar/video dengan caption .sticker")
}

// convertToStickerWebp memanggil ffmpeg untuk convert gambar/video jadi stiker webp 512x512
// dengan background transparan. Untuk video/gif, animasi dibatasi 6 detik pertama & 10fps
// supaya ukuran file stiker tetap wajar.
func convertToStickerWebp(input []byte, isVideo bool) ([]byte, error) {
	tmpDir, err := os.MkdirTemp("", "sticker-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)

	inExt := ".jpg"
	if isVideo {
		inExt = ".mp4"
	}
	inPath := filepath.Join(tmpDir, "input"+inExt)
	outPath := filepath.Join(tmpDir, "output.webp")

	if err := os.WriteFile(inPath, input, 0644); err != nil {
		return nil, err
	}

	scaleFilter := "scale=512:512:force_original_aspect_ratio=decrease,pad=512:512:(ow-iw)/2:(oh-ih)/2:color=0x00000000"

	var cmd *exec.Cmd
	if isVideo {
		cmd = exec.Command("ffmpeg",
			"-y", "-i", inPath,
			"-vcodec", "libwebp",
			"-filter:v", "fps=fps=10,"+scaleFilter,
			"-loop", "0", "-preset", "default", "-an", "-vsync", "0",
			"-ss", "0", "-t", "6",
			outPath,
		)
	} else {
		cmd = exec.Command("ffmpeg",
			"-y", "-i", inPath,
			"-vf", scaleFilter,
			"-vcodec", "libwebp",
			"-lossless", "0", "-q:v", "70", "-compression_level", "6",
			outPath,
		)
	}

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %v (%s)", err, stderr.String())
	}

	return os.ReadFile(outPath)
}
