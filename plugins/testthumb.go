package plugins

import (
	"context"
	"strings"

	"wabot-go/internal/messaging"
	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterTestThumb)
}

// testThumbImageURL adalah gambar uji untuk kartu link preview.
// URL ini langsung mengembalikan file gambar (bukan halaman HTML).
const testThumbImageURL = "https://imgh.in/host/a4nvq2"

// testThumbCardURL adalah URL yang ditulis di TEKS + MatchedText. Sengaja
// diberi query "?v=3" yang tidak mengubah isi gambar (host mengabaikannya)
// supaya klien WhatsApp menganggap ini kartu BARU — klien meng-cache
// preview per URL.
const testThumbCardURL = testThumbImageURL + "?v=3"

// RegisterTestThumb mendaftarkan command `.testthumb` — DEMO tipis untuk
// helper messaging.SendLinkCard. URL boleh dioverride:
// `.testthumb https://contoh/gambar.jpg`. Logika kartu yang terbukti jalan
// tinggal di internal/messaging, bukan di sini.
func RegisterTestThumb(r *Registry) {
	r.Register(Command{
		Name:        "testthumb",
		Description: "Uji kartu link preview dengan thumbnail (tes)",
		Category:    CategoryUmum,
		Handler: func(ctx context.Context, mctx *store.MessageContext) error {
			cardURL := testThumbCardURL
			for _, a := range mctx.Args {
				if strings.HasPrefix(a, "http") {
					cardURL = a
				}
			}
			if err := messaging.SendLinkCard(ctx, mctx, messaging.LinkCard{
				PageURL:     cardURL,
				Title:       "Flora GO — Tes Thumbnail",
				Description: "Kartu preview uji coba dengan thumbnail gambar.",
				ThumbURL:    cardURL,
			}); err != nil {
				return store.ReplyText(ctx, mctx, err.Error())
			}
			return nil
		},
	})
}
