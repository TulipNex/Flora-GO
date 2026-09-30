package plugins

import (
	"context"
	"fmt"
	"strings"

	"wabot-go/internal/messaging"
	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterRichTest2)
}

// RegisterRichTest2 mendaftarkan command UJI SEMENTARA ".richtest2"
// (owner-only, tersembunyi): verifikasi perbaikan source/suggest/tip +
// regresi teks, setelah perbaikan dari referensi NIXCODE. DIHAPUS setelah
// vonis kedua terkumpul.
func RegisterRichTest2(r *Registry) {
	r.Register(Command{
		Name:        "richtest2",
		Description: "EKSPERIMEN SEMENTARA ronde 2 (akan dihapus)",
		Category:    CategoryOwner,
		Handler: func(ctx context.Context, mctx *store.MessageContext) error {
			if msg := requireOwner(mctx); msg != "" {
				return store.ReplyText(ctx, mctx, msg)
			}
			if len(mctx.Args) == 0 {
				return store.ReplyText(ctx, mctx,
					"Pakai: .richtest2 <source|suggest|tip|text> ...\n"+
						"source <icon> | <url> | <nama>\n"+
						"suggest <s1> | <s2>\n"+
						"tip <teks>\n"+
						"text <teks>")
			}
			sub := strings.ToLower(strings.TrimSpace(mctx.Args[0]))
			rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(mctx.RawArgs), mctx.Args[0]))
			segs := func() []string {
				var out []string
				for _, p := range strings.Split(rest, "|") {
					if t := strings.TrimSpace(p); t != "" {
						out = append(out, t)
					}
				}
				return out
			}()
			report := func(label string, id string, err error) error {
				if err != nil {
					return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal %s: %v", label, err))
				}
				return store.ReplyText(ctx, mctx, fmt.Sprintf("Terkirim %s (id %s). Lapor: tampil rich?", label, id))
			}
			switch sub {
			case "source":
				if len(segs) < 3 {
					return store.ReplyText(ctx, mctx, "Format: .richtest2 source <icon> | <url> | <nama>")
				}
				id, err := messaging.NewAIRichBuilder().SetTitle("Uji Rich").
					AddText("Sumber di bawah:").AddSource(segs[0], segs[1], segs[2]).Send(ctx, mctx)
				return report("r2-source", id, err)
			case "suggest":
				if len(segs) == 0 {
					return store.ReplyText(ctx, mctx, "Format: .richtest2 suggest <s1> | <s2>")
				}
				id, err := messaging.NewAIRichBuilder().SetTitle("Uji Rich").AddSuggest(segs).Send(ctx, mctx)
				return report("r2-suggest", id, err)
			case "tip":
				if rest == "" {
					return store.ReplyText(ctx, mctx, "Format: .richtest2 tip <teks>")
				}
				id, err := messaging.NewAIRichBuilder().SetTitle("Uji Rich").AddTip(rest).Send(ctx, mctx)
				return report("r2-tip", id, err)
			case "text":
				if rest == "" {
					return store.ReplyText(ctx, mctx, "Format: .richtest2 text <teks>")
				}
				id, err := messaging.NewAIRichBuilder().SetTitle("Uji Rich").AddText(rest).Send(ctx, mctx)
				return report("r2-text (regresi)", id, err)
			default:
				return store.ReplyText(ctx, mctx, "Subcommand: source/suggest/tip/text.")
			}
		},
	})
}
