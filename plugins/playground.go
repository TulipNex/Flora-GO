package plugins

import (
	"context"
	"fmt"
	"strings"

	"wabot-go/internal/store"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/types/events"
)

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
func CheckPlayTap(client *whatsmeow.Client, evt *events.Message, text string) bool {
	_ = text
	id, ok := nativeFlowTapID(evt)
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
			Bullet("`.playground cta` / `carousel` / `a2ui` / `airich` / `event` untuk bagian tertentu"),
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
			"`footer: ...` · teks kecil di bawah CTA (maks 60)",
			"`image: ...` · URL gambar header (http/https, maks 4 MB)",
			"`slot: buttons` · isi bubble interactive: `buttons` atau `carousel`",
		))
	}

	if all || part == "cta" || part == "button" || part == "tombol" {
		sb.WriteString(Section("CTA",
			"`+ cta reply | Label | id` · balasan cepat (id opsional, otomatis play:N)",
			"`+ cta url | Label | https://...`",
			"`+ cta copy | Label | KODE` · salin ke clipboard",
			"`+ cta call | Label | 62812...`",
			"`+ cta location | Label` · minta lokasi pengguna",
			"`+ cta signup`",
			"`+ cta select | Judul` lalu `+ row | Judul | Deskripsi | id`",
			"`- cta 2` · hapus CTA ke-2",
			Hint("Maks 10 CTA per bubble/kartu; label & teks row maks 1000 rune."),
		))
	}

	if all || part == "carousel" || part == "kartu" {
		sb.WriteString(Section("Carousel",
			"`slot: carousel`",
			"`+ card | Judul | Isi`",
			"`+ cta ...` setelah `+ card` menempel ke kartu terakhir",
			"`- card 1` · hapus kartu pertama",
			Hint("1-10 kartu. CTA di luar kartu ditolak — tulis setelah kartunya."),
		))
	}

	if all || part == "a2ui" || part == "bloks" {
		sb.WriteString(Section("A2UI",
			"`+ a2ui heading | Teks`",
			"`+ a2ui text | Teks`",
			"`+ a2ui card | Judul | Deskripsi`",
			"`+ a2ui image | https://...`",
			"`- a2ui 1` · hapus elemen pertama",
			Hint("A2UI menempel di bubble interactive, bisa bersama CTA/carousel."),
		))
	}

	if all || part == "airich" {
		sb.WriteString(Section("Airich",
			"`kind: airich`",
			"`+ section heading | Teks`",
			"`+ section md | Teks markdown`",
			"`+ section code | go | func main() { ... }` · syntax highlighting",
			"`+ section table | Kolom1,Kolom2 | Baris1A,Baris1B | Baris2A,Baris2B`",
			"`+ section suggest | Saran 1 | Saran 2` · follow-up prompt chips",
			"`+ section tip | Teks informasi/bantuan`",
			"`+ section meta | Teks metadata kecil`",
			"`+ section imagine | https://... | GENERATING [atau READY] | status update`",
			"`+ section source | Judul | Subjudul | https://... | [icon_url]`",
			"`+ section image | https://...`",
			"`+ section link | Label | https://...` · tautan di footer",
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
			"+ cta url | Beli Sekarang | https://contoh.com/beli",
			"+ cta reply | Tanya Admin",
			"```",
			Hint("Reply blok di atas (termasuk #playground), lalu kirim `.send`."),
		))
	}

	return sb.String()
}
