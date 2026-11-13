package plugins

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"wabot-go/internal/store"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/types/events"
)

func init() {
	registerPlugin(RegisterPlayground)
}

// RegisterPlayground mendaftarkan dua command playground:
//   - `.playground [bagian]` — referensi semua elemen script (kategori Dev).
//   - `.send` — parse + kirim SATU bubble dari script yang di-reply.
//
// State-nya adalah pesan script itu sendiri, jadi tidak ada draft/sesi yang
// perlu disimpan.
func RegisterPlayground(r *Registry) {
	r.Register(Command{
		Name:        "playground",
		Description: "Referensi script bubble playground (semua elemen)",
		Category:    CategoryDev,
		Handler:     handlePlayground,
	})
	r.Register(Command{
		Name:        "send",
		Description: "Kirim satu bubble dari script #playground (reply script)",
		Category:    CategoryDev,
		Handler:     handlePlaySend,
	})
}

func handlePlayground(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	part := ""
	if len(mctx.Args) > 0 {
		part = strings.ToLower(strings.TrimSpace(mctx.Args[0]))
	}
	return store.ReplyText(ctx, mctx, playUsage(part))
}

func handlePlaySend(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	if mctx.RawEvt == nil {
		return store.ReplyText(ctx, mctx, playSendHint())
	}

	quoted := store.UnwrapMessage(store.ExtractQuotedMessage(mctx.RawEvt))
	if quoted == nil {
		return store.ReplyText(ctx, mctx, playSendHint())
	}
	script := store.MessageText(quoted)
	if strings.TrimSpace(script) == "" {
		return store.ReplyText(ctx, mctx, ErrMsg("Playground", "pesan yang di-reply bukan teks script; kirim script sebagai teks biasa dulu."))
	}
	if n := utf8.RuneCountInString(script); n > playMaxScriptRunes {
		return store.ReplyText(ctx, mctx, ErrMsg("Playground", fmt.Sprintf("script %d rune, maks %d.", n, playMaxScriptRunes)))
	}

	spec, errs := playParse(script)
	if len(errs) == 0 {
		errs = playValidate(&spec)
	}
	if len(errs) > 0 {
		_ = store.SendReaction(ctx, mctx, "❌")
		return store.ReplyText(ctx, mctx, playErrorText(errs))
	}

	msg, extra, err := playRender(ctx, mctx.Client, spec)
	if err != nil {
		_ = store.SendReaction(ctx, mctx, "❌")
		return store.ReplyText(ctx, mctx, ErrMsg("Playground", "gagal menyiapkan bubble: "+err.Error()))
	}

	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, msg, extra)
	if err != nil {
		_ = store.SendReaction(ctx, mctx, "❌")
		return store.ReplyText(ctx, mctx, ErrMsg("Playground", fmt.Sprintf("gagal kirim bubble: %v", err)))
	}

	_ = store.SendReaction(ctx, mctx, "✅")
	label := playLabel(spec)
	store.LogOutgoing(mctx, "playground", label)
	store.TrackBotMessageID(resp.ID, "playground "+label)
	return nil
}

// IsPlaygroundScript melaporkan apakah pesan ini script playground milik
// owner (baris pertama #playground). Dipanggil handler AI supaya isi script —
// yang bebas memuat kata "flora" di title/footer — tidak dijawab Flora.
func IsPlaygroundScript(evt *events.Message) bool {
	if evt == nil || !IsOwnerEvent(evt) {
		return false
	}
	msg := store.UnwrapMessage(evt.Message)
	if msg == nil {
		return false
	}
	return playHasMarker(store.MessageText(msg))
}

// CheckPlayTap mencegat tap tombol playground (id berawalan "play:").
// Reuse btnTestPayloadID agar semua bentuk respons tap tertangkap.
func CheckPlayTap(client *whatsmeow.Client, evt *events.Message, text string) bool {
	_ = text
	id, ok := btnTestPayloadID(evt)
	if !ok || !strings.HasPrefix(id, playTapPrefix) {
		return false
	}
	mctx := &store.MessageContext{Client: client, Chat: evt.Info.Chat, Sender: evt.Info.Sender, RawEvt: evt}
	_ = store.ReplyText(context.Background(), mctx, fmt.Sprintf("Tap diterima: %s", id))
	return true
}

// playSendHint ditampilkan saat `.send` tidak me-reply pesan script.
func playSendHint() string {
	return Block("Playground",
		"Reply ke pesan script (baris pertama "+playMarker+"), lalu kirim `.send`.",
		Hint("`.playground` untuk referensi elemen dan contoh script."),
	)
}

// playErrorText merangkum kesalahan parser/validator, dibatasi supaya satu
// script rusak tidak membalas ratusan baris.
func playErrorText(errs []string) string {
	const maxLines = 15
	if len(errs) > maxLines {
		rest := len(errs) - maxLines
		errs = append(errs[:maxLines:maxLines], fmt.Sprintf("... dan %d kesalahan lain", rest))
	}
	rows := make([]string, 0, len(errs))
	for _, e := range errs {
		rows = append(rows, Bullet(e))
	}
	return Block("Script ditolak", rows...) +
		Section("Rujukan", Hint("`.playground` untuk daftar elemen dan contoh script."))
}

// playLabel dipakai untuk log/arsip: jenis bubble + judul bila ada.
func playLabel(sp playSpec) string {
	if sp.title != "" {
		return sp.kind + ": " + sp.title
	}
	return sp.kind
}

// playUsage merangkai blok referensi `.playground`. part kosong = semua.
func playUsage(part string) string {
	all := part == "" || part == "all" || part == "semua"

	var sb strings.Builder
	sb.WriteString(Block("Playground",
		"Susun satu bubble WhatsApp dari script, lalu reply script itu dengan `.send`.",
		Hint("Baris pertama script wajib "+playMarker),
	))

	if all {
		sb.WriteString(Section("Alur",
			Bullet("ketik script sebagai pesan biasa (contoh di bagian akhir)"),
			Bullet("reply pesan script, lalu kirim `.send`"),
			Bullet("`.playground tombol` / `carousel` / `a2ui` / `airich` / `event` untuk bagian tertentu"),
			Hint("Sukses = reaksi ✅ tanpa teks tambahan; gagal = ❌ + daftar kesalahan per baris."),
		))
	}

	if all || part == "umum" {
		sb.WriteString(Section("Umum",
			Opt("istilah", "`kunci: nilai` mengganti field · `+ elemen ...` menambah · `- elemen N` menghapus · `#` komentar"),
			"`kind: interactive` · interactive (default), airich, event, phone",
			"`title: ...` · judul header (maks 60 rune)",
			"`subtitle: ...` · subjudul header (maks 60)",
			"`body: ...` · isi bubble, wajib untuk interactive (maks 1024)",
			"`footer: ...` · teks kecil di bawah tombol (maks 60)",
			"`image: ...` · URL gambar header (http/https, maks 4 MB)",
			"`slot: buttons` · isi bubble interactive: `buttons` atau `carousel`",
		))
	}

	if all || part == "tombol" || part == "button" {
		sb.WriteString(Section("Tombol",
			"`+ button reply | Label | id` · balasan cepat (id opsional, otomatis play:N)",
			"`+ button url | Label | https://...`",
			"`+ button copy | Label | KODE` · salin ke clipboard",
			"`+ button call | Label | 62812...`",
			"`+ button location | Label` · minta lokasi pengguna",
			"`+ button signup`",
			"`+ button select | Judul` lalu `+ row | Judul | Deskripsi | id`",
			"`- button 2` · hapus tombol ke-2",
			Hint("Maks 10 tombol per bubble/kartu; label maks 20 rune."),
		))
	}

	if all || part == "carousel" || part == "kartu" {
		sb.WriteString(Section("Carousel",
			"`slot: carousel`",
			"`+ card | Judul | Isi`",
			"`+ button ...` setelah `+ card` menempel ke kartu terakhir",
			"`- card 1` · hapus kartu pertama",
			Hint("1-10 kartu. Tombol di luar kartu ditolak — tulis setelah kartunya."),
		))
	}

	if all || part == "a2ui" || part == "bloks" {
		sb.WriteString(Section("A2UI",
			"`+ a2ui heading | Teks`",
			"`+ a2ui text | Teks`",
			"`+ a2ui card | Judul | Deskripsi`",
			"`+ a2ui image | https://...`",
			"`- a2ui 1` · hapus elemen pertama",
			Hint("A2UI menempel di bubble interactive, bisa bersama tombol/carousel."),
		))
	}

	if all || part == "airich" {
		sb.WriteString(Section("Airich",
			"`kind: airich`",
			"`+ section heading | Teks`",
			"`+ section md | Teks markdown`",
			"`+ section image | https://...`",
			"`+ section link | Label | https://...` · tombol footer",
			"`+ section html | Judul | popioo` atau `soundtest` atau `url:https://...`",
			"`- section 1` · hapus elemen pertama",
			Hint("title/body otomatis jadi heading/markdown pertama; footer tidak dipakai."),
		))
	}

	if all || part == "event" || part == "lain" {
		sb.WriteString(Section("Event",
			"`kind: event` · undangan acara, wajib `title:`",
			"`image: https://...` · thumbnail acara",
			"`kind: phone` · minta nomor telepon, tanpa field lain",
		))
	}

	if all {
		sb.WriteString(Section("Contoh",
			"```",
			playMarker,
			"title: Promo Kopi",
			"body: Diskon 20% untuk semua menu hari ini.",
			"footer: Flora • Promo",
			"+ button url | Beli Sekarang | https://contoh.com/beli",
			"+ button reply | Tanya Admin",
			"```",
			Hint("Reply blok di atas (termasuk #playground), lalu kirim `.send`."),
		))
	}

	return sb.String()
}
