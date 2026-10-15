package plugins

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"wabot-go/internal/config"
	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterMenuCard)
}

// RegisterMenuCard mendaftarkan command `.thumbnail` (alias `.thumb`) dan
// `.favicon` (alias `.fav`), kategori Owner + gate owner: ubah thumbnail &
// favicon kartu .menu TANPA edit .env/restart. Nilai tersimpan di
// bot_settings (tahan restart), prioritas di atas .env (lihat menu.go).
// Subcommand tiap command: `<link>` set, `reset` kembali ke .env, polos
// tampilkan nilai aktif + sumbernya (kustom/.env).
func RegisterMenuCard(r *Registry) {
	r.Register(Command{
		Name:        "thumbnail",
		Aliases:     []string{"thumb"},
		Description: "Set thumbnail kartu .menu (.thumbnail <link>/reset)",
		Category:    CategoryOwner,
		Handler:     handleThumbnailCmd,
	})
	r.Register(Command{
		Name:        "favicon",
		Aliases:     []string{"fav"},
		Description: "Set favicon kartu .menu (.favicon <link>/reset)",
		Category:    CategoryOwner,
		Handler:     handleFaviconCmd,
	})
}

func handleThumbnailCmd(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	return handleMenuAssetCmd(ctx, mctx, "thumbnail")
}

func handleFaviconCmd(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	return handleMenuAssetCmd(ctx, mctx, "favicon")
}

// handleMenuAssetCmd adalah logic bersama thumbnail & favicon — hanya nama
// kunci dan label yang beda.
func handleMenuAssetCmd(ctx context.Context, mctx *store.MessageContext, kind string) error {
	label := "Thumbnail"
	getter := store.GroupsFor(mctx.Client).GetMenuThumbURL
	setter := store.GroupsFor(mctx.Client).SetMenuThumbURL
	fallback := strings.TrimSpace(config.MenuThumbURL)
	if kind == "favicon" {
		label = "Favicon"
		getter = store.GroupsFor(mctx.Client).GetMenuFaviconURL
		setter = store.GroupsFor(mctx.Client).SetMenuFaviconURL
		fallback = strings.TrimSpace(config.MenuFaviconURL)
	}

	// Polos: tampilkan nilai aktif + sumbernya.
	if len(mctx.Args) == 0 {
		current, err := getter(ctx)
		if err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal baca %s: %v", strings.ToLower(label), err))
		}
		if current != "" {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("%s aktif: %s (kustom). Reset: .%s reset", label, current, strings.ToLower(label)))
		}
		if fallback == "" {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("%s aktif: default otomatis. Set: .%s <link>", label, strings.ToLower(label)))
		}
		return store.ReplyText(ctx, mctx, fmt.Sprintf("%s aktif: %s (.env). Set: .%s <link>", label, fallback, strings.ToLower(label)))
	}

	arg := strings.TrimSpace(mctx.Args[0])
	// Reset: hapus kustom → kembali ke .env.
	if strings.EqualFold(arg, "reset") {
		if err := setter(ctx, ""); err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal reset %s: %v", strings.ToLower(label), err))
		}
		return store.ReplyText(ctx, mctx, fmt.Sprintf("%s dikembalikan ke .env.", label))
	}

	// Validasi URL gambar langsung: skema http/https wajib; HEAD 10 detik —
	// content-type non-image yang PASTI ditolak; HEAD gagal (host protektif)
	// tetap diterima supaya host ketat tidak terkunci, dengan catatan.
	// Nilai lama dipertahankan bila validasi gagal (fail-closed).
	link := arg
	if !strings.HasPrefix(strings.ToLower(link), "http://") && !strings.HasPrefix(strings.ToLower(link), "https://") {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Link harus http(s), contoh: .%s https://contoh/gambar.jpg", strings.ToLower(label)))
	}
	note := ""
	if ok, ct := checkImageURL(link); !ok {
		if ct == "" {
			note = " (catatan: server tidak menjawab HEAD, belum tentu gambar — cek via .menu)"
		} else {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Link bukan gambar (content-type %s). Nilai lama dipertahankan.", ct))
		}
	}
	if err := setter(ctx, link); err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal simpan %s: %v", strings.ToLower(label), err))
	}
	return store.ReplyText(ctx, mctx, fmt.Sprintf("%s diganti.%s Cek via .menu", label, note))
}

// checkImageURL HEAD ke URL dan memastikan content-type image/*. Mengembalikan
// (true, "") bila ya; (false, contentType) bila pasti bukan gambar;
// (false, "") bila tak bisa dipastikan (network error/timeout).
func checkImageURL(rawURL string) (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, rawURL, nil)
	if err != nil {
		return false, ""
	}
	req.Header.Set("User-Agent", "wabot-go/1.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, ""
	}
	ct := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	if i := strings.Index(ct, ";"); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	if strings.HasPrefix(ct, "image/") {
		return true, ct
	}
	if ct == "" {
		return false, ""
	}
	return false, ct
}
