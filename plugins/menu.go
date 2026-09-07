package plugins

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"wabot-go/internal/config"
	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterMenu)
}

// RegisterMenu mendaftarkan command ".menu" yang menampilkan daftar semua
// command lain secara otomatis (tidak perlu diupdate manual tiap nambah plugin).
func RegisterMenu(r *Registry) {
	r.Register(Command{
		Name:        "menu",
		Description: "Menampilkan daftar semua perintah",
		Handler: func(ctx context.Context, mctx *store.MessageContext) error {
			all := r.All()

			names := make([]string, 0, len(all))
			for name := range all {
				names = append(names, name)
			}
			sort.Strings(names)

			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("👋 Halo, saya *%s*!\n%s.\n\n", config.BotName, config.BotDescription))
			sb.WriteString("📋 *Daftar Perintah*\n\n")
			for _, name := range names {
				cmd := all[name]
				sb.WriteString(fmt.Sprintf("• %s%s\n", CommandPrefixForMenu, cmd.Name))
			}

			return store.ReplyText(ctx, mctx, sb.String())
		},
	})
}

// CommandPrefixForMenu dipakai khusus di plugin ini supaya tidak ada
// dependency melingkar ke package handler (yang menyimpan prefix asli).
// Ubah nilainya kalau kamu mengubah CommandPrefix di internal/handler/handler.go.
const CommandPrefixForMenu = "."
