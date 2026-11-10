package plugins

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"wabot-go/internal/config"
	"wabot-go/internal/heavy"
	"wabot-go/internal/logfmt"
	"wabot-go/internal/store"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
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
		Aliases:     []string{"s"},
		Description: "Ubah gambar/video (kirim langsung atau reply) jadi stiker",
		Category:    CategoryMedia,
		Handler: func(ctx context.Context, mctx *store.MessageContext) error {
			img, vid, err := extractStickerMedia(mctx)
			if err != nil {
				return store.ReplyText(ctx, mctx, err.Error())
			}

			var mediaBytes []byte
			isVideo := vid != nil

			if img != nil {
				mediaBytes, err = mctx.Client.Download(ctx, img)
			} else {
				mediaBytes, err = mctx.Client.Download(ctx, vid)
			}
			if err != nil {
				logfmt.Warn("sticker: download gagal (%v)\n", err)
				return store.ReplyText(ctx, mctx, ErrMsg("Stiker", "Gagal unduh media, coba lagi sebentar."))
			}

			webpBytes, err := convertToStickerWebp(ctx, mediaBytes, isVideo)
			if err != nil {
				logfmt.Warn("sticker: convert gagal (%v)\n", err)
				return store.ReplyText(ctx, mctx, ErrMsg("Stiker", "Gagal convert ke stiker, coba file lain."))
			}

			author := config.ResolveStickerAuthor(mctx.SenderPushName())
			webpBytes, err = store.WriteStickerExif(webpBytes, config.ResolveStickerPackname(), author)
			if err != nil {
				logfmt.Warn("sticker: exif gagal (%v)\n", err)
				return store.ReplyText(ctx, mctx, ErrMsg("Stiker", "Gagal siapkan stiker, coba lagi."))
			}

			label := "image->sticker"
			if isVideo {
				label = "video->sticker"
			}
			if err := sendStickerWebp(ctx, mctx, webpBytes, "sticker", label); err != nil {
				logfmt.Warn("sticker: kirim gagal (%v)\n", err)
				return store.ReplyText(ctx, mctx, ErrMsg("Stiker", "Gagal kirim stiker, coba lagi."))
			}
			return nil
		},
	})
}

// BuildAndSendSticker membuat stiker dari gambar/video di pesan pemicu
// (kirim langsung + caption, atau yang di-reply) lalu mengirimkannya ke
// chat — logic yang SAMA PERSIS dengan command .sticker, dibungkus untuk
// dipakai ulang oleh tool AI make_sticker. packname/author kosong berarti
// pakai default config. Butuh ffmpeg di server, error-nya diteruskan agar
// AI bisa menyampaikannya ke user.
func BuildAndSendSticker(ctx context.Context, mctx *store.MessageContext, packname, author string) error {
	if strings.TrimSpace(packname) == "" {
		packname = config.ResolveStickerPackname()
	}
	if strings.TrimSpace(author) == "" {
		author = config.ResolveStickerAuthor(mctx.SenderPushName())
	} else if strings.EqualFold(author, "@user") {
		author = mctx.SenderPushName()
	}

	img, vid, err := extractStickerMedia(mctx)
	if err != nil {
		return err
	}

	var mediaBytes []byte
	isVideo := vid != nil
	if img != nil {
		mediaBytes, err = mctx.Client.Download(ctx, img)
	} else {
		mediaBytes, err = mctx.Client.Download(ctx, vid)
	}
	if err != nil {
		logfmt.Warn("sticker(tool): download gagal (%v)\n", err)
		return fmt.Errorf("gagal download media, coba lagi sebentar")
	}

	webpBytes, err := convertToStickerWebp(ctx, mediaBytes, isVideo)
	if err != nil {
		logfmt.Warn("sticker(tool): convert gagal (%v)\n", err)
		return fmt.Errorf("gagal convert ke stiker, coba file lain")
	}

	webpBytes, err = store.WriteStickerExif(webpBytes, packname, author)
	if err != nil {
		logfmt.Warn("sticker(tool): exif gagal (%v)\n", err)
		return fmt.Errorf("gagal siapkan stiker, coba lagi")
	}

	label := "image->sticker"
	if isVideo {
		label = "video->sticker"
	}
	if err := sendStickerWebp(ctx, mctx, webpBytes, "sticker", label); err != nil {
		logfmt.Warn("sticker(tool): kirim gagal (%v)\n", err)
		return fmt.Errorf("gagal kirim stiker, coba lagi")
	}
	return nil
}

// sendStickerWebp mengupload webp stiker lalu mengirimkannya ke chat.
//
// Blok upload+kirim ini sebelumnya diduplikasi di sticker.go dan
// stickerwatermark.go (audit duplikasi A3), dan duplikasinya sudah melahirkan
// bug nyata: .swm lupa mengisi Width/Height sehingga stiker hasil .swm bisa
// dirender Android dengan latar hitam solid. Sekarang satu helper, satu
// perilaku, dipanggil kedua command.
//
// logType/logLabel hanya untuk LogOutgoing (mis. "sticker"/"image->sticker"
// atau "stickerwatermark"/"packname=... author=...").
func sendStickerWebp(ctx context.Context, mctx *store.MessageContext, webp []byte, logType, logLabel string) error {
	uploaded, err := mctx.Client.Upload(ctx, webp, whatsmeow.MediaImage)
	if err != nil {
		return fmt.Errorf("upload: %w", err)
	}

	// Width/Height WAJIB diisi eksplisit — kalau kosong, klien WhatsApp
	// (terutama Android) sering salah render area transparan dan menggambar
	// latar hitam solid alih-alih transparan, walau file webp-nya sendiri
	// sudah benar. Canvas selalu 512x512 oleh convertToStickerWebp (pad
	// transparan), jadi tidak perlu dibaca dinamis dari file.
	_, err = mctx.Client.SendMessage(ctx, mctx.Chat, &waE2E.Message{
		StickerMessage: &waE2E.StickerMessage{
			URL:           proto.String(uploaded.URL),
			DirectPath:    proto.String(uploaded.DirectPath),
			MediaKey:      uploaded.MediaKey,
			Mimetype:      proto.String("image/webp"),
			FileEncSHA256: uploaded.FileEncSHA256,
			FileSHA256:    uploaded.FileSHA256,
			FileLength:    proto.Uint64(uploaded.FileLength),
			Width:         proto.Uint32(512),
			Height:        proto.Uint32(512),
		},
	})
	if err != nil {
		return fmt.Errorf("kirim: %w", err)
	}

	store.LogOutgoing(mctx, logType, logLabel)
	return nil
}

// maxStickerVideoSeconds adalah durasi maksimum video untuk stiker
// (keputusan owner): convert video panjang menahan CPU/RAM panel lama —
// tolak di depan via metadata WhatsApp (tanpa download/ffprobe). 0 =
// durasi tak diketahui (metadata kosong) = diloloskan.
const maxStickerVideoSeconds = 10

// extractStickerMedia mengambil ImageMessage/VideoMessage baik dari pesan langsung
// (kirim media dengan caption .sticker) maupun dari pesan yang di-reply (quoted).
//
// Pesan di-unwrap dulu dan quoted diambil lewat ExtractQuotedMessage — satu
// jalur ContextInfo bersama (audit A2), supaya tetap jalan di grup
// ber-Pesan-Sementara / pesan sekali-lihat.
func extractStickerMedia(mctx *store.MessageContext) (*waE2E.ImageMessage, *waE2E.VideoMessage, error) {
	msg := store.UnwrapMessage(mctx.RawEvt.Message)
	if msg != nil {
		if img := msg.GetImageMessage(); img != nil {
			return img, nil, nil
		}
		if vid := msg.GetVideoMessage(); vid != nil {
			if vid.GetSeconds() > maxStickerVideoSeconds {
				return nil, nil, fmt.Errorf("video kepanjangan (maks %d detik untuk stiker)", maxStickerVideoSeconds)
			}
			return nil, vid, nil
		}
	}

	if quoted := store.UnwrapMessage(store.ExtractQuotedMessage(mctx.RawEvt)); quoted != nil {
		if img := quoted.GetImageMessage(); img != nil {
			return img, nil, nil
		}
		if vid := quoted.GetVideoMessage(); vid != nil {
			if vid.GetSeconds() > maxStickerVideoSeconds {
				return nil, nil, fmt.Errorf("video kepanjangan (maks %d detik untuk stiker)", maxStickerVideoSeconds)
			}
			return nil, vid, nil
		}
	}

	return nil, nil, fmt.Errorf("reply atau kirim gambar/video dengan caption .sticker")
}

// convertToStickerWebp memanggil ffmpeg untuk convert gambar/video jadi stiker webp 512x512
// dengan background transparan. Untuk video/gif, animasi dibatasi 6 detik pertama & 10fps
// supaya ukuran file stiker tetap wajar.
func convertToStickerWebp(ctx context.Context, input []byte, isVideo bool) ([]byte, error) {
	baseTmp, err := store.BotTempDir()
	if err != nil {
		return nil, err
	}
	tmpDir, err := os.MkdirTemp(baseTmp, "sticker-*")
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

	// Scale ke maksimum 512x512 sambil pertahankan rasio aspek asli
	// ("decrease" memastikan gambar diperkecil kalau lebih besar dari 512
	// di salah satu sisi, tapi dibiarkan apa adanya kalau sudah lebih kecil),
	// LALU di-pad ke canvas persis 512x512 dengan warna transparan
	// (0x00000000), konten diposisikan di tengah.
	//
	// Padding ini WAJIB: klien WhatsApp (terutama Android) mengasumsikan
	// canvas sticker persis 512x512. Kalau canvas hasil convert non-persegi
	// (mis. 512x300 tanpa pad), klien akan menyesuaikan/stretch konten itu
	// ke area render sticker yang persegi — hasilnya konten yang aslinya
	// portrait/landscape terlihat gepeng/dipaksa jadi rasio 1:1. Dengan pad
	// transparan, canvas selalu 512x512 tapi area kosong di luar konten
	// asli tetap transparan sehingga wallpaper chat tetap terlihat tembus.
	scaleFilter := "scale=512:512:force_original_aspect_ratio=decrease," +
		"pad=512:512:(ow-iw)/2:(oh-ih)/2:color=0x00000000,format=rgba"

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
	release, acqErr := heavy.AcquireTranscode(ctx)
	if acqErr != nil {
		return nil, acqErr
	}
	defer release()
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %v (%s)", err, stderr.String())
	}

	return os.ReadFile(outPath)
}
