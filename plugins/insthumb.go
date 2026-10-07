package plugins

import (
	"context"
	"fmt"
	"strings"

	"wabot-go/internal/scraper"
	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterInsThumb)
}

// RegisterInsThumb mendaftarkan command `.insthumb` (alias `.it`):
// inspeksi metadata kartu link sebuah URL (judul, deskripsi, thumbnail,
// favicon) — data mentah untuk merakit kartu maupun debugging.
// Eksekusi async karena fetch jaringan (pola .get/downloader).
func RegisterInsThumb(r *Registry) {
	r.Register(Command{
		Name:        "insthumb",
		Aliases:     []string{"it"},
		Description: "Lihat metadata link (.insthumb <url>)",
		Category:    CategoryOwner,
		Handler:     handleInsThumb,
	})
}

func handleInsThumb(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	rawURL := ""
	if len(mctx.Args) > 0 {
		rawURL = mctx.Args[0]
	} else {
		// Fallback: reply ke pesan berisi link (kartu link bot, pesan
		// user, caption) — ambil URL http pertama dari teks quoted.
		rawURL = insThumbQuotedURL(mctx)
	}
	if strings.TrimSpace(rawURL) == "" {
		return store.ReplyText(ctx, mctx, "Format: .insthumb <url>\nContoh: .insthumb https://example.com/artikel\nAtau reply pesan berisi link lalu .insthumb")
	}
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		rawURL = "https://" + rawURL
	}

	go func() {
		bg := context.Background()
		meta, err := scraper.FetchPageMeta(bg, rawURL)
		if err != nil {
			_ = store.ReplyText(bg, mctx, fmt.Sprintf("Gagal baca metadata: %v", err))
			return
		}
		_ = store.ReplyText(bg, mctx, formatInsThumb(rawURL, meta))
	}()
	return nil
}

// insThumbQuotedURL mengambil URL http pertama dari pesan yang di-reply
// (unwrap dulu supaya reply di grup ber-Pesan-Sementara tetap terbaca).
// "" kalau tidak ada reply / tidak mengandung link. Pola regex + trim
// tanda baca tepi reuse milik downloader (firstSupportedURL).
func insThumbQuotedURL(mctx *store.MessageContext) string {
	quoted := store.UnwrapMessage(store.ExtractQuotedMessage(mctx.RawEvt))
	if quoted == nil {
		return ""
	}
	var texts []string
	if s := strings.TrimSpace(quoted.GetConversation()); s != "" {
		texts = append(texts, s)
	}
	if ext := quoted.GetExtendedTextMessage(); ext != nil {
		if s := strings.TrimSpace(ext.GetText()); s != "" {
			texts = append(texts, s)
		}
	}
	if img := quoted.GetImageMessage(); img != nil {
		if s := strings.TrimSpace(img.GetCaption()); s != "" {
			texts = append(texts, s)
		}
	}
	for _, t := range texts {
		for _, raw := range urlRe.FindAllString(t, -1) {
			if u := strings.TrimRight(raw, ".,;:!?)'\""); u != "" {
				return u
			}
		}
	}
	return ""
}

// formatInsThumb merangkai 5 field yang diminta: link, title, desc,
// thumb url, favicon url. Field kosong ditandai "-" (bukan error) supaya
// user tahu apa yang tidak disediakan situs.
func formatInsThumb(rawURL string, meta *scraper.PageMeta) string {
	val := func(s string) string {
		if strings.TrimSpace(s) == "" {
			return "-"
		}
		return s
	}
	return fmt.Sprintf(
		"Link: %s\nTitle: %s\nDesc: %s\nThumb: %s\nFavicon: %s",
		rawURL,
		val(meta.Title),
		val(meta.Description),
		val(meta.ImageURL),
		val(meta.FaviconURL),
	)
}
