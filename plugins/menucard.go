package plugins

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"strings"
	"time"

	"wabot-go/internal/config"
	"wabot-go/internal/messaging"
	"wabot-go/internal/store"

	"github.com/polymorfa/hypermeow/proto/waE2E"
)

func init() {
	registerPlugin(RegisterMenuCard)
}

// RegisterMenuCard mendaftarkan command `.thumbnail` (alias `.thumb`) dan
// `.favicon` (alias `.fav`), kategori Owner + gate owner: ubah thumbnail &
// favicon kartu .menu TANPA edit .env/restart. Nilai tersimpan di
// bot_settings (tahan restart), prioritas di atas .env (lihat menu.go).
// Subcommand tiap command: gambar (kirim/reply), `<link>` set URL,
// `reset` kembali ke .env, polos tampilkan nilai aktif + sumbernya.
func RegisterMenuCard(r *Registry) {
	r.Register(Command{
		Name:        "thumbnail",
		Aliases:     []string{"thumb"},
		Description: "Set thumbnail kartu .menu (gambar / <link> / reset)",
		Category:    CategoryOwner,
		Handler:     handleThumbnailCmd,
	})
	r.Register(Command{
		Name:        "favicon",
		Aliases:     []string{"fav"},
		Description: "Set favicon kartu .menu (gambar / <link> / reset)",
		Category:    CategoryOwner,
		Handler:     handleFaviconCmd,
	})
}

// maxMenuAssetBytes membatasi gambar sumber yang disimpan ke bot_settings
// via .thumbnail/.favicon. Gambar WhatsApp biasanya jauh di bawah ini; batas
// mencegah DB membengkak karena kiriman iseng. Normalisasi (≤1024px /
// ≤512KB untuk PNG ber-alpha) terjadi saat kartu dikirim.
const maxMenuAssetBytes = 4 << 20 // 4MB

// menuAsset mengumpulkan fungsi + label satu aset kartu supaya thumbnail dan
// favicon memakai satu handler (hanya kunci DB & label yang beda).
type menuAsset struct {
	kind     string
	label    string
	getURL   func(context.Context) (string, error)
	setURL   func(context.Context, string) error
	getBlob  func(context.Context) ([]byte, error)
	setBlob  func(context.Context, []byte) error
	fallback string
}

func thumbAsset(mctx *store.MessageContext) menuAsset {
	return menuAsset{
		kind:     "thumbnail",
		label:    "Thumbnail",
		getURL:   store.GroupsFor(mctx.Client).GetMenuThumbURL,
		setURL:   store.GroupsFor(mctx.Client).SetMenuThumbURL,
		getBlob:  store.GroupsFor(mctx.Client).GetMenuThumbBlob,
		setBlob:  store.GroupsFor(mctx.Client).SetMenuThumbBlob,
		fallback: strings.TrimSpace(config.MenuThumbURL),
	}
}

func faviconAsset(mctx *store.MessageContext) menuAsset {
	return menuAsset{
		kind:     "favicon",
		label:    "Favicon",
		getURL:   store.GroupsFor(mctx.Client).GetMenuFaviconURL,
		setURL:   store.GroupsFor(mctx.Client).SetMenuFaviconURL,
		getBlob:  store.GroupsFor(mctx.Client).GetMenuFaviconBlob,
		setBlob:  store.GroupsFor(mctx.Client).SetMenuFaviconBlob,
		fallback: strings.TrimSpace(config.MenuFaviconURL),
	}
}

func handleThumbnailCmd(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	return handleMenuAssetCmd(ctx, mctx, thumbAsset(mctx))
}

func handleFaviconCmd(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	return handleMenuAssetCmd(ctx, mctx, faviconAsset(mctx))
}

// handleMenuAssetCmd logic bersama thumbnail & favicon. Urutan: argumen
// eksplisit (reset → URL → bantuan) dulu, baru gambar (kirim/reply), lalu
// status. Argumen menang atas gambar supaya reply gambar + ".thumbnail reset"
// tidak malah menyimpan gambar.
func handleMenuAssetCmd(ctx context.Context, mctx *store.MessageContext, a menuAsset) error {
	arg := ""
	if len(mctx.Args) > 0 {
		arg = strings.TrimSpace(mctx.Args[0])
	}

	// Jalur 1: reset — hapus blob + URL kustom → kembali ke .env.
	if strings.EqualFold(arg, "reset") {
		if err := a.setBlob(ctx, nil); err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal reset %s: %v", strings.ToLower(a.label), err))
		}
		if err := a.setURL(ctx, ""); err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal reset %s: %v", strings.ToLower(a.label), err))
		}
		return store.ReplyText(ctx, mctx, fmt.Sprintf("%s dikembalikan ke .env.", a.label))
	}

	// Jalur 2: URL gambar langsung, boleh DAFTAR dipisah koma (dicoba
	// berurutan saat .menu dikirim). Skema http/https wajib; HEAD 10 detik —
	// content-type non-image yang PASTI ditolak; HEAD gagal (host protektif)
	// tetap diterima supaya host ketat tidak terkunci, dengan catatan.
	// Nilai lama dipertahankan bila validasi gagal (fail-closed).
	if isHTTPURL(arg) {
		sources := messaging.SplitThumbSources(arg)
		var rejected []string
		uncertain := 0
		for _, src := range sources {
			ok, ct := checkImageURL(src)
			if ok {
				continue
			}
			if ct == "" {
				uncertain++
				continue
			}
			rejected = append(rejected, fmt.Sprintf("%s (content-type %s)", src, ct))
		}
		if len(rejected) > 0 {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Link bukan gambar: %s. Nilai lama dipertahankan.",
				strings.Join(rejected, "; ")))
		}
		note := ""
		if uncertain > 0 {
			note = fmt.Sprintf(" (catatan: %d link tidak menjawab HEAD, belum tentu gambar — cek via .menu)", uncertain)
		}
		// Gambar tersimpan dihapus supaya URL baru benar-benar dipakai.
		if err := a.setBlob(ctx, nil); err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal bersihkan gambar lama: %v", err))
		}
		if err := a.setURL(ctx, strings.Join(sources, ",")); err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal simpan %s: %v", strings.ToLower(a.label), err))
		}
		return store.ReplyText(ctx, mctx, fmt.Sprintf("%s diganti.%s Cek via .menu", a.label, note))
	}

	// Argumen lain yang bukan URL/reset: tunjukkan cara pakai (fail-closed,
	// nilai lama tidak disentuh).
	if arg != "" {
		return store.ReplyText(ctx, mctx, fmt.Sprintf(
			"Kirim gambar dengan caption .%s, atau .%s <link> (contoh: https://contoh/gambar.jpg), atau .%s reset",
			a.kind, a.kind, a.kind))
	}

	// Jalur 3: gambar dikirim langsung / di-reply. Disimpan apa adanya di
	// bot_settings supaya .menu tidak perlu HTTP sama sekali.
	if img, ok := menuAssetImage(mctx); ok {
		data, err := mctx.Client.Download(ctx, img)
		if err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal unduh gambar: %v", err))
		}
		if len(data) > maxMenuAssetBytes {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gambar terlalu besar (%d KB, maks %d KB). Kecilkan dulu.",
				len(data)/1024, maxMenuAssetBytes/1024))
		}
		cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
			return store.ReplyText(ctx, mctx, "Gambar tidak bisa dibaca (pakai JPG/PNG).")
		}
		if err := a.setBlob(ctx, data); err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal simpan %s: %v", strings.ToLower(a.label), err))
		}
		// URL lama dihapus supaya sumber aktif tidak ambigu (blob menang).
		if err := a.setURL(ctx, ""); err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal bersihkan URL lama: %v", err))
		}
		return store.ReplyText(ctx, mctx, fmt.Sprintf("%s disimpan dari gambar (%dx%d, %d KB). Cek via .menu",
			a.label, cfg.Width, cfg.Height, len(data)/1024))
	}

	// Jalur 4: tanpa argumen & tanpa gambar = tampilkan nilai aktif + sumber.
	blob, err := a.getBlob(ctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal baca %s: %v", strings.ToLower(a.label), err))
	}
	if len(blob) > 0 {
		return store.ReplyText(ctx, mctx, fmt.Sprintf(
			"%s aktif: gambar tersimpan (%d KB). Ganti: kirim gambar dengan caption .%s, atau .%s <link>. Reset: .%s reset",
			a.label, len(blob)/1024, a.kind, a.kind, a.kind))
	}
	current, err := a.getURL(ctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal baca %s: %v", strings.ToLower(a.label), err))
	}
	if current != "" {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("%s aktif: %s (kustom). Reset: .%s reset", a.label, current, a.kind))
	}
	if a.fallback == "" {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("%s aktif: default otomatis. Set: kirim gambar dengan caption .%s atau .%s <link>", a.label, a.kind, a.kind))
	}
	return store.ReplyText(ctx, mctx, fmt.Sprintf("%s aktif: %s (.env). Set: kirim gambar dengan caption .%s atau .%s <link>", a.label, a.fallback, a.kind, a.kind))
}

// isHTTPURL true kalau s adalah URL http(s) (dipakai untuk membedakan
// argumen URL dari argumen lain, tanpa parsing penuh).
func isHTTPURL(s string) bool {
	low := strings.ToLower(s)
	return strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://")
}

// menuAssetImage mengambil ImageMessage dari pesan yang dikirim langsung
// (caption .thumbnail/.favicon) atau dari pesan yang di-reply. Video sengaja
// tidak dipakai (bukan jalur thumbnail); unwrap lewat helper store supaya
// pesan sementara / sekali-lihat tetap terbaca.
func menuAssetImage(mctx *store.MessageContext) (*waE2E.ImageMessage, bool) {
	if msg := store.UnwrapMessage(mctx.RawEvt.Message); msg != nil {
		if img := msg.GetImageMessage(); img != nil {
			return img, true
		}
	}
	if quoted := store.UnwrapMessage(store.ExtractQuotedMessage(mctx.RawEvt)); quoted != nil {
		if img := quoted.GetImageMessage(); img != nil {
			return img, true
		}
	}
	return nil, false
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
