package plugins

import (
	"context"
	"strings"

	"wabot-go/internal/logfmt"
	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterLeave)
}

func RegisterLeave(r *Registry) {
	r.Register(Command{
		Name:        "leave",
		Description: "Keluar dari grup ini",
		Category:    CategoryOwner,
		Handler:     handleLeave,
	})
}

func leaveUsage() string {
	return Help(HelpSpec{
		Title:   "Keluar grup",
		Cmd:     "Leave",
		Formats: []string{".leave [nomor]"},
		Contoh:  ".leave 001",
		Note:    "Nomor urut grup lihat di .listgrup. Di dalam grup bisa tanpa nomor.",
	})
}

func handleLeave(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}

	targetChat := mctx.Chat
	arg := strings.TrimSpace(strings.Join(mctx.Args, " "))
	if arg != "" {
		resolved, _, err := sprResolveGroupID(ctx, mctx, arg)
		if err != nil {
			return store.ReplyText(ctx, mctx, Fail("Grup tidak ditemukan", err))
		}
		targetChat = resolved
	} else if !mctx.IsGroup() {
		return store.ReplyText(ctx, mctx, leaveUsage())
	}

	if targetChat == mctx.Chat {
		if err := store.ReplyText(ctx, mctx, "Baik, bot keluar dari grup ini."); err != nil {
			return err
		}
	} else {
		if err := store.ReplyText(ctx, mctx, "Baik, bot keluar dari grup tersebut."); err != nil {
			return err
		}
	}

	if err := mctx.Client.LeaveGroup(ctx, targetChat); err != nil {
		logfmt.Warn("Gagal keluar dari grup %s: %v\n", targetChat, err)
	}
	return nil
}
