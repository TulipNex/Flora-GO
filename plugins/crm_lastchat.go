package plugins

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterLastChat)
}

func RegisterLastChat(r *Registry) {
	r.Register(Command{
		Name:        "lastchat",
		Description: "Pesan terakhir chat",
		Category:    CategoryOwner,
		Handler:     handleLastChat,
	})
}

func handleLastChat(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	n := 10
	if len(mctx.Args) > 0 {
		if v, err := strconv.Atoi(strings.TrimSpace(mctx.Args[0])); err == nil {
			n = v
		}
	}
	if store.CRM() == nil {
		return store.ReplyText(ctx, mctx, "Arsip mati (crm.db tidak terbuka).")
	}
	rows, err := store.CRM().RecentByChat(mctx.Chat.String(), n)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal baca arsip: %v", err))
	}
	if len(rows) == 0 {
		return store.ReplyText(ctx, mctx, "Belum ada pesan terarsip di chat ini.")
	}
	// Terbaru-dulu dari DB → balik kronologis seperti tampilan chat.
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	// Resolve LID→PN di-cache per pengirim (bisa sampai 100 baris).
	labels := make(map[string]string)
	var sb strings.Builder
	sb.WriteString("*Pesan Terakhir*\n\n")
	for i, row := range rows {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		tipe, preview := "-", "-"
		if decoded, derr := row.Message(); derr == nil {
			tipe, preview = store.DescribeMessage(decoded)
		} else {
			preview = "(gagal decode)"
		}
		fmt.Fprintf(&sb, "`[%02d] #%d | %s`\n%s • %s\n",
			i+1, row.Rowid, formatCRMTime(row.TS),
			crmSenderCached(ctx, mctx.Client, row.Sender, labels),
			tipe)
		sb.WriteString(crmItalicLine(truncateCRMLine(preview, 150)))
	}
	return store.ReplyText(ctx, mctx, sb.String())
}
