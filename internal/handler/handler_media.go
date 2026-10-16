package handler

import (
	"wabot-go/internal/store"

	"github.com/polymorfa/hypermeow/types/events"
)

// extractText mengambil teks dari berbagai tipe pesan (teks biasa, caption gambar, dll).
//
// Pesan di-unwrap dulu lewat store.UnwrapMessage — WhatsApp membungkus isi
// pesan asli satu-tiga layer di dalam EphemeralMessage ("Pesan Sementara" di
// grup) atau ViewOnceMessage (foto/video sekali lihat), sehingga tanpa
// unwrap ini semua getter di bawah akan selalu kosong untuk pesan bertipe
// tersebut dan bot terlihat "diam" di grup yang mengaktifkan fitur itu.
// Fungsi unwrap yang sama juga dipakai oleh ExtractStanzaID/
// IsStickerMessage/IsMediaMessage di internal/store/trigger_extract.go
// supaya perilakunya konsisten di semua jalur.
func extractText(evt *events.Message) string {
	msg := store.UnwrapMessage(evt.Message)
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

	return ""
}
