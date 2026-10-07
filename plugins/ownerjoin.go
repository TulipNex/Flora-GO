package plugins

import (
	"context"
	"fmt"
	"strings"

	"wabot-go/internal/logfmt"
	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterOwnerJoin)
}

// RegisterOwnerJoin mendaftarkan command `.join` dan `.leave` — keduanya
// kategori Owner (hanya owner, disembunyikan dari .menu non-owner).
func RegisterOwnerJoin(r *Registry) {
	r.Register(Command{
		Name:        "join",
		Description: "Masuk ke grup via link invite (.join <link>)",
		Category:    CategoryOwner,
		Handler:     handleJoin,
	})
	r.Register(Command{
		Name:        "leave",
		Description: "Keluar dari grup ini",
		Category:    CategoryOwner,
		Handler:     handleLeave,
	})
}

func handleJoin(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	raw := strings.TrimSpace(strings.Join(mctx.Args, ""))
	if raw == "" {
		return store.ReplyText(ctx, mctx, "Sertakan link invite grup, contoh: .join https://chat.whatsapp.com/XXXX")
	}

	// Terima link penuh atau kode mentah. inviteLinkRe (moderation.go)
	// menangkap kodenya dari URL; kalau tidak cocok, anggap argumennya
	// memang kode invite-nya langsung.
	code := raw
	if m := inviteLinkRe.FindStringSubmatch(raw); len(m) == 2 {
		code = m[1]
	}

	jid, err := mctx.Client.JoinGroupWithLink(ctx, code)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal masuk grup: %v", err))
	}
	return store.ReplyText(ctx, mctx, fmt.Sprintf("Berhasil masuk grup: %s", jid.String()))
}

func handleLeave(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	if !mctx.IsGroup() {
		return store.ReplyText(ctx, mctx, "Command ini hanya bisa dipakai di dalam grup yang mau ditinggalkan.")
	}

	// Balasan dikirim DULU karena setelah keluar bot tidak bisa kirim
	// apa-apa lagi ke grup ini. Kalau LeaveGroup-nya gagal, kegagalannya
	// hanya tercatat di console (owner bisa lihat + coba lagi).
	if err := store.ReplyText(ctx, mctx, "Baik, bot keluar dari grup ini."); err != nil {
		return err
	}
	if err := mctx.Client.LeaveGroup(ctx, mctx.Chat); err != nil {
		logfmt.Warn("Gagal keluar dari grup %s: %v\n", mctx.Chat, err)
	}
	return nil
}
