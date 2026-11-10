package plugins

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"strings"
	"time"

	"wabot-go/internal/external"
	"wabot-go/internal/logfmt"
	"wabot-go/internal/messaging"
	"wabot-go/internal/store"

	"github.com/google/uuid"
	whatsmeow "github.com/polymorfa/hypermeow"
	waBinary "github.com/polymorfa/hypermeow/binary"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func init() {
	registerPlugin(RegisterEventTest)
}

const (
	eventTestDefaultTitle    = "NIXCODE"
	eventTestDefaultImageURL = "https://cdn.ornzora.eu.cc/4d4f3591-1c3e-4e1b-b1c2-45faeb91b781-FIORA.jpg"
	eventTestContextSource   = "event_invitation"
)

// RegisterEventTest mendaftarkan `.eventtest` (alias `.eventmsg`, `.eventinv`):
// uji coba kirim pesan undangan acara WhatsApp (EventInviteMessage) dengan
// metadata node context_source="event_invitation" (kategori Dev).
func RegisterEventTest(r *Registry) {
	r.Register(Command{
		Name:        "eventtest",
		Aliases:     []string{"eventmsg", "eventinv"},
		Description: "Uji pesan undangan acara / EventInviteMessage (Dev)",
		Category:    CategoryDev,
		Handler:     handleEventTest,
	})
}

func handleEventTest(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}

	title := strings.TrimSpace(mctx.RawArgs)
	if title == "" {
		title = eventTestDefaultTitle
	}

	if err := eventTestSend(ctx, mctx, title); err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal kirim pesan event: %v", err))
	}
	return store.ReplyText(ctx, mctx, fmt.Sprintf("Pesan event invite (%s) terkirim. Cek tampilan kartu di HP.", title))
}

// eventTestExtractImage mengecek apakah ada gambar terlampir atau di-reply.
func eventTestExtractImage(mctx *store.MessageContext) (*waE2E.ImageMessage, bool) {
	if mctx.RawEvt == nil {
		return nil, false
	}
	if msg := store.UnwrapMessage(mctx.RawEvt.Message); msg != nil {
		if img := msg.GetImageMessage(); img != nil {
			return img, true
		}
	}
	if quoted := store.UnwrapMessage(store.ExtractQuotedMessage(mctx.RawEvt)); quoted != nil {
		if img := quoted.GetImageMessage(); img != nil {
			return img, true
		}
	}
	return nil, false
}

// eventTestGetThumbnail mengambil thumbnail dari pesan reply/lampiran atau mengunduh dari URL default.
func eventTestGetThumbnail(ctx context.Context, mctx *store.MessageContext) []byte {
	// 1. Coba dari lampiran / reply gambar
	if img, ok := eventTestExtractImage(mctx); ok && mctx.Client != nil {
		dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		data, err := mctx.Client.Download(dctx, img)
		cancel()
		if err == nil && len(data) > 0 {
			if norm := messaging.HeaderJPEG(data, 640, 85); len(norm) > 0 {
				return norm
			}
			return data
		}
	}

	// 2. Fallback: unduh dari URL default
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	data, err := external.DownloadBytes(dctx, eventTestDefaultImageURL)
	cancel()
	if err != nil || len(data) == 0 {
		logfmt.Warn("eventtest: unduh default thumbnail gagal: %v\n", err)
		return nil
	}

	if norm := messaging.HeaderJPEG(data, 640, 85); len(norm) > 0 {
		return norm
	}
	// Bila HeaderJPEG gagal decode (misal format webp eksotik), verifikasi config biasa
	if _, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		return data
	}
	return nil
}

// eventTestBuildMessage menyusun protobuf EventInviteMessage lengkap.
func eventTestBuildMessage(title string, thumb []byte, startTime time.Time) *waE2E.Message {
	return &waE2E.Message{
		EventInviteMessage: &waE2E.EventInviteMessage{
			EventID:       proto.String(uuid.New().String()),
			EventTitle:    proto.String(title),
			JPEGThumbnail: thumb,
			StartTime:     proto.Int64(startTime.Unix()),
		},
	}
}

// eventTestExtra menyusun SendRequestExtra dengan metadata context_source="event_invitation".
func eventTestExtra() whatsmeow.SendRequestExtra {
	return whatsmeow.SendRequestExtra{
		ID: whatsmeow.GenerateMessageID(),
		AdditionalNodes: &[]waBinary.Node{
			{
				Tag: "meta",
				Attrs: waBinary.Attrs{
					"context_source": eventTestContextSource,
				},
			},
		},
	}
}

// eventTestSend mengirimkan pesan EventInviteMessage ke ruang chat tujuan.
func eventTestSend(ctx context.Context, mctx *store.MessageContext, title string) error {
	if title == "" {
		title = eventTestDefaultTitle
	}

	thumb := eventTestGetThumbnail(ctx, mctx)
	msg := eventTestBuildMessage(title, thumb, time.Now())
	extra := eventTestExtra()

	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, msg, extra)
	if err != nil {
		return err
	}

	store.LogOutgoing(mctx, "eventtest", title)
	store.TrackBotMessageID(resp.ID, "eventtest "+title)
	return nil
}
