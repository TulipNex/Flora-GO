package store

import "go.mau.fi/whatsmeow/types/events"

// GetQuotedBotText mengecek apakah pesan masuk (evt) adalah reply ke
// sebuah pesan yang PERNAH dikirim bot, dan kalau ya, mengembalikan TEKS
// bubble bot yang di-reply itu. Mengembalikan string kosong kalau pesan
// ini bukan reply ke bot, atau kalau teks bubble-nya sudah terbuang dari
// cache in-memory (lihat maxTrackedIDs di bot_message_tracker.go).
//
// Reply di WhatsApp SELALU masuk lewat ContextInfo pada ExtendedTextMessage
// (pesan teks biasa dengan quote), konsisten dengan cara extractText()
// (internal/handler/handler.go) membaca teks pesan masuk — reply murni
// tanpa media tidak pernah muncul sebagai Conversation polos.
func GetQuotedBotText(evt *events.Message) string {
	msg := evt.Message
	if msg == nil {
		return ""
	}

	ext := msg.GetExtendedTextMessage()
	if ext == nil {
		return ""
	}

	ctxInfo := ext.GetContextInfo()
	if ctxInfo == nil {
		return ""
	}

	stanzaID := ctxInfo.GetStanzaID()
	if stanzaID == "" {
		return ""
	}

	return GetBotMessageText(stanzaID)
}
