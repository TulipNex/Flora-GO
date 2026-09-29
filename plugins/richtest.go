package plugins

import (
	"context"
	"fmt"
	"strings"

	"wabot-go/internal/messaging"
	"wabot-go/internal/scraper"
	"wabot-go/internal/store"

	"go.mau.fi/whatsmeow"
)

func init() {
	registerPlugin(RegisterRichTest)
}

// RegisterRichTest mendaftarkan command UJI SEMENTARA ".richtest"
// (owner-only, kategori Owner = tersembunyi): satu subcommand per elemen
// builder rich & interaktif supaya tiap elemen punya pasangan ujinya.
// DIHAPUS setelah semua vonis terkumpul. Sintaks per subcommand ada di
// usageRichTest.
func RegisterRichTest(r *Registry) {
	r.Register(Command{
		Name:        "richtest",
		Description: "EKSPERIMEN SEMENTARA: uji tiap elemen builder (akan dihapus)",
		Category:    CategoryOwner,
		Handler:     handleRichTest,
	})
}

const usageRichTest = "Uji builder:\n" +
	".richtest text <teks>\n" +
	".richtest table <judul> | <kol..> | <baris..>\n" +
	".richtest code <lang> | <kode>\n" +
	".richtest source <icon> | <url> | <nama>\n" +
	".richtest tip <teks>\n" +
	".richtest suggest <s1> | <s2>\n" +
	".richtest richmix\n" +
	".richtest buttons <teks> | <label>|<id> | url:<l>|<u> | call:<l>|<n> | copy:<l>|<k>\n" +
	".richtest select <teks> | <Section> | <row>|<id> | ...\n" +
	".richtest misc <teks>\n" +
	".richtest media <url> | <teks> | <label>|<id>\n" +
	".richtest legacy <teks> | <label>..\n" +
	".richtest carousel <url1> <url2>\n" +
	"Lapor tiap kiriman: tampil? tombol klik?"

func handleRichTest(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	if len(mctx.Args) == 0 {
		return store.ReplyText(ctx, mctx, usageRichTest)
	}
	sub := strings.ToLower(strings.TrimSpace(mctx.Args[0]))
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(mctx.RawArgs), mctx.Args[0]))
	segs := splitRichPipe(rest)

	report := func(label string, id string, err error) error {
		if err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal %s: %v", label, err))
		}
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Terkirim %s (id %s). Lapor: tampil? tombol klik?", label, id))
	}

	switch sub {
	case "text":
		if rest == "" {
			return store.ReplyText(ctx, mctx, usageRichTest)
		}
		id, err := messaging.NewAIRichBuilder().SetTitle("Uji Rich").AddText(rest).Send(ctx, mctx)
		return report("rich-text", id, err)
	case "table":
		if len(segs) < 3 {
			return store.ReplyText(ctx, mctx, "Format: .richtest table <judul> | <kol..> | <baris..>")
		}
		b := messaging.NewAIRichBuilder().SetTitle(segs[0])
		var table [][]string
		for _, s := range segs[1:] {
			table = append(table, splitRichComma(s))
		}
		id, err := b.AddTable(table).Send(ctx, mctx)
		return report("rich-table", id, err)
	case "code":
		lang, code := splitRichFirst(rest, "|")
		if strings.TrimSpace(code) == "" {
			return store.ReplyText(ctx, mctx, "Format: .richtest code <lang> | <kode>")
		}
		id, err := messaging.NewAIRichBuilder().SetTitle("Uji Rich").AddCode(strings.TrimSpace(lang), code).Send(ctx, mctx)
		return report("rich-code", id, err)
	case "source":
		if len(segs) < 3 {
			return store.ReplyText(ctx, mctx, "Format: .richtest source <icon> | <url> | <nama>")
		}
		id, err := messaging.NewAIRichBuilder().SetTitle("Uji Rich").
			AddText("Sumber di bawah:").AddSource(segs[0], segs[1], segs[2]).Send(ctx, mctx)
		return report("rich-source", id, err)
	case "tip":
		if rest == "" {
			return store.ReplyText(ctx, mctx, usageRichTest)
		}
		id, err := messaging.NewAIRichBuilder().SetTitle("Uji Rich").AddTip(rest).Send(ctx, mctx)
		return report("rich-tip", id, err)
	case "suggest":
		if len(segs) == 0 {
			return store.ReplyText(ctx, mctx, usageRichTest)
		}
		id, err := messaging.NewAIRichBuilder().SetTitle("Uji Rich").AddSuggest(segs).Send(ctx, mctx)
		return report("rich-suggest", id, err)
	case "richmix":
		id, err := messaging.NewAIRichBuilder().SetTitle("Uji Rich").
			AddText("*Demo* gabungan teks + tabel:").
			AddTable([][]string{{"Item", "Harga"}, {"Nasi goreng", "15000"}}).
			Send(ctx, mctx)
		return report("rich-mix", id, err)
	case "buttons":
		if len(segs) < 2 {
			return store.ReplyText(ctx, mctx, "Format: .richtest buttons <teks> | <label>|<id> | ...")
		}
		b := messaging.NewNativeFlowBuilder().SetBody(segs[0]).SetFooter("Uji tombol")
		for _, s := range segs[1:] {
			label, val := splitRichFirst(s, "|")
			switch {
			case strings.HasPrefix(label, "url:"):
				b.AddURL(strings.TrimSpace(strings.TrimPrefix(label, "url:")), val, false)
			case strings.HasPrefix(label, "call:"):
				b.AddCall(strings.TrimSpace(strings.TrimPrefix(label, "call:")), val)
			case strings.HasPrefix(label, "copy:"):
				b.AddCopy(strings.TrimSpace(strings.TrimPrefix(label, "copy:")), val)
			default:
				b.AddReply(label, val)
			}
		}
		id, err := b.Send(ctx, mctx)
		return report("btn-mixed", id, err)
	case "select":
		if len(segs) < 3 {
			return store.ReplyText(ctx, mctx, "Format: .richtest select <teks> | <Section> | <row>|<id> | ...")
		}
		var rows []messaging.SelectRow
		for _, s := range segs[2:] {
			title, id := splitRichFirst(s, "|")
			if strings.TrimSpace(title) == "" {
				continue
			}
			rows = append(rows, messaging.SelectRow{Header: segs[1], Title: title, ID: id})
		}
		if len(rows) == 0 {
			return store.ReplyText(ctx, mctx, "Minimal satu baris valid.")
		}
		id, err := messaging.NewNativeFlowBuilder().SetBody(segs[0]).
			AddSelection(segs[1], []messaging.SelectSection{{Title: segs[1], Rows: rows}}).Send(ctx, mctx)
		return report("btn-select", id, err)
	case "misc":
		if rest == "" {
			return store.ReplyText(ctx, mctx, usageRichTest)
		}
		id, err := messaging.NewNativeFlowBuilder().SetBody(rest).
			AddLocation().AddReminder("Ingatkan", "rem1").AddAddress("Kirim alamat", "addr1").Send(ctx, mctx)
		return report("btn-misc", id, err)
	case "media":
		if len(segs) < 3 {
			return store.ReplyText(ctx, mctx, "Format: .richtest media <url> | <teks> | <label>|<id>")
		}
		up, mime, err := uploadTestImage(ctx, mctx, segs[0])
		if err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal siapkan gambar: %v", err))
		}
		label, btnID := splitRichFirst(segs[2], "|")
		id, err := messaging.NewNativeFlowBuilder().SetBody(segs[1]).
			SetImageHeader(up, mime).AddReply(label, btnID).Send(ctx, mctx)
		return report("btn-media", id, err)
	case "legacy":
		if len(segs) < 2 {
			return store.ReplyText(ctx, mctx, "Format: .richtest legacy <teks> | <label>..")
		}
		b := messaging.NewLegacyButtonsBuilder().SetBody(segs[0]).SetFooter("Uji legacy")
		for _, s := range segs[1:] {
			b.AddButton(s, "")
		}
		id, err := b.Send(ctx, mctx)
		return report("btn-legacy", id, err)
	case "carousel":
		if len(mctx.Args) < 3 {
			return store.ReplyText(ctx, mctx, "Format: .richtest carousel <url1> <url2>")
		}
		cb := messaging.NewCarouselBuilder()
		for i, u := range mctx.Args[1:3] {
			up, mime, err := uploadTestImage(ctx, mctx, u)
			if err != nil {
				return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal siapkan gambar %d: %v", i+1, err))
			}
			card := messaging.NewCarouselCard().
				SetTitle(fmt.Sprintf("Kartu %d", i+1)).SetBody("Isi kartu").
				SetImage(up, mime).AddReply("Pilih", fmt.Sprintf("card-%d", i+1))
			if err := cb.AddCard(card); err != nil {
				return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal rakit kartu %d: %v", i+1, err))
			}
		}
		id, err := cb.Send(ctx, mctx)
		return report("carousel", id, err)
	default:
		return store.ReplyText(ctx, mctx, usageRichTest)
	}
}

// splitRichPipe memecah teks ber-pipe dengan baris baru utuh (dari RawArgs).
func splitRichPipe(s string) []string {
	var out []string
	for _, p := range strings.Split(s, "|") {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// splitRichComma memecah sel koma tabel.
func splitRichComma(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// splitRichFirst memecah pada pemisah PERTAMA (nilai boleh mengandungnya).
func splitRichFirst(s, sep string) (string, string) {
	if i := strings.Index(s, sep); i >= 0 {
		return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+len(sep):])
	}
	return strings.TrimSpace(s), ""
}

// uploadTestImage mengunduh gambar URL lalu menguploadnya untuk header
// media (uji buttons-media & carousel). Mengembalikan hasil upload + mime.
func uploadTestImage(ctx context.Context, mctx *store.MessageContext, rawURL string) (*whatsmeow.UploadResponse, string, error) {
	data, err := scraper.DownloadBytes(ctx, rawURL)
	if err != nil {
		return nil, "", err
	}
	mime := "image/jpeg"
	if len(data) > 3 && data[0] == 0x89 && data[1] == 'P' && data[2] == 'N' && data[3] == 'G' {
		mime = "image/png"
	}
	up, err := mctx.Client.Upload(ctx, data, whatsmeow.MediaImage)
	if err != nil {
		return nil, "", err
	}
	return &up, mime, nil
}
