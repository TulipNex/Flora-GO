package plugins

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"wabot-go/internal/config"
	"wabot-go/internal/messaging"
	"wabot-go/internal/store"
	"wabot-go/internal/timex"

	"github.com/polymorfa/hypermeow/types"
)

func init() {
	registerPlugin(RegisterMenu)
}

// Elemen kartu menu (link, judul, deskripsi, thumbnail, favicon) dibaca
// dari .env (MENU_LINK, MENU_TITLE, MENU_DESC, MENU_THUMB_URL,
// MENU_FAVICON_URL — lihat config.go). Kosong = default otomatis seperti
// di bawah, jadi bot tetap jalan tanpa setting .env tambahan.

// RegisterMenu mendaftarkan command ".menu" tiga mode (meniru model menu
// berkategori): tanpa argumen = sapaan + daftar KATEGORI saja; "all" =
// semua command semua kategori; "<kategori>" = command kategori itu saja.
func RegisterMenu(r *Registry) {
	r.Register(Command{
		Name:        "menu",
		Description: "Daftar perintah (.menu / .menu all / .menu <kategori>)",
		Category:    CategoryUmum,
		Handler: func(ctx context.Context, mctx *store.MessageContext) error {
			all := r.All()
			showOwner := IsOwner(mctx)

			// Kelompokkan nama command per kategori, tiap kelompok di-sort
			// alfabet supaya urutan stabil walau map All() acak.
			grouped := make(map[string][]string, len(CategoryOrder))
			for name, cmd := range all {
				// Command hidden (privat, mis. tool owner) tidak masuk menu
				// APAPUN — bahkan untuk owner sekalipun.
				if cmd.Hidden {
					continue
				}
				// Di clone (jadibot), command main-only disembunyikan supaya
				// menu tidak menawarkan yang bakal ditolak router.
				if cmd.CloneBlocked() && !store.IsMainClient(mctx.Client) {
					continue
				}
				cat := cmd.Category
				if cat == "" {
					// Pengaman untuk plugin lama yang lupa isi Category:
					// jangan hilangkan dari menu, tampung ke Umum.
					cat = CategoryUmum
				}
				if (cat == CategoryOwner || cat == CategoryDev) && !showOwner {
					continue
				}
				grouped[cat] = append(grouped[cat], name)
			}
			for _, names := range grouped {
				sort.Strings(names)
			}

			arg := ""
			if len(mctx.Args) > 0 {
				arg = strings.ToLower(strings.TrimSpace(mctx.Args[0]))
			}

			// Hitung command yang TERLIHAT pemanggil ini (kategori Owner
			// sudah difilter dari grouped untuk non-owner) — angka ini
			// yang dipakai deskripsi default supaya <n> selalu sama dengan
			// jumlah command yang benar-benar terbuka di tampilan ini.
			var body string
			shown := 0
			switch {
			case arg == "all":
				body = buildAllMenu(grouped)
				for _, names := range grouped {
					shown += len(names)
				}
			case arg != "":
				selected := matchCategory(arg)
				names, ok := grouped[selected]
				if !ok || len(names) == 0 {
					return store.ReplyText(ctx, mctx, fmt.Sprintf("Kategori *%s* tidak ditemukan.\nTersedia: %s", mctx.Args[0], availableCategories(grouped)))
				}
				body = buildSingleMenu(grouped, selected)
				shown = len(names)
			default:
				body = buildCategoryMenu(grouped)
				for _, names := range grouped {
					shown += len(names)
				}
			}

			// Menu dikirim sebagai kartu link + isi lengkap sebagai body —
			// meniru model menu berkartu. Tiap elemen kartu diambil dari
			// .env kalau diisi, kalau kosong pakai default otomatis:
			// link = wa.me nomor bot (fallback nomor owner), judul =
			// nama+versi bot, deskripsi = jumlah command TERLIHAT.
			pageURL := strings.TrimSpace(config.MenuLink)
			if pageURL == "" {
				botNum := config.PrimaryOwnerNumber()
				if id := mctx.Client.Store.ID; id != nil && id.ToNonAD().User != "" {
					botNum = id.ToNonAD().User
				}
				pageURL = "https://wa.me/" + botNum
			}
			title := strings.TrimSpace(config.MenuTitle)
			if title == "" {
				title = fmt.Sprintf("%s %s", config.BotName, config.BotVersion)
			}
			desc := strings.TrimSpace(config.MenuDesc)
			if desc == "" {
				desc = fmt.Sprintf("%d commands tersedia", shown)
			}
			greeting, mentions := greetingHeader(mctx)
			// Thumbnail & favicon: gambar tersimpan (.thumbnail dari kiriman
			// gambar) menang atas URL kustom DB, yang menang atas .env; gagal
			// baca DB = fallback .env (fail-soft, kartu tetap terkirim).
			thumbBytes, thumbURL := resolveMenuAsset(ctx, thumbAsset(mctx))
			faviconBytes, faviconURL := resolveMenuAsset(ctx, faviconAsset(mctx))
			if err := messaging.SendLinkCard(ctx, mctx, messaging.LinkCard{
				PageURL:      pageURL,
				Title:        title,
				Description:  desc,
				ThumbURL:     thumbURL,
				ThumbBytes:   thumbBytes,
				FaviconURL:   faviconURL,
				FaviconBytes: faviconBytes,
				Body:         greeting + "\n\n*Daftar Menu*\n" + body,
				Mentions:     mentions,
			}); err != nil {
				return store.ReplyText(ctx, mctx, err.Error())
			}
			return nil
		},
	})
}

// resolveMenuAsset mengembalikan sumber aset kartu .menu: byte lokal (gambar
// yang disimpan lewat .thumbnail/.favicon) menang atas URL kustom DB, yang
// menang atas .env. Gagal baca DB = fail-soft (pakai .env) supaya kartu tetap
// terkirim.
func resolveMenuAsset(ctx context.Context, a menuAsset) (blob []byte, urls string) {
	if b, err := a.getBlob(ctx); err == nil && len(b) > 0 {
		return b, ""
	}
	urls = a.fallback
	if custom, err := a.getURL(ctx); err == nil && custom != "" {
		urls = custom
	}
	return nil, urls
}

// greetingHeader menyusun sapaan + perkenalan, keduanya bold, dipisah satu
// baris kosong: "*Halo @<user>, selamat <periode>!*" lalu "*Saya <NamaBot>,
// <deskripsi>.*". Sambutan men-tag pengirim ASLI (bukan PushName): teks
// memakai "@<User>" dari JID pengirim yang sudah ToNonAD (di grup mode LID
// = "@<LID>") supaya pairing mention valid dan klien me-render-nya jadi
// nama kontak yang bisa diketuk. Periode tetap dari jam zona aktif (dayPeriod).
// Mengembalikan teks + daftar JID untuk field Mentions kartu link; kalau
// pengirim tak ter-resolve, fallback "kamu" TANPA mention agar pairing
// tidak rusak.
func greetingHeader(mctx *store.MessageContext) (string, []types.JID) {
	periode := dayPeriod(timex.Now())
	sender := mctx.Sender.ToNonAD()
	if sender.User == "" {
		return fmt.Sprintf("*Halo kamu, selamat %s!*\n\n*Saya %s, %s.*",
			periode, config.BotName, config.BotDescription), nil
	}
	return fmt.Sprintf("*Halo @%s, selamat %s!*\n\n*Saya %s, %s.*",
			sender.User, periode, config.BotName, config.BotDescription),
		[]types.JID{sender}
}

// matchCategory mencocokkan argumen ke nama kategori (case-insensitive).
// Mengembalikan string kosong kalau tidak ada yang cocok.
func matchCategory(arg string) string {
	for _, cat := range CategoryOrder {
		if strings.EqualFold(cat, arg) {
			return cat
		}
	}
	return ""
}

// availableCategories merangkai daftar kategori yang boleh dilihat pemanggil
// (kategori Owner sudah difilter sebelum fungsi ini dipanggil).
func availableCategories(grouped map[string][]string) string {
	var cats []string
	for _, cat := range CategoryOrder {
		if len(grouped[cat]) > 0 {
			cats = append(cats, cat)
		}
	}
	// Kategori di luar CategoryOrder (kalau ada) tetap disebut supaya tidak
	// ada yang tersembunyi.
	for cat, names := range grouped {
		if !isKnownCategory(cat) && len(names) > 0 {
			cats = append(cats, cat)
		}
	}
	return strings.Join(cats, ", ")
}

// buildCategoryMenu menyusun daftar KATEGORI saja (tanpa command) + petunjuk
// pemakaian — tampilan default `.menu`.
func buildCategoryMenu(grouped map[string][]string) string {
	var sb strings.Builder
	for _, cat := range CategoryOrder {
		if len(grouped[cat]) == 0 {
			continue
		}
		sb.WriteString(fmt.Sprintf("▸ *%s*\n", cat))
	}
	for cat, names := range grouped {
		if !isKnownCategory(cat) && len(names) > 0 {
			sb.WriteString(fmt.Sprintf("▸ *%s*\n", cat))
		}
	}
	sb.WriteString("\n.menu <kategori>\n.menu all")
	return sb.String()
}

// buildAllMenu menyusun SEMUA kategori + command-nya (`.menu all`).
func buildAllMenu(grouped map[string][]string) string {
	var sb strings.Builder
	for _, cat := range CategoryOrder {
		names := grouped[cat]
		if len(names) == 0 {
			continue
		}
		sb.WriteString(fmt.Sprintf("▾ *%s*\n", cat))
		for _, name := range names {
			sb.WriteString(fmt.Sprintf("  · %s%s\n", CommandPrefixForMenu, name))
		}
		sb.WriteString("\n")
	}
	// Loop kedua HANYA untuk kategori di luar CategoryOrder (mis. plugin
	// baru pakai string kategori sendiri) — kategori dikenal sudah tampil
	// di loop pertama, jangan diulang.
	for cat, names := range grouped {
		if isKnownCategory(cat) || len(names) == 0 {
			continue
		}
		sb.WriteString(fmt.Sprintf("▾ *%s*\n", cat))
		for _, name := range names {
			sb.WriteString(fmt.Sprintf("  · %s%s\n", CommandPrefixForMenu, name))
		}
		sb.WriteString("\n")
	}
	sb.WriteString(fmt.Sprintf("%d commands", countCommands(grouped)))
	return sb.String()
}

// buildSingleMenu menyusun SEMUA kategori dengan yang diminta DIBUKA di
// posisinya, sisanya tertutup (`.menu <kategori>`) — meniru tampilan contoh:
//
//	Daftar Menu
//	▸ Umum
//	▾ Grup
//	  · _.add_
//	  · _.antilink_
//	  · ... (total 19 command)
//	▸ Stiker
func buildSingleMenu(grouped map[string][]string, selected string) string {
	var sb strings.Builder
	writeCat := func(cat string, names []string) {
		if cat == selected {
			sb.WriteString(fmt.Sprintf("▾ *%s*\n", cat))
			for _, name := range names {
				sb.WriteString(fmt.Sprintf("  · _%s%s_\n", CommandPrefixForMenu, name))
			}
		} else {
			sb.WriteString(fmt.Sprintf("▸ *%s*\n", cat))
		}
	}
	for _, cat := range CategoryOrder {
		if len(grouped[cat]) == 0 {
			continue
		}
		writeCat(cat, grouped[cat])
	}
	for cat, names := range grouped {
		if !isKnownCategory(cat) && len(names) > 0 {
			writeCat(cat, names)
		}
	}
	return sb.String()
}

func countCommands(grouped map[string][]string) int {
	total := 0
	for _, names := range grouped {
		total += len(names)
	}
	return total
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
