package plugins

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"wabot-go/internal/arena"
	"wabot-go/internal/logfmt"
	"wabot-go/internal/messaging"
	"wabot-go/internal/store"
	"wabot-go/internal/textx"
	"wabot-go/internal/timex"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func init() {
	registerPlugin(RegisterSeedream)
}

// seedream.go — masuk tunggal ke arena.ai (chat langsung, satu model per
// sesi). Owner-only: category Owner sudah otomatis menyembunyikan command ini
// dari non-owner, DAN handler tetap memanggil requireOwner (sabuk + bretel,
// karena kategori bisa saja diubah orang lain nanti).
//
// Satu command, tiga pekerjaan — tanpa subcommand generate/edit, karena
// niatnya bisa dibaca dari isi pesan:
//
//	.seedream <prompt>              -> generate
//	.seedream <prompt> + foto       -> edit (foto dilampirkan/di-reply)
//	.seedream cookie <cookie>       -> simpan sesi arena.ai
//	.seedream cookie                -> status sesi
//	.seedream                        -> petunjuk
//
// Model default seedream-5.0-pro (rasio 1:1 saja menurut catalog arena.ai).
// Ganti lewat env ARENA_MODEL, mis. ke "seedream-4.5" atau "seedream-5.0-lite".
//
// Kenapa ada kuota harian + jeda: satu generate memakai kuota akun arena.ai
// milik owner dan menyalakan Chrome ~300MB di panel 1 core. Tanpa pembatas,
// satu orang yang menekan command berulang bisa mengosongkan kuota akun atau
// memicu OOM panel.

const (
	seedreamDefaultTimeout  = 240 * time.Second
	seedreamMaxPromptRunes  = 1000
	seedreamDefaultDaily    = 20
	seedreamDefaultCooldown = 30 * time.Second
	// seedreamCaptchaBackoff: jeda setelah arena.ai menolak token reCAPTCHA.
	// Percobaan beruntun justru memperburuk skor risk-analysis Google, jadi
	// bot berhenti mencoba sebentar alih-alih menembak terus.
	seedreamCaptchaBackoff = 10 * time.Minute
)

// Pembatas per-owner. Disimpan di memori (bukan DB): sisa kuota harian
// memang tidak perlu bertahan melewati restart, dan tabel baru berarti
// menaikkan schema version di store — biaya yang tak sepadan.
var (
	seedreamLimMu   sync.Mutex
	seedreamDay     string
	seedreamUsed    int
	seedreamLastUse time.Time
	seedreamBlocked time.Time // sampai kapan percobaan ditolak (backoff captcha)
)

// RegisterSeedream mendaftarkan satu command arena.ai (alias .sd). Generate,
// edit, dan cookie semuanya lewat sini — command terpisah (.sedit/.scookie)
// dihapus supaya owner tidak perlu mengingat tiga nama.
func RegisterSeedream(r *Registry) {
	r.Register(Command{
		Name:        "seedream",
		Aliases:     []string{"sd"},
		Description: "Generate/edit gambar + cookie arena.ai (owner)",
		Category:    CategoryOwner,
		Handler:     handleSeedream,
	})
}

// handleSeedream adalah satu pintu untuk semua pekerjaan arena.ai. Urutannya:
// subcommand "cookie" lebih dulu (kata pertama), lalu deteksi media: ada foto
// = edit, tidak ada = generate. Tanpa argumen dan tanpa media/prompt = blok
// petunjuk (bukan error), mengikuti gaya command lain.
func handleSeedream(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}

	args := strings.TrimSpace(mctx.RawArgs)
	// Subcommand eksplisit satu-satunya: cookie. Sisa baris dipakai apa
	// adanya sebagai nilai cookie (bisa sangat panjang, jangan dipecah).
	if kind, rest := seedreamSubcommand(args); kind == "cookie" {
		return handleArenaCookie(ctx, mctx, rest)
	}

	// Kata kerja opsional di awal prompt dibuang ("generate ...", "edit ...")
	// supaya kebiasaan lama tetap jalan walau subcommand-nya tidak wajib.
	prompt := seedreamStripVerb(args)

	img, vid, _ := extractStickerMedia(mctx)
	if img != nil || vid != nil {
		return seedreamEdit(ctx, mctx, img, vid, prompt)
	}
	if prompt == "" {
		// Argumen kosong: prompt masih bisa datang dari pesan yang di-reply
		// (teks maupun caption media non-gambar).
		prompt = seedreamPrompt(mctx)
	}
	if prompt == "" {
		return store.ReplyText(ctx, mctx, seedreamHelpBlock())
	}
	return seedreamGenerate(ctx, mctx, prompt)
}

// seedreamHelpBlock adalah petunjuk singkat .seedream; tampil saat command
// dipanggil tanpa argumen yang bisa dipakai.
func seedreamHelpBlock() string {
	return Block("Seedream",
		Opt("Generate", ".seedream <prompt>"),
		Opt("Edit", ".seedream <prompt> + reply gambar"),
		Opt("Cookie", ".seedream cookie <cookie>"),
	)
}

// seedreamSubcommand memisahkan subcommand eksplisit dari argumen. Hanya
// "cookie" yang dianggap subcommand — generate/edit ditentukan dari ada
// tidaknya foto, jadi prompt bebas diawali kata apa pun.
func seedreamSubcommand(args string) (kind, rest string) {
	first, rest := seedreamSplitFirst(args)
	if strings.EqualFold(first, "cookie") {
		return "cookie", strings.TrimSpace(rest)
	}
	return "", args
}

// seedreamStripVerb membuang kata kerja opsional di awal prompt ("edit",
// "generate", "gen") supaya ".seedream edit <prompt>" dan ".seedream
// generate <prompt>" tetap terasa alami walau subcommand-nya tidak wajib.
func seedreamStripVerb(args string) string {
	first, rest := seedreamSplitFirst(args)
	switch strings.ToLower(first) {
	case "edit", "generate", "gen":
		return strings.TrimSpace(rest)
	}
	return strings.TrimSpace(args)
}

// seedreamSplitFirst memecah teks di spasi pertama.
func seedreamSplitFirst(s string) (first, rest string) {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, " \t\r\n"); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

// seedreamGenerate menjalankan text-to-image: validasi prompt + kuota, lalu
// generate di goroutine terpisah supaya event loop hypermeow tidak tertahan
// (Chrome start + reCAPTCHA + render bisa puluhan detik di panel 1 core).
func seedreamGenerate(ctx context.Context, mctx *store.MessageContext, prompt string) error {
	if utf8.RuneCountInString(prompt) > seedreamMaxPromptRunes {
		return store.ReplyText(ctx, mctx, ErrMsg("Seedream",
			fmt.Sprintf("Prompt kepanjangan (maks %d karakter).", seedreamMaxPromptRunes)))
	}
	if wait, left, err := seedreamBudget(); err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Seedream", err.Error()))
	} else if wait > 0 {
		return store.ReplyText(ctx, mctx, ErrMsg("Seedream",
			fmt.Sprintf("Tunggu %d detik lagi. Sisa kuota hari ini: %d.", int(wait.Seconds())+1, left)))
	}

	go func() {
		bg := context.Background()
		tctx, cancel := context.WithTimeout(bg, seedreamTimeout())
		defer cancel()

		// Keterangan tunggu dikirim lebih dulu supaya owner tahu perintahnya
		// sudah diterima; gambar hasilnya nanti yang me-reply perintah.
		if err := store.SendText(bg, mctx, seedreamWaitText("")); err != nil {
			logfmt.Warn("seedream tunggu: %v\n", err)
		}

		data, model, err := arena.GenerateImage(tctx, prompt)
		if err != nil {
			// Generate gagal (mis. cookie basi) tidak boleh ikut memakan
			// kuota akun arena.ai owner — kalau tidak, satu kali cookie basi
			// bisa mengunci seluruh kuota hari itu.
			seedreamRefund()
			if errors.Is(err, arena.ErrRecaptcha) {
				seedreamBlockCaptcha()
			}
			logfmt.Warn("seedream: %v\n", err)
			_ = store.ReplyText(bg, mctx, ErrMsg("Seedream", seedreamErrorText(err)))
			return
		}
		if err := seedreamSend(bg, mctx, data, string(model), "", prompt); err != nil {
			logfmt.Warn("seedream kirim: %v\n", err)
			_ = store.ReplyText(bg, mctx, ErrMsg("Seedream", err.Error()))
		}
	}()
	return nil
}

// seedreamPrompt mengambil prompt dari argumen; kalau kosong, dari pesan yang
// di-reply (teks maupun caption media). bratInputText sudah melakukan tepat
// itu (termasuk unwrap pesan sekali-lihat), jadi dipakai ulang alih-alih
// menyalin logika ContextInfo yang sama.
func seedreamPrompt(mctx *store.MessageContext) string {
	return strings.TrimSpace(bratInputText(mctx))
}

// seedreamBudget memeriksa jeda + kuota harian. Mengembalikan sisa waktu
// tunggu (0 = boleh jalan) dan sisa kuota setelah pemakaian ini.
func seedreamBudget() (wait time.Duration, left int, err error) {
	seedreamLimMu.Lock()
	defer seedreamLimMu.Unlock()

	today := time.Now().Format("2006-01-02")
	if seedreamDay != today {
		seedreamDay = today
		seedreamUsed = 0
	}
	if time.Now().Before(seedreamBlocked) {
		left := time.Until(seedreamBlocked)
		return 0, seedreamDailyLimit() - seedreamUsed, fmt.Errorf(
			"arena.ai menolak verifikasi reCAPTCHA, jadi bot berhenti mencoba sementara. Coba lagi dalam %d menit.",
			int(left.Minutes())+1)
	}
	limit := seedreamDailyLimit()
	if seedreamUsed >= limit {
		return 0, 0, fmt.Errorf("Kuota harian habis (%d/%d). Ulang besok atau naikkan ARENA_MAX_DAILY.", seedreamUsed, limit)
	}
	cooldown := seedreamCooldown()
	if !seedreamLastUse.IsZero() {
		if elapsed := time.Since(seedreamLastUse); elapsed < cooldown {
			return cooldown - elapsed, limit - seedreamUsed, nil
		}
	}
	seedreamUsed++
	seedreamLastUse = time.Now()
	return 0, limit - seedreamUsed, nil
}

// seedreamBlockCaptcha memasang backoff setelah token reCAPTCHA ditolak.
// Menembak ulang berkali-kali hanya menurunkan skor risk-analysis Google,
// jadi lebih baik menunggu.
func seedreamBlockCaptcha() {
	seedreamLimMu.Lock()
	defer seedreamLimMu.Unlock()
	seedreamBlocked = time.Now().Add(seedreamCaptchaBackoff)
}

// seedreamRefund mengembalikan satu jatah kuota saat generate gagal sebelum
// gambar jadi. Jeda (cooldown) sengaja tidak direset — percobaan gagal tetap
// membebani panel, jadi tidak boleh bisa di-spam.
func seedreamRefund() {
	seedreamLimMu.Lock()
	defer seedreamLimMu.Unlock()
	if seedreamUsed > 0 {
		seedreamUsed--
	}
}

// seedreamSend mengirim byte gambar sebagai ImageMessage (pola sama dengan
// .rvo: upload -> bangun protobuf -> SendMessage), lengkap dengan thumbnail
// inline supaya preview di chat tidak kosong. mode kosong = hasil generate;
// "edit" menandai hasil edit foto.
//
// Pesan ini me-reply perintah user (ContextInfo) supaya hasil gambar menempel
// pada command yang memintanya, bukan menggantung lepas di chat.
func seedreamSend(ctx context.Context, mctx *store.MessageContext, data []byte, model, mode, prompt string) error {
	if err := messaging.CheckMediaBytes(data); err != nil {
		return fmt.Errorf("hasil gambar tidak valid: %v", err)
	}
	uploaded, err := mctx.Client.Upload(ctx, data, whatsmeow.MediaImage)
	if err != nil {
		return fmt.Errorf("gagal upload gambar: %v", err)
	}

	caption := seedreamCaption(mode, model, prompt)
	im := &waE2E.ImageMessage{
		URL:               proto.String(uploaded.URL),
		DirectPath:        proto.String(uploaded.DirectPath),
		MediaKey:          uploaded.MediaKey,
		MediaKeyTimestamp: proto.Int64(time.Now().Unix()),
		Mimetype:          proto.String("image/jpeg"),
		FileEncSHA256:     uploaded.FileEncSHA256,
		FileSHA256:        uploaded.FileSHA256,
		FileLength:        proto.Uint64(uploaded.FileLength),
		Caption:           proto.String(caption),
		JPEGThumbnail:     messaging.InlineThumbForImage(data),
		ContextInfo: &waE2E.ContextInfo{
			StanzaID:      proto.String(mctx.RawEvt.Info.ID),
			Participant:   proto.String(mctx.Sender.String()),
			QuotedMessage: mctx.RawEvt.Message,
		},
	}
	if cfg, _, cerr := image.DecodeConfig(bytes.NewReader(data)); cerr == nil && cfg.Width > 0 {
		im.Width = proto.Uint32(uint32(cfg.Width))
		im.Height = proto.Uint32(uint32(cfg.Height))
	}

	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, &waE2E.Message{ImageMessage: im})
	if err != nil {
		return fmt.Errorf("gagal kirim gambar: %v", err)
	}
	store.LogOutgoing(mctx, "seedream", caption)
	store.TrackBotMessageID(resp.ID, caption)
	return nil
}

// seedreamCaptionMaxRunes adalah batas aman panjang caption WhatsApp (batas
// kerasnya 1024 karakter); sisanya dipotong dengan elipsis.
const seedreamCaptionMaxRunes = 1000

// seedreamCaption menyusun caption hasil gambar:
//
//	Generate With seedream-5.0-pro
//	> prompt yang dipakai
//
// mode "edit" menghasilkan "Edit With ...". Nama penyedia (arena.ai) sengaja
// tidak dicantumkan: caption ini terlihat oleh siapa pun di chat, dan yang
// relevan hanya jenis pekerjaan, model, dan prompt-nya.
func seedreamCaption(mode, model, prompt string) string {
	kind := "Generate"
	if mode == "edit" {
		kind = "Edit"
	}
	model = strings.TrimSpace(model)
	if model == "" {
		model = arenaModelName()
	}
	head := kind + " With " + model

	prompt = flattenPrompt(prompt)
	if prompt == "" {
		return head
	}

	// Awalan baris kedua ("\n> ") ikut dihitung; satu rune disisakan untuk
	// elipsis bila prompt harus dipotong.
	const prefix = "\n> "
	budget := seedreamCaptionMaxRunes - utf8.RuneCountInString(head+prefix)
	if budget <= 1 {
		return head
	}
	if short, _, truncated := textx.TruncateRunes(prompt, budget-1, false); truncated {
		return head + prefix + short + "…"
	}
	return head + prefix + prompt
}

// flattenPrompt merapatkan prompt multi-baris jadi satu baris: caption hanya
// punya satu baris prompt (awalan "> " tidak boleh pecah di tengah).
func flattenPrompt(prompt string) string {
	return strings.Join(strings.Fields(prompt), " ")
}

// seedreamWaitText adalah keterangan tunggu yang dikirim segera setelah
// perintah diterima. Sengaja tanpa emoji dan dikirim sebagai pesan biasa
// (bukan quote) supaya tidak menumpuk balasan; gambar hasilnya yang me-reply
// perintah user.
func seedreamWaitText(mode string) string {
	if mode == "edit" {
		return "Sedang mengedit foto, tunggu sebentar..."
	}
	return "Sedang membuat gambar, tunggu sebentar..."
}

// seedreamErrorText menerjemahkan error adapter menjadi kalimat yang bisa
// ditindaklanjuti owner, bukan pesan teknis mentah.
func seedreamErrorText(err error) string {
	switch {
	case errors.Is(err, arena.ErrNoCookie):
		return "Cookie arena.ai belum ada. Login di browser, salin cookie dari request create-evaluation, lalu kirim .seedream cookie <cookie>."
	case errors.Is(err, arena.ErrCookieRejected):
		return "Cookie arena.ai sudah tidak berlaku (sesi kedaluwarsa). Ambil cookie baru dari browser lalu .seedream cookie <cookie>."
	case errors.Is(err, arena.ErrRecaptcha):
		return "reCAPTCHA ditolak arena.ai. Biasanya karena cookie sesi basi — perbarui dengan .seedream cookie; kalau tetap gagal, coba lagi beberapa menit kemudian."
	case errors.Is(err, arena.ErrLowMemory):
		return "Panel sedang kehabisan memori untuk browser. Coba lagi setelah proses lain selesai."
	case errors.Is(err, arena.ErrChromeOld):
		return "Chrome di panel terlalu tua untuk mode headless baru (butuh >= 112). Laporkan ke host panel."
	case errors.Is(err, arena.ErrChromeMissing), errors.Is(err, arena.ErrBrowser):
		return fmt.Sprintf("Browser gagal disiapkan (%v). Cek log bot.", err)
	case errors.Is(err, arena.ErrModelUnknown):
		return fmt.Sprintf("Model tidak dikenal: %v", err)
	case errors.Is(err, arena.ErrModelFailed):
		return "arena.ai menerima permintaan tapi modelnya gagal membuat gambar. Coba lagi sebentar lagi."
	case errors.Is(err, context.DeadlineExceeded):
		return "Waktu habis — arena.ai tidak menyelesaikan gambar. Coba lagi."
	default:
		return fmt.Sprintf("Gagal membuat gambar (%v).", err)
	}
}

// handleArenaCookie menyimpan cookie baru dari owner (dipanggil dari
// .seedream cookie <cookie>); raw kosong berarti owner minta status sesi.
//
// Cookie tidak pernah dikembalikan ke chat (sama sekali) — hanya jumlah
// entri yang tersimpan, supaya pesan sukses tidak membocorkan sesi login ke
// riwayat grup.
func handleArenaCookie(ctx context.Context, mctx *store.MessageContext, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return store.ReplyText(ctx, mctx, arenaCookieStatusBlock())
	}
	if err := arena.WriteCookie(raw); err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Cookie", err.Error()))
	}
	rows := []string{
		Row("Status", "tersimpan"),
		Row("Entri", strconv.Itoa(strings.Count(raw, "="))),
	}
	// Langsung tampilkan masa berlaku supaya owner tahu kapan harus
	// memperbarui lagi (access token arena.ai ~1 jam).
	if st, serr := arena.CookieStatusOf(); serr == nil && st.Parsed && !st.ExpiresAt.IsZero() {
		rows = append(rows,
			Row("Berakhir", expiryWhen(st.ExpiresAt)),
			Row("Sisa", durasiRingkas(time.Until(st.ExpiresAt))))
	}
	rows = append(rows, Row("Model", arenaModelName()))
	return store.ReplyText(ctx, mctx, Block("Cookie", rows...))
}

// arenaCookieStatusBlock menampilkan status sesi tersimpan tanpa membocorkan
// token: kapan access token kedaluwarsa dan model yang dipakai. Tanpa
// argumen, .seedream cookie jadi alat diagnosa, bukan hanya cara menyetel
// cookie.
func arenaCookieStatusBlock() string {
	st, err := arena.CookieStatusOf()
	if err != nil {
		return Block("Cookie",
			Row("Status", "belum ada"),
			Row("Cara setel", ".seedream cookie <cookie>"),
			Row("Ambil dari", "DevTools > Network > request create-evaluation > header cookie"),
			Row("Wajib memuat", "arena-auth-prod-v1"),
		)
	}

	status := "aktif"
	switch {
	case st.Parsed && st.Expired():
		status = "kedaluwarsa"
	case !st.Parsed:
		status = "tersimpan (masa berlaku tak terbaca)"
	}

	rows := []string{Row("Status", status)}
	if st.Parsed && !st.ExpiresAt.IsZero() {
		rows = append(rows,
			Row("Berakhir", expiryWhen(st.ExpiresAt)),
			Row("Sisa", durasiRingkas(time.Until(st.ExpiresAt))))
	}
	rows = append(rows, Row("Model", arenaModelName()))
	// Model edit hanya relevan kalau memang berbeda dari model generate;
	// kalau sama, dua baris model cuma mengulang informasi yang sama.
	if edit := arenaEditModelName(); edit != arenaModelName() {
		rows = append(rows, Row("Model edit", edit))
	}
	return Block("Cookie", rows...)
}

// expiryWhen menampilkan satu waktu dalam zona aktif bot (BOT_TIMEZONE),
// lengkap dengan nama zona supaya tidak ambigu — panel menjalankan proses di
// UTC, jadi time.Local akan menampilkan jam yang berbeda 7-9 jam dari WIB/WITA/WIT.
// Sisa waktunya ditampilkan terpisah (baris "Sisa") supaya tanggalnya mudah
// dibaca dan tidak berdesakan dengan durasi.
func expiryWhen(t time.Time) string {
	return t.In(timex.Zone()).Format("02 Jan 15:04") + " " + timex.Name()
}

// durasiRingkas mengubah durasi jadi "3 jam 12 menit" / "58 menit" /
// "5 hari" untuk ditampilkan ke owner.
func durasiRingkas(d time.Duration) string {
	if d < 0 {
		d = -d
		return durasiRingkas(d) + " lalu"
	}
	if d < time.Minute {
		return "kurang dari 1 menit"
	}
	if d < time.Hour {
		return fmt.Sprintf("%d menit", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		h := int(d.Hours())
		m := int(d.Minutes()) - h*60
		if m == 0 {
			return fmt.Sprintf("%d jam", h)
		}
		return fmt.Sprintf("%d jam %d menit", h, m)
	}
	return fmt.Sprintf("%d hari", int(d.Hours()/24))
}

// arenaModelName hanya untuk tampilan bantuan.
func arenaModelName() string {
	if v := strings.TrimSpace(os.Getenv("ARENA_MODEL")); v != "" {
		return v
	}
	return "seedream-5.0-pro"
}

// seedreamTimeout membaca ARENA_TIMEOUT (detik atau durasi Go).
func seedreamTimeout() time.Duration {
	if d := envSeconds("ARENA_TIMEOUT"); d > 0 {
		return d
	}
	return seedreamDefaultTimeout
}

// seedreamDailyLimit membaca ARENA_MAX_DAILY.
func seedreamDailyLimit() int {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("ARENA_MAX_DAILY"))); err == nil && n > 0 {
		return n
	}
	return seedreamDefaultDaily
}

// seedreamCooldown membaca ARENA_COOLDOWN_SECONDS.
func seedreamCooldown() time.Duration {
	if d := envSeconds("ARENA_COOLDOWN_SECONDS"); d > 0 {
		return d
	}
	return seedreamDefaultCooldown
}

// envSeconds membaca env angka (detik) atau durasi Go ("2m").
func envSeconds(key string) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return 0
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d
	}
	if n, err := strconv.Atoi(raw); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	return 0
}
