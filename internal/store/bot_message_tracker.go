package store

import "sync"

// botMessages menyimpan ID pesan yang baru-baru ini dikirim bot BESERTA
// teks isinya, dipakai untuk dua keperluan:
//  1. Mendeteksi apakah sebuah pesan masuk adalah reply ke pesan bot
//     (dicek lewat ContextInfo.StanzaID pesan masuk) — sudah ada sejak
//     Fase 2.
//  2. BARU: mengambil kembali TEKS bubble bot yang di-reply, supaya bisa
//     disertakan sebagai konteks ke AI saat user reply salah satu bubble
//     dari balasan multi-bubble (Fase 5) — user tidak perlu mengulang
//     topik yang ingin ditanyakan lebih lanjut.
//
// Diubah dari map[string]struct{} (cuma set ID) menjadi map[string]string
// (ID -> teks) untuk mendukung kebutuhan #2 di atas, tanpa mengubah
// perilaku #1 yang sudah ada.
var (
	botMessagesMu sync.Mutex
	botMessages   = make(map[string]string)
	botMessageOrder []string
	maxTrackedIDs   = 200
)

// TrackBotMessageID mencatat ID pesan yang baru dikirim bot BESERTA
// teksnya. Dipanggil dari store.ReplyText dan store.SendText setiap kali
// bot berhasil mengirim pesan.
func TrackBotMessageID(id, text string) {
	if id == "" {
		return
	}
	botMessagesMu.Lock()
	defer botMessagesMu.Unlock()

	if _, exists := botMessages[id]; exists {
		return
	}

	botMessages[id] = text
	botMessageOrder = append(botMessageOrder, id)

	// buang yang paling lama kalau kepenuhan
	if len(botMessageOrder) > maxTrackedIDs {
		oldest := botMessageOrder[0]
		botMessageOrder = botMessageOrder[1:]
		delete(botMessages, oldest)
	}
}

// IsBotMessageID mengecek apakah suatu ID pesan pernah dikirim bot.
func IsBotMessageID(id string) bool {
	if id == "" {
		return false
	}
	botMessagesMu.Lock()
	defer botMessagesMu.Unlock()
	_, exists := botMessages[id]
	return exists
}

// GetBotMessageText mengambil kembali teks pesan bot dari ID-nya (kalau
// masih tersimpan di cache — pesan lama yang sudah terbuang karena
// maxTrackedIDs mengembalikan string kosong). Dipakai untuk menyertakan
// konteks bubble yang di-reply user ke dalam prompt AI.
func GetBotMessageText(id string) string {
	if id == "" {
		return ""
	}
	botMessagesMu.Lock()
	defer botMessagesMu.Unlock()
	return botMessages[id]
}
