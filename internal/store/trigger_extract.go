package store

import "go.mau.fi/whatsmeow/types/events"

// ExtractStanzaID mengambil ContextInfo.StanzaID (ID pesan yang di-reply)
// dari berbagai tipe pesan yang mendukung quote/reply.
// Balikan "" berarti pesan ini bukan reply ke apa pun.
//
// Pesan di-unwrap dulu (lihat UnwrapMessage) supaya reply yang dikirim di
// dalam grup ber-Pesan-Sementara / pesan sekali-lihat tetap terdeteksi.
func ExtractStanzaID(evt *events.Message) string {
	msg := UnwrapMessage(evt.Message)
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
//
// Pesan di-unwrap dulu (lihat UnwrapMessage) supaya stiker yang dikirim di
// dalam grup ber-Pesan-Sementara / pesan sekali-lihat tetap terdeteksi.
func IsStickerMessage(evt *events.Message) bool {
	msg := UnwrapMessage(evt.Message)
	return msg != nil && msg.GetStickerMessage() != nil
}

// IsMediaMessage mengecek apakah pesan ini berupa media
// (gambar, video, audio, atau dokumen) — dipakai untuk mendeteksi
// "reply media tanpa caption" pada trigger AI.
//
// Pesan di-unwrap dulu (lihat UnwrapMessage) supaya media yang dikirim di
// dalam grup ber-Pesan-Sementara / pesan sekali-lihat tetap terdeteksi.
func IsMediaMessage(evt *events.Message) bool {
	msg := UnwrapMessage(evt.Message)
	if msg == nil {
		return false
	}
	return msg.GetImageMessage() != nil ||
		msg.GetVideoMessage() != nil ||
		msg.GetAudioMessage() != nil ||
		msg.GetDocumentMessage() != nil
}
