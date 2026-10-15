package store

import "github.com/polymorfa/hypermeow/types/events"

// GetQuotedBotText mengecek apakah pesan masuk (evt) adalah reply ke
// sebuah pesan yang PERNAH dikirim bot, dan kalau ya, mengembalikan TEKS
// bubble bot yang di-reply itu. Mengembalikan string kosong kalau pesan
// ini bukan reply ke bot, atau kalau teks bubble-nya sudah terbuang dari
// cache in-memory (lihat maxTrackedIDs di bot_message_tracker.go).
//
// StanzaID diambil lewat ExtractStanzaID (trigger_extract.go) alih-alih
// membaca ContextInfo sendiri di sini — sebelumnya fungsi ini hanya
// mengecek GetExtendedTextMessage(), sehingga GAGAL mendeteksi reply yang:
//   - dikirim di dalam grup ber-Pesan-Sementara/pesan sekali-lihat (belum
//     di-unwrap), atau
//   - disertai media (reply berupa gambar/video/audio/stiker/dokumen,
//     bukan teks polos) — ContextInfo untuk kasus ini tersimpan di getter
//     tipe media yang bersangkutan, bukan di ExtendedTextMessage.
// ExtractStanzaID sudah menangani unwrap dan keenam tipe pesan tersebut,
// jadi reuse di sini menghindari dua logika ekstraksi StanzaID yang bisa
// diam-diam menyimpang lagi di masa depan.
func GetQuotedBotText(evt *events.Message) string {
	stanzaID := ExtractStanzaID(evt)
	if stanzaID == "" {
		return ""
	}
	return GetBotMessageText(stanzaID)
}
