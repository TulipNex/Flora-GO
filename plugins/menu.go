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
// Daftar dikelompokkan per Category (lihat CategoryXxx di registry.go) supaya
// user langsung paham fungsi tiap command tanpa harus menebak dari namanya.
func RegisterMenu(r *Registry) {
	r.Register(Command{
		Name:        "menu",
		Description: "Menampilkan daftar semua perintah",
		Category:    CategoryUmum,
		Handler: func(ctx context.Context, mctx *store.MessageContext) error {
			all := r.All()

			// Kelompokkan nama command per kategori, tiap kelompok di-sort
			// alfabet supaya urutan stabil walau map All() acak.
			grouped := make(map[string][]string, len(CategoryOrder))
			for name, cmd := range all {
				cat := cmd.Category
				if cat == "" {
					// Pengaman untuk plugin lama yang lupa isi Category:
					// jangan hilangkan dari menu, tampung ke Umum.
					cat = CategoryUmum
				}
				grouped[cat] = append(grouped[cat], name)
			}
			for _, names := range grouped {
				sort.Strings(names)
			}

			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("👋 Halo, saya *%s*!\n%s.\n\n", config.BotName, config.BotDescription))
			sb.WriteString("📋 *Daftar Perintah*\n")

			// Tampilkan kategori sesuai CategoryOrder dulu, supaya urutan
			// menu konsisten (Umum -> Grup -> Stiker -> Panggilan -> AI).
			for _, cat := range CategoryOrder {
				names := grouped[cat]
				if len(names) == 0 {
					continue
				}
				sb.WriteString(fmt.Sprintf("\n*%s*\n", cat))
				for _, name := range names {
					sb.WriteString(fmt.Sprintf("• %s%s\n", CommandPrefixForMenu, name))
				}
			}

			// Kalau ada kategori di luar CategoryOrder (mis. plugin baru
			// memakai string kategori sendiri tanpa update CategoryOrder),
			// tetap tampilkan di bawah supaya tidak ada command yang hilang.
			for cat, names := range grouped {
				if isKnownCategory(cat) {
					continue
				}
				sb.WriteString(fmt.Sprintf("\n*%s*\n", cat))
				for _, name := range names {
					sb.WriteString(fmt.Sprintf("• %s%s\n", CommandPrefixForMenu, name))
				}
			}

			return store.ReplyText(ctx, mctx, sb.String())
		},
	})
}

// isKnownCategory mengecek apakah kategori sudah ada di CategoryOrder.
func isKnownCategory(cat string) bool {
	for _, known := range CategoryOrder {
		if known == cat {
			return true
		}
	}
	return false
}

// CommandPrefixForMenu dipakai khusus di plugin ini supaya tidak ada
// dependency melingkar ke package handler (yang menyimpan prefix asli).
// Ubah nilainya kalau kamu mengubah CommandPrefix di internal/handler/handler.go.
const CommandPrefixForMenu = "."
