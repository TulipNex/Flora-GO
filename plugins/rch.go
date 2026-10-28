package plugins

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"wabot-go/internal/logfmt"
	"wabot-go/internal/reactionwa"
	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterRCH)
}

// RegisterRCH mendaftarkan `.rch` (owner): kirim reaksi emoji ke satu pesan
// WhatsApp Channel lewat situs reactionwa.online, tanpa apikey. Login Google
// cukup sekali lewat `.rch login <refresh token>`; captcha Turnstile dibuat
// Chrome panel saat command dipakai.
func RegisterRCH(r *Registry) {
	r.Register(Command{
		Name:        "rch",
		Description: "Kirim reaksi ke pesan WhatsApp Channel",
		Category:    CategoryOwner,
		Handler:     handleRCH,
	})
}

// rchHelpBlock adalah petunjuk singkat .rch, tampil saat command dipanggil
// tanpa argumen yang bisa dipakai.
func rchHelpBlock() string {
	return Block("RCH",
		Opt("React", ".rch <link pesan> <emoji>"),
		Opt("Status", ".rch status"),
		Opt("Login", ".rch login <refresh token>"),
	)
}

func handleRCH(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}

	raw := strings.TrimSpace(mctx.RawArgs)
	first, rest := seedreamSplitFirst(raw)
	switch strings.ToLower(first) {
	case "":
		return store.ReplyText(ctx, mctx, rchHelpBlock())
	case "login", "token":
		return handleRCHLogin(ctx, mctx, strings.TrimSpace(rest))
	case "status":
		return handleRCHStatus(ctx, mctx)
	}

	// Sisanya: link pesan + emoji. Link wajib token pertama supaya emoji
	// yang mengandung spasi (jarang, tapi mungkin) tidak menggeser link.
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return store.ReplyText(ctx, mctx, rchHelpBlock())
	}
	link, emojis := fields[0], reactionwa.SplitEmojis(fields[1:])
	clean, err := reactionwa.NormalizeLink(link)
	if err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("RCH", rchErrorText(err)))
	}
	if len(emojis) == 0 {
		return store.ReplyText(ctx, mctx, ErrMsg("RCH", "Sertakan minimal satu emoji. Contoh: .rch "+clean+" 😭"))
	}
	if len(emojis) > reactionwa.MaxEmojis {
		return store.ReplyText(ctx, mctx, ErrMsg("RCH", fmt.Sprintf("Maksimal %d emoji per kirim.", reactionwa.MaxEmojis)))
	}
	if !reactionwa.HasToken() {
		return store.ReplyText(ctx, mctx, ErrMsg("RCH", "Sesi belum diatur. Login sekali dengan .rch login <refresh token>."))
	}

	// Membuat captcha + menunggu server bisa memakan puluhan detik; jalankan
	// di goroutine supaya event loop hypermeow tidak tertahan. Hasilnya yang
	// me-reply perintah, pesan tunggu dikirim tanpa quote.
	go func() {
		bg := context.Background()
		if err := store.SendText(bg, mctx, "Sedang mengirim reaksi, tunggu sebentar..."); err != nil {
			logfmt.Warn("rch tunggu: %v\n", err)
		}

		cctx, cancel := context.WithTimeout(bg, 4*time.Minute)
		defer cancel()

		res, err := reactionwa.Shared().React(cctx, clean, emojis)
		if err != nil {
			logfmt.Warn("rch: %v\n", err)
			_ = store.ReplyText(bg, mctx, ErrMsg("RCH", rchErrorText(err)))
			return
		}

		rows := []string{
			Row("Status", rchResultStatus(res)),
			Row("Emoji", strings.Join(emojis, " ")),
			Row("Link", clean),
		}
		if res.Task != nil && res.Task.ID != "" {
			rows = append(rows, Row("Tugas", res.Task.ID))
		}
		if res.VIP != nil && res.VIP.PackageName != "" {
			rows = append(rows, Row("Paket", res.VIP.PackageName))
			rows = append(rows, Row("Poin", fmt.Sprintf("%d", res.VIP.PointRemaining)))
		}
		_ = store.ReplyText(bg, mctx, Block("RCH", rows...))
	}()
	return nil
}

// handleRCHLogin menyimpan refresh token Firebase lalu memastikan token itu
// bisa dipakai. Nilai token tidak pernah di-echo; hanya akunnya yang tampil.
func handleRCHLogin(ctx context.Context, mctx *store.MessageContext, token string) error {
	if token == "" {
		return store.ReplyText(ctx, mctx, Block("RCH",
			Row("Sesi", rchSessionState()),
			Row("Cara setel", ".rch login <refresh token>"),
			Row("Ambil token", "DevTools > Console di halaman Free: baca IndexedDB firebaseLocalStorageDb > stsTokenManager.refreshToken"),
		))
	}

	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	acc, err := reactionwa.Shared().Login(cctx, token)
	if err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("RCH", rchErrorText(err)))
	}
	rows := []string{
		Row("Status", "tersimpan"),
	}
	if acc.Email != "" {
		rows = append(rows, Row("Akun", acc.Email))
	}
	return store.ReplyText(ctx, mctx, Block("RCH", rows...))
}

// handleRCHStatus menampilkan akun yang tersimpan + masa berlaku access token.
func handleRCHStatus(ctx context.Context, mctx *store.MessageContext) error {
	if !reactionwa.HasToken() {
		return store.ReplyText(ctx, mctx, Block("RCH",
			Row("Sesi", "belum diatur"),
			Row("Cara setel", ".rch login <refresh token>"),
		))
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	acc, err := reactionwa.Shared().Account(cctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("RCH", rchErrorText(err)))
	}
	rows := []string{Row("Sesi", "aktif")}
	if acc.Email != "" {
		rows = append(rows, Row("Akun", acc.Email))
	}
	if !acc.ExpiresAt.IsZero() {
		rows = append(rows, Row("Token", "aktif s/d "+expiryWhen(acc.ExpiresAt)))
	}
	rows = append(rows, Row("Captcha", "Chrome panel (saat mengirim)"))
	return store.ReplyText(ctx, mctx, Block("RCH", rows...))
}

func rchSessionState() string {
	if reactionwa.HasToken() {
		return "tersimpan (pakai .rch status untuk cek akun)"
	}
	return "belum diatur"
}

// rchResultStatus merangkum balasan server: pesan "masuk antrean" lebih
// informatif daripada sekadar "terkirim", karena reaksi diproses asinkron.
func rchResultStatus(res *reactionwa.Result) string {
	if res == nil {
		return "diproses"
	}
	msg := strings.TrimSpace(res.Message)
	if msg == "" {
		return "masuk antrean"
	}
	return msg
}

// rchErrorText menerjemahkan error client menjadi kalimat yang bisa
// ditindaklanjuti owner.
func rchErrorText(err error) string {
	switch {
	case errors.Is(err, reactionwa.ErrNoToken):
		return "Sesi belum diatur. Login sekali dengan .rch login <refresh token>."
	case errors.Is(err, reactionwa.ErrTokenRejected):
		return "Sesi ditolak/kedaluwarsa. Ambil refresh token baru dari browser, lalu kirim .rch login <refresh token>."
	case errors.Is(err, reactionwa.ErrLimit):
		return "Jatah reaksi habis atau terlalu sering. Coba lagi nanti."
	case errors.Is(err, reactionwa.ErrCaptcha):
		return "Verifikasi captcha gagal. Coba lagi sebentar; kalau terus gagal, pastikan Xvfb/Chrome siap di panel."
	case errors.Is(err, reactionwa.ErrLink):
		msg := strings.TrimSpace(strings.TrimPrefix(err.Error(), reactionwa.ErrLink.Error()+":"))
		if msg != "" && msg != reactionwa.ErrLink.Error() {
			return "Link tidak valid: " + msg
		}
		return "Link harus link pesan channel, bukan link profil (ada nomor pesan di belakang)."
	case errors.Is(err, reactionwa.ErrBrowser):
		return "Chrome captcha gagal disiapkan: " + err.Error()
	}
	return err.Error()
}
