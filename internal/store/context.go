package store

import (
	"context"
	"fmt"
	"strings"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types"
	"github.com/polymorfa/hypermeow/types/events"
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
	// format multi-baris (.welcome set/.bye set) WAJIB pakai field ini,
	// bukan strings.Join(Args, " ").
	RawArgs string
}

// SenderPushName mengembalikan nama tampilan (PushName) pengirim dari event WhatsApp.
// Jika PushName kosong (misal pengguna belum set nama), fallback ke nomor pengguna (Sender.User).
func (mctx *MessageContext) SenderPushName() string {
	if mctx != nil && mctx.RawEvt != nil && strings.TrimSpace(mctx.RawEvt.Info.PushName) != "" {
		return strings.TrimSpace(mctx.RawEvt.Info.PushName)
	}
	if mctx != nil && mctx.Sender.User != "" {
		return mctx.Sender.User
	}
	return ""
}

// archiveOutgoingIfMain memusatkan guard "hanya bot utama" + pengiriman teks
// keluar ke antrian arsip async, supaya tak diulang di setiap helper teks.
// Arsip kini non-blocking di sisi store, jadi TIDAK perlu `go` di sini.
func archiveOutgoingIfMain(mctx *MessageContext, msgID string, msg *waE2E.Message) {
	if mctx != nil && IsMainClient(mctx.Client) {
		ArchiveOutgoingMessage(mctx.Chat.String(), msgID, msg)
	}
}

// ReplyText adalah helper supaya plugin tidak perlu menulis ulang boilerplate
// SendMessage tiap kali membalas pesan teks biasa.
func ReplyText(ctx context.Context, mctx *MessageContext, text string) error {
	outMsg := &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String(text),
			ContextInfo: &waE2E.ContextInfo{
				StanzaID:      proto.String(mctx.RawEvt.Info.ID),
				Participant:   proto.String(mctx.Sender.String()),
				QuotedMessage: mctx.RawEvt.Message,
			},
		},
	}
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, outMsg)
	if err == nil {
		LogOutgoing(mctx, "text", text)
		// Dicatat supaya trigger detector AI bisa mengenali kalau nanti
		// user reply ke pesan ini (lihat internal/ai/trigger.go), DAN
		// supaya teks bubble ini bisa diambil kembali kalau nanti
		// di-reply (lihat GetBotMessageText, dipakai untuk konteks reply
		// multi-bubble).
		TrackBotMessageID(resp.ID, text)
		// Arsip keluar FIORA/CRM: async fire-and-forget supaya latensi
		// reply nol; fail-soft di dalam bila DB arsip mati.
		archiveOutgoingIfMain(mctx, string(resp.ID), outMsg)
	}
	return err
}

// ReplyLocation mengirim "pin lokasi" (LocationMessage) yang me-reply pesan
// pemicu. Dipakai `.gemini` untuk mengganti link Google Maps di jawaban dengan
// bubble lokasi sungguhan: nama tampil tebal, alamat di baris bawahnya, dan
// tombol "Buka di peta" mengikuti koordinat. Konteks chat WA sendiri tidak bisa
// menampilkan pratinjau peta untuk link mentah, jadi pin inilah bentuk yang
// bisa diketuk langsung.
func ReplyLocation(ctx context.Context, mctx *MessageContext, name, address string, lat, lng float64) error {
	return sendLocation(ctx, mctx, true, name, address, lat, lng)
}

// SendLocation mengirim pin lokasi sebagai pesan lepas (tanpa quote), dipakai
// bubble lanjutan pada jawaban multi-bubble.
func SendLocation(ctx context.Context, mctx *MessageContext, name, address string, lat, lng float64) error {
	return sendLocation(ctx, mctx, false, name, address, lat, lng)
}

func sendLocation(ctx context.Context, mctx *MessageContext, reply bool, name, address string, lat, lng float64) error {
	if mctx == nil || mctx.Client == nil {
		return fmt.Errorf("konteks pesan tidak tersedia")
	}
	outMsg := &waE2E.Message{
		LocationMessage: &waE2E.LocationMessage{
			DegreesLatitude:  proto.Float64(lat),
			DegreesLongitude: proto.Float64(lng),
			Name:             proto.String(name),
			Address:          proto.String(address),
		},
	}
	if reply && mctx.RawEvt != nil && mctx.RawEvt.Info.ID != "" {
		outMsg.LocationMessage.ContextInfo = &waE2E.ContextInfo{
			StanzaID:      proto.String(mctx.RawEvt.Info.ID),
			Participant:   proto.String(mctx.Sender.String()),
			QuotedMessage: mctx.RawEvt.Message,
		}
	}
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, outMsg)
	if err == nil {
		LogOutgoing(mctx, "location", name)
		archiveOutgoingIfMain(mctx, string(resp.ID), outMsg)
	}
	return err
}

// ReplyTextAgent sama seperti ReplyText tapi mencatat agent:true —
// dipakai HANYA bubble pertama balasan AI (lihat sendBubbles).
func ReplyTextAgent(ctx context.Context, mctx *MessageContext, text string) error {
	outMsg := &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String(text),
			ContextInfo: &waE2E.ContextInfo{
				StanzaID:      proto.String(mctx.RawEvt.Info.ID),
				Participant:   proto.String(mctx.Sender.String()),
				QuotedMessage: mctx.RawEvt.Message,
			},
		},
	}
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, outMsg)
	if err == nil {
		LogOutgoingAgent(mctx, "text", text)
		// Sama seperti ReplyText: catat ID agar reply ke bubble ini
		// dikenali trigger AI.
		TrackBotMessageID(resp.ID, text)
		archiveOutgoingIfMain(mctx, string(resp.ID), outMsg)
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
	outMsg := &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String(text),
			ContextInfo: &waE2E.ContextInfo{
				StanzaID:      proto.String(mctx.RawEvt.Info.ID),
				Participant:   proto.String(mctx.Sender.String()),
				QuotedMessage: mctx.RawEvt.Message,
				MentionedJID:  bare,
			},
		},
	}
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, outMsg)
	if err == nil {
		LogOutgoing(mctx, "text-mention", text)
		TrackBotMessageID(resp.ID, text)
		archiveOutgoingIfMain(mctx, string(resp.ID), outMsg)
	}
	return err
}

// SendTextAgent sama seperti SendText tapi mencatat agent:true — dipakai
// bubble ke-2 dst balasan AI.
func SendTextAgent(ctx context.Context, mctx *MessageContext, text string) error {
	outMsg := &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String(text),
		},
	}
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, outMsg)
	if err == nil {
		LogOutgoingAgent(mctx, "text", text)
		TrackBotMessageID(resp.ID, text)
		archiveOutgoingIfMain(mctx, string(resp.ID), outMsg)
	}
	return err
}

// SendTextWithMentions mengirim pesan lepas (tanpa quote) + mention beneran.
// Dipakai pengumuman timer yang tak punya pesan asal untuk di-quote
// (mis. tantangan tictactoe kedaluwarsa). Syarat pairing sama seperti
// ReplyTextWithMentions: teks WAJIB mengandung "@<User>" yang sama persis
// dengan JID-nya.
func SendTextWithMentions(ctx context.Context, mctx *MessageContext, text string, mentions []types.JID) error {
	bare := make([]string, 0, len(mentions))
	for _, jid := range mentions {
		bare = append(bare, jid.ToNonAD().String())
	}
	outMsg := &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String(text),
			ContextInfo: &waE2E.ContextInfo{
				MentionedJID: bare,
			},
		},
	}
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, outMsg)
	if err == nil {
		LogOutgoing(mctx, "text-mention", text)
		TrackBotMessageID(resp.ID, text)
		archiveOutgoingIfMain(mctx, string(resp.ID), outMsg)
	}
	return err
}

// SendReaction memberi reaksi emoji ke pesan pemicu, dipakai sebagai tanda
// "diterima" tanpa bubble teks (mis. .gemini). Reaksi sengaja TIDAK diarsipkan
// dan kegagalannya tidak pernah fatal: pemanggil boleh mengabaikan error.
func SendReaction(ctx context.Context, mctx *MessageContext, emoji string) error {
	if mctx == nil || mctx.Client == nil || mctx.RawEvt == nil || mctx.RawEvt.Info.ID == "" {
		return nil
	}
	if strings.TrimSpace(emoji) == "" {
		return nil
	}
	msg := mctx.Client.BuildReaction(mctx.Chat, mctx.Sender, mctx.RawEvt.Info.ID, emoji)
	_, err := mctx.Client.SendMessage(ctx, mctx.Chat, msg)
	return err
}

// SendText mengirim pesan teks BIASA (bukan reply/quote) ke chat tujuan.
// Dipakai untuk bubble ke-2 dst pada output multi-bubble AI (Fase 5) —
// hanya bubble pertama yang reply ke pesan user, sisanya pesan lepas
// supaya terasa natural seperti orang mengetik beberapa pesan berturut.
func SendText(ctx context.Context, mctx *MessageContext, text string) error {
	outMsg := &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String(text),
		},
	}
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, outMsg)
	if err == nil {
		LogOutgoing(mctx, "text", text)
		TrackBotMessageID(resp.ID, text)
		archiveOutgoingIfMain(mctx, string(resp.ID), outMsg)
	}
	return err
}
