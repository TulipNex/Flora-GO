package plugins

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"strings"

	"wabot-go/internal/external"
	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterStockDownloaders)
}

// steptodownCmd merangkum satu command unduh stok berbasis steptodown.com.
type steptodownCmd struct {
	name    string   // nama command tanpa prefix, mis. "getty"
	aliases []string // alias opsional
	tool    string   // path tool di steptodown, mis. "getty-images-downloader"
	label   string   // judul caption, mis. "Getty Images"
	hint    string   // petunjuk argumen untuk pesan bantuan
	example string   // contoh argumen
	key     string   // label baris identitas di caption ("ID"/"URL"); default "Input"
	// resolve menormalkan+memvalidasi input; default: trim, wajib non-kosong.
	resolve func(raw string) (string, error)
}

// RegisterStockDownloaders mendaftarkan command unduh gambar dari situs stok
// lain lewat perantara steptodown.com (kategori Unduh, semua user):
//
//	.getty  <url halaman Getty>
//	.istock <url halaman iStock>
//
// Keduanya memakai backend sama dengan .shutterstock (get.php ->
// images/steptodown.comNNN.jpg), jadi cukup satu factory handler. Wajib URL
// halaman penuh — ID mentah tidak dikenali server untuk Getty/iStock.
func RegisterStockDownloaders(r *Registry) {
	registerSteptodown(r, steptodownCmd{
		name: "getty", tool: "getty-images-downloader", label: "Getty Images",
		hint: "<url>", key: "URL",
		example: "https://www.gettyimages.com/detail/photo/...",
	})
	registerSteptodown(r, steptodownCmd{
		name: "istock", tool: "istock-downloader", label: "iStock",
		hint: "<url>", key: "URL",
		example: "https://www.istockphoto.com/photo/...",
	})
}

// registerSteptodown mendaftarkan satu command stok dari deskriptor.
func registerSteptodown(r *Registry, c steptodownCmd) {
	r.Register(Command{
		Name:        c.name,
		Aliases:     c.aliases,
		Description: fmt.Sprintf("Gambar %s (.%s %s)", c.label, c.name, c.hint),
		Category:    CategoryUnduh,
		Handler:     func(ctx context.Context, mctx *store.MessageContext) error { return handleSteptodown(ctx, mctx, c) },
	})
}

// handleSteptodown menjalankan unduhan generik: resolve input -> async unduh
// + upload (antrean dlSem yang sama dengan downloader) -> kirim sebagai gambar.
func handleSteptodown(ctx context.Context, mctx *store.MessageContext, c steptodownCmd) error {
	if len(mctx.Args) == 0 {
		return store.ReplyText(ctx, mctx, Help(HelpSpec{
			Title:   "Unduh gambar " + c.label,
			Cmd:     c.name,
			Formats: []string{"." + c.name + " " + c.hint},
			Contoh:  "." + c.name + " " + c.example,
		}))
	}
	input, err := resolveStockInput(c, mctx.Args[0])
	if err != nil {
		return store.ReplyText(ctx, mctx, Sentence(err.Error()))
	}

	go func() {
		bg := context.Background()
		select {
		case dlSem <- struct{}{}:
			defer func() { <-dlSem }()
		default:
			_ = store.ReplyText(bg, mctx, QueueFullMsg())
			return
		}

		if _, err := sendDlStatus(bg, mctx, "Mengunduh gambar "+c.label+"..."); err != nil {
			return
		}
		data, derr := external.SteptodownImage(bg, c.tool, input)
		if derr != nil {
			_ = store.ReplyText(bg, mctx, Sentence(derr.Error()))
			return
		}
		var lines []string
		if cfg, _, cerr := image.DecodeConfig(bytes.NewReader(data)); cerr == nil && cfg.Width > 0 {
			lines = append(lines, fmt.Sprintf("- Resolusi : %d x %d", cfg.Width, cfg.Height))
		}
		lines = append(lines, fmt.Sprintf("- Ukuran : %.1f MB", float64(len(data))/(1<<20)))
		caption := Title(c.label) + "\n\n" + Code(lines...)
		if serr := sendDlBytesImage(bg, mctx, data, caption); serr != nil {
			_ = store.ReplyText(bg, mctx, Fail("Gagal mengirim gambar", serr))
		}
	}()
	return nil
}

// resolveStockInput menerapkan resolve kustom command bila ada; default
// mengembalikan input ter-trim dan menolak yang kosong.
func resolveStockInput(c steptodownCmd, raw string) (string, error) {
	if c.resolve != nil {
		return c.resolve(raw)
	}
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", fmt.Errorf("argumen kosong")
	}
	return s, nil
}
