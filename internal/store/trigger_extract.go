package store

import "go.mau.fi/whatsmeow/types/events"

// ExtractStanzaID mengambil ContextInfo.StanzaID (ID pesan yang di-reply)
// dari berbagai tipe pesan yang mendukung quote/reply.
// Balikan "" berarti pesan ini bukan reply ke apa pun.
func ExtractStanzaID(evt *events.Message) string {
	msg := evt.Message
	if msg == nil {
		return ""
	}

	if ext := msg.GetExtendedTextMessage(); ext != nil && ext.GetContextInfo() != nil {
		return ext.GetContextInfo().GetStanzaID()
	}
	if img := msg.GetImageMessage(); img != nil && img.GetContextInfo() != nil {
		return img.GetContextInfo().GetStanzaID()
	}
	if vid := msg.GetVideoMessage(); vid != nil && vid.GetContextInfo() != nil {
		return vid.GetContextInfo().GetStanzaID()
	}
	if aud := msg.GetAudioMessage(); aud != nil && aud.GetContextInfo() != nil {
		return aud.GetContextInfo().GetStanzaID()
	}
	if stk := msg.GetStickerMessage(); stk != nil && stk.GetContextInfo() != nil {
		return stk.GetContextInfo().GetStanzaID()
	}
	if doc := msg.GetDocumentMessage(); doc != nil && doc.GetContextInfo() != nil {
		return doc.GetContextInfo().GetStanzaID()
	}

	return ""
}

// IsReplyToBot mengecek apakah pesan ini adalah reply ke pesan
// yang sebelumnya dikirim oleh bot (dicek lewat tracker di
// bot_message_tracker.go).
func IsReplyToBot(evt *events.Message) bool {
	stanzaID := ExtractStanzaID(evt)
	if stanzaID == "" {
		return false
	}
	return IsBotMessageID(stanzaID)
}

// IsStickerMessage mengecek apakah pesan ini berupa stiker.
func IsStickerMessage(evt *events.Message) bool {
	return evt.Message != nil && evt.Message.GetStickerMessage() != nil
}

// IsMediaMessage mengecek apakah pesan ini berupa media
// (gambar, video, audio, atau dokumen) — dipakai untuk mendeteksi
// "reply media tanpa caption" pada trigger AI.
func IsMediaMessage(evt *events.Message) bool {
	if evt.Message == nil {
		return false
	}
	return evt.Message.GetImageMessage() != nil ||
		evt.Message.GetVideoMessage() != nil ||
		evt.Message.GetAudioMessage() != nil ||
		evt.Message.GetDocumentMessage() != nil
}
