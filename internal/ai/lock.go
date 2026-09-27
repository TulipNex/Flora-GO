package ai

import (
	"strings"
)

// LockAction adalah hasil parsing perintah lock/unlock dari teks pesan.
type LockAction int

const (
	// LockNone berarti pesan bukan perintah lock/unlock.
	LockNone LockAction = iota
	// LockOn berarti minta mengunci trigger natural Flora.
	LockOn
	// LockOff berarti minta membuka kunci trigger natural Flora.
	LockOff
)

// ParseLockCommand mendeteksi perintah "lock flo|flora" / "unlock flo|flora"
// atau "lock|unlock @flora" (mention bot, botMentioned=true). Pencocokan
// EKSak setelah normalisasi (lowercase, trim, collapse spasi) — kalimat
// seperti "tolong lock flo dong" SENGAJA tidak cocok supaya tidak ada lock
// tak sengaja dari obrolan biasa.
func ParseLockCommand(text string, botMentioned bool) LockAction {
	words := strings.Fields(strings.ToLower(strings.TrimSpace(text)))
	if len(words) == 0 {
		return LockNone
	}

	var action LockAction
	switch words[0] {
	case "lock":
		action = LockOn
	case "unlock":
		action = LockOff
	default:
		return LockNone
	}

	// Bentuk "lock flo" / "unlock flora": tepat 2 kata, kata kedua nama bot.
	if len(words) == 2 && (words[1] == "flo" || words[1] == "flora" || words[1] == "flor") {
		return action
	}

	// Bentuk "lock @flora": mention bot (teks @-nya bebas, yang penting
	// memang menandai bot). Tanpa mention bot = bukan perintah lock.
	if botMentioned {
		return action
	}

	return LockNone
}
