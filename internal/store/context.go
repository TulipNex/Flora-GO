package store

import (
	"context"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// MessageContext adalah "paket data" yang dikirim ke setiap plugin/command.
// Semua plugin baru cukup pakai struct ini, tidak perlu tahu detail whatsmeow.
type MessageContext struct {
	Client *whatsmeow.Client
	Chat   types.JID // JID room/chat tujuan balasan
	Sender types.JID // JID pengirim pesan
	RawEvt *events.Message
	Args   []string // argumen setelah nama command, misal ".ping foo bar" -> ["foo","bar"]
}

// ReplyText adalah helper supaya plugin tidak perlu menulis ulang boilerplate
// SendMessage tiap kali membalas pesan teks biasa.
func ReplyText(ctx context.Context, mctx *MessageContext, text string) error {
	_, err := mctx.Client.SendMessage(ctx, mctx.Chat, &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String(text),
			ContextInfo: &waE2E.ContextInfo{
				StanzaID:    proto.String(mctx.RawEvt.Info.ID),
				Participant: proto.String(mctx.Sender.String()),
				QuotedMessage: mctx.RawEvt.Message,
			},
		},
	})
	if err == nil {
		LogOutgoing(mctx, "text", text)
	}
	return err
}
