package plugins

import (
	"context"
	"fmt"

	"wabot-go/internal/store"
	"google.golang.org/protobuf/encoding/protojson"
)

func init() {
	registerPlugin(RegisterInsp)
}

func RegisterInsp(r *Registry) {
	r.Register(Command{
		Name:        "insp",
		Description: "Raw JSON pesan",
		Category:    CategoryOwner,
		Handler:     handleInsp,
	})
}

func handleInsp(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	row, src, err := resolveCRMTarget(mctx)
	if err != nil {
		if err.Error() == "usage" {
			var total int64
			if store.CRM() != nil {
				total = store.CRM().Count()
			}
			return store.ReplyText(ctx, mctx, crmUsage(total))
		}
		return store.ReplyText(ctx, mctx, err.Error())
	}
	decoded, derr := row.Message()
	if derr != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal decode %s: %v", src, derr))
	}
	payload, err := protojson.MarshalOptions{Multiline: true}.Marshal(decoded)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal marshal %s: %v", src, err))
	}
	// Bubble WA bukan tempat JSON raksasa (preceden maxGetTextChars .get):
	// kebesaran → dokumen + caption ringkas.
	if len(payload) > 20000 {
		tipe, _ := store.DescribeMessage(decoded)
		return crmSendDoc(ctx, mctx, fmt.Sprintf("insp-%s-%d.json", tipe, row.Rowid), "application/json",
			string(payload), fmt.Sprintf("```Rowid  :#%d \nTipe   : %s, \nUkuran : %d bytes```", row.Rowid, tipe, len(payload)))
	}
	return store.ReplyText(ctx, mctx, string(payload))
}
