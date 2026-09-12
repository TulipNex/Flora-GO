package ai

import "regexp"

// TriggerRegex mendeteksi kata panggilan bot dalam teks bebas.
// \b memastikan whole-word match (tidak nyangkut di tengah kata lain,
// misal "flo" tidak match ke "floor").
var TriggerRegex = regexp.MustCompile(`(?i)\b(flora|flor|flo)\b`)

// TriggerInput adalah data minimal yang dibutuhkan untuk memutuskan
// apakah sebuah pesan memicu AI. Dibuat sebagai struct polos (bukan
// langsung terima tipe event whatsmeow) supaya package ai/ tidak perlu
// import whatsmeow — ekstraksi dari event dilakukan di sisi pemanggil
// (internal/handler), package ini murni logic keputusan.
type TriggerInput struct {
	Text            string // teks pesan (kosong kalau pesan cuma media tanpa caption)
	IsReplyToBot    bool   // true kalau pesan ini reply ke pesan yang dikirim bot
	IsStickerMsg    bool   // true kalau pesan ini berupa stiker
	IsMediaMsg      bool   // true kalau pesan ini berupa media (gambar/video/audio/dokumen)
	HasActiveSession bool  // true kalau sender ini punya sesi AI yang masih aktif (belum idle-timeout)
}

// TriggerResult menjelaskan alasan match, berguna untuk logging/debug.
type TriggerResult struct {
	Matched bool
	Reason  string // "keyword" | "reply" | "sticker_reply" | "media_reply_no_caption"
}

// DetectTrigger memutuskan apakah pesan ini harus memicu AI.
//
// Urutan prioritas cek:
//  1. Kata kunci ("flora"/"flor"/"flo") di teks — selalu memicu,
//     terlepas dari status sesi (ini yang "membangunkan" sesi baru).
//  2. Reply ke pesan bot, sticker-reply, atau media-reply tanpa
//     caption — HANYA memicu kalau sender masih punya sesi aktif
//     (mencegah reply ke pesan bot yang sudah lama/basi ikut ke-trigger).
func DetectTrigger(in TriggerInput) TriggerResult {
	if in.Text != "" && TriggerRegex.MatchString(in.Text) {
		return TriggerResult{Matched: true, Reason: "keyword"}
	}

	if !in.HasActiveSession {
		return TriggerResult{Matched: false}
	}

	if in.IsStickerMsg && in.IsReplyToBot {
		return TriggerResult{Matched: true, Reason: "sticker_reply"}
	}

	if in.Text == "" && in.IsMediaMsg && in.IsReplyToBot {
		return TriggerResult{Matched: true, Reason: "media_reply_no_caption"}
	}

	if in.IsReplyToBot {
		return TriggerResult{Matched: true, Reason: "reply"}
	}

	return TriggerResult{Matched: false}
}
