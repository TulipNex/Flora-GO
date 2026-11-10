package plugins

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"wabot-go/internal/ai"
	"wabot-go/internal/geminiapi"
	"wabot-go/internal/identity"
	"wabot-go/internal/logfmt"
	"wabot-go/internal/store"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/types"
	"github.com/polymorfa/hypermeow/types/events"
)

func init() {
	registerPlugin(RegisterGemini)
}

// gemini.go — chat Gemini lewat antarmuka web dalam MODE TAMU (tanpa login).
//
// Kenapa mode tamu: versi sebelumnya memakai cookie akun Google khusus bot,
// dan bagian yang paling sering rusak justru sesinya (cookie pendamping
// kedaluwarsa, rotasi gagal, login ulang tiap restart). Halaman Gemini
// menerima permintaan tanpa akun selama klien mengirim cookie tamu (NID) yang
// diambil dari HEAD gemini.google.com, jadi tidak ada lagi berkas kredensial
// yang harus dirawat.
//
// Konsekuensinya: server TIDAK menyimpan percakapan tamu (lanjutan selalu
// ditolak dengan kode 1097). Riwayat karena itu disimpan bot di memori dan
// ditempel ke prompt berikutnya. Memori dipilih (bukan SQLite) supaya cocok
// dengan umur sesi yang pendek: 5 menit tanpa pesan = sesi mati, riwayat
// dibuang.
//
// Pemakaian (owner, boleh di DM maupun grup):
//
//	gemini <pertanyaan>      buka sesi / lanjutkan percakapan
//	(reply bubble bot)       lanjutkan tanpa kata pemicu
//	.gemini on               aktifkan sesi kosong (opsional)
//	.gemini off              akhiri sesi + buang riwayat
//	.gemini new              buang riwayat, sesi tetap aktif
//	.gemini status           status sesi + koneksi
//	.gemini diag             diagnosa jalur tamu (cookie → halaman → chat)
const (
	geminiTrigger = "gemini"

	geminiChatTimeout  = 3 * time.Minute
	geminiProbeTimeout = 20 * time.Second
	geminiDiagTimeout  = 3 * time.Minute

	geminiMaxPromptRunes = 8000
	geminiBubbleRunes    = 1000
	geminiBubbleDelay    = 3 * time.Second
	geminiMaxSystemRunes = 4000

	// geminiSessionTTL: sesi (beserta riwayatnya) mati setelah selama ini
	// tanpa pesan pemicu/balasan. Tanpa TTL, riwayat percakapan owner akan
	// menumpuk di memori selamanya.
	geminiSessionTTL = 5 * time.Minute

	// Batas riwayat yang ditempel ke prompt. Semua angka dalam rune.
	geminiMaxTurns        = 8
	geminiMaxHistoryRunes = 6000
	geminiTurnFieldRunes  = 1200
)

// geminiAckEmoji adalah reaksi yang dikirim ke pesan prompt sebagai tanda
// "diterima". Pengganti pesan tunggu: chat tetap bersih, jawaban nanti yang
// me-reply perintah.
const geminiAckEmoji = "✨"

// geminiWaitEmoji menggantikan ✨ bila giliran belum selesai setelah
// geminiAckEscalate. Tetap reaksi, bukan bubble teks.
const geminiWaitEmoji = "⏳"

// geminiAckEscalate adalah jeda sebelum reaksi ✨ berubah menjadi ⏳. Antrean
// server Gemini 8–20 dtk, jadi jawaban yang wajar hampir selalu melewatinya.
const geminiAckEscalate = 10 * time.Second

// geminiTurn adalah satu giliran percakapan yang disimpan bot. Bot adalah
// satu-satunya pemegang riwayat: server tidak menyimpannya untuk tamu.
type geminiTurn struct {
	User   string
	Bot    string
	Pinned bool
}

// geminiSession adalah keadaan sesi satu pengirim di satu chat. last dipakai
// TTL: sesi yang tidak disentuh selama geminiSessionTTL dibuang.
type geminiSession struct {
	history []geminiTurn
	last    time.Time
}

var (
	geminiMu       sync.Mutex
	geminiSessions = map[string]*geminiSession{}
	// geminiBusy menyerialkan satu permintaan per CHAT (bukan per pengirim):
	// dua permintaan serentak ke satu identitas tamu hanya akan saling
	// menunggu di klien.
	geminiBusy = map[string]bool{}

	geminiClientOnce sync.Once
	geminiAPIClient  *geminiapi.Client

	// geminiNow bisa diganti di tes supaya TTL dapat diuji tanpa tidur.
	geminiNow = time.Now
)

// geminiAPI mengembalikan klien tunggal (satu identitas tamu = satu antrean
// request; klien menyerialkan Ask di dalamnya).
func geminiAPI() *geminiapi.Client {
	geminiClientOnce.Do(func() {
		geminiAPIClient = geminiapi.New()
	})
	return geminiAPIClient
}

// RegisterGemini mendaftarkan .gemini (alias .gm). Command ini owner-only
// (kategori Owner otomatis main-only + disembunyikan dari non-owner) dan
// handler tetap memanggil requireOwner sebagai sabuk kedua.
func RegisterGemini(r *Registry) {
	r.Register(Command{
		Name:        "gemini",
		Aliases:     []string{"gm"},
		Description: "Chat Gemini mode tamu: on/off/new/status/diag (owner)",
		Category:    CategoryOwner,
		Handler:     handleGemini,
	})
}

// handleGemini adalah gerbang subcommand. Tanpa argumen = petunjuk, bukan
// error, mengikuti gaya .seedream. Teks bebas setelah ".gemini" diperlakukan
// sebagai prompt supaya owner bisa memakai bentuk command maupun kata pemicu.
func handleGemini(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}

	sub, rest := geminiSplitFirst(mctx.RawArgs)
	switch strings.ToLower(sub) {
	case "":
		return store.ReplyText(ctx, mctx, geminiHelpBlock())
	case "on":
		return geminiEnable(ctx, mctx)
	case "off":
		return geminiDisable(ctx, mctx)
	case "new", "reset":
		return geminiNewChat(ctx, mctx)
	case "sistem", "system":
		return geminiSystemCmd(ctx, mctx, rest)
	case "login", "cookie":
		return store.ReplyText(ctx, mctx, Block("Gemini",
			Row("Login", "tidak diperlukan"),
			"",
			"Mode tamu mengambil cookie sendiri dari gemini.google.com.",
			"Tidak ada akun atau cookie yang perlu disimpan.",
		))
	case "status":
		return geminiStatus(ctx, mctx)
	case "diag":
		return geminiDiag(ctx, mctx)
	default:
		// Bukan subcommand: seluruh RawArgs adalah prompt.
		prompt := strings.TrimSpace(mctx.RawArgs)
		if prompt == "" {
			return store.ReplyText(ctx, mctx, geminiHelpBlock())
		}
		go geminiRun(mctx.Client, mctx.RawEvt, prompt)
		return nil
	}
}

func geminiSplitFirst(s string) (first, rest string) {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, " \t\r\n"); i >= 0 {
		return s[:i], strings.TrimSpace(s[i+1:])
	}
	return s, ""
}

func geminiHelpBlock() string {
	return Block("Gemini",
		Opt("Mulai / lanjut", "gemini <pertanyaan>"),
		Opt("Lanjutkan", "balas bubble bot"),
		Opt("Aktifkan", ".gemini on"),
		Opt("Matikan", ".gemini off"),
		Opt("Riwayat baru", ".gemini new"),
		Opt("Prompt sistem", ".gemini sistem <teks>"),
		Opt("Status", ".gemini status"),
		Opt("Diagnosa", ".gemini diag"),
		"",
		"Mode tamu, tanpa login. Riwayat disimpan di memori bot dan",
		fmt.Sprintf("dibuang setelah %d menit tanpa pesan.", int(geminiSessionTTL.Minutes())),
	)
}

// --- keadaan sesi (memori saja) ---

// geminiKey mengembalikan kunci sesi stabil: satu pengirim di satu chat.
// Riwayat sengaja per pengirim, bukan per grup, supaya percakapan owner tidak
// tercampur bila bot dipakai beberapa orang di grup yang sama.
func geminiKey(client *whatsmeow.Client, chat, sender, alt types.JID) string {
	return chat.String() + "|" + identity.CanonicalUser(context.Background(), client, sender, alt)
}

func geminiKeyOf(mctx *store.MessageContext) string {
	var alt types.JID
	if mctx.RawEvt != nil {
		alt = mctx.RawEvt.Info.SenderAlt
	}
	return geminiKey(mctx.Client, mctx.Chat, mctx.Sender, alt)
}

// geminiPruneLocked membuang sesi yang sudah melewati TTL. Dipanggil setiap
// akses supaya tidak perlu goroutine penjaga; jumlah sesi dibatasi owner.
func geminiPruneLocked(now time.Time) {
	for key, st := range geminiSessions {
		if now.Sub(st.last) > geminiSessionTTL {
			delete(geminiSessions, key)
		}
	}
}

// geminiSessionGet mengembalikan salinan riwayat sesi yang masih hidup.
func geminiSessionGet(key string) ([]geminiTurn, bool) {
	geminiMu.Lock()
	defer geminiMu.Unlock()
	geminiPruneLocked(geminiNow())
	st := geminiSessions[key]
	if st == nil {
		return nil, false
	}
	return append([]geminiTurn(nil), st.history...), true
}

// geminiSessionEnsure membuat sesi (kalau belum ada) dan memperbarui waktunya.
// Dipakai saat prompt diterima: kata pemicu membuka sesi tanpa perlu `.on`.
func geminiSessionEnsure(key string) {
	now := geminiNow()
	geminiMu.Lock()
	defer geminiMu.Unlock()
	geminiPruneLocked(now)
	st := geminiSessions[key]
	if st == nil {
		st = &geminiSession{}
		geminiSessions[key] = st
	}
	st.last = now
}

func geminiSessionTouch(key string) {
	now := geminiNow()
	geminiMu.Lock()
	defer geminiMu.Unlock()
	if st := geminiSessions[key]; st != nil {
		st.last = now
	}
}

func geminiSessionAddTurn(key string, turn geminiTurn) {
	now := geminiNow()
	geminiMu.Lock()
	defer geminiMu.Unlock()
	st := geminiSessions[key]
	if st == nil {
		st = &geminiSession{}
		geminiSessions[key] = st
	}
	st.history = geminiAppendTurn(st.history, turn)
	st.last = now
}

func geminiSessionClear(key string) bool {
	geminiMu.Lock()
	defer geminiMu.Unlock()
	geminiPruneLocked(geminiNow())
	st := geminiSessions[key]
	if st == nil {
		return false
	}
	st.history = nil
	st.last = geminiNow()
	return true
}

func geminiSessionDrop(key string) bool {
	geminiMu.Lock()
	defer geminiMu.Unlock()
	geminiPruneLocked(geminiNow())
	_, ok := geminiSessions[key]
	delete(geminiSessions, key)
	return ok
}

// geminiSessionInfo melaporkan jumlah giliran + sisa umur sesi.
func geminiSessionInfo(key string) (turns int, left time.Duration, active bool) {
	now := geminiNow()
	geminiMu.Lock()
	defer geminiMu.Unlock()
	geminiPruneLocked(now)
	st := geminiSessions[key]
	if st == nil {
		return 0, 0, false
	}
	return len(st.history), geminiSessionTTL - now.Sub(st.last), true
}

// geminiChatClaim menandai satu chat sedang memproses permintaan.
func geminiChatClaim(chat string) bool {
	geminiMu.Lock()
	defer geminiMu.Unlock()
	if geminiBusy[chat] {
		return false
	}
	geminiBusy[chat] = true
	return true
}

func geminiChatRelease(chat string) {
	geminiMu.Lock()
	defer geminiMu.Unlock()
	delete(geminiBusy, chat)
}

// --- riwayat percakapan ---

// geminiDropOldestNonPinned membuang giliran tertua yang bukan pinned dari
// riwayat. Mengembalikan slice baru dan status apakah ada yang dibuang.
func geminiDropOldestNonPinned(history []geminiTurn) ([]geminiTurn, bool) {
	for i, t := range history {
		if !t.Pinned {
			out := make([]geminiTurn, 0, len(history)-1)
			out = append(out, history[:i]...)
			out = append(out, history[i+1:]...)
			return out, true
		}
	}
	return history, false
}

// geminiPinnedSystemRunes menghitung panjang ekstra teks sistem pada giliran pinned
// agar total prompt di geminiBuildPrompt boleh melewati 8000 rune sebesar teks sistem tersebut.
func geminiPinnedSystemRunes(history []geminiTurn) int {
	for _, t := range history {
		if t.Pinned {
			const sep = "\n\n[Pertanyaan]\n"
			if idx := strings.Index(t.User, sep); idx >= 0 {
				return utf8.RuneCountInString(t.User[:idx+len(sep)])
			}
			return utf8.RuneCountInString(t.User)
		}
	}
	return 0
}

// geminiAppendTurn menambahkan satu giliran lalu memangkas riwayat: field
// panjang non-pinned dipotong 1200 rune (giliran pinned tidak dipotong),
// dan pemangkasan riwayat (8 giliran / 6000 rune) membuang giliran selain
// yang pinned lebih dulu.
func geminiAppendTurn(history []geminiTurn, turn geminiTurn) []geminiTurn {
	if !turn.Pinned {
		turn.User = geminiClipRunes(turn.User, geminiTurnFieldRunes)
	}
	turn.Bot = geminiClipRunes(turn.Bot, geminiTurnFieldRunes)
	history = append(history, turn)
	for len(history) > geminiMaxTurns || geminiHistoryRunes(history) > geminiMaxHistoryRunes {
		if len(history) <= 1 {
			break
		}
		var dropped bool
		history, dropped = geminiDropOldestNonPinned(history)
		if !dropped {
			break
		}
	}
	return history
}

func geminiHistoryRunes(history []geminiTurn) int {
	total := 0
	for _, t := range history {
		total += utf8.RuneCountInString(t.User) + utf8.RuneCountInString(t.Bot)
	}
	return total
}

// geminiHistoryPrompt merakit prompt dengan konteks percakapan sebelumnya.
// Mode tamu tidak bisa melanjutkan percakapan di server, jadi inilah satu-
// satunya cara bot menjaga konteks.
func geminiHistoryPrompt(history []geminiTurn, prompt string) string {
	var b strings.Builder
	b.WriteString("Berikut percakapan sebelumnya:\n")
	for _, t := range history {
		b.WriteString("User: ")
		b.WriteString(t.User)
		b.WriteString("\nGemini: ")
		b.WriteString(t.Bot)
		b.WriteString("\n")
	}
	b.WriteString("\nPertanyaan terbaru:\n")
	b.WriteString(prompt)
	return b.String()
}

// geminiBuildPrompt membuang giliran tertua non-pinned sampai prompt + riwayat muat
// dalam batas geminiMaxPromptRunes. Total request boleh melewati 8000 rune sebesar
// teks sistem pada giliran pinned, dan giliran pinned tidak pernah dibuang.
func geminiBuildPrompt(history []geminiTurn, prompt string) string {
	prompt = strings.TrimSpace(prompt)
	for len(history) > 0 {
		limit := geminiMaxPromptRunes + geminiPinnedSystemRunes(history)
		built := geminiHistoryPrompt(history, prompt)
		if utf8.RuneCountInString(built) <= limit {
			return built
		}
		var dropped bool
		history, dropped = geminiDropOldestNonPinned(history)
		if !dropped {
			return built
		}
	}
	return prompt
}

func geminiClipRunes(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max]) + "…"
}

// --- subcommand ---

func geminiEnable(ctx context.Context, mctx *store.MessageContext) error {
	key := geminiKeyOf(mctx)
	turns, _, active := geminiSessionInfo(key)
	geminiSessionEnsure(key)

	rows := []string{
		Row("Mode", "aktif"),
		Row("Riwayat", geminiTurnsText(turns)),
	}
	if active {
		rows[0] = Row("Mode", "sudah aktif")
	}
	state, err := geminiProbe(ctx)
	if err != nil {
		logfmt.Warn("gemini probe: %v\n", err)
	}
	rows = append(rows, Row("Koneksi", state))
	rows = append(rows, "", "Kirim *gemini <pertanyaan>* atau balas bubble bot.")
	return store.ReplyText(ctx, mctx, Block("Gemini", rows...))
}

func geminiDisable(ctx context.Context, mctx *store.MessageContext) error {
	key := geminiKeyOf(mctx)
	if !geminiSessionDrop(key) {
		return store.ReplyText(ctx, mctx, ErrMsg("Gemini", "Sesi memang sedang nonaktif."))
	}
	return store.ReplyText(ctx, mctx, Block("Gemini",
		Row("Mode", "nonaktif"),
		Row("Riwayat", "dibuang"),
		"",
		"Sesi baru dibuka oleh *gemini <pertanyaan>* berikutnya.",
	))
}

func geminiNewChat(ctx context.Context, mctx *store.MessageContext) error {
	key := geminiKeyOf(mctx)
	if !geminiSessionClear(key) {
		geminiSessionEnsure(key)
		return store.ReplyText(ctx, mctx, Block("Gemini",
			Row("Mode", "aktif"),
			Row("Riwayat", "kosong"),
			"",
			"Belum ada riwayat untuk dibuang; pesan berikutnya memulai percakapan.",
		))
	}
	return store.ReplyText(ctx, mctx, Block("Gemini",
		Row("Mode", "aktif"),
		Row("Riwayat", "dibersihkan"),
		"",
		"Pesan berikutnya memulai percakapan tanpa konteks lama.",
	))
}

func geminiStatus(ctx context.Context, mctx *store.MessageContext) error {
	key := geminiKeyOf(mctx)
	turns, left, active := geminiSessionInfo(key)
	mode := "nonaktif"
	if active {
		mode = "aktif"
	}
	state, err := geminiProbe(ctx)
	if err != nil {
		logfmt.Warn("gemini status: %v\n", err)
	}

	sysText := "tidak ada"
	if gdb := store.GroupsFor(mctx.Client); gdb != nil {
		if sp, err := gdb.GetGeminiSystemPrompt(ctx); err == nil && strings.TrimSpace(sp) != "" {
			sysText = fmt.Sprintf("%d karakter", utf8.RuneCountInString(strings.TrimSpace(sp)))
		}
	}

	rows := []string{
		Row("Mode", mode),
		Row("Sistem", sysText),
		Row("Riwayat", geminiTurnsText(turns)),
		Row("Kedaluwarsa", geminiLeftText(active, left)),
		Row("Koneksi", state),
		Row("Penyimpanan", "memori bot"),
	}
	if !active {
		rows = append(rows, "", "Sesi dibuka otomatis oleh *gemini <pertanyaan>*.")
	}
	return store.ReplyText(ctx, mctx, Block("Gemini", rows...))
}

// geminiSystemAction menentukan jenis aksi dari sisa argumen subcommand .gemini sistem:
// "show" (kosong), "reset" (kata reset|hapus|off|clear), atau "set" untuk teks lainnya
// (termasuk bila diawali kata "reset ").
func geminiSystemAction(rest string) string {
	trimmed := strings.TrimSpace(rest)
	if trimmed == "" {
		return "show"
	}
	switch strings.ToLower(trimmed) {
	case "reset", "hapus", "off", "clear":
		return "reset"
	default:
		return "set"
	}
}

// geminiSystemRequest merangkai prompt sistem dan pertanyaan menjadi satu pesan
// untuk giliran pertama.
func geminiSystemRequest(sys, prompt string) string {
	sys = strings.TrimSpace(sys)
	prompt = strings.TrimSpace(prompt)
	if sys == "" {
		return prompt
	}
	return fmt.Sprintf("[Instruksi sistem]\n%s\n\n[Pertanyaan]\n%s", sys, prompt)
}

func geminiSystemCmd(ctx context.Context, mctx *store.MessageContext, rest string) error {
	gdb := store.GroupsFor(mctx.Client)
	if gdb == nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Gemini", "Database penyimpanan tidak tersedia."))
	}

	act := geminiSystemAction(rest)
	switch act {
	case "show":
		curr, err := gdb.GetGeminiSystemPrompt(ctx)
		if err != nil {
			logfmt.Warn("gemini baca prompt sistem: %v\n", err)
			return store.ReplyText(ctx, mctx, ErrMsg("Gemini", "Gagal membaca prompt sistem."))
		}
		curr = strings.TrimSpace(curr)
		if curr == "" {
			return store.ReplyText(ctx, mctx, Block("Gemini sistem",
				Row("Status", "belum disetel"),
				"",
				"Ketik *.gemini sistem <teks>* untuk menyetel (maks 4000 karakter).",
			))
		}
		return store.ReplyText(ctx, mctx, Block("Gemini sistem",
			Row("Panjang", fmt.Sprintf("%d karakter", utf8.RuneCountInString(curr))),
			"",
			curr,
			"",
			"Ketik *.gemini sistem reset* untuk menghapus.",
		))

	case "reset":
		if err := gdb.SetGeminiSystemPrompt(ctx, ""); err != nil {
			logfmt.Warn("gemini hapus prompt sistem: %v\n", err)
			return store.ReplyText(ctx, mctx, ErrMsg("Gemini", "Gagal menghapus prompt sistem."))
		}
		return store.ReplyText(ctx, mctx, Block("Gemini sistem",
			Row("Status", "dihapus"),
			"",
			"Prompt sistem dihapus. Jalankan *.gemini new* bila ingin langsung berlaku ke sesi aktif.",
		))

	case "set":
		text := strings.TrimSpace(rest)
		if count := utf8.RuneCountInString(text); count > geminiMaxSystemRunes {
			return store.ReplyText(ctx, mctx, ErrMsg("Gemini",
				fmt.Sprintf("Prompt sistem kepanjangan (%d karakter, maks %d).", count, geminiMaxSystemRunes)))
		}
		if err := gdb.SetGeminiSystemPrompt(ctx, text); err != nil {
			logfmt.Warn("gemini simpan prompt sistem: %v\n", err)
			return store.ReplyText(ctx, mctx, ErrMsg("Gemini", "Gagal menyimpan prompt sistem."))
		}
		return store.ReplyText(ctx, mctx, Block("Gemini sistem",
			Row("Status", "tersimpan"),
			Row("Panjang", fmt.Sprintf("%d karakter", utf8.RuneCountInString(text))),
			"",
			"Prompt sistem akan disertakan pada sesi baru.",
			"Jalankan *.gemini new* bila ingin langsung menerapkannya ke sesi aktif saat ini.",
		))
	}
	return nil
}

func geminiTurnsText(turns int) string {
	if turns <= 0 {
		return "kosong"
	}
	return fmt.Sprintf("%d giliran", turns)
}

func geminiLeftText(active bool, left time.Duration) string {
	if !active {
		return "-"
	}
	if left < 0 {
		left = 0
	}
	return left.Round(time.Second).String()
}

// geminiProbe memeriksa jalur tamu (HEAD + /app, tanpa kuota chat) dan
// mengembalikan label pendek untuk ditampilkan.
func geminiProbe(ctx context.Context) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, geminiProbeTimeout)
	defer cancel()
	if err := geminiAPI().Ping(cctx); err != nil {
		return "tidak terverifikasi: " + geminiTruncateText(geminiErrorText(err), 120), err
	}
	return "valid", nil
}

// geminiDiag adalah perintah diagnosa jalur tamu: cookie → halaman → satu
// percakapan. Keluarannya hanya status, tidak pernah nilai cookie.
func geminiDiag(ctx context.Context, mctx *store.MessageContext) error {
	cctx, cancel := context.WithTimeout(ctx, geminiDiagTimeout)
	defer cancel()
	d := geminiAPI().Diag(cctx)

	rows := []string{
		Row("Cookie", geminiStageText(d.Cookie)),
		Row("Halaman", geminiStageText(d.Meta)),
		Row("Chat", geminiStageText(d.Chat)),
	}
	// Durasi ditampilkan juga saat chat gagal: beda antara "berhenti di 30,0
	// dtk" (batas transport) dan "gagal dalam 0,2 dtk" (ditolak server) adalah
	// petunjuk utama saat mendiagnosis jalur tamu.
	if d.Latency > 0 {
		rows = append(rows, Row("Durasi", d.Latency.Round(time.Millisecond).String()))
	}
	if d.Answer != "" {
		rows = append(rows, Row("Jawaban", geminiTruncateText(d.Answer, 120)))
	}
	if d.Dump != "" {
		rows = append(rows, Row("Dump", filepath.Base(d.Dump)))
	}
	rows = append(rows, "", geminiDiagVerdict(d))
	return store.ReplyText(ctx, mctx, Block("Gemini diag", rows...))
}

func geminiStageText(s geminiapi.Stage) string {
	if s.OK {
		return "ok · " + s.Detail
	}
	if s.Detail == "" && s.Err != nil {
		s.Detail = s.Err.Error()
	}
	return "gagal · " + geminiTruncateText(s.Detail, 160)
}

// geminiDiagVerdict menyimpulkan penyebab kegagalan dari tahap pertama yang mati.
func geminiDiagVerdict(d *geminiapi.Diag) string {
	switch {
	case !d.Cookie.OK:
		return "Cookie tamu gagal diambil: masalahnya jaringan/blokir IP, bukan akun."
	case !d.Meta.OK:
		return "Parameter halaman tidak ditemukan: protokol web Gemini berubah, kode perlu disesuaikan."
	case !d.Chat.OK:
		switch {
		case errors.Is(d.Chat.Err, geminiapi.ErrLimit):
			return "Kuota/IP tamu kena batas. Coba lagi beberapa saat."
		case errors.Is(d.Chat.Err, geminiapi.ErrEmpty):
			return "Server menjawab tanpa teks: bentuk permintaan berubah (periksa Dump)."
		case errors.Is(d.Chat.Err, geminiapi.ErrStaleConv):
			return "Server menolak percakapan; mode tamu memang tidak mendukung lanjutan."
		default:
			return "Chat gagal: lihat baris Chat di atas."
		}
	}
	return "Jalur tamu sehat: cookie, halaman, dan chat bekerja."
}

// --- routing pasif ---

// CheckGeminiSession dipanggil handler untuk pesan non-command. Mengembalikan
// true bila pesan dikonsumsi sesi Gemini (diproses async), supaya tidak bocor
// ke autodownload/AI. Berlaku di DM maupun grup, selama pengirim owner.
func CheckGeminiSession(client *whatsmeow.Client, evt *events.Message, text string) bool {
	if evt == nil || !store.IsMainClient(client) {
		return false
	}
	if !IsOwnerEvent(evt) {
		return false
	}

	key := geminiKey(client, evt.Info.Chat, evt.Info.Sender, evt.Info.SenderAlt)
	_, active := geminiSessionGet(key)

	prompt, triggered := geminiTriggerOf(evt, text, active)
	if !triggered {
		return false
	}
	// "gemini" polos: jangan kirim prompt kosong, cukup ingatkan cara pakai —
	// tapi tetap dikonsumsi supaya tidak dijawab AI.
	if strings.TrimSpace(prompt) == "" {
		go func() {
			bg := context.Background()
			mctx := geminiContext(client, evt)
			_ = store.ReplyText(bg, mctx, ErrMsg("Gemini",
				"Tulis pertanyaan setelah kata gemini, atau balas bubble bot."))
		}()
		return true
	}

	go geminiRun(client, evt, prompt)
	return true
}

// geminiContext merakit MessageContext untuk jalur pasif (tanpa command).
func geminiContext(client *whatsmeow.Client, evt *events.Message) *store.MessageContext {
	return &store.MessageContext{
		Client: client,
		Chat:   evt.Info.Chat,
		Sender: evt.Info.Sender,
		RawEvt: evt,
	}
}

// geminiTriggerOf memutuskan apakah pesan memicu sesi Gemini, dan dengan
// prompt apa. Aturannya sempit supaya mode tidak menelan chat biasa:
//   - teks diawali "gemini" (diikuti spasi/tanda baca) → selalu memicu
//     (membuka sesi baru bila belum ada)
//   - reply ke bubble bot → hanya saat sesi masih hidup
//
// Lampiran tidak didukung: mode tamu tidak bisa mengunggah berkas.
func geminiTriggerOf(evt *events.Message, text string, active bool) (prompt string, ok bool) {
	text = strings.TrimSpace(text)
	if rest, cut := geminiCutPrefix(text); cut {
		return rest, true
	}
	if active && text != "" && store.IsReplyToBot(evt) {
		return text, true
	}
	return "", false
}

// geminiCutPrefix membuang kata pemicu "gemini" di awal teks.
func geminiCutPrefix(text string) (string, bool) {
	t := strings.TrimSpace(text)
	if len(t) < len(geminiTrigger) {
		return "", false
	}
	if !strings.EqualFold(t[:len(geminiTrigger)], geminiTrigger) {
		return "", false
	}
	rest := t[len(geminiTrigger):]
	if rest == "" {
		return "", true
	}
	switch rest[0] {
	case ' ', '\t', '\r', '\n', ':', ',', '-':
		return strings.TrimLeft(rest, " \t\r\n:,-"), true
	}
	return "", false
}

// --- pipeline satu giliran ---

// geminiEscalateAck mengubah reaksi tanda-terima menjadi ⏳ bila giliran belum
// selesai setelah jeda. Tidak pernah mengirim teks: kalau done sudah tertutup
// (jawaban atau kesalahan selesai) fungsi ini tidak melakukan apa pun.
func geminiEscalateAck(after time.Duration, done <-chan struct{}, send func() error) {
	if send == nil {
		return
	}
	t := time.NewTimer(after)
	defer t.Stop()
	select {
	case <-done:
		return
	case <-t.C:
	}
	if err := send(); err != nil {
		logfmt.Warn("gemini reaksi tunggu: %v\n", err)
	}
}

func geminiRun(client *whatsmeow.Client, evt *events.Message, prompt string) {
	if evt == nil {
		return
	}
	mctx := geminiContext(client, evt)
	chatKey := evt.Info.Chat.String()
	key := geminiKey(client, evt.Info.Chat, evt.Info.Sender, evt.Info.SenderAlt)

	if !geminiChatClaim(chatKey) {
		_ = store.ReplyText(context.Background(), mctx, ErrMsg("Gemini",
			"Masih memproses pesan sebelumnya. Tunggu sampai selesai."))
		return
	}
	defer geminiChatRelease(chatKey)

	bg := context.Background()
	ctx, cancel := context.WithTimeout(bg, geminiChatTimeout)
	defer cancel()

	// Prompt yang diterima memperbarui TTL sesi; riwayat lama ikut dibaca
	// sebelum giliran ini ditambahkan.
	geminiSessionEnsure(key)
	history, _ := geminiSessionGet(key)

	// Reaksi sebagai tanda diterima (tanpa bubble teks tunggu).
	if err := store.SendReaction(ctx, mctx, geminiAckEmoji); err != nil {
		logfmt.Warn("gemini reaksi: %v\n", err)
	}
	// Bila giliran belum selesai setelah ±10 dtk, reaksi berganti menjadi ⏳ —
	// tetap tanpa teks. turnDone ditutup setelah jawaban terkirim, jadi ⏳
	// tidak pernah muncul setelah jawaban ada.
	turnDone := make(chan struct{})
	defer close(turnDone)
	go geminiEscalateAck(geminiAckEscalate, turnDone, func() error {
		rctx, rcancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer rcancel()
		return store.SendReaction(rctx, mctx, geminiWaitEmoji)
	})

	prompt = strings.TrimSpace(prompt)
	if utf8.RuneCountInString(prompt) > geminiMaxPromptRunes {
		_ = store.ReplyText(bg, mctx, ErrMsg("Gemini",
			fmt.Sprintf("Pertanyaan kepanjangan (maks %d karakter).", geminiMaxPromptRunes)))
		return
	}

	first := len(history) == 0
	var sysPrompt string
	if first {
		if gdb := store.GroupsFor(client); gdb != nil {
			sp, err := gdb.GetGeminiSystemPrompt(ctx)
			if err != nil {
				logfmt.Warn("gemini baca prompt sistem: %v\n", err)
			} else {
				sysPrompt = strings.TrimSpace(sp)
			}
		}
	}

	isPinned := first && sysPrompt != ""
	reqPrompt := prompt
	if isPinned {
		reqPrompt = geminiSystemRequest(sysPrompt, prompt)
	}

	res, err := geminiAPI().Ask(ctx, geminiBuildPrompt(history, reqPrompt))
	if err != nil {
		logfmt.Warn("gemini: %v\n", err)
		_ = store.ReplyText(bg, mctx, ErrMsg("Gemini", geminiErrorText(err)))
		return
	}

	text := geminiCleanAnswer(res.Text)
	if text == "" {
		_ = store.ReplyText(bg, mctx, ErrMsg("Gemini", "Gemini tidak mengirim teks."))
		return
	}
	geminiSessionAddTurn(key, geminiTurn{
		User:   reqPrompt,
		Bot:    text,
		Pinned: isPinned,
	})

	if err := geminiSendText(ctx, mctx, text); err != nil {
		logfmt.Warn("gemini kirim: %v\n", err)
		_ = store.ReplyText(bg, mctx, ErrMsg("Gemini", err.Error()))
		return
	}
	geminiSessionTouch(key)
}

// Karakter tak terlihat & penanda internal sisa stream Gemini. Pembersihan
// Markdown standar ditangani ai.SanitizeForWhatsApp (pembersih yang sama
// dipakai flora agent): **tebal** → *tebal*, "---" dibuang, "### Judul"
// jadi *Judul*, baris kosong beruntun dirapatkan.
var (
	geminiInvisibleRe = regexp.MustCompile("[\u200b\u200e\u200f\u2060\ufeff]")
	geminiCtrlTagRe   = regexp.MustCompile(`<ctrl\d+>`)
)

// geminiCleanAnswer merapikan jawaban sebelum dikirim ke WhatsApp.
func geminiCleanAnswer(text string) string {
	text = geminiInvisibleRe.ReplaceAllString(text, "")
	text = geminiCtrlTagRe.ReplaceAllString(text, "")
	return strings.TrimSpace(ai.SanitizeForWhatsApp(text))
}

// geminiSendText mengirim jawaban; potongan pertama me-reply perintah user,
// sisanya pesan lepas (pola multi-bubble AI).
func geminiSendText(ctx context.Context, mctx *store.MessageContext, text string) error {
	text = geminiCleanAnswer(text)
	chunks := geminiSplitReply(text, geminiBubbleRunes)
	if len(chunks) == 0 {
		return fmt.Errorf("jawaban kosong")
	}
	if err := store.ReplyText(ctx, mctx, chunks[0]); err != nil {
		return err
	}
	for _, chunk := range chunks[1:] {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(geminiBubbleDelay):
		}
		if err := store.SendText(ctx, mctx, chunk); err != nil {
			return err
		}
	}
	return nil
}

var (
	geminiNumPrefixRe  = regexp.MustCompile(`^(\d+|[a-zA-Z])[.)]\s+`)
	geminiNumBracketRe = regexp.MustCompile(`^\[\d+\]\s+`)
)

func isNumberedLine(line string) bool {
	return geminiNumPrefixRe.MatchString(line) || geminiNumBracketRe.MatchString(line)
}

func isListMarkerLine(line string) bool {
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "* ") || strings.HasPrefix(line, "- ") ||
		strings.HasPrefix(line, "• ") || strings.HasPrefix(line, "+ ") ||
		strings.HasPrefix(line, "* *") || isNumberedLine(line) {
		return true
	}
	return false
}

func isHeadingCandidate(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" || utf8.RuneCountInString(line) > 200 {
		return false
	}
	// 1. Simbol bullet list (*, -, •, +) atau penomoran
	if isListMarkerLine(line) {
		return true
	}
	// 2. Markdown heading (# , ## , ### )
	if strings.HasPrefix(line, "# ") || strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "### ") {
		return true
	}
	// 3. Standalone bold: "*Judul*"
	if strings.HasPrefix(line, "*") && strings.HasSuffix(line, "*") && len(line) >= 3 {
		return true
	}
	// 4. Baris berakhiran titik dua (misal "Catatan:", "*Fitur:*")
	if strings.HasSuffix(line, ":") || strings.HasSuffix(line, ":*") {
		return true
	}
	return false
}

func getLastNonEmptyLine(s string) string {
	s = strings.TrimRight(s, " \t\r\n")
	if s == "" {
		return ""
	}
	idx := strings.LastIndexAny(s, "\r\n")
	if idx < 0 {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(s[idx+1:])
}

func getFirstNonEmptyLine(s string) string {
	s = strings.TrimLeft(s, " \t\r\n")
	if s == "" {
		return ""
	}
	idx := strings.IndexAny(s, "\r\n")
	if idx < 0 {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(s[:idx])
}

// isOrphanHeadingCut mendeteksi apakah memotong di batas before|after akan
// meninggalkan baris judul/penomoran sendirian di akhir before tanpa menyertakan
// paragraf penjelasnya (yang terlempar ke after).
func isOrphanHeadingCut(before, after string) bool {
	lastLine := getLastNonEmptyLine(before)
	if lastLine == "" || !isHeadingCandidate(lastLine) {
		return false
	}
	nextLine := getFirstNonEmptyLine(after)
	if nextLine == "" {
		return false
	}

	// Heading mandiri (*Judul* atau baris berakhiran ":") jangan pernah
	// diputus sendirian di akhir bubble jika ada teks lanjutan di bubble berikutnya.
	isStandalone := (strings.HasPrefix(lastLine, "*") && strings.HasSuffix(lastLine, "*") && !isListMarkerLine(lastLine)) ||
		strings.HasSuffix(lastLine, ":") || strings.HasSuffix(lastLine, ":*")
	if isStandalone {
		return true
	}

	// Untuk item list / penomoran (*, -, 1., dsb):
	// Jika baris berikutnya BUKAN item list / nomor baru, berarti baris berikutnya
	// adalah paragraf penjelasan bagi judul/nomor ini.
	if !isListMarkerLine(nextLine) {
		return true
	}

	return false
}

// geminiSplitReply memecah teks panjang menjadi potongan bubble <= size rune
// (default geminiBubbleRunes = 1000). Pemotongan memprioritaskan newline
// terdekat ke belakang dalam rentang [500, 1000) yang tidak memisahkan judul/
// penomoran dari paragrafnya, lalu fallback spasi terdekat di rentang itu,
// dan bila tidak ada jatuh ke potong paksa di batas size. Jamin cut > 0 agar tidak loop.
func geminiSplitReply(text string, size int) []string {
	text = strings.TrimSpace(text)
	if size <= 0 {
		size = geminiBubbleRunes
	}
	if utf8.RuneCountInString(text) <= size {
		if text == "" {
			return nil
		}
		return []string{text}
	}
	runes := []rune(text)
	minSplit := size / 2
	if minSplit < 1 {
		minSplit = 1
	}
	var out []string
	for len(runes) > size {
		cut := -1
		fallbackNewline := -1
		// 1. Cari newline terdekat ke belakang dalam rentang [minSplit, size)
		// yang TIDAK memisahkan judul/penomoran dari paragraf penjelasnya.
		for i := size - 1; i >= minSplit; i-- {
			if runes[i] == '\n' {
				cand := i + 1
				if fallbackNewline < 0 {
					fallbackNewline = cand
				}
				if !isOrphanHeadingCut(string(runes[:cand]), string(runes[cand:])) {
					cut = cand
					break
				}
			}
		}
		// Bila semua newline di rentang [minSplit, size) memisahkan judul,
		// gunakan newline fallback.
		if cut < 0 && fallbackNewline > 0 {
			cut = fallbackNewline
		}
		// 2. Kalau tidak ada newline, cari spasi terdekat ke belakang di rentang itu
		if cut < 0 {
			for i := size - 1; i >= minSplit; i-- {
				if runes[i] == ' ' || runes[i] == '\t' || runes[i] == '\r' {
					cut = i + 1
					break
				}
			}
		}
		// 3. Fallback: potong paksa di batas size
		if cut <= 0 {
			cut = size
		}
		chunk := strings.TrimSpace(string(runes[:cut]))
		if chunk != "" {
			out = append(out, chunk)
		}
		runes = runes[cut:]
	}
	if rest := strings.TrimSpace(string(runes)); rest != "" {
		out = append(out, rest)
	}
	return out
}

// geminiTruncateText memotong teks panjang dengan elipsis.
func geminiTruncateText(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max]) + "…"
}

// --- teks error ---

// geminiErrorText menerjemahkan error adapter jadi kalimat yang bisa
// ditindaklanjuti.
func geminiErrorText(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, geminiapi.ErrGuestCookie):
		return "Gagal mengambil sesi tamu Gemini (jaringan atau IP diblokir). Coba lagi."
	case errors.Is(err, geminiapi.ErrMeta):
		return "Parameter halaman Gemini tidak ditemukan (protokol web berubah). Jalankan .gemini diag."
	case errors.Is(err, geminiapi.ErrLimit):
		return "Batas penggunaan Gemini tercapai untuk tamu/IP ini. Coba lagi beberapa saat."
	case errors.Is(err, geminiapi.ErrStaleConv):
		return "Gemini menolak percakapan ini. Jalankan .gemini new lalu ulangi."
	case errors.Is(err, geminiapi.ErrEmpty), errors.Is(err, geminiapi.ErrProtocol):
		return "Balasan Gemini tidak terbaca (bisa jadi protokol webnya berubah). Coba lagi."
	case errors.Is(err, context.DeadlineExceeded):
		return "Waktu habis menunggu Gemini. Coba lagi."
	case errors.Is(err, geminiapi.ErrHTTP):
		return fmt.Sprintf("Permintaan ke Gemini gagal: %v", err)
	default:
		return fmt.Sprintf("Gagal: %v", err)
	}
}
