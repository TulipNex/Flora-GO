package ai

import "fmt"

// BuildTextWithQuotedContext menggabungkan teks pesan user dengan teks
// bubble bot yang di-reply (kalau ada), supaya AI tahu persis topik mana
// yang dimaksud user tanpa user perlu mengulang isi topiknya.
//
// Kalau quotedText kosong (pesan bukan reply ke bot, atau teks bubble
// sudah terbuang dari cache), fungsi ini mengembalikan userText apa
// adanya — TIDAK menambah embel-embel apapun, supaya perilaku untuk
// pesan biasa (bukan reply) sama persis seperti sebelum fitur ini ada.
func BuildTextWithQuotedContext(quotedText, userText string) string {
	if quotedText == "" {
		return userText
	}
	return fmt.Sprintf("[Membalas pesanmu sebelumnya: %q]\n%s", quotedText, userText)
}
