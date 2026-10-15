package plugins

import (
	"context"
	"fmt"
	"strings"

	"wabot-go/internal/config"
	"wabot-go/internal/store"

	"github.com/polymorfa/hypermeow/proto/waE2E"
)

func init() {
	registerPlugin(RegisterStickerWatermark)
}

// RegisterStickerWatermark mendaftarkan command ".stickerwatermark" (alias ".swm")
// -> reply gambar atau stiker lalu ketik .swm <packname>|<author> untuk mengubah
// metadata (packname & author) stiker tersebut dan mengirimkannya kembali sebagai
// stiker baru. Kalau argumen tidak diisi, dipakai default dari config.go.
func RegisterStickerWatermark(r *Registry) {
	r.Register(Command{
		Name:        "stickerwatermark",
		Aliases:     []string{"swm"},
		Description: "Reply gambar/stiker + .swm <packname>|<author> untuk ganti metadata stiker",
		Category:    CategoryMedia,
		Handler: func(ctx context.Context, mctx *store.MessageContext) error {
			packname, author := parseStickerWatermarkArgs(mctx.Args)

			img, sticker, err := extractWatermarkMedia(mctx)
			if err != nil {
				return store.ReplyText(ctx, mctx, err.Error())
			}

			var mediaBytes []byte
			isSticker := sticker != nil

			if isSticker {
				mediaBytes, err = mctx.Client.Download(ctx, sticker)
			} else {
				mediaBytes, err = mctx.Client.Download(ctx, img)
			}
			if err != nil {
				return store.ReplyText(ctx, mctx, "Gagal download media: "+err.Error())
			}

			var webpBytes []byte
			if isSticker {
				// Sudah webp jadi (stiker) — tidak perlu resize ulang, cukup
				// ganti metadatanya.
				webpBytes = mediaBytes
			} else {
				// Gambar biasa — convert dulu ke webp 512x512 lewat pipeline
				// yang sama dengan command .sticker.
				webpBytes, err = convertToStickerWebp(mediaBytes, false)
				if err != nil {
					return store.ReplyText(ctx, mctx, "Gagal convert ke stiker: "+err.Error())
				}
			}

			newWebp, err := store.WriteStickerExif(webpBytes, packname, author)
			if err != nil {
				return store.ReplyText(ctx, mctx, "Gagal set metadata stiker: "+err.Error())
			}

			// Upload+kirim lewat helper bersama (bukan blok duplikat lokal) —
			// termasuk Width/Height yang sebelumnya lupa diisi oleh .swm
			// (audit A3), dan otomatis LogOutgoing dengan label di bawah.
			label := fmt.Sprintf("packname=%q author=%q", packname, author)
			if err := sendStickerWebp(ctx, mctx, newWebp, "stickerwatermark", label); err != nil {
				return store.ReplyText(ctx, mctx, "Gagal kirim stiker: "+err.Error())
			}
			return nil
		},
	})
}

// parseStickerWatermarkArgs mem-parse "<packname>|<author>" dari argumen command.
// mctx.Args sudah dipecah per-spasi oleh handler, jadi digabung ulang dengan
// spasi dulu di sini supaya packname/author boleh mengandung spasi (mis.
// "Flora GO|Mitra Wann"), baru dipecah lagi berdasarkan "|". Bagian yang
// kosong (atau argumen tidak diisi sama sekali) jatuh balik ke default di
// config.go.
func parseStickerWatermarkArgs(args []string) (packname, author string) {
	packname = config.StickerPackname
	author = config.StickerAuthor

	joined := strings.TrimSpace(strings.Join(args, " "))
	if joined == "" {
		return packname, author
	}

	parts := strings.SplitN(joined, "|", 2)
	if p := strings.TrimSpace(parts[0]); p != "" {
		packname = p
	}
	if len(parts) == 2 {
		if a := strings.TrimSpace(parts[1]); a != "" {
			author = a
		}
	}
	return packname, author
}

// extractWatermarkMedia mengambil ImageMessage atau StickerMessage dari pesan
// yang di-reply (quoted). Berbeda dari extractStickerMedia di sticker.go,
// command ini HANYA baca dari reply (bukan pesan langsung), dan mendukung
// StickerMessage — bukan hanya gambar/video.
//
// Quoted diambil lewat ExtractQuotedMessage + UnwrapMessage — satu jalur
// ContextInfo bersama (audit A2), supaya reply di grup ber-Pesan-Sementara /
// pesan sekali-lihat tetap terbaca.
func extractWatermarkMedia(mctx *store.MessageContext) (*waE2E.ImageMessage, *waE2E.StickerMessage, error) {
	quoted := store.UnwrapMessage(store.ExtractQuotedMessage(mctx.RawEvt))
	if quoted == nil {
		return nil, nil, fmt.Errorf("reply ke gambar atau stiker dulu, lalu ketik .swm <packname>|<author>")
	}

	if img := quoted.GetImageMessage(); img != nil {
		return img, nil, nil
	}
	if sticker := quoted.GetStickerMessage(); sticker != nil {
		return nil, sticker, nil
	}

	return nil, nil, fmt.Errorf("pesan yang di-reply harus berupa gambar atau stiker")
}
