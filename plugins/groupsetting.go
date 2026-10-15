package plugins

import (
	"context"
	"fmt"
	"strings"

	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterGroupSetting)
}

// RegisterGroupSetting mendaftarkan command pengaturan grup:
// .setname, .setdesc, .open, .close, .linkgc
func RegisterGroupSetting(r *Registry) {
	r.Register(Command{
		Name:        "setname",
		Description: "Ganti nama grup, contoh: .setname Nama Baru",
		Category:    CategoryGrup,
		Handler:     handleSetName,
	})
	r.Register(Command{
		Name:        "setdesc",
		Description: "Ganti deskripsi grup, contoh: .setdesc Deskripsi baru",
		Category:    CategoryGrup,
		Handler:     handleSetDesc,
	})
	r.Register(Command{
		Name:        "open",
		Description: "Buka grup, semua member boleh kirim pesan",
		Category:    CategoryGrup,
		Handler:     handleOpen,
	})
	r.Register(Command{
		Name:        "close",
		Description: "Kunci grup, cuma admin yang boleh kirim pesan",
		Category:    CategoryGrup,
		Handler:     handleClose,
	})
	r.Register(Command{
		Name:        "linkgc",
		Description: "Ambil link invite grup saat ini",
		Category:    CategoryGrup,
		Handler:     handleLinkGC,
	})
}

func handleSetName(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireGroupAdminGate(ctx, mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	newName := strings.TrimSpace(strings.Join(mctx.Args, " "))
	if newName == "" {
		return store.ReplyText(ctx, mctx, Block("Setname", Row("Cara", ".setname <nama baru>"), Row("Contoh", ".setname Nama Grup Baru")))
	}

	if err := mctx.Client.SetGroupName(ctx, mctx.Chat, newName); err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Setname", fmt.Sprintf("Gagal ganti nama grup: %v", err)))
	}
	return store.ReplyText(ctx, mctx, Block("Setname", Row("Nama baru", newName)))
}

func handleSetDesc(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireGroupAdminGate(ctx, mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	newDesc := strings.TrimSpace(strings.Join(mctx.Args, " "))
	if newDesc == "" {
		return store.ReplyText(ctx, mctx, Block("Setdesc", Row("Cara", ".setdesc <deskripsi baru>"), Row("Contoh", ".setdesc Selamat datang di grup kami")))
	}

	if err := mctx.Client.SetGroupTopic(ctx, mctx.Chat, "", "", newDesc); err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Setdesc", fmt.Sprintf("Gagal ganti deskripsi grup: %v", err)))
	}
	return store.ReplyText(ctx, mctx, Block("Setdesc", Row("Status", "Deskripsi grup diperbarui")))
}

func handleOpen(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireGroupAdminGate(ctx, mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}

	if err := mctx.Client.SetGroupAnnounce(ctx, mctx.Chat, false); err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Open", fmt.Sprintf("Gagal membuka grup: %v", err)))
	}
	if err := store.GroupsFor(mctx.Client).SetClosed(ctx, mctx.Chat.String(), false); err != nil {
		// Aksi WhatsApp sudah berhasil, catatan lokal gagal — tetap lanjut,
		// tapi kabari supaya tahu status di sisi bot bisa jadi tidak sinkron.
		return store.ReplyText(ctx, mctx, ErrMsg("Open", fmt.Sprintf("Grup dibuka, tapi catatan lokal gagal disimpan: %v", err)))
	}
	return store.ReplyText(ctx, mctx, Block("Open", Row("Status", "Grup dibuka, semua member boleh kirim pesan")))
}

func handleClose(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireGroupAdminGate(ctx, mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}

	if err := mctx.Client.SetGroupAnnounce(ctx, mctx.Chat, true); err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Close", fmt.Sprintf("Gagal mengunci grup: %v", err)))
	}
	if err := store.GroupsFor(mctx.Client).SetClosed(ctx, mctx.Chat.String(), true); err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Close", fmt.Sprintf("Grup dikunci, tapi catatan lokal gagal disimpan: %v", err)))
	}
	return store.ReplyText(ctx, mctx, Block("Close", Row("Status", "Grup dikunci, cuma admin yang bisa kirim pesan")))
}

func handleLinkGC(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireGroupAdminGate(ctx, mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}

	code, err := mctx.Client.GetGroupInviteLink(ctx, mctx.Chat, false)
	if err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Link Grup", fmt.Sprintf("Gagal ambil link invite: %v", err)))
	}
	return store.ReplyText(ctx, mctx, Block("Link Grup", Row("Link", code)))
}
