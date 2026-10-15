package plugins

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterListGrup)
}

// RegisterListGrup mendaftarkan command ".listgrup" (alias: listgroup,
// listgc) — daftar grup yang diikuti bot beserta JID-nya. Dipakai owner
// untuk menyalin JID ke flag -g milik .spr/.sbpr/.sbprprofile agar command
// bisa dieksekusi dari PC tanpa muncul di grup.
func RegisterListGrup(r *Registry) {
	r.Register(Command{
		Name:        "listgrup",
		Aliases:     []string{"listgroup", "listgc"},
		Description: "Daftar grup bot + JID (owner, untuk flag -g)",
		Category:    CategoryOwner,
		Handler:     handleListGrup,
	})
}

func handleListGrup(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	groups, err := mctx.Client.GetJoinedGroups(ctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal ambil daftar grup: %v", err))
	}
	if len(groups) == 0 {
		return store.ReplyText(ctx, mctx, "Bot belum join grup mana pun.")
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })
	var sb strings.Builder
	fmt.Fprintf(&sb, "Grup bot (%d):", len(groups))
	for i, g := range groups {
		name := g.Name
		if name == "" {
			name = "(tanpa nama)"
		}
		fmt.Fprintf(&sb, "\n%d. %s\n   %s", i+1, name, g.JID.String())
	}
	return store.ReplyText(ctx, mctx, sb.String())
}
