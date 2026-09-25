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
	// RawArgs adalah teks mentah setelah nama command dengan whitespace ASLI
	// (termasuk baris baru) yang dipertahankan. Args di atas dipecah pakai
	// strings.Fields yang menghancurkan baris baru — command yang butuh
	// format multi-baris (.setwelcome/.setbye) WAJIB pakai field ini, bukan
	// strings.Join(Args, " ").
	RawArgs string
}

// ReplyText adalah helper supaya plugin tidak perlu menulis ulang boilerplate
// SendMessage tiap kali membalas pesan teks biasa.
func ReplyText(ctx context.Context, mctx *MessageContext, text string) error {
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String(text),
			ContextInfo: &waE2E.ContextInfo{
				StanzaID:      proto.String(mctx.RawEvt.Info.ID),
				Participant:   proto.String(mctx.Sender.String()),
				QuotedMessage: mctx.RawEvt.Message,
			},
		},
	})
	if err == nil {
		LogOutgoing(mctx, "text", text)
		// Dicatat supaya trigger detector AI bisa mengenali kalau nanti
		// user reply ke pesan ini (lihat internal/ai/trigger.go), DAN
		// supaya teks bubble ini bisa diambil kembali kalau nanti
		// di-reply (lihat GetBotMessageText, dipakai untuk konteks reply
		// multi-bubble).
		TrackBotMessageID(resp.ID, text)
	}
	return err
}

// ReplyTextWithMentions sama seperti ReplyText (tetap me-reply pesan pemicu)
// tapi menyertakan MentionedJID supaya "@..." di teks menjadi tag beneran
// (highlight + notif), bukan sekadar teks polos.
//
// SYARAT PAIRING (lihat handleTagAll di plugins/groupmanage.go): teks WAJIB
// mengandung "@<User>" untuk tiap JID di mentions dengan User yang SAMA
// PERSIS dengan JID-nya. Di grup mode LID itu berarti "@<LID>", bukan
// "@<nomor HP>" — klien WhatsApp yang me-render-nya jadi nama kontak.
// Tiap JID dinormalisasi via ToNonAD di sini supaya device/agent tidak ikut
// terbawa dan merusak pairing.
func ReplyTextWithMentions(ctx context.Context, mctx *MessageContext, text string, mentions []types.JID) error {
	bare := make([]string, 0, len(mentions))
	for _, jid := range mentions {
		bare = append(bare, jid.ToNonAD().String())
	}
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String(text),
			ContextInfo: &waE2E.ContextInfo{
				StanzaID:      proto.String(mctx.RawEvt.Info.ID),
				Participant:   proto.String(mctx.Sender.String()),
				QuotedMessage: mctx.RawEvt.Message,
				MentionedJID:  bare,
			},
		},
	})
	if err == nil {
		LogOutgoing(mctx, "text-mention", text)
		TrackBotMessageID(resp.ID, text)
	}
	return err
}

// SendText mengirim pesan teks BIASA (bukan reply/quote) ke chat tujuan.
// Dipakai untuk bubble ke-2 dst pada output multi-bubble AI (Fase 5) —
// hanya bubble pertama yang reply ke pesan user, sisanya pesan lepas
// supaya terasa natural seperti orang mengetik beberapa pesan berturut.
func SendText(ctx context.Context, mctx *MessageContext, text string) error {
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String(text),
		},
	})
	if err == nil {
		LogOutgoing(mctx, "text", text)
		TrackBotMessageID(resp.ID, text)
	}
	return err
}
