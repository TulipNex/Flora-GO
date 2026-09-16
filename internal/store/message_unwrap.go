package store

import "go.mau.fi/whatsmeow/proto/waE2E"

// UnwrapMessage membuka bungkus EphemeralMessage (mode "Pesan Sementara" di
// grup) dan semua varian ViewOnceMessage (foto/video sekali lihat), lalu
// mengembalikan pesan asli di dalamnya.
//
// WhatsApp membungkus isi pesan asli satu-tiga layer di dalam wrapper ini —
// Conversation/ExtendedTextMessage/ImageMessage/dst TIDAK berada langsung di
// level teratas *waE2E.Message untuk pesan bertipe tersebut. Semua fungsi
// yang membaca evt.Message secara langsung (extractText di
// internal/handler, dan ExtractStanzaID/IsStickerMessage/IsMediaMessage di
// file ini) WAJIB memanggil UnwrapMessage lebih dulu, kalau tidak pesan di
// grup ber-Pesan-Sementara akan selalu tampak kosong/tidak dikenali.
//
// Aman dipanggil berulang kali / pada pesan yang tidak dibungkus apa pun —
// akan langsung mengembalikan msg apa adanya kalau tidak ada wrapper yang
// cocok.
func UnwrapMessage(msg *waE2E.Message) *waE2E.Message {
	if msg == nil {
		return nil
	}

	if eph := msg.GetEphemeralMessage(); eph != nil {
		msg = eph.GetMessage()
	}
	if msg == nil {
		return nil
	}

	if vo := msg.GetViewOnceMessage(); vo != nil {
		msg = vo.GetMessage()
	}
	if msg == nil {
		return nil
	}

	if vo2 := msg.GetViewOnceMessageV2(); vo2 != nil {
		msg = vo2.GetMessage()
	}
	if msg == nil {
		return nil
	}

	if vo2e := msg.GetViewOnceMessageV2Extended(); vo2e != nil {
		msg = vo2e.GetMessage()
	}

	return msg
}
