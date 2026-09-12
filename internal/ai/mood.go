package ai

import "strings"

// Mood merepresentasikan suasana hati user yang terdeteksi dari teks pesan.
// Versi Fase 4 sengaja sederhana: hanya deteksi mood USER (bukan mood bot
// sendiri, bukan crush detector) sesuai keputusan desain di blueprint v2.
type Mood string

const (
	MoodNeutral  Mood = "neutral"
	MoodSad      Mood = "sad"
	MoodAngry    Mood = "angry"
	MoodHappy    Mood = "happy"
	MoodStressed Mood = "stressed"
)

// moodKeywords adalah daftar kata kunci per mood, dicek sebagai substring
// case-insensitive. Bukan daftar lengkap/ilmiah — cukup untuk sinyal kasar
// yang membantu nada balasan AI, bukan analisis psikologis.
var moodKeywords = map[Mood][]string{
	MoodSad:      {"sedih", "kecewa", "capek banget", "down", "galau", "nangis"},
	MoodAngry:    {"kesel", "marah", "bete", "anjir", "kzl", "emosi"},
	MoodHappy:    {"seneng", "senang", "yeay", "asik", "asyik", "mantap", "seru"},
	MoodStressed: {"stress", "stres", "pusing", "banyak kerjaan", "deadline", "capek"},
}

// DetectUserMood mengecek teks pesan terhadap kata kunci di atas.
// Mengembalikan mood pertama yang cocok; kalau tidak ada yang cocok,
// mengembalikan MoodNeutral.
func DetectUserMood(text string) Mood {
	lower := strings.ToLower(text)
	for mood, keywords := range moodKeywords {
		for _, kw := range keywords {
			if strings.Contains(lower, kw) {
				return mood
			}
		}
	}
	return MoodNeutral
}

// MoodModifier mengembalikan satu baris instruksi tambahan untuk system
// prompt, disesuaikan mood yang terdeteksi. String kosong untuk MoodNeutral
// (tidak perlu modifier tambahan kalau tidak ada sinyal khusus).
func MoodModifier(mood Mood) string {
	switch mood {
	case MoodSad:
		return "Catatan: user sepertinya sedang sedih. Tanggapi dengan empati, jangan terlalu ceria atau bercanda dulu."
	case MoodAngry:
		return "Catatan: user sepertinya sedang kesal. Jangan menambah emosi, tetap tenang dan membantu."
	case MoodStressed:
		return "Catatan: user sepertinya sedang stres/banyak kerjaan. Jawab ringkas dan langsung ke inti, jangan bertele-tele."
	case MoodHappy:
		return "Catatan: user sepertinya sedang senang. Boleh ikut antusias secukupnya."
	default:
		return ""
	}
}
