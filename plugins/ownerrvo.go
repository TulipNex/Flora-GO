package plugins

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"time"

	"wabot-go/internal/messaging"
	"wabot-go/internal/store"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func init() {
	registerPlugin(RegisterOwnerRVO)
}

// maxRVOMediaBytes membatasi media view-once yang dibuka (100MB). Download
// menahan seluruh byte di memori, jadi tanpa batas video besar bisa
// menghabiskan RAM container panel.
const maxRVOMediaBytes = 100 << 20

// RegisterOwnerRVO mendaftarkan command `.rvo` (alias revealvo/viewonce):
// reply pesan sekali-lihat (foto/video/voice note) lalu ketik .rvo — bot
// mengunduhnya dan mengirim ulang sebagai media BIASA yang bisa dibuka
// berkali-kali.
//
// CATATAN PRIVASI: command ini terbuka untuk semua user — artinya siapa
// saja bisa membuka sekali-lihat siapa saja. Kalau keberatan, kembalikan
// gate owner (tambah requireOwner di handleRVO) atau batasi ke admin grup.
func RegisterOwnerRVO(r *Registry) {
	r.Register(Command{
		Name:        "rvo",
		Aliases:     []string{"revealvo", "viewonce"},
		Description: "Buka pesan sekali-lihat yang di-reply",
		Category:    CategoryAlat,
		Handler:     handleRVO,
	})
}

func handleRVO(ctx context.Context, mctx *store.MessageContext) error {
	// Quoted diambil + di-unwrap lewat jalur ContextInfo bersama — bungkus
	// ViewOnceMessage/Ephemeral di pesan yang di-reply ikut terbuka di sini
	// (lihat store/trigger_extract.go + message_unwrap.go).
	quoted := store.UnwrapMessage(store.ExtractQuotedMessage(mctx.RawEvt))
	if quoted == nil {
		return store.ReplyText(ctx, mctx, "Reply pesan sekali-lihat (foto/video/voice note) yang ingin dibuka.")
	}

	if img := quoted.GetImageMessage(); img != nil {
		return resendRVOMedia(ctx, mctx, img, whatsmeow.MediaImage, img.GetMimetype(), img.GetCaption(), false, "rvo-image")
	}
	if vid := quoted.GetVideoMessage(); vid != nil {
		return resendRVOMedia(ctx, mctx, vid, whatsmeow.MediaVideo, vid.GetMimetype(), vid.GetCaption(), false, "rvo-video")
	}
	if aud := quoted.GetAudioMessage(); aud != nil {
		return resendRVOMedia(ctx, mctx, aud, whatsmeow.MediaAudio, aud.GetMimetype(), "", aud.GetPTT(), "rvo-audio")
	}
	return store.ReplyText(ctx, mctx, "Pesan yang di-reply bukan foto, video, atau voice note.")
}

// resendRVOMedia mengunduh media view-once lalu mengirimnya ulang sebagai
// media biasa (bukan sekali-lihat) ke chat yang sama. ptt hanya relevan
// untuk audio (voice note tetap voice note, bukan audio biasa).
func resendRVOMedia(ctx context.Context, mctx *store.MessageContext, media whatsmeow.DownloadableMessage, mediaType whatsmeow.MediaType, mime, caption string, ptt bool, logLabel string) error {
	data, err := mctx.Client.Download(ctx, media)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal download media sekali-lihat: %v", err))
	}
	if len(data) > maxRVOMediaBytes {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Media terlalu besar untuk dibuka (%.1f MB, batas 100MB).", float64(len(data))/(1<<20)))
	}

	// Normalisasi/gate SEBELUM upload (lihat sendDlBytesVideoAlbum).
	if mediaType == whatsmeow.MediaVideo {
		var err error
		data, err = messaging.PrepareVideoBytes(ctx, data)
		if err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal siapkan video: %v", err))
		}
	} else if mediaType == whatsmeow.MediaImage {
		if err := messaging.CheckMediaBytes(data); err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal siapkan gambar: %v", err))
		}
	}
	uploaded, err := mctx.Client.Upload(ctx, data, mediaType)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal upload media: %v", err))
	}

	var msg *waE2E.Message
	switch mediaType {
	case whatsmeow.MediaImage:
		thumb := messaging.InlineThumbForImage(data)
		im := &waE2E.ImageMessage{
			URL:               proto.String(uploaded.URL),
			DirectPath:        proto.String(uploaded.DirectPath),
			MediaKey:          uploaded.MediaKey,
			MediaKeyTimestamp: proto.Int64(time.Now().Unix()),
			Mimetype:          proto.String(mime),
			FileEncSHA256:     uploaded.FileEncSHA256,
			FileSHA256:        uploaded.FileSHA256,
			FileLength:        proto.Uint64(uploaded.FileLength),
			Caption:           proto.String(caption),
			JPEGThumbnail:     thumb,
		}
		if cfg, _, cerr := image.DecodeConfig(bytes.NewReader(data)); cerr == nil && cfg.Width > 0 {
			im.Width = proto.Uint32(uint32(cfg.Width))
			im.Height = proto.Uint32(uint32(cfg.Height))
		}
		msg = &waE2E.Message{ImageMessage: im}
	case whatsmeow.MediaVideo:
		thumb := messaging.InlineThumbForVideo(ctx, data)
		vm := &waE2E.VideoMessage{
			URL:               proto.String(uploaded.URL),
			DirectPath:        proto.String(uploaded.DirectPath),
			MediaKey:          uploaded.MediaKey,
			MediaKeyTimestamp: proto.Int64(time.Now().Unix()),
			Mimetype:          proto.String(mime),
			FileEncSHA256:     uploaded.FileEncSHA256,
			FileSHA256:        uploaded.FileSHA256,
			FileLength:        proto.Uint64(uploaded.FileLength),
			Caption:           proto.String(caption),
			JPEGThumbnail:     thumb,
		}
		if len(thumb) > 0 {
			if cfg, _, cerr := image.DecodeConfig(bytes.NewReader(thumb)); cerr == nil && cfg.Width > 0 {
				vm.Width = proto.Uint32(uint32(cfg.Width))
				vm.Height = proto.Uint32(uint32(cfg.Height))
			}
		}
		msg = &waE2E.Message{VideoMessage: vm}
	default: // whatsmeow.MediaAudio
		msg = &waE2E.Message{
			AudioMessage: &waE2E.AudioMessage{
				URL:           proto.String(uploaded.URL),
				DirectPath:    proto.String(uploaded.DirectPath),
				MediaKey:      uploaded.MediaKey,
				Mimetype:      proto.String(mime),
				FileEncSHA256: uploaded.FileEncSHA256,
				FileSHA256:    uploaded.FileSHA256,
				FileLength:    proto.Uint64(uploaded.FileLength),
				PTT:           proto.Bool(ptt),
			},
		}
	}

	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, msg)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal kirim media: %v", err))
	}
	store.LogOutgoing(mctx, logLabel, caption)
	store.TrackBotMessageID(resp.ID, caption)
	return nil
}
