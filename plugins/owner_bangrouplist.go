package plugins

import (
	"context"
	"fmt"
	"strings"

	"wabot-go/internal/store"

	"github.com/polymorfa/hypermeow/types"
)

func init() {
	registerPlugin(RegisterBanGroupList)
}

func RegisterBanGroupList(r *Registry) {
	r.Register(Command{
		Name:        "bangrouplist",
		Description: "Daftar grup di-ban",
		Category:    CategoryOwner,
		Handler:     handleBanGroupList,
	})
}

func handleBanGroupList(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	list, err := store.GroupsFor(mctx.Client).ListBannedGroups(ctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, Fail("Gagal baca daftar blokir grup", err))
	}
	if len(list) == 0 {
		return store.ReplyText(ctx, mctx, "Daftar blokir grup kosong.")
	}
	// Tampilkan NAMA grup (bukan JID) supaya langsung dikenali owner.
	// Nama diambil live via GetGroupInfo; kalau gagal (mis. bot sudah keluar
	// dari grup itu) fallback ke JID mentah — daftar tetap lengkap, cuma
	// satu baris yang tampil sebagai ID. Fail-soft per baris supaya satu
	// grup yang error tidak menggagalkan seluruh daftar.
	var sb strings.Builder
	sb.WriteString(Title("Daftar blokir grup") + "\n\n")
	for _, raw := range list {
		name := groupNameOrFallback(ctx, mctx, raw)
		sb.WriteString(fmt.Sprintf("- %s\n", name))
	}
	return store.ReplyText(ctx, mctx, strings.TrimRight(sb.String(), "\n"))
}

// groupNameOrFallback mengambil nama grup dari WhatsApp untuk ditampilkan di
// daftar ban. Mengembalikan JID mentah kalau JID tidak valid atau info grup
// tidak bisa diambil.
func groupNameOrFallback(ctx context.Context, mctx *store.MessageContext, raw string) string {
	gjid, err := types.ParseJID(raw)
	if err != nil || gjid.IsEmpty() {
		return raw
	}
	info, err := mctx.Client.GetGroupInfo(ctx, gjid)
	if err != nil || info.Name == "" {
		return raw
	}
	return info.Name
}
