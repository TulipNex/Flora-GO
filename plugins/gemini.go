package plugins

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"wabot-go/internal/ai"
	"wabot-go/internal/external"
	"wabot-go/internal/geminiapi"
	"wabot-go/internal/logfmt"
	"wabot-go/internal/messaging"
	"wabot-go/internal/store"
	"wabot-go/internal/textx"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types/events"
	"google.golang.org/protobuf/proto"
)

func init() {
	registerPlugin(RegisterGemini)
}

// gemini.go — chat Gemini lewat antarmuka web (cookie akun Google bot),
// owner-only dan hanya di DM owner.
//
// Kenapa lewat web dan bukan API resmi: akun bot tidak punya API key, tapi
// punya kuota web (termasuk pembuatan gambar dan pembacaan lampiran). Yang
// dipakai hanya cookie sesi; tidak ada browser/Chrome seperti arena.ai karena
// endpoint web Gemini tidak meminta captcha.
//
// Kontrak penyimpanan (keputusan owner): riwayat percakapan hidup DI SERVER
// Gemini. Bot hanya menyimpan:
//   - data/gemini-cookie.txt  (kredensial, tidak pernah di-echo ke chat)
//   - penunjuk percakapan (conv_id/resp_id/choice_id) di MEMORI, hilang saat
//     restart — itu sebabnya mode kembali nonaktif setelah bot restart.
//
// Tidak ada transkrip lokal dan tidak ada tabel baru: pesan yang lewat sesi
// ini juga dikecualikan dari arsip CRM (geminiPrivateMessage di crm.go +
// MessageContext.SkipArchive), karena arsip akan sama saja dengan menyimpan
// transkrip.
//
// Pemakaian:
//
//	.gemini on               aktifkan sesi di DM ini
//	.gemini off              akhiri sesi (pointer percakapan ikut dibuang)
//	.gemini new              mulai percakapan baru di Gemini
//	.gemini login <cookie>   simpan cookie akun bot (sekali saja)
//	.gemini status           status cookie + sesi
//	.gemini diag             diagnosa cookie (bukti TS basi vs sesi mati)
//	gemini <pertanyaan>      kirim prompt (mode aktif)
//	(reply bubble bot)       lanjutkan percakapan tanpa kata gemini
const (
	geminiCookieFile = "data/gemini-cookie.txt"
	geminiTrigger    = "gemini"

	geminiChatTimeout   = 3 * time.Minute
	geminiProbeTimeout  = 15 * time.Second
	geminiDownloadLimit = 30 * time.Second

	geminiMaxPromptRunes = 8000
	geminiReplyChunk     = 3500
	geminiMaxChunks      = 6
	geminiCaptionMax     = 1000

	geminiDefaultUploadMB = 20
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

// geminiSession adalah keadaan sesi satu chat. conv hanya di memori: tidak
// ditulis ke disk/DB mana pun.
type geminiSession struct {
	conv *geminiapi.Conversation
	busy bool
}

var (
	geminiMu       sync.Mutex
	geminiSessions = map[string]*geminiSession{}

	geminiClientOnce sync.Once
	geminiAPIClient  *geminiapi.Client
)

// geminiAPI mengembalikan klien tunggal (satu akun = satu antrean request;
// klien menyerialkan Chat di dalamnya).
func geminiAPI() *geminiapi.Client {
	geminiClientOnce.Do(func() {
		geminiAPIClient = geminiapi.New(geminiCookieFile)
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
		Description: "Chat Gemini web: on/off/new/login/status/diag (owner)",
		Category:    CategoryOwner,
		Handler:     handleGemini,
	})
}

// handleGemini adalah gerbang subcommand. Tanpa argumen = petunjuk, bukan
// error, mengikuti gaya .seedream.
func handleGemini(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	// Seluruh fitur ini DM-only: `.gemini login <cookie>` di grup akan
	// memampangkan cookie ke semua anggota, dan sesi chat pribadi tidak ada
	// gunanya di grup.
	if mctx.IsGroup() {
		return store.ReplyText(ctx, mctx, ErrMsg("Gemini",
			"Command ini hanya bisa dipakai di chat pribadi owner."))
	}
	// Balasan command ini (termasuk blok status yang menyebut nama cookie)
	// tidak perlu masuk arsip CRM.
	mctx.SkipArchive = true

	sub, rest := geminiSplitFirst(mctx.RawArgs)
	switch strings.ToLower(sub) {
	case "":
		return store.ReplyText(ctx, mctx, geminiHelpBlock())
	case "on":
		return geminiEnable(ctx, mctx)
	case "off":
		return geminiDisable(ctx, mctx)
	case "new":
		return geminiNewChat(ctx, mctx)
	case "login":
		return geminiLogin(ctx, mctx, rest)
	case "status":
		return geminiStatus(ctx, mctx)
	case "diag":
		return geminiDiag(ctx, mctx, rest)
	default:
		return store.ReplyText(ctx, mctx, ErrMsg("Gemini",
			"Subcommand tidak dikenal. Pakai on, off, new, login, status, atau diag."))
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
		Opt("Aktifkan", ".gemini on"),
		Opt("Matikan", ".gemini off"),
		Opt("Percakapan baru", ".gemini new"),
		Opt("Login cookie", ".gemini login <cookie>"),
		Opt("Status", ".gemini status"),
		Opt("Diagnosa cookie", ".gemini diag"),
		Opt("Uji lampiran", ".gemini diag upload"),
		Opt("Uji gambar", ".gemini diag image"),
		"",
		"Setelah aktif, kirim *gemini <pertanyaan>* atau balas bubble bot.",
		"Riwayat percakapan disimpan di server Gemini, bukan di bot.",
	)
}

// --- keadaan sesi (memori saja) ---

func geminiSessionEnable(key string) bool {
	geminiMu.Lock()
	defer geminiMu.Unlock()
	if _, ok := geminiSessions[key]; ok {
		return false
	}
	geminiSessions[key] = &geminiSession{}
	return true
}

func geminiSessionDisable(key string) bool {
	geminiMu.Lock()
	defer geminiMu.Unlock()
	_, ok := geminiSessions[key]
	delete(geminiSessions, key)
	return ok
}

func geminiSessionOn(key string) bool {
	geminiMu.Lock()
	defer geminiMu.Unlock()
	_, ok := geminiSessions[key]
	return ok
}

// geminiSessionStart menandai sesi sibuk dan mengembalikan pointer percakapan.
// started=false berarti sesi tidak aktif ATAU sedang sibuk (busy=true).
func geminiSessionStart(key string) (conv *geminiapi.Conversation, started, busy bool) {
	geminiMu.Lock()
	defer geminiMu.Unlock()
	st := geminiSessions[key]
	if st == nil {
		return nil, false, false
	}
	if st.busy {
		return nil, false, true
	}
	st.busy = true
	if st.conv == nil {
		return nil, true, false
	}
	c := *st.conv
	return &c, true, false
}

// geminiSessionRelease melepas status sibuk tanpa menyentuh pointer.
func geminiSessionRelease(key string) {
	geminiMu.Lock()
	defer geminiMu.Unlock()
	if st := geminiSessions[key]; st != nil {
		st.busy = false
	}
}

// geminiSessionStore menyimpan pointer percakapan terbaru dari server.
func geminiSessionStore(key string, conv *geminiapi.Conversation) {
	if conv == nil {
		return
	}
	geminiMu.Lock()
	defer geminiMu.Unlock()
	if st := geminiSessions[key]; st != nil {
		st.conv = conv
	}
}

func geminiSessionResetConv(key string) {
	geminiMu.Lock()
	defer geminiMu.Unlock()
	if st := geminiSessions[key]; st != nil {
		st.conv = nil
	}
}

func geminiSessionTurns(key string) int {
	geminiMu.Lock()
	defer geminiMu.Unlock()
	if st := geminiSessions[key]; st != nil && st.conv != nil {
		return st.conv.Turn
	}
	return 0
}

// --- subcommand ---

func geminiEnable(ctx context.Context, mctx *store.MessageContext) error {
	if _, err := geminiapi.LoadCookieFile(geminiCookieFile); err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Gemini", geminiErrorText(err)))
	}
	// Cek sesi dulu supaya owner tahu cookie masih sah SEBELUM mengirim prompt
	// pertama; kegagalan jaringan tidak memblokir (mode tetap aktif).
	_, err := geminiSessionProbe(ctx)
	if errors.Is(err, geminiapi.ErrCookieRejected) {
		return store.ReplyText(ctx, mctx, ErrMsg("Gemini", geminiErrorText(err)))
	}
	key := mctx.Chat.String()
	already := !geminiSessionEnable(key)

	rows := []string{
		Row("Mode", "aktif"),
		Row("Model", geminiapi.ModelLabel(geminiModelText())),
	}
	if already {
		rows[0] = Row("Mode", "sudah aktif")
	}
	if err != nil {
		rows = append(rows, Row("Catatan", "pemeriksaan sesi gagal, prompt tetap dicoba"))
	}
	rows = append(rows, "", "Kirim *gemini <pertanyaan>* atau balas bubble bot.")
	return store.ReplyText(ctx, mctx, Block("Gemini", rows...))
}

func geminiDisable(ctx context.Context, mctx *store.MessageContext) error {
	key := mctx.Chat.String()
	if !geminiSessionDisable(key) {
		return store.ReplyText(ctx, mctx, ErrMsg("Gemini", "Sesi memang sedang nonaktif."))
	}
	rows := []string{
		Row("Mode", "nonaktif"),
		"",
		"Penunjuk percakapan dibuang dari memori. Riwayat tetap ada di akun Gemini.",
	}
	return store.ReplyText(ctx, mctx, Block("Gemini", rows...))
}

func geminiNewChat(ctx context.Context, mctx *store.MessageContext) error {
	key := mctx.Chat.String()
	if !geminiSessionOn(key) {
		return store.ReplyText(ctx, mctx, ErrMsg("Gemini", "Aktifkan dulu dengan .gemini on."))
	}
	geminiSessionResetConv(key)
	rows := []string{
		Row("Mode", "aktif"),
		Row("Percakapan", "baru"),
		"",
		"Pesan berikutnya membuka percakapan baru di Gemini.",
	}
	return store.ReplyText(ctx, mctx, Block("Gemini", rows...))
}

// geminiLogin menyimpan cookie akun bot. Nilai cookie diperlakukan seperti
// password: tidak pernah dikembalikan ke chat, hanya jumlah entri + hasil uji.
func geminiLogin(ctx context.Context, mctx *store.MessageContext, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		// Cookie bisa ditempel dari pesan yang di-reply (panjang, rawan
		// terpotong kalau harus satu baris command).
		raw = geminiQuotedText(mctx.RawEvt)
	}
	if raw == "" {
		return store.ReplyText(ctx, mctx, ErrMsg("Cookie",
			"Tempel cookie setelah perintah: .gemini login <cookie>"))
	}
	raw = geminiNormalizeCookieString(raw)
	cs := geminiapi.ParseCookieHeader(raw)
	if !cs.HasSession() {
		if !strings.Contains(raw, "=") {
			cs.Set("__Secure-1PSID", raw)
		}
	}
	if !cs.HasSession() {
		return store.ReplyText(ctx, mctx, ErrMsg("Cookie",
			"Cookie tidak memuat __Secure-1PSID. Salin seluruh header Cookie dari request gemini.google.com di browser."))
	}
	if err := cs.Save(geminiCookieFile); err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Cookie", fmt.Sprintf("Gagal menyimpan: %v", err)))
	}
	// Muat ulang ke klien yang sudah hidup supaya tidak memakai cookie lama.
	// Rotasi `__Secure-1PSIDTS` dilakukan di dalam geminiSessionProbe.
	if err := geminiAPI().LoadCookies(); err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Cookie", geminiErrorText(err)))
	}

	state, perr := geminiSessionProbe(ctx)
	rows := []string{
		Row("Status", "tersimpan"),
		Row("Entri", strconv.Itoa(cs.Len())),
		Row("Sesi", state),
		Row("Model", geminiapi.ModelLabel(geminiModelText())),
	}
	if perr != nil {
		logfmt.Warn("gemini cookie: %v\n", perr)
	}
	return store.ReplyText(ctx, mctx, Block("Cookie", rows...))
}

func geminiStatus(ctx context.Context, mctx *store.MessageContext) error {
	key := mctx.Chat.String()
	on := geminiSessionOn(key)
	mode := "nonaktif"
	if on {
		mode = "aktif"
	}
	state, err := geminiSessionProbe(ctx)
	if err != nil {
		logfmt.Warn("gemini status: %v\n", err)
	}
	turns := geminiSessionTurns(key)
	conv := "belum mulai"
	if turns > 0 {
		conv = fmt.Sprintf("%d giliran", turns)
	}
	rows := []string{
		Row("Mode", mode),
		Row("Cookie", state),
		Row("Berkas", geminiCookieFileState()),
		Row("Percakapan", conv),
		Row("Model", geminiapi.ModelLabel(geminiModelText())),
		Row("Penyimpanan", "riwayat di server Gemini"),
	}
	if !on {
		rows = append(rows, "", "Aktifkan dengan .gemini on")
	}
	return store.ReplyText(ctx, mctx, Block("Gemini", rows...))
}

// geminiSessionProbe memeriksa cookie ke server (rotasi + GET /app, tanpa
// kuota chat) dan mengembalikan label pendek untuk ditampilkan. Rotasi lebih
// dulu karena `__Secure-1PSIDTS` berumur ±10 menit: tanpa itu sesi yang
// sebenarnya sehat bisa tampak "ditolak".
func geminiSessionProbe(ctx context.Context) (string, error) {
	api := geminiAPI()
	if err := api.LoadCookies(); err != nil {
		return "belum ada", err
	}
	cctx, cancel := context.WithTimeout(ctx, geminiProbeTimeout)
	defer cancel()
	// Rotasi diperlakukan sebagai persiapan, bukan bukti: kalau gagal karena
	// jaringan, hasil Ping tidak boleh dilabeli "ditolak" (itu menuduh cookie
	// mati padahal yang gagal cuma rotasi).
	rotateFailed := false
	if err := api.Rotate(cctx); err != nil {
		if errors.Is(err, geminiapi.ErrCookieRejected) {
			return "ditolak", err
		}
		logfmt.Warn("gemini probe: rotasi: %v\n", err)
		rotateFailed = true
	}
	err := api.Ping(cctx)
	switch {
	case err == nil:
		return "valid", nil
	case errors.Is(err, geminiapi.ErrCookieRejected):
		if rotateFailed {
			return "tidak terverifikasi", err
		}
		return "ditolak", err
	default:
		return "tidak terverifikasi", err
	}
}

// geminiCookieFileState melaporkan isi berkas cookie (jumlah entri + keadaan
// simpan terakhir) tanpa pernah menyebut nilai cookie.
func geminiCookieFileState() string {
	cs, err := geminiapi.LoadCookieFile(geminiCookieFile)
	if err != nil || cs == nil {
		return "belum ada"
	}
	state := fmt.Sprintf("%d entri", cs.Len())
	if err := geminiAPI().SaveError(); err != nil {
		state += " · simpan gagal"
	}
	return state
}

// geminiDiagTimeout memberi ruang untuk rotasi + tiga GET /app berurutan.
const geminiDiagTimeout = 60 * time.Second

// geminiDiagUploadTimeout mencakup unggah + satu giliran chat (antrean server
// Gemini bisa 8–20 dtk sebelum jawaban pertama).
const geminiDiagUploadTimeout = 2 * time.Minute

// geminiDiagImageTimeout mencakup satu giliran generate gambar + unduhannya.
// Generate gambar di server Gemini bisa jauh lebih lama daripada jawaban teks.
const geminiDiagImageTimeout = 3 * time.Minute

// geminiDiag adalah perintah diagnosa: memisahkan "cookie mati" dari "cookie
// volatile basi". Semua keluaran hanya nama/status, tidak pernah nilai cookie.
// `diag upload` menguji jalur lampiran; `diag image` menguji generate + unduh.
func geminiDiag(ctx context.Context, mctx *store.MessageContext, rest string) error {
	api := geminiAPI()
	switch strings.ToLower(strings.TrimSpace(rest)) {
	case "upload":
		return geminiDiagUpload(ctx, mctx, api)
	case "image", "gambar":
		return geminiDiagImage(ctx, mctx, api)
	}
	cctx, cancel := context.WithTimeout(ctx, geminiDiagTimeout)
	defer cancel()
	d := api.Diag(cctx)

	rows := []string{
		Row("Berkas", geminiDiagFile(d)),
		Row("Rotasi", geminiDiagRotate(d)),
		Row("/app", geminiDiagProbe(d.App)),
		Row("Tanpa TS", geminiDiagProbe(d.AppNoTS)),
		Row("Hanya 1PSID", geminiDiagProbe(d.AppOnly1PSID)),
	}
	if d.SaveErr != nil {
		rows = append(rows, Row("Simpan", geminiTruncateText(d.SaveErr.Error(), 160)))
	}
	rows = append(rows, "", geminiDiagVerdict(d))
	return store.ReplyText(ctx, mctx, Block("Gemini diag", rows...))
}

// geminiDiagUpload melaporkan hasil uji jalur lampiran: unggah → satu giliran
// chat dengan lampiran. Dipakai saat chat ber-lampiran gagal tanpa pesan jelas.
func geminiDiagUpload(ctx context.Context, mctx *store.MessageContext, api *geminiapi.Client) error {
	cctx, cancel := context.WithTimeout(ctx, geminiDiagUploadTimeout)
	defer cancel()
	p := api.DiagUpload(cctx)

	rows := []string{Row("Berkas uji", geminiByteSize(p.Bytes)+" PNG")}
	if p.UploadErr != nil {
		rows = append(rows, Row("Unggah", "gagal: "+geminiTruncateText(p.UploadErr.Error(), 160)))
	} else {
		rows = append(rows, Row("Unggah", fmt.Sprintf("ok · id %d karakter", p.FileIDLen)))
	}
	switch {
	case p.UploadErr != nil:
		// Tidak sampai chat: tidak ada yang bisa dilaporkan.
	case p.ChatErr != nil:
		rows = append(rows, Row("Chat", "gagal: "+geminiTruncateText(p.ChatErr.Error(), 160)))
	case p.Images > 0:
		rows = append(rows, Row("Chat", fmt.Sprintf("ok · %d gambar", p.Images)))
	case strings.TrimSpace(p.Text) == "":
		rows = append(rows, Row("Chat", "kosong"))
	default:
		rows = append(rows, Row("Chat", "ok · "+geminiTruncateText(p.Text, 80)))
	}
	if p.DumpPath != "" {
		rows = append(rows, Row("Dump", filepath.Base(p.DumpPath)))
	}
	rows = append(rows, "", geminiDiagUploadVerdict(p))
	return store.ReplyText(ctx, mctx, Block("Gemini diag", rows...))
}

// geminiDiagImage melaporkan hasil uji generate + unduh gambar. Semua baris
// hanya status/ukuran; URL bertanda tangan tidak pernah ditampilkan.
func geminiDiagImage(ctx context.Context, mctx *store.MessageContext, api *geminiapi.Client) error {
	cctx, cancel := context.WithTimeout(ctx, geminiDiagImageTimeout)
	defer cancel()
	p := api.DiagImage(cctx)

	var rows []string
	switch {
	case p.ChatErr != nil:
		rows = append(rows, Row("Generate", "gagal: "+geminiTruncateText(p.ChatErr.Error(), 160)))
	case p.Images == 0:
		rows = append(rows, Row("Generate", "ok · tanpa gambar"))
	default:
		rows = append(rows, Row("Generate", fmt.Sprintf("ok · %d gambar", p.Images)))
	}
	for i, m := range p.Media {
		rows = append(rows, Row(fmt.Sprintf("Bentuk %d", i+1), m.Shape))
		label := fmt.Sprintf("Unduh %d", i+1)
		if len(m.Data) > 0 {
			rows = append(rows, Row(label, fmt.Sprintf("%s · via %s", geminiByteSize(len(m.Data)), m.Used)))
			continue
		}
		rows = append(rows, Row(label, "gagal · "+m.Host))
		for _, a := range m.Attempts {
			rows = append(rows, Row("  "+a.Name, geminiMediaAttemptLine(a)))
			if a.Snippet != "" && (a.Status >= 400 || a.Name == "chrome") {
				rows = append(rows, Row("  balasan", geminiTruncateText(a.Snippet, 120)))
			}
		}
	}
	if len(p.Candidates) > len(p.Media) {
		rows = append(rows, Row("Kandidat", fmt.Sprintf("%d URL · semua di berkas", len(p.Candidates))))
	}
	if p.URLFile != "" {
		rows = append(rows, Row("Berkas URL", filepath.Base(p.URLFile)))
	}
	if p.DumpPath != "" {
		rows = append(rows, Row("Dump", filepath.Base(p.DumpPath)))
	}
	rows = append(rows, "", geminiDiagImageVerdict(p))
	return store.ReplyText(ctx, mctx, Block("Gemini diag", rows...))
}

// geminiMediaAttemptLine meringkas satu percobaan unduh untuk chat.
func geminiMediaAttemptLine(a geminiapi.MediaAttempt) string {
	switch {
	case a.OK:
		return "ok"
	case a.Err != nil && a.Status == 0:
		return "gagal: " + geminiTruncateText(a.Err.Error(), 120)
	case a.Status == 0:
		return "kosong"
	}
	out := fmt.Sprintf("%d", a.Status)
	if a.ContentType != "" {
		out += " · " + geminiTruncateText(a.ContentType, 48)
	}
	if a.Status < 400 && !a.OK {
		out += " · bukan gambar"
	}
	return out
}

func geminiDiagImageVerdict(p geminiapi.ImageProbe) string {
	switch {
	case errors.Is(p.ChatErr, geminiapi.ErrCookieRejected):
		return "Cookie ditolak saat minta gambar. Login ulang dulu, lalu ulangi."
	case p.ChatErr != nil:
		return "Gemini tidak menyelesaikan permintaan gambar (lihat baris Generate)."
	case p.Images == 0:
		return "Gemini tidak mengembalikan gambar; masalahnya di generasi, bukan unduhan."
	}

	ok := 0
	all403 := len(p.Media) > 0
	for _, m := range p.Media {
		if len(m.Data) > 0 {
			ok++
		}
		if len(m.Attempts) == 0 {
			all403 = false
		}
		for _, a := range m.Attempts {
			if a.Status != http.StatusForbidden {
				all403 = false
			}
		}
	}
	switch {
	case len(p.Media) == 0:
		return "Gambar terdeteksi tapi tidak ada URL yang bisa diunduh."
	case ok == len(p.Media):
		return "Jalur gambar sehat: semua gambar berhasil diunduh."
	case ok > 0:
		return "Sebagian gambar berhasil; sisanya gagal (lihat baris Unduh)."
	case all403:
		return "Semua percobaan (termasuk Chrome) ditolak HTTP 403. Buka berkas URL di HP: incognito dan saat login. Kalau incognito berhasil, penyebabnya IP/TLS panel; kalau hanya saat login berhasil, gambar butuh cookie googleusercontent."
	default:
		return "Gambar dihasilkan tapi tidak bisa diunduh; lihat status tiap percobaan."
	}
}

func geminiDiagUploadVerdict(p geminiapi.UploadProbe) string {
	switch {
	case p.UploadErr != nil:
		return "Unggahan gagal: masalahnya di jalur unggah/storage, bukan di chat."
	case errors.Is(p.ChatErr, geminiapi.ErrCookieRejected):
		return "Cookie ditolak saat chat. Jalankan .gemini status lalu login ulang bila perlu."
	case errors.Is(p.ChatErr, geminiapi.ErrEmpty):
		return "Unggahan lolos tapi jawaban kosong: bentuk permintaan ber-lampiran yang bermasalah. Periksa berkas Dump di atas."
	case p.ChatErr != nil:
		return "Unggahan lolos, chat gagal karena hal lain (lihat baris Chat)."
	default:
		return "Jalur lampiran sehat: unggah dan jawaban ber-lampiran bekerja."
	}
}

func geminiDiagFile(d *geminiapi.Diag) string {
	if len(d.FileNames) == 0 {
		return "kosong / belum ada"
	}
	out := fmt.Sprintf("%d cookie", len(d.FileNames))
	if d.FileHasTS {
		out += " · ada TS"
	}
	return out
}

func geminiDiagRotate(d *geminiapi.Diag) string {
	if d.RotateErr != nil {
		return "gagal: " + geminiTruncateText(d.RotateErr.Error(), 120)
	}
	if d.Rotate.Status == 0 {
		return "dilewati"
	}
	out := fmt.Sprintf("HTTP %d", d.Rotate.Status)
	if len(d.Rotate.SetNames) > 0 {
		out += " · " + strings.Join(d.Rotate.SetNames, ", ")
	}
	if d.Rotate.Changed {
		out += " · berubah"
	} else {
		out += " · tetap"
	}
	return out
}

func geminiDiagProbe(p geminiapi.AppProbe) string {
	if p.Err != nil {
		return "gagal: " + geminiTruncateText(p.Err.Error(), 120)
	}
	if p.Status == 0 {
		return "tidak dijalankan"
	}
	out := fmt.Sprintf("HTTP %d · %s", p.Status, geminiByteSize(p.Bytes))
	switch {
	case p.HasToken:
		out += " · token ada"
	case p.HasLogin:
		out += " · halaman login"
	default:
		out += " · token tidak ada"
	}
	return out
}

// geminiDiagVerdict menyimpulkan penyebab dari tiga probe /app.
func geminiDiagVerdict(d *geminiapi.Diag) string {
	switch {
	case len(d.FileNames) == 0:
		return "Berkas cookie kosong. Simpan dengan .gemini login <cookie>."
	case d.App.HasToken:
		return "Sesi sehat dengan cookie lengkap."
	case d.AppNoTS.HasToken:
		return "Cookie volatile (TS) yang tersimpan merusak sesi; bot mengulang tanpa TS, jadi chat tetap jalan. Kalau .gemini status masih ragu, jalankan .gemini diag lagi setelah satu percakapan."
	case d.AppOnly1PSID.HasToken:
		return "__Secure-1PSID masih sah; hanya cookie turunan yang bermasalah. Bot mengulang tanpa cookie volatile."
	case d.AppOnly1PSID.HasLogin:
		return "Google menampilkan halaman login: __Secure-1PSID sudah tidak berlaku. Login ulang di browser, lalu .gemini login <cookie>."
	default:
		return "Tidak bisa disimpulkan; lihat status tiap baris di atas."
	}
}

func geminiByteSize(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// geminiTruncateText memotong teks panjang dengan elipsis.
func geminiTruncateText(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max]) + "…"
}

// geminiNormalizeCookieString menerima cookie dalam beberapa bentuk yang
// lazim ditempel owner: satu baris header "name=value; ...", banyak baris
// (satu cookie per baris), atau hanya nilai __Secure-1PSID.
func geminiNormalizeCookieString(raw string) string {
	raw = strings.TrimPrefix(raw, "Cookie:")
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, "\n") {
		parts := make([]string, 0, 8)
		for _, line := range strings.Split(raw, "\n") {
			line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
			line = strings.TrimSuffix(line, ";")
			if line != "" {
				parts = append(parts, line)
			}
		}
		raw = strings.Join(parts, "; ")
	}
	return strings.TrimSuffix(strings.TrimSpace(raw), ";")
}

// --- routing pasif ---

// CheckGeminiSession dipanggil handler untuk pesan non-command. Mengembalikan
// true bila pesan dikonsumsi sesi Gemini (diproses async), supaya tidak bocor
// ke autodownload/AI.
func CheckGeminiSession(client *whatsmeow.Client, evt *events.Message, text string) bool {
	if evt == nil || evt.Info.IsGroup || !store.IsMainClient(client) {
		return false
	}
	if !IsOwnerEvent(evt) {
		return false
	}
	key := evt.Info.Chat.String()
	if !geminiSessionOn(key) {
		return false
	}

	prompt, hasAttach, triggered := geminiTriggerOf(evt, text)
	if !triggered {
		return false
	}
	// "gemini" polos tanpa lampiran: jangan kirim prompt kosong, cukup
	// ingatkan cara pakai — tapi tetap dikonsumsi supaya tidak dijawab AI.
	if strings.TrimSpace(prompt) == "" && !hasAttach {
		go func() {
			bg := context.Background()
			mctx := geminiContext(client, evt)
			_ = store.ReplyText(bg, mctx, ErrMsg("Gemini",
				"Mode aktif. Tulis pertanyaan setelah kata gemini, atau balas bubble bot."))
		}()
		return true
	}

	go geminiRun(client, evt, prompt)
	return true
}

// geminiContext merakit MessageContext untuk jalur pasif (tanpa command).
func geminiContext(client *whatsmeow.Client, evt *events.Message) *store.MessageContext {
	return &store.MessageContext{
		Client:      client,
		Chat:        evt.Info.Chat,
		Sender:      evt.Info.Sender,
		RawEvt:      evt,
		SkipArchive: true,
	}
}

// geminiPrivateMessage true bila pesan ini milik sesi Gemini owner sehingga
// tidak boleh masuk arsip CRM: command .gemini/.gm (bisa memuat cookie) atau
// pesan yang memang akan diteruskan ke Gemini saat mode aktif.
func geminiPrivateMessage(evt *events.Message) bool {
	if evt == nil || evt.Info.IsGroup || !IsOwnerEvent(evt) {
		return false
	}
	low := strings.ToLower(strings.TrimSpace(geminiDirectText(evt)))
	if strings.HasPrefix(low, ".gemini") || strings.HasPrefix(low, ".gm ") || low == ".gm" {
		return true
	}
	if !geminiSessionOn(evt.Info.Chat.String()) {
		return false
	}
	_, _, triggered := geminiTriggerOf(evt, geminiDirectText(evt))
	return triggered
}

// geminiTriggerOf memutuskan apakah pesan memicu sesi Gemini, dan dengan
// prompt apa. Aturannya sempit supaya mode tidak menelan chat biasa:
//   - teks diawali "gemini" (diikuti spasi/tanda baca) → prompt = sisanya
//   - reply ke bubble bot → prompt = teks/caption user (boleh kosong kalau
//     ada lampiran)
func geminiTriggerOf(evt *events.Message, text string) (prompt string, hasAttach bool, ok bool) {
	text = strings.TrimSpace(text)
	hasAttach = geminiAttachmentOf(evt) != nil
	if rest, ok := geminiCutPrefix(text); ok {
		return rest, hasAttach, true
	}
	if text != "" && store.IsReplyToBot(evt) {
		return text, hasAttach, true
	}
	// Reply tanpa teks tapi membawa/menyertakan media tetap diteruskan.
	if text == "" && hasAttach && store.IsReplyToBot(evt) {
		return "", hasAttach, true
	}
	return "", hasAttach, false
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

// geminiDirectText mengambil teks pesan user sendiri (bukan isi bubble yang
// di-reply): conversation, extended text, atau caption media.
func geminiDirectText(evt *events.Message) string {
	if evt == nil {
		return ""
	}
	return geminiMessageText(store.UnwrapMessage(evt.Message))
}

// geminiQuotedText mengambil teks dari pesan yang di-reply (dipakai untuk
// menempel cookie lewat reply).
func geminiQuotedText(evt *events.Message) string {
	return geminiMessageText(store.UnwrapMessage(store.ExtractQuotedMessage(evt)))
}

func geminiMessageText(msg *waE2E.Message) string {
	if msg == nil {
		return ""
	}
	candidates := []string{
		msg.GetConversation(),
		msg.GetExtendedTextMessage().GetText(),
		msg.GetImageMessage().GetCaption(),
		msg.GetVideoMessage().GetCaption(),
		msg.GetDocumentMessage().GetCaption(),
	}
	for _, c := range candidates {
		if t := strings.TrimSpace(c); t != "" {
			return t
		}
	}
	return ""
}

// geminiAttachment adalah lampiran yang bisa diteruskan ke Gemini.
type geminiAttachment struct {
	kind string // image | document | video | audio | sticker
	img  *waE2E.ImageMessage
	doc  *waE2E.DocumentMessage
}

func geminiAttachmentOf(evt *events.Message) *geminiAttachment {
	if evt == nil {
		return nil
	}
	if a := geminiAttachmentFrom(store.UnwrapMessage(evt.Message)); a != nil {
		return a
	}
	return geminiAttachmentFrom(store.UnwrapMessage(store.ExtractQuotedMessage(evt)))
}

func geminiAttachmentFrom(msg *waE2E.Message) *geminiAttachment {
	if msg == nil {
		return nil
	}
	if img := msg.GetImageMessage(); img != nil {
		return &geminiAttachment{kind: "image", img: img}
	}
	if doc := msg.GetDocumentMessage(); doc != nil {
		return &geminiAttachment{kind: "document", doc: doc}
	}
	if msg.GetVideoMessage() != nil {
		return &geminiAttachment{kind: "video"}
	}
	if msg.GetAudioMessage() != nil {
		return &geminiAttachment{kind: "audio"}
	}
	if msg.GetStickerMessage() != nil {
		return &geminiAttachment{kind: "sticker"}
	}
	return nil
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
	key := evt.Info.Chat.String()
	mctx := geminiContext(client, evt)

	conv, started, busy := geminiSessionStart(key)
	if !started {
		if busy {
			_ = store.ReplyText(context.Background(), mctx, ErrMsg("Gemini",
				"Masih memproses pesan sebelumnya. Tunggu sampai selesai."))
		}
		return
	}
	defer geminiSessionRelease(key)

	bg := context.Background()
	ctx, cancel := context.WithTimeout(bg, geminiChatTimeout)
	defer cancel()

	att := geminiAttachmentOf(evt)
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
		// Context sendiri: ctx utama (3 menit) bisa sudah dibatalkan saat
		// goroutine ini menembak, dan reaksi bukan bagian alur jawaban.
		rctx, rcancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer rcancel()
		return store.SendReaction(rctx, mctx, geminiWaitEmoji)
	})

	files, err := geminiPrepareFiles(ctx, mctx, att)
	if err != nil {
		_ = store.ReplyText(bg, mctx, ErrMsg("Gemini", err.Error()))
		return
	}
	if strings.TrimSpace(prompt) == "" {
		prompt = geminiDefaultPrompt(att)
	}
	if utf8.RuneCountInString(prompt) > geminiMaxPromptRunes {
		_ = store.ReplyText(bg, mctx, ErrMsg("Gemini",
			fmt.Sprintf("Pertanyaan kepanjangan (maks %d karakter).", geminiMaxPromptRunes)))
		return
	}

	res, err := geminiChatOnce(ctx, prompt, conv, files)
	if errors.Is(err, geminiapi.ErrStaleConv) {
		// Server membuang percakapan (mis. terlalu lama / dihapus dari akun):
		// mulai percakapan baru tanpa mengganggu owner. Pointer lama dibuang
		// supaya nomor giliran ikut mulai dari awal.
		logfmt.Warn("gemini: percakapan lama tidak berlaku, mulai baru\n")
		conv = nil
		res, err = geminiChatOnce(ctx, prompt, nil, files)
	}
	if err != nil {
		logfmt.Warn("gemini: %v\n", err)
		_ = store.ReplyText(bg, mctx, ErrMsg("Gemini", geminiErrorText(err)))
		return
	}

	if res.Conv != nil {
		turn := 1
		if conv != nil {
			turn = conv.Turn + 1
		}
		res.Conv.Turn = turn
		geminiSessionStore(key, res.Conv)
	}

	if err := geminiDeliver(ctx, mctx, prompt, res); err != nil {
		logfmt.Warn("gemini kirim: %v\n", err)
		_ = store.ReplyText(bg, mctx, ErrMsg("Gemini", err.Error()))
	}
}

func geminiChatOnce(ctx context.Context, prompt string, conv *geminiapi.Conversation, files []geminiapi.File) (*geminiapi.Result, error) {
	return geminiAPI().Chat(ctx, geminiapi.Request{
		Prompt:   prompt,
		Model:    geminiModelText(),
		Thinking: geminiThinking(),
		Conv:     conv,
		Files:    files,
	})
}

// geminiPrepareFiles mengunduh lampiran dari WhatsApp lalu mengunggahnya ke
// storage Gemini. Gambar dinormalkan lewat seditPrepareImage (≤4 MB, sisi
// ≤1600) supaya unggahan cepat dan tidak menghabiskan kuota.
func geminiPrepareFiles(ctx context.Context, mctx *store.MessageContext, att *geminiAttachment) ([]geminiapi.File, error) {
	if att == nil {
		return nil, nil
	}
	dctx, cancel := context.WithTimeout(ctx, geminiDownloadLimit)
	defer cancel()

	switch att.kind {
	case "image":
		data, err := mctx.Client.Download(dctx, att.img)
		if err != nil {
			return nil, fmt.Errorf("gagal mengunduh gambar: %v", err)
		}
		prepared, err := seditPrepareImage(ctx, data)
		if err != nil {
			return nil, fmt.Errorf("gambar tidak bisa disiapkan: %v", err)
		}
		f, err := geminiAPI().Upload(ctx, "gambar.jpg", "image/jpeg", prepared)
		if err != nil {
			return nil, err
		}
		return []geminiapi.File{f}, nil
	case "document":
		maxBytes := int64(geminiMaxUploadMB()) << 20
		if length := int64(att.doc.GetFileLength()); length > maxBytes {
			return nil, fmt.Errorf("dokumen terlalu besar (%s, maks %d MB)",
				humanBytes(length), geminiMaxUploadMB())
		}
		data, err := mctx.Client.Download(dctx, att.doc)
		if err != nil {
			return nil, fmt.Errorf("gagal mengunduh dokumen: %v", err)
		}
		if int64(len(data)) > maxBytes {
			return nil, fmt.Errorf("dokumen terlalu besar (%s, maks %d MB)",
				humanBytes(int64(len(data))), geminiMaxUploadMB())
		}
		name := sanitizeFileName(att.doc.GetFileName())
		if name == "" {
			name = "lampiran"
		}
		f, err := geminiAPI().Upload(ctx, name, "", data)
		if err != nil {
			return nil, err
		}
		return []geminiapi.File{f}, nil
	case "video":
		return nil, fmt.Errorf("lampiran video belum didukung; kirim gambar atau dokumen")
	default:
		return nil, fmt.Errorf("lampiran %s belum didukung; kirim gambar atau dokumen", att.kind)
	}
}

func geminiDefaultPrompt(att *geminiAttachment) string {
	switch {
	case att == nil:
		return ""
	case att.kind == "document":
		return "Ringkas isi dokumen ini."
	default:
		return "Jelaskan gambar ini."
	}
}

// geminiDeliver mengirim hasil: gambar (kalau ada) sebagai bubble gambar,
// teksnya jadi caption; tanpa gambar → teks dipecah agar tidak menabrak batas
// panjang pesan WhatsApp.
func geminiDeliver(ctx context.Context, mctx *store.MessageContext, prompt string, res *geminiapi.Result) error {
	imagesSent := 0
	var lastImgErr error
	for _, u := range res.Images {
		data, err := geminiFetchImage(ctx, u)
		if err != nil {
			logfmt.Warn("gemini: unduh gambar hasil: %v\n", err)
			lastImgErr = err
			continue
		}
		caption := ""
		if imagesSent == 0 {
			caption = geminiImageCaption(res.Text, prompt)
		}
		if err := geminiSendImage(ctx, mctx, data, caption, imagesSent == 0); err != nil {
			logfmt.Warn("gemini: kirim gambar hasil: %v\n", err)
			continue
		}
		imagesSent++
	}
	if imagesSent > 0 {
		return nil
	}

	text := geminiCleanAnswer(res.Text)
	if text == "" {
		if len(res.Images) > 0 {
			if lastImgErr != nil {
				return fmt.Errorf("Gambar hasil Gemini (%d) gagal diunduh: %s",
					len(res.Images), geminiTruncateText(lastImgErr.Error(), 200))
			}
			return fmt.Errorf("Gambar hasil Gemini (%d) gagal diunduh. Coba lagi sebentar.", len(res.Images))
		}
		return fmt.Errorf("Gemini tidak mengirim teks maupun gambar")
	}
	return geminiSendText(ctx, mctx, text)
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
	chunks := geminiSplitReply(text, geminiReplyChunk)
	if len(chunks) == 0 {
		return fmt.Errorf("jawaban kosong")
	}
	if err := store.ReplyText(ctx, mctx, chunks[0]); err != nil {
		return err
	}
	for _, chunk := range chunks[1:] {
		if err := store.SendText(ctx, mctx, chunk); err != nil {
			return err
		}
	}
	return nil
}

// geminiSplitReply memecah teks panjang, diusahakan putus di batas baris.
func geminiSplitReply(text string, size int) []string {
	text = strings.TrimSpace(text)
	if size <= 0 || utf8.RuneCountInString(text) <= size {
		if text == "" {
			return nil
		}
		return []string{text}
	}
	runes := []rune(text)
	var out []string
	for len(runes) > size {
		cut := size
		for i := size - 1; i > size/2; i-- {
			if runes[i] == '\n' {
				cut = i + 1
				break
			}
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
	if len(out) > geminiMaxChunks {
		out = out[:geminiMaxChunks]
		out[len(out)-1] += "\n…(jawaban dipotong)"
	}
	return out
}

// geminiImageCaption menggabungkan teks jawaban (kalau ada) dengan penanda
// model. Caption dibatasi 1000 rune: lebih dari itu WhatsApp memotong sendiri.
func geminiImageCaption(text, prompt string) string {
	head := geminiImageHead(prompt)
	text = flattenPrompt(geminiCleanAnswer(text))
	if text == "" {
		return head
	}
	budget := geminiCaptionMax - utf8.RuneCountInString(head) - 2
	if budget <= 20 {
		return head
	}
	if short, _, truncated := textx.TruncateRunes(text, budget-1, false); truncated {
		text = short + "…"
	}
	return text + "\n\n" + head
}

func geminiImageHead(prompt string) string {
	head := "Gemini · " + geminiapi.ModelLabel(geminiModelText())
	if p := flattenPrompt(prompt); p != "" {
		if short, _, truncated := textx.TruncateRunes(p, 200, false); truncated {
			p = short + "…"
		}
		head += "\n> " + p
	}
	return head
}

// geminiFetchImage mengunduh gambar hasil. Jalur utama `DownloadMedia` mencoba
// beberapa bentuk permintaan lalu Chrome panel sebagai rung terakhir (URL
// `gg-dl` menolak klien HTTP biasa dengan 403); pengunduh umum dan IPv4 tetap
// dipakai sebagai cadangan. Error jalur sesi tidak ditutupi error fallback:
// pesan itulah yang memberi tahu penyebab sebenarnya (status/tipe).
func geminiFetchImage(ctx context.Context, rawURL string) ([]byte, error) {
	sessionData, sessionErr := geminiAPI().DownloadMedia(ctx, rawURL)
	if sessionErr == nil && len(sessionData) > 0 {
		return sessionData, nil
	}
	if sessionErr != nil {
		logfmt.Warn("gemini: unduh sesi: %v\n", sessionErr)
	}

	data, err := external.DownloadBytes(ctx, rawURL)
	if err == nil && len(data) > 0 {
		return data, nil
	}
	if err != nil {
		logfmt.Warn("gemini: unduh umum: %v\n", err)
	}
	data2, err2 := external.DownloadBytesIPv4(ctx, rawURL)
	if err2 == nil && len(data2) > 0 {
		return data2, nil
	}
	if err2 != nil {
		logfmt.Warn("gemini: unduh IPv4: %v\n", err2)
	}

	switch {
	case sessionErr != nil:
		return nil, sessionErr
	case err != nil:
		return nil, err
	case err2 != nil:
		return nil, err2
	}
	return nil, fmt.Errorf("gambar hasil kosong")
}

// geminiSendImage mengirim byte gambar sebagai ImageMessage, me-reply perintah
// user untuk bubble pertama (sama seperti .seedream).
func geminiSendImage(ctx context.Context, mctx *store.MessageContext, data []byte, caption string, asReply bool) error {
	im, err := messaging.UploadImageMessage(ctx, mctx.Client, data)
	if err != nil {
		return fmt.Errorf("gagal upload gambar: %v", err)
	}
	im.Caption = proto.String(caption)
	if asReply {
		im.ContextInfo = &waE2E.ContextInfo{
			StanzaID:      proto.String(mctx.RawEvt.Info.ID),
			Participant:   proto.String(mctx.Sender.String()),
			QuotedMessage: mctx.RawEvt.Message,
		}
	}
	// Dimensi dan thumbnail sudah diisi UploadImageMessage.
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, &waE2E.Message{ImageMessage: im})
	if err != nil {
		return fmt.Errorf("gagal kirim gambar: %v", err)
	}
	store.LogOutgoing(mctx, "image", caption)
	store.TrackBotMessageID(resp.ID, caption)
	return nil
}

// --- konfigurasi & teks error ---

func geminiModelText() string {
	if v := strings.TrimSpace(os.Getenv("GEMINI_MODEL_TEXT")); v != "" {
		return v
	}
	return "flash"
}

func geminiThinking() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("GEMINI_THINKING")))
	return v == "1" || v == "on" || v == "true"
}

func geminiMaxUploadMB() int {
	if v := strings.TrimSpace(os.Getenv("GEMINI_MAX_UPLOAD_MB")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return geminiDefaultUploadMB
}

// geminiErrorText menerjemahkan error adapter jadi kalimat yang bisa
// ditindaklanjuti, tanpa membocorkan nilai cookie.
func geminiErrorText(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, geminiapi.ErrNoCookie):
		return "Cookie Gemini belum ada. Ambil header Cookie dari gemini.google.com di browser akun bot, lalu kirim .gemini login <cookie>."
	case errors.Is(err, geminiapi.ErrCookieRejected):
		return "Cookie Gemini ditolak (sesi berakhir). Login ulang di browser akun bot, salin header Cookie, lalu .gemini login <cookie>."
	case errors.Is(err, geminiapi.ErrLimit):
		return "Batas penggunaan Gemini tercapai untuk akun ini. Coba lagi beberapa saat."
	case errors.Is(err, geminiapi.ErrUpload):
		return fmt.Sprintf("Gagal mengunggah lampiran ke Gemini: %v", err)
	case errors.Is(err, geminiapi.ErrEmpty), errors.Is(err, geminiapi.ErrShape):
		return "Balasan Gemini tidak terbaca (bisa jadi protokol webnya berubah). Coba lagi."
	case errors.Is(err, context.DeadlineExceeded):
		return "Waktu habis menunggu Gemini. Coba lagi."
	case errors.Is(err, geminiapi.ErrHTTP):
		return fmt.Sprintf("Permintaan ke Gemini gagal: %v", err)
	default:
		return fmt.Sprintf("Gagal: %v", err)
	}
}
