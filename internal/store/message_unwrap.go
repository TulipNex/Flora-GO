package store

import "github.com/polymorfa/hypermeow/proto/waE2E"

// UnwrapMessage membuka bungkus EphemeralMessage (mode "Pesan Sementara" di
// grup) dan semua varian ViewOnceMessage (foto/video/voice note sekali lihat),
// DocumentWithCaptionMessage, BotInvokeMessage, dsb. secara tuntas berulang
// (loop hingga tidak ada lagi wrapper), lalu mengembalikan pesan inti di dalamnya.
//
// WhatsApp dapat membungkus isi pesan asli dalam beberapa lapisan bersarang —
// Conversation/ExtendedTextMessage/ImageMessage/VideoMessage/AudioMessage/dst
// TIDAK berada langsung di level teratas *waE2E.Message untuk pesan bertipe tersebut.
//
// Aman dipanggil berulang kali / pada pesan yang tidak dibungkus apa pun —
// akan langsung mengembalikan msg apa adanya kalau tidak ada wrapper yang
// cocok.
func UnwrapMessage(msg *waE2E.Message) *waE2E.Message {
	if msg == nil {
		return nil
	}

	for i := 0; i < 10; i++ {
		unwrapped := false
		if eph := msg.GetEphemeralMessage(); eph != nil && eph.GetMessage() != nil {
			msg = eph.GetMessage()
			unwrapped = true
			continue
		}
		if vo := msg.GetViewOnceMessage(); vo != nil && vo.GetMessage() != nil {
			msg = vo.GetMessage()
			unwrapped = true
			continue
		}
		if vo2 := msg.GetViewOnceMessageV2(); vo2 != nil && vo2.GetMessage() != nil {
			msg = vo2.GetMessage()
			unwrapped = true
			continue
		}
		if vo2e := msg.GetViewOnceMessageV2Extension(); vo2e != nil && vo2e.GetMessage() != nil {
			msg = vo2e.GetMessage()
			unwrapped = true
			continue
		}
		if doc := msg.GetDocumentWithCaptionMessage(); doc != nil && doc.GetMessage() != nil {
			msg = doc.GetMessage()
			unwrapped = true
			continue
		}
		if bot := msg.GetBotInvokeMessage(); bot != nil && bot.GetMessage() != nil {
			msg = bot.GetMessage()
			unwrapped = true
			continue
		}
		if !unwrapped {
			break
		}
	}

	return msg
}
