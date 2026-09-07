package store

import (
	"fmt"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types/events"
)

const logTimeFormat = "2006-01-02 15:04:05"

// LogIncoming mencetak log setiap pesan masuk ke console panel.
// Format: [timestamp] MASUK | nomor (username) | chat: lokasi | tipe: X | teks
func LogIncoming(evt *events.Message) {
	fmt.Printf("[%s] 📥 MASUK | %s (%s) | chat: %s | tipe: %s | %s\n",
		evt.Info.Timestamp.Format(logTimeFormat),
		evt.Info.Sender.String(),
		evt.Info.PushName,
		evt.Info.Chat.String(),
		messageType(evt.Message),
		messageSummaryText(evt.Message),
	)
}

// LogOutgoing mencetak log setiap pesan yang dikirim bot ke console panel.
// msgType dan text diisi oleh pemanggil (plugin) sesuai jenis pesan yang dikirim,
// karena tidak semua tipe pesan keluar (contact, sticker, dll) dibangun dari waE2E.Message
// yang sama seperti pesan masuk.
func LogOutgoing(mctx *MessageContext, msgType, text string) {
	senderJID := "-"
	if mctx.Client != nil && mctx.Client.Store != nil {
		senderJID = mctx.Client.Store.ID.String()
	}
	fmt.Printf("[%s] 📤 KIRIM | %s (Bot) | chat: %s | tipe: %s | %s\n",
		time.Now().Format(logTimeFormat),
		senderJID,
		mctx.Chat.String(),
		msgType,
		text,
	)
}

// messageType menentukan tipe pesan dari isi protobuf Message untuk kebutuhan log.
func messageType(msg *waE2E.Message) string {
	switch {
	case msg == nil:
		return "unknown"
	case msg.GetConversation() != "" || msg.GetExtendedTextMessage() != nil:
		return "text"
	case msg.GetImageMessage() != nil:
		return "image"
	case msg.GetVideoMessage() != nil:
		return "video"
	case msg.GetAudioMessage() != nil:
		return "audio"
	case msg.GetDocumentMessage() != nil:
		return "document"
	case msg.GetStickerMessage() != nil:
		return "sticker"
	case msg.GetContactMessage() != nil:
		return "contact"
	case msg.GetLocationMessage() != nil:
		return "location"
	default:
		return "other"
	}
}

// messageSummaryText mengambil teks yang relevan untuk ditampilkan di log
// (teks biasa, caption, atau info singkat untuk tipe non-teks).
func messageSummaryText(msg *waE2E.Message) string {
	if msg == nil {
		return ""
	}
	if msg.GetConversation() != "" {
		return msg.GetConversation()
	}
	if ext := msg.GetExtendedTextMessage(); ext != nil {
		return ext.GetText()
	}
	if img := msg.GetImageMessage(); img != nil {
		return img.GetCaption()
	}
	if vid := msg.GetVideoMessage(); vid != nil {
		return vid.GetCaption()
	}
	if doc := msg.GetDocumentMessage(); doc != nil {
		return doc.GetFileName()
	}
	if contact := msg.GetContactMessage(); contact != nil {
		return contact.GetDisplayName()
	}
	return "(tanpa teks)"
}
