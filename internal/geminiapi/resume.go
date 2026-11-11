package geminiapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
)

// Resume adalah "tiket percakapan" yang dikembalikan server: tiga id yang
// membuat Gemini mengingat percakapan tanpa bot perlu menyalin ulang riwayat.
//
// Ini akselerator, bukan syarat: bila server menolak tiket (kode 1097) atau
// tiket kosong, pemanggil kembali memakai riwayat di dalam prompt.
type Resume struct {
	ConversationID string
	ResponseID     string
	ReplyID        string
	// CookieID adalah sidik jari sesi tamu yang membuat tiket ini — hash
	// pendek, bukan nilai cookie. Percakapan tamu terikat pada cookie `NID`:
	// begitu cookie berganti (rotasi 30 menit, atau proses baru setelah
	// restart), tiket lama dijawab `1097` (dibuktikan probe Nov 2026). Dengan
	// sidik jari ini bot membuang tiket basi tanpa mengirim permintaan yang
	// pasti gagal. Kosong = belum ditandai (tiket tetap dikirim seperti dulu).
	CookieID string
}

// Empty true bila tidak ada satu pun id yang bisa dipakai.
func (r Resume) Empty() bool {
	return r.ConversationID == "" && r.ResponseID == "" && r.ReplyID == ""
}

// array menyusun resumeArray 10 slot seperti klien web: tiga id di depan,
// enam null, dan string kosong di akhir. Bentuk inilah yang dikirim di slot
// ketiga f.req.
func (r Resume) array() []any {
	out := make([]any, 10)
	out[0] = r.ConversationID
	out[1] = r.ResponseID
	out[2] = r.ReplyID
	out[9] = ""
	return out
}

// AskRequest adalah satu permintaan chat. UID kosong berarti memakai identitas
// klien yang dibuat saat New(); isi sendiri hanya bila identitas perlu
// dipertahankan lintas restart (lihat plugins/gemini.go).
type AskRequest struct {
	Prompt string
	Resume Resume
	UID    string
}

// newUID membuat UUID v4 huruf besar, dipakai sebagai identitas klien tamu.
// Nilainya bukan rahasia: hanya supaya rangkaian permintaan tampak berasal
// dari klien yang sama, seperti browser.
func newUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand praktis tidak pernah gagal; kalau gagal, identitas acak
		// bukan hal kritis untuk kegagalan permintaan.
		return strings.Repeat("0", 32)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // versi 4
	b[8] = (b[8] & 0x3f) | 0x80 // varian RFC 4122
	h := strings.ToUpper(hex.EncodeToString(b[:]))
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// hexToken menghasilkan 2*n karakter hex acak (pengganti crypto.randomBytes(n)
// di referensi JS: 8 byte untuk header, 16 byte untuk payload).
func hexToken(n int) string {
	if n <= 0 {
		return ""
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return strings.Repeat("0", n*2)
	}
	return hex.EncodeToString(b)
}

// envBool membaca saklar dari environment. Nilai kosong = def.
func envBool(name string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "on", "yes":
		return true
	case "0", "false", "off", "no":
		return false
	}
	return def
}

// buildChatRequestFull menyusun f.req bentuk lengkap seperti halaman Gemini
// (80 slot), termasuk identitas klien dan tiket percakapan. Bentuk ini
// dipertahankan apa adanya supaya server melihat permintaan yang sama dengan
// browser; slot yang tidak dipakai tetap null.
func buildChatRequestFull(prompt, lang string, r Resume, uid string) string {
	if strings.TrimSpace(lang) == "" {
		lang = defaultLang
	}
	inner := make([]any, 80)
	inner[0] = []any{prompt, 0, nil, nil, nil, nil, 0}
	inner[1] = []any{lang}
	inner[2] = r.array()
	inner[3] = ""
	inner[4] = hexToken(16)
	inner[6] = []any{1}
	inner[7] = 1
	inner[10] = 1
	inner[11] = 0
	inner[17] = []any{[]any{0}}
	inner[18] = 0
	inner[27] = 1
	inner[30] = []any{4}
	inner[41] = []any{2}
	inner[53] = 0
	inner[59] = uid
	inner[61] = []any{}
	inner[68] = 2
	inner[79] = 1
	return marshalChatRequest(inner)
}

// buildChatRequest (bentuk lama) menyusun f.req minimal tiga slot:
// [[prompt], [lang], []]. Tetap dipertahankan sebagai jalur cadangan bila
// bentuk penuh ditolak server atau dimatikan lewat GEMINI_FULL_PAYLOAD=0.
func buildChatRequest(prompt, lang string) string {
	if strings.TrimSpace(lang) == "" {
		lang = defaultLang
	}
	inner := []any{
		[]any{prompt},
		[]any{lang},
		[]any{},
	}
	return marshalChatRequest(inner)
}

// marshalChatRequest membungkus payload dalam bentuk luar [null, "<json>"],
// persis seperti yang diharapkan endpoint StreamGenerate.
func marshalChatRequest(inner []any) string {
	raw, err := json.Marshal(inner)
	if err != nil {
		// Isinya hanya tipe JSON dasar; kegagalan di sini berarti bug
		// pemrograman, bukan masukan pengguna.
		return "[]"
	}
	outer, err := json.Marshal([]any{nil, string(raw)})
	if err != nil {
		return "[]"
	}
	return string(outer)
}

// Resume mengambil tiket percakapan dari hasil satu giliran.
func (r *Result) Resume() Resume {
	return Resume{
		ConversationID: r.ConversationID,
		ResponseID:     r.ResponseID,
		ReplyID:        r.ReplyID,
	}
}
