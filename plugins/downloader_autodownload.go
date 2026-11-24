package plugins

import (
	"context"
	"strings"

	"wabot-go/internal/logfmt"
	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterAutodownload)
}

func RegisterAutodownload(r *Registry) {
	r.Register(Command{
		Name:        "autodownload",
		Description: "Download otomatis link medsos",
		Category:    CategoryUmum,
		Handler:     handleAutodownload,
	})
}

// autodownloadHelp merangkai balasan baku .autodownload (template B): status
// sekarang + cara mengubahnya.
func autodownloadHelp(on bool) string {
	status := "nonaktif"
	if on {
		status = "aktif"
	}
	return Help(HelpSpec{
		Title:   "Auto download",
		Status:  status,
		Cmd:     "Set",
		Formats: []string{".autodownload <state>"},
		Contoh:  ".autodownload off",
		Params: []string{
			"state : on atau off",
			"on    : aktif",
			"off   : nonaktif",
		},
	})
}

// handleAutodownload mengatur saklar autodownload per chat. Di grup hanya
// admin/owner yang boleh (pakai gate moderasi yang sama dengan antilink);
// di private chat pengetiknya = pemilik chat, jadi selalu lolos.
func handleAutodownload(ctx context.Context, mctx *store.MessageContext) error {
	if mctx.IsGroup() {
		if msg := requireModGate(ctx, mctx); msg != "" {
			return store.ReplyText(ctx, mctx, msg)
		}
	}
	arg := ""
	if len(mctx.Args) > 0 {
		arg = strings.ToLower(strings.TrimSpace(mctx.Args[0]))
	}
	switch arg {
	case "on", "aktif", "enable":
		if err := store.GroupsFor(mctx.Client).SetAutodownload(ctx, mctx.Chat.String(), true); err != nil {
			logfmt.Warn("autodownload: gagal menyalakan (%v)\n", err)
			return store.ReplyText(ctx, mctx, "Gagal menyalakan autodownload, coba lagi.")
		}
		return store.ReplyText(ctx, mctx, autodownloadHelp(true))
	case "off", "mati", "disable":
		if err := store.GroupsFor(mctx.Client).SetAutodownload(ctx, mctx.Chat.String(), false); err != nil {
			logfmt.Warn("autodownload: gagal mematikan (%v)\n", err)
			return store.ReplyText(ctx, mctx, "Gagal mematikan autodownload, coba lagi.")
		}
		return store.ReplyText(ctx, mctx, autodownloadHelp(false))
	default:
		on, err := store.GroupsFor(mctx.Client).IsAutodownloadOn(ctx, mctx.Chat.String())
		if err != nil {
			logfmt.Warn("autodownload: gagal baca status (%v)\n", err)
			return store.ReplyText(ctx, mctx, "Gagal membaca status autodownload, coba lagi.")
		}
		return store.ReplyText(ctx, mctx, autodownloadHelp(on))
	}
}
