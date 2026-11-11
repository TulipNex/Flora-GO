package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	"wabot-go/internal/timex"

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
// Konteks percakapan ditangani dua lapis:
//  1. Tiket percakapan (lihat geminiapi.Resume) — bila server mengembalikan id
//     percakapan, giliran berikutnya cukup mengirim tiket itu dan server yang
//     mengingat. Tiket hanya hidup di MEMORI: percakapan tamu terikat NILAI
//     cookie `NID`, dan cookie itu selalu baru di proses baru (atau tiap
//     rotasi 30 menit), sehingga tiket simpanan pasti dijawab `1097`. Tiket
//     yang sidik jari cookienya sudah berbeda dibuang sebelum dikirim
//     (`ErrTicketStale`), jadi tidak ada kuota yang terbuang.
//  2. Riwayat di dalam prompt — jalan cadangan bila tiket tidak ada atau
//     ditolak server (kode 1097). Riwayat giliran disalin ke bot_settings
//     (tanpa kredensial apa pun), sehingga konteks tetap hidup setelah restart.
//
// Tiket hanya akselerator: kegagalan mengambil/memakainya tidak boleh
// menggagalkan jawaban, cukup kembali ke riwayat tanpa memberi tahu owner.
//
// Pemakaian (owner, boleh di DM maupun grup):
//
//	gemini <pertanyaan>      buka sesi / lanjutkan percakapan
//	(reply bubble bot)       lanjutkan tanpa kata pemicu
//	.gemini on               aktifkan sesi kosong (opsional)
//	.gemini off              akhiri sesi + buang riwayat
//	.gemini new              buang riwayat + tiket, sesi tetap aktif
//	.gemini lokasi <kota>    lokasi acuan jawaban (bukan lokasi server)
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

	// geminiMaxLocationRunes membatasi teks lokasi: cukup untuk
	// "Makassar, Sulawesi Selatan" tanpa membengkakkan blok konteks.
	geminiMaxLocationRunes = 160

	// geminiLocationKey adalah baris bot_settings tempat lokasi pengguna
	// disimpan (teks biasa, bukan kredensial). Kosong = belum disetel, dan blok
	// konteks meminta Gemini menanyakan kota/daerah lebih dulu.
	geminiLocationKey = "gemini_lokasi"

	// geminiSessionTTL: sesi (beserta riwayatnya) mati setelah selama ini
	// tanpa pesan pemicu/balasan. Tanpa TTL, riwayat percakapan owner akan
	// menumpuk di memori selamanya. Nilai ini juga menjadi batas umur riwayat
	// yang disimpan di bot_settings, supaya keduanya tidak bertentangan
	// (riwayat hidup lebih lama dari sesi = konteks tak terduga).
	geminiSessionTTL = 10 * time.Minute

	// Batas riwayat yang ditempel ke prompt. Semua angka dalam rune.
	geminiMaxTurns        = 8
	geminiMaxHistoryRunes = 6000
	geminiTurnFieldRunes  = 1200

	// geminiHistoryPrefix menandai baris bot_settings yang menyimpan riwayat
	// giliran. Isinya teks percakapan + waktu simpan — TIDAK ADA cookie/token,
	// karena percakapan tamu terikat nilai cookie `NID` yang selalu baru
	// (tiket yang disimpan ke disk pasti basi setelah restart).
	geminiHistoryPrefix = "gemini_hist:"

	// geminiLegacyResumePrefix adalah kunci versi lama yang menyimpan tiket
	// percakapan. Tiket itu tidak bisa dipakai lagi setelah restart (cookie
	// baru), jadi setiap pembersihan ikut membuang sisa barisnya.
	geminiLegacyResumePrefix = "gemini_resume:"
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
// TTL: sesi yang tidak disentuh selama geminiSessionTTL dibuang. resume/uid
// adalah tiket percakapan yang membuat server mengingat konteks; tiket hanya
// hidup di memori (percakapan tamu terikat cookie `NID` yang berganti tiap
// proses/rotasi, jadi menyimpannya ke disk tidak ada gunanya). Riwayat giliran
// yang disalin ke bot_settings itulah yang membuat konteks tahan restart.
type geminiSession struct {
	history []geminiTurn
	resume  geminiapi.Resume
	uid     string
	last    time.Time
}

// geminiSessionState adalah bentuk JSON riwayat yang disimpan di bot_settings.
// Isinya hanya giliran percakapan + waktu simpan (untuk menegakkan umur sesi
// setelah restart) — bukan cookie, token, atau kredensial apa pun.
type geminiSessionState struct {
	Turns []geminiTurn `json:"turns"`
	At    int64        `json:"at"`
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
	case "lokasi", "location":
		return geminiLocationCmd(ctx, mctx, rest)
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
		Opt("Lokasi acuan", ".gemini lokasi <kota/daerah>"),
		Opt("Status", ".gemini status"),
		Opt("Diagnosa", ".gemini diag"),
		"",
		"Mode tamu, tanpa login. Bot mengingat percakapan; bila server mendukung,",
		"konteks juga disimpan di sisi server. Sesi dibuang setelah",
		fmt.Sprintf("%d menit tanpa pesan.", int(geminiSessionTTL.Minutes())),
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
	st.resume, st.uid = geminiapi.Resume{}, ""
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

// geminiSessionResume mengembalikan tiket percakapan dari memori.
func geminiSessionResume(key string) (geminiapi.Resume, string) {
	geminiMu.Lock()
	defer geminiMu.Unlock()
	geminiPruneLocked(geminiNow())
	if st := geminiSessions[key]; st != nil {
		return st.resume, st.uid
	}
	return geminiapi.Resume{}, ""
}

// geminiSessionSetResume menyimpan tiket percakapan di memori (sesi dibuat
// bila belum ada; TTL tidak diperbarui — itu tugas geminiSessionTouch).
func geminiSessionSetResume(key string, r geminiapi.Resume, uid string) {
	geminiMu.Lock()
	defer geminiMu.Unlock()
	st := geminiSessions[key]
	if st == nil {
		st = &geminiSession{}
		geminiSessions[key] = st
	}
	st.resume, st.uid = r, uid
}

// geminiTicketMatchesCookie melaporkan apakah tiket masih mungkin diterima
// server: tiket hanya berlaku selama cookie tamu (`NID`) yang membuatnya masih
// sama. Tiket tanpa sidik jari (versi lama) dianggap cocok; tiket yang sidik
// jarinya berbeda dibuang pemanggil sebelum dikirim.
func geminiTicketMatchesCookie(r geminiapi.Resume) bool {
	if r.Empty() {
		return false
	}
	if r.CookieID == "" {
		return true
	}
	id := geminiAPI().CookieID()
	return id == "" || id == r.CookieID
}

// geminiSessionRestore mengisi riwayat sesi dari database (mis. setelah
// restart). Sesi yang sudah punya riwayat tidak ditimpa, dan waktu sentuh
// terakhir tidak diubah — TTL tetap dihitung dari aktivitas terakhir pemanggil.
func geminiSessionRestore(key string, turns []geminiTurn) {
	geminiMu.Lock()
	defer geminiMu.Unlock()
	st := geminiSessions[key]
	if st == nil {
		st = &geminiSession{}
		geminiSessions[key] = st
	}
	if len(st.history) == 0 {
		st.history = turns
	}
}

// geminiStateKey memetakan kunci sesi (chat|pengirim) menjadi kunci
// bot_settings yang aman: hash pendek, tanpa karakter JID.
func geminiStateKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return geminiHistoryPrefix + hex.EncodeToString(sum[:8])
}

// geminiEncodeHistory membungkus riwayat menjadi JSON untuk bot_settings.
// Dipisah dari I/O supaya umur/batasnya bisa diuji tanpa database.
func geminiEncodeHistory(turns []geminiTurn, at time.Time) (string, bool) {
	if len(turns) == 0 {
		return "", false
	}
	raw, err := json.Marshal(geminiSessionState{Turns: turns, At: at.Unix()})
	if err != nil {
		return "", false
	}
	return string(raw), true
}

// geminiDecodeHistory membaca riwayat dari JSON. Riwayat yang lebih tua dari
// geminiSessionTTL dianggap sudah mati (sama seperti sesi di memori), begitu
// pula data yang tidak terbaca: hasilnya nil, bukan error.
func geminiDecodeHistory(raw string, now time.Time) []geminiTurn {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var st geminiSessionState
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		logfmt.Warn("gemini riwayat tidak terbaca: %v\n", err)
		return nil
	}
	if st.At <= 0 || now.Sub(time.Unix(st.At, 0)) > geminiSessionTTL {
		return nil
	}
	return st.Turns
}

// geminiLoadHistory membaca riwayat giliran dari bot_settings. Riwayat
// kedaluwarsa langsung dihapus supaya arti "sesi mati" tetap sama dengan versi
// yang hanya di memori.
func geminiLoadHistory(client *whatsmeow.Client, key string) []geminiTurn {
	gdb := store.GroupsFor(client)
	if gdb == nil {
		return nil
	}
	raw, err := gdb.GetGeminiState(context.Background(), geminiStateKey(key))
	if err != nil {
		logfmt.Warn("gemini baca riwayat: %v\n", err)
		return nil
	}
	turns := geminiDecodeHistory(raw, geminiNow())
	if turns == nil && strings.TrimSpace(raw) != "" {
		geminiDeleteState(client, key)
	}
	return turns
}

// geminiSaveHistory menyimpan riwayat giliran (fail-soft: gagal menyimpan hanya
// berarti kehilangan keunggulan lintas restart, bukan kegagalan balasan).
func geminiSaveHistory(client *whatsmeow.Client, key string, turns []geminiTurn) {
	gdb := store.GroupsFor(client)
	if gdb == nil {
		return
	}
	raw, ok := geminiEncodeHistory(turns, geminiNow())
	if !ok {
		return
	}
	if err := gdb.SetGeminiState(context.Background(), geminiStateKey(key), raw); err != nil {
		logfmt.Warn("gemini simpan riwayat: %v\n", err)
	}
}

// geminiDeleteState membuang riwayat percakapan dari bot_settings, sekaligus
// sisa tiket versi lama yang sudah tidak bisa dipakai.
func geminiDeleteState(client *whatsmeow.Client, key string) {
	gdb := store.GroupsFor(client)
	if gdb == nil {
		return
	}
	ctx := context.Background()
	if err := gdb.DeleteGeminiState(ctx, geminiStateKey(key)); err != nil {
		logfmt.Warn("gemini hapus riwayat: %v\n", err)
	}
	legacy := geminiLegacyResumePrefix + strings.TrimPrefix(geminiStateKey(key), geminiHistoryPrefix)
	if err := gdb.DeleteGeminiState(ctx, legacy); err != nil {
		logfmt.Warn("gemini hapus tiket lama: %v\n", err)
	}
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

// geminiPromptForTurn memilih isi permintaan: bila tiket percakapan ada, server
// sudah mengingat konteks sehingga prompt dikirim apa adanya; bila tidak, riwayat
// ditempel seperti versi awal fitur ini.
func geminiPromptForTurn(history []geminiTurn, prompt string, resume geminiapi.Resume) string {
	if !resume.Empty() {
		return strings.TrimSpace(prompt)
	}
	return geminiBuildPrompt(history, prompt)
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
	// Riwayat di disk ikut dibuang, supaya sesi benar-benar bersih setelah
	// restart sekalipun. Riwayat yang hanya ada di database (proses baru)
	// tetap dihitung sebagai sesi aktif.
	hadDisk := len(geminiLoadHistory(mctx.Client, key)) > 0
	geminiDeleteState(mctx.Client, key)
	if !geminiSessionDrop(key) && !hadDisk {
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
	// Riwayat di disk juga dibuang: tanpa ini, "new" hanya mengosongkan memori
	// sementara riwayat lama masih tersimpan dan bisa dimuat lagi.
	hadDisk := len(geminiLoadHistory(mctx.Client, key)) > 0
	geminiDeleteState(mctx.Client, key)
	if !geminiSessionClear(key) && !hadDisk {
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

	// Konteks: tiket percakapan (server yang mengingat) atau riwayat di prompt.
	// Bila server sudah menolak tiket, atau cookie tamu sudah berganti (tiket
	// pasti ditolak `1097`), tulis apa adanya supaya tidak terlihat berhasil
	// padahal konteks dibawa lewat riwayat.
	api := geminiAPI()
	contextText := "dari riwayat bot"
	if api.TicketRejected() {
		contextText = "dari riwayat bot (server menolak)"
	}
	storageText := "memori bot"
	if len(geminiLoadHistory(mctx.Client, key)) > 0 {
		storageText = "memori + database"
	}
	if api.TicketEnabled() {
		if resume, _ := geminiSessionResume(key); !resume.Empty() {
			if geminiTicketMatchesCookie(resume) {
				contextText = "diingat server"
			} else {
				contextText = "dari riwayat bot (sesi tamu berganti)"
			}
		}
	}

	locationText := "belum disetel"
	if loc := geminiLocationGet(ctx, mctx.Client); loc != "" {
		locationText = loc
	}

	rows := []string{
		Row("Mode", mode),
		Row("Sistem", sysText),
		Row("Lokasi", locationText),
		Row("Riwayat", geminiTurnsText(turns)),
		Row("Konteks", contextText),
		Row("Kedaluwarsa", geminiLeftText(active, left)),
		Row("Koneksi", state),
		Row("Penyimpanan", storageText),
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

// --- lokasi pengguna & konteks waktu ---

// geminiLocationGet membaca lokasi acuan pengguna dari bot_settings. Fail-soft:
// kegagalan baca hanya berarti blok konteks memakai teks "belum disetel".
func geminiLocationGet(ctx context.Context, client *whatsmeow.Client) string {
	gdb := store.GroupsFor(client)
	if gdb == nil {
		return ""
	}
	raw, err := gdb.GetGeminiState(ctx, geminiLocationKey)
	if err != nil {
		logfmt.Warn("gemini baca lokasi: %v\n", err)
		return ""
	}
	return strings.TrimSpace(raw)
}

// geminiLocationSet menyimpan lokasi acuan (nilai kosong menghapusnya).
func geminiLocationSet(ctx context.Context, client *whatsmeow.Client, value string) error {
	gdb := store.GroupsFor(client)
	if gdb == nil {
		return fmt.Errorf("database penyimpanan tidak tersedia")
	}
	return gdb.SetGeminiState(ctx, geminiLocationKey, strings.TrimSpace(value))
}

// geminiContextBlock menyusun blok konteks yang ditempel pada giliran pertama
// sesi. Ini jawaban atas masalah nyata: Gemini mode tamu menebak lokasi dari IP
// server (Jerman), sehingga "toko roti terdekat" dijawab alamat Jerman. Tanpa
// akun, satu-satunya cara membantah tebakan itu adalah memberi tahu waktu &
// lokasi pengguna di dalam prompt.
func geminiContextBlock(now time.Time, location string) string {
	var b strings.Builder
	b.WriteString("[Konteks]\n")
	fmt.Fprintf(&b, "Waktu sekarang: %s (%s, UTC%s).\n",
		now.Format("Senin, 02 Jan 2006 15:04"), timex.Name(), now.Format("-07:00"))
	if loc := strings.TrimSpace(location); loc != "" {
		fmt.Fprintf(&b, "Lokasi pengguna: %s.\n", loc)
	} else {
		b.WriteString("Lokasi pengguna: belum disetel. Kalau jawaban bergantung pada lokasi, tanyakan dulu kota/daerahnya.\n")
	}
	b.WriteString("Pakai waktu dan lokasi pengguna di atas sebagai acuan. Jangan memakai lokasi server atau IP sebagai lokasi pengguna. " +
		"Jangan mengarang nama tempat, alamat, atau jam buka: kalau datanya tidak ada, katakan tidak tahu dan sarankan mencari di Google Maps.")
	return b.String()
}

// geminiTurnContext adalah baris pendek yang ditempel di SETIAP giliran. Blok
// penuh di atas cukup sekali, tapi jam harus tetap benar setelah sesi berjalan
// lama atau bot restart (blok penuh ikut tersimpan bersama riwayat lama), dan
// perubahan `.gemini lokasi` harus langsung berlaku tanpa `.gemini new`.
func geminiTurnContext(now time.Time, location string) string {
	line := fmt.Sprintf("[Sekarang: %s %s", now.Format("02 Jan 2006 15:04"), timex.Name())
	if loc := strings.TrimSpace(location); loc != "" {
		line += " · " + loc
	}
	return line + "]"
}

// geminiMergeSystem menggabungkan prompt sistem owner dengan blok konteks
// otomatis. Blok konteks TIDAK boleh menimpa teks owner.
func geminiMergeSystem(sys, block string) string {
	sys = strings.TrimSpace(sys)
	block = strings.TrimSpace(block)
	if sys == "" {
		return block
	}
	if block == "" {
		return sys
	}
	return sys + "\n\n" + block
}

// geminiLocationCmd menangani .gemini lokasi (tampil / setel / reset).
func geminiLocationCmd(ctx context.Context, mctx *store.MessageContext, rest string) error {
	current := geminiLocationGet(ctx, mctx.Client)
	value := strings.TrimSpace(rest)

	switch {
	case value == "":
		shown := "belum disetel"
		if current != "" {
			shown = current
		}
		return store.ReplyText(ctx, mctx, Block("Gemini lokasi",
			Row("Lokasi", shown),
			"",
			"Setel dengan *.gemini lokasi <kota/daerah>*.",
			"Berlaku pada pesan berikutnya, tanpa perlu *.gemini new*.",
			"*.gemini lokasi reset* mengosongkan.",
		))

	case strings.EqualFold(value, "reset"), strings.EqualFold(value, "hapus"):
		if err := geminiLocationSet(ctx, mctx.Client, ""); err != nil {
			logfmt.Warn("gemini hapus lokasi: %v\n", err)
			return store.ReplyText(ctx, mctx, ErrMsg("Gemini", "Gagal menghapus lokasi."))
		}
		return store.ReplyText(ctx, mctx, Block("Gemini lokasi",
			Row("Lokasi", "dikosongkan"),
			"",
			"Gemini akan menanyakan kota/daerah bila jawaban bergantung pada lokasi.",
		))

	default:
		if count := utf8.RuneCountInString(value); count > geminiMaxLocationRunes {
			return store.ReplyText(ctx, mctx, ErrMsg("Gemini",
				fmt.Sprintf("Lokasi kepanjangan (%d karakter, maks %d).", count, geminiMaxLocationRunes)))
		}
		if err := geminiLocationSet(ctx, mctx.Client, value); err != nil {
			logfmt.Warn("gemini simpan lokasi: %v\n", err)
			return store.ReplyText(ctx, mctx, ErrMsg("Gemini", "Gagal menyimpan lokasi."))
		}
		return store.ReplyText(ctx, mctx, Block("Gemini lokasi",
			Row("Lokasi", value),
			"",
			"Berlaku pada pesan berikutnya, tanpa perlu *.gemini new*.",
		))
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
	// Nama cookie (tanpa nilainya) ditampilkan karena hilangnya `NID` adalah
	// penyebab nyata percakapan lanjutan ditolak `1097`: server menganggap
	// setiap permintaan sebagai sesi tamu baru.
	if names := geminiAPI().CookieNames(); len(names) > 0 {
		rows = append(rows, Row("Isi cookie", strings.Join(names, ", ")))
	}
	// Durasi ditampilkan juga saat chat gagal: beda antara "berhenti di 30,0
	// dtk" (batas transport) dan "gagal dalam 0,2 dtk" (ditolak server) adalah
	// petunjuk utama saat mendiagnosis jalur tamu.
	if d.Latency > 0 {
		rows = append(rows, Row("Durasi", d.Latency.Round(time.Millisecond).String()))
	}
	if d.Shape != "" {
		rows = append(rows, Row("Bentuk", d.Shape))
	}
	if d.Chat.OK {
		ticketText := "tidak ada (riwayat prompt)"
		if !d.Ticket.Empty() {
			ticketText = "ya · " + d.Ticket.ConversationID
		}
		rows = append(rows, Row("Tiket", ticketText))
		// Tiket yang baru diterima diagnosa belum tentu dipakai: bila server
		// sudah menolak tiket sebelumnya, status itu ditulis apa adanya.
		if geminiAPI().TicketRejected() {
			rows = append(rows, Row("Pemakaian", "dimatikan · server menolak tiket, pakai riwayat"))
		}
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
	api := geminiAPI()
	return geminiDiagVerdictFor(d, api.CookieNames(), api.TicketRejected())
}

// geminiDiagVerdictFor memisahkan keputusan dari klien supaya bisa diuji tanpa
// jaringan. cookieNames hanya nama (tanpa nilai) — cukup untuk memastikan `NID`
// ada, karena tanpanya server menolak melanjutkan percakapan (`1097`).
func geminiDiagVerdictFor(d *geminiapi.Diag, cookieNames []string, ticketRejected bool) string {
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
			return "Server menolak percakapan (kode 1096/1097): sesi tamu basi, bukan bug bot."
		default:
			return "Chat gagal: lihat baris Chat di atas."
		}
	}
	if d.Shape == "minimal" {
		return "Jalur tamu sehat dengan bentuk permintaan minimal: konteks dibawa lewat riwayat prompt, bukan tiket."
	}
	// NID hilang = percakapan lanjutan pasti ditolak 1097 (terbukti lewat probe
	// panel). Satu giliran tetap jalan, jadi kegagalan ini mudah disalahartikan
	// sebagai "tiket ditolak server".
	if len(cookieNames) > 0 && !geminiNamesHaveNID(cookieNames) {
		return "Jalur tamu sehat untuk satu giliran, tapi cookie NID tidak ada: percakapan lanjutan akan ditolak server (1097). Periksa jaringan/IP keluar bot."
	}
	if ticketRejected {
		return "Jalur tamu sehat. Server pernah menolak tiket percakapan, jadi bot memakai riwayat prompt sampai sesi tamu diperbarui (cookie baru)."
	}
	if d.Ticket.Empty() {
		return "Jalur tamu sehat: cookie, halaman, dan chat bekerja. Server tidak memberi tiket percakapan, jadi konteks dibawa lewat riwayat prompt."
	}
	return "Jalur tamu sehat: cookie, halaman, chat bekerja, dan server memberi tiket percakapan (konteks diingat server)."
}

// geminiNamesHaveNID melaporkan apakah sesi tamu memuat cookie NID. Tanpa NID,
// server tidak mau melanjutkan percakapan (`1097`) walau satu giliran berhasil.
func geminiNamesHaveNID(names []string) bool {
	for _, name := range names {
		if name == "NID" {
			return true
		}
	}
	return false
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
	if !active && store.IsReplyToBot(evt) {
		// Setelah restart, sesi di memori hilang tapi riwayat masih tersimpan
		// di database. Anggap sesi hidup selama riwayatnya belum kedaluwarsa,
		// supaya membalas bubble bot tetap bekerja seperti sebelum restart.
		if len(geminiLoadHistory(client, key)) > 0 {
			active = true
		}
	}

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
	// sebelum giliran ini ditambahkan. Riwayat yang hanya ada di database
	// (proses baru setelah restart) dimuat di sini: tiket percakapan tidak bisa
	// diandalkan lintas proses karena sesi tamu terikat cookie `NID` yang
	// selalu baru.
	geminiSessionEnsure(key)
	history, _ := geminiSessionGet(key)
	if len(history) == 0 {
		if diskTurns := geminiLoadHistory(client, key); len(diskTurns) > 0 {
			geminiSessionRestore(key, diskTurns)
			history, _ = geminiSessionGet(key)
		}
	}

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

	// Konteks waktu & lokasi selalu ditempel, tanpa perlu disetel owner. Gemini
	// mode tamu menebak lokasi dari IP server (Jerman), jadi tanpa konteks ini
	// "toko roti terdekat" dijawab alamat Jerman. Blok penuh hanya sekali (di
	// giliran pertama, ikut tersimpan bersama riwayat); baris pendek ditempel
	// tiap giliran supaya jam tetap benar setelah sesi lama/restart, dan
	// perubahan `.gemini lokasi` langsung berlaku tanpa `.gemini new`.
	now := timex.Now()
	location := geminiLocationGet(ctx, client)
	sysText := geminiMergeSystem(sysPrompt, geminiContextBlock(now, location))

	// Tiket percakapan: hanya dari memori. Tiket tidak disimpan ke disk karena
	// percakapan tamu terikat cookie `NID`, dan cookie itu selalu baru di
	// proses baru — tiket hasil proses sebelumnya pasti ditolak `1097`.
	// Tiket kosong berarti konteks dibawa lewat riwayat di dalam prompt. Saat
	// bentuk permintaan minimal dipakai (GEMINI_FULL_PAYLOAD=0 atau fallback),
	// server tidak menerima tiket, jadi konteks juga dari riwayat.
	api := geminiAPI()
	resume, uid := geminiSessionResume(key)
	if !api.TicketEnabled() {
		resume = geminiapi.Resume{}
	}

	// Giliran "pinned" menyimpan teks sistem + blok konteks. Saat tiket dipakai,
	// server sudah memegang percakapan, jadi blok penuh tidak diulang; baris
	// waktu singkat di bawah tetap ikut di setiap permintaan.
	isPinned := first && resume.Empty()
	reqPrompt := geminiTurnContext(now, location) + "\n\n" + prompt
	if isPinned {
		reqPrompt = geminiSystemRequest(sysText, prompt)
		reqPrompt = geminiTurnContext(now, location) + "\n\n" + reqPrompt
	}

	if uid == "" {
		uid = api.UID()
	}

	res, err := api.AskRequest(ctx, geminiapi.AskRequest{
		Prompt: geminiPromptForTurn(history, reqPrompt, resume),
		Resume: resume,
		UID:    uid,
	})
	if err != nil && !resume.Empty() &&
		(errors.Is(err, geminiapi.ErrStaleConv) || errors.Is(err, geminiapi.ErrTicketStale)) {
		// Dua sebab tiket tidak dipakai lagi:
		//   ErrStaleConv   = server menolak (percakapan tamu kedaluwarsa);
		//   ErrTicketStale = cookie/NID sudah berganti, jadi permintaan TIDAK
		//                    dikirim (server pasti menjawab 1097).
		// Yang kedua normal (rotasi cookie tiap 30 menit / restart), jadi
		// tidak perlu dicetak: dulu baris "tiket ditolak" di console membuat
		// owner mengira ada kerusakan.
		if errors.Is(err, geminiapi.ErrStaleConv) {
			logfmt.Warn("gemini: tiket percakapan ditolak (%v), pakai riwayat prompt\n", err)
		}
		resume = geminiapi.Resume{}
		geminiSessionSetResume(key, resume, uid)
		retryNow := timex.Now()
		isPinned = first
		reqPrompt = geminiSystemRequest(sysText, prompt)
		reqPrompt = geminiTurnContext(retryNow, location) + "\n\n" + reqPrompt
		res, err = api.AskRequest(ctx, geminiapi.AskRequest{
			Prompt: geminiPromptForTurn(history, reqPrompt, resume),
			UID:    uid,
		})
	}
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
	// Riwayat disalin ke database: inilah yang membuat konteks tetap ada
	// setelah restart (bukan tiket, yang selalu basi karena cookie baru).
	if turns, _ := geminiSessionGet(key); len(turns) > 0 {
		geminiSaveHistory(client, key, turns)
	}

	// Tiket untuk giliran berikutnya (memori saja). Sidik jari sesi tamu ikut
	// ditandai supaya giliran berikutnya tahu apakah tiket masih berlaku.
	// Bila server tidak mengembalikan id, tiket lama tidak ditimpa supaya
	// konteks tidak hilang. Bentuk minimal tidak memakai tiket.
	if api.TicketEnabled() {
		if ticket := res.Resume(); !ticket.Empty() {
			ticket.CookieID = api.CookieID()
			geminiSessionSetResume(key, ticket, uid)
		}
	}

	if err := geminiSendAnswer(ctx, mctx, text); err != nil {
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
	text = geminiStripAgentTags(text)
	return strings.TrimSpace(ai.SanitizeForWhatsApp(text))
}

// geminiStripAgentTags membuang penanda media internal Gemini, misalnya
//
//	<Image alt="Masjid Al Alam" caption="Masjid Al Alam di Teluk Kendari" src="image_agent_tag_123"/>
//
// Tag itu BUKAN gambar sungguhan: src-nya id internal, bukan URL, jadi kalau
// dibiarkan hanya menjadi teks kotor di chat. Pemindaian mengikuti tanda kutip
// supaya atribut yang memuat ">" tidak memotong tag di tengah (regex `<[^>]*>`
// gagal pada kasus itu dan menyisakan potongan seperti `b"/>`).
//
// Tag `<Image>`/`<img>` selalu dibuang; tag lain (`<Video>`, `<Audio>`, …) hanya
// dibuang bila memuat penanda `_agent_tag_`, sehingga teks biasa yang memakai
// kurung sudut tidak ikut terhapus.
func geminiStripAgentTags(text string) string {
	if !strings.Contains(text, "<") {
		return text
	}
	buf := make([]byte, 0, len(text))
	skipSpace := false
	removed := false
	for i := 0; i < len(text); {
		if text[i] == '<' {
			if end, ok := geminiAgentTagEnd(text, i); ok {
				removed = true
				if next, ok := geminiTagLineEnd(text, i, end, &buf); ok {
					// Tag berdiri sendiri di barisnya: buang seluruh barisnya,
					// bukan hanya tag-nya. Kalau tidak, jawaban berisi lubang
					// dua baris kosong di tempat gambar yang dibuang.
					i = next
					skipSpace = false
					continue
				}
				i = end
				skipSpace = true
				continue
			}
		}
		if skipSpace && (text[i] == ' ' || text[i] == '\t') && (len(buf) == 0 || geminiIsSpaceByte(buf[len(buf)-1])) {
			i++
			continue
		}
		skipSpace = false
		buf = append(buf, text[i])
		i++
	}
	if !removed {
		return string(buf)
	}
	// Sisa baris kosong beruntun dirapatkan jadi satu baris kosong.
	return string(geminiCollapseBlankLines(buf))
}

// geminiTagLineEnd menangani tag yang menempati barisnya sendiri (hanya spasi
// sebelum tag dan sesudahnya sampai akhir baris). Bila benar, inden baris itu
// dan newline penutupnya dibuang dari buf, dan indeks lanjut dikembalikan.
func geminiTagLineEnd(text string, start, end int, buf *[]byte) (int, bool) {
	lineStart := strings.LastIndexByte(text[:start], '\n') + 1
	for _, c := range text[lineStart:start] {
		if c != ' ' && c != '\t' && c != '\r' {
			return 0, false
		}
	}
	k := end
	for k < len(text) && (text[k] == ' ' || text[k] == '\t' || text[k] == '\r') {
		k++
	}
	if k < len(text) && text[k] != '\n' {
		return 0, false
	}
	// Buang inden baris tag (sudah terlanjur ditulis ke buf).
	trimmed := *buf
	for len(trimmed) > 0 && (trimmed[len(trimmed)-1] == ' ' || trimmed[len(trimmed)-1] == '\t') {
		trimmed = trimmed[:len(trimmed)-1]
	}
	*buf = trimmed
	if k < len(text) {
		return k + 1, true
	}
	// Tag di baris terakhir: buang newline sebelum barisnya supaya tidak
	// tersisa baris kosong di ujung.
	trimmed = *buf
	if len(trimmed) > 0 && trimmed[len(trimmed)-1] == '\n' {
		trimmed = trimmed[:len(trimmed)-1]
		if len(trimmed) > 0 && trimmed[len(trimmed)-1] == '\r' {
			trimmed = trimmed[:len(trimmed)-1]
		}
	}
	*buf = trimmed
	return k, true
}

// geminiCollapseBlankLines merapatkan newline beruntun (3 ke atas) menjadi satu
// baris kosong, tanpa menyentuh newline tunggal/ganda yang memang disengaja.
func geminiCollapseBlankLines(buf []byte) []byte {
	out := make([]byte, 0, len(buf))
	run := 0
	for _, c := range buf {
		if c == '\n' {
			run++
			if run > 2 {
				continue
			}
		} else {
			run = 0
		}
		out = append(out, c)
	}
	return out
}

// geminiAgentTagEnd mengembalikan indeks setelah '>' penutup bila teks pada
// posisi start adalah tag media internal Gemini.
func geminiAgentTagEnd(text string, start int) (int, bool) {
	j := start + 1
	if j < len(text) && text[j] == '/' {
		return 0, false
	}
	nameStart := j
	for j < len(text) && isAgentTagNameChar(text[j]) {
		j++
	}
	if j == nameStart {
		return 0, false
	}
	name := strings.ToLower(text[nameStart:j])
	var quote byte
	for ; j < len(text); j++ {
		c := text[j]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '<':
			return 0, false
		case c == '>':
			if name == "image" || name == "img" {
				return j + 1, true
			}
			return j + 1, strings.Contains(strings.ToLower(text[start:j+1]), "_agent_tag_")
		}
	}
	return 0, false
}

func isAgentTagNameChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_'
}

// geminiIsSpaceByte true untuk spasi/tab/newline — dipakai memutuskan apakah
// spasi setelah tag yang dibuang perlu ikut dibuang (menghindari spasi ganda).
func geminiIsSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
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
	case errors.Is(err, geminiapi.ErrTicketStale):
		return "Sesi tamu Gemini sudah berganti, jadi percakapan lama tidak bisa dilanjutkan. Coba lagi."
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
