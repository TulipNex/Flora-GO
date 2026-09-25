package store

import (
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types/events"
)

// extractContextInfo membuka bungkus pesan (UnwrapMessage) lalu mengembalikan
// ContextInfo pertama yang ditemukan di antara tipe-tipe pesan yang memang
// mendukung quote/reply/mention (teks, gambar, video, audio, stiker, dokumen).
//
// Ini SATU-SATUNYA tempat pembacaan ContextInfo di seluruh bot (hasil audit
// duplikasi kategori A2). Sebelumnya ada 3 implementasi berbeda dengan tingkat
// kerobustan berbeda: ExtractStanzaID mengunwrap + cek 6 tipe, tapi
// MentionedJIDs/QuotedParticipant (group_helpers.go) dan extractor quoted di
// plugins/sticker.go / stickerwatermark.go hanya baca ExtendedTextMessage dari
// pesan mentah TANPA unwrap — akibatnya command ber-basis reply seperti
// ".kick" (reply), ".sticker" (reply), dan ".swm" gagal di grup
// ber-Pesan-Sementara/pesan sekali-lihat. Semua pemanggil sekarang delegasi ke
// sini supaya perilaku unwrap & cakupan tipe tidak pernah menyimpang lagi.
func extractContextInfo(evt *events.Message) *waE2E.ContextInfo {
	msg := UnwrapMessage(evt.Message)
	if msg == nil {
		return nil
	}

	if ext := msg.GetExtendedTextMessage(); ext != nil && ext.GetContextInfo() != nil {
		return ext.GetContextInfo()
	}
	if img := msg.GetImageMessage(); img != nil && img.GetContextInfo() != nil {
		return img.GetContextInfo()
	}
	if vid := msg.GetVideoMessage(); vid != nil && vid.GetContextInfo() != nil {
		return vid.GetContextInfo()
	}
	if aud := msg.GetAudioMessage(); aud != nil && aud.GetContextInfo() != nil {
		return aud.GetContextInfo()
	}
	if stk := msg.GetStickerMessage(); stk != nil && stk.GetContextInfo() != nil {
		return stk.GetContextInfo()
	}
	if doc := msg.GetDocumentMessage(); doc != nil && doc.GetContextInfo() != nil {
		return doc.GetContextInfo()
	}

	return nil
}

// ExtractStanzaID mengambil ContextInfo.StanzaID (ID pesan yang di-reply)
// dari berbagai tipe pesan yang mendukung quote/reply.
// Balikan "" berarti pesan ini bukan reply ke apa pun.
func ExtractStanzaID(evt *events.Message) string {
	ci := extractContextInfo(evt)
	if ci == nil {
		return ""
	}
	return ci.GetStanzaID()
}

// ExtractMentionedJIDs mengambil daftar JID yang di-mention (@tag) di pesan
// ini, lewat satu jalur ContextInfo bersama (lihat extractContextInfo).
func ExtractMentionedJIDs(evt *events.Message) []string {
	ci := extractContextInfo(evt)
	if ci == nil {
		return nil
	}
	return ci.GetMentionedJID()
}

// ExtractQuotedParticipant mengambil JID pengirim pesan yang di-reply
// (ContextInfo.Participant). Mengembalikan string kosong kalau bukan reply.
func ExtractQuotedParticipant(evt *events.Message) string {
	ci := extractContextInfo(evt)
	if ci == nil {
		return ""
	}
	return ci.GetParticipant()
}

// ExtractQuotedMessage mengambil isi pesan yang di-reply (ContextInfo.QuotedMessage)
// apa adanya, untuk dibaca tipe medianya (mis. .sticker/.swm yang reply ke
// gambar). Mengembalikan nil kalau bukan reply. Hasilnya BISA saja masih
// terbungkus wrapper (Ephemeral/ViewOnce) — pemanggil yang butuh isi pesannya
// sebaiknya lewatkan lagi ke UnwrapMessage.
func ExtractQuotedMessage(evt *events.Message) *waE2E.Message {
	ci := extractContextInfo(evt)
	if ci == nil {
		return nil
	}
	return ci.GetQuotedMessage()
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
