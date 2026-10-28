package plugins

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"wabot-go/internal/logfmt"
	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterListGrup)
}

// RegisterListGrup mendaftarkan command ".listgrup" (alias: listgroup,
// listgc) — daftar grup bot beserta ID permanen. ID dipakai oleh .userdb sync.
func RegisterListGrup(r *Registry) {
	r.Register(Command{
		Name:        "listgrup",
		Aliases:     []string{"listgroup", "listgc"},
		Description: "Daftar grup bot + ID permanen (owner)",
		Category:    CategoryOwner,
		Handler:     handleListGrup,
	})
}

func handleListGrup(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	gdb := store.GroupsFor(mctx.Client)
	groups, err := mctx.Client.GetJoinedGroups(ctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal ambil daftar grup: %v", err))
	}
	if len(groups) == 0 {
		return store.ReplyText(ctx, mctx, "Bot belum join grup mana pun.")
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })

	present := make([]string, 0, len(groups))
	for _, g := range groups {
		name := g.Name
		if name == "" {
			name = "(tanpa nama)"
		}
		if _, err := gdb.UpsertGroupID(ctx, g.JID.String(), name); err != nil {
			logfmt.Warn("listgrup: gagal indeks %s: %v\n", g.JID.String(), err)
		}
		present = append(present, g.JID.String())
	}
	if err := gdb.MarkGroupIDsAbsent(ctx, present); err != nil {
		logfmt.Warn("listgrup: gagal tandai absent: %v\n", err)
	}

	ids, err := gdb.ListGroupIDs(ctx, true)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal baca indeks grup: %v", err))
	}

	var sb strings.Builder
	sb.WriteString("Bot telah bergabung di grup:")
	for _, e := range ids {
		name := e.Name
		if name == "" {
			name = "(tanpa nama)"
		}
		fmt.Fprintf(&sb, "\n[%03d] %s", e.Seq, name)
	}
	return store.ReplyText(ctx, mctx, sb.String())
}
