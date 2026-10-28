package plugins

import (
	"context"
	"fmt"
	"strings"
	"time"

	"wabot-go/internal/logfmt"
	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterDevTest)
}

// devTestGap adalah jeda antar-bubble uji. Agregator mengirim ~11 pesan
// interaktif beruntun; jeda mencegah server menolak/menahan sebagai spam.
const devTestGap = 1200 * time.Millisecond

// RegisterDevTest mendaftarkan `.devtest` kategori Dev: menjalankan SEMUA
// varian uji tombol/kartu sekaligus dalam satu perintah (supaya tidak perlu
// memanggil .btntest/.booktest/.carouseltest satu per satu). Grup opsional:
// `btn` (btntest), `book` (booktest), `carousel` (carouseltest), atau
// `all`/kosong (semua). Reuse helper kirim tiap command — tidak ada perakitan
// proto yang diduplikasi di sini.
func RegisterDevTest(r *Registry) {
	r.Register(Command{
		Name:        "devtest",
		Description: "Uji SEMUA varian tombol/kartu (.devtest [all|btn|book|carousel])",
		Category:    CategoryDev,
		Handler:     handleDevTest,
	})
}

// devTestStep satu langkah uji: label ringkas + fungsi kirim (tanpa balasan).
type devTestStep struct {
	label string
	run   func(ctx context.Context, mctx *store.MessageContext) error
}

// devTestSteps menyusun daftar langkah untuk grup tertentu. Grup tak dikenal
// = semua langkah.
func devTestSteps(group string) []devTestStep {
	btn := []devTestStep{
		{"btntest native", func(ctx context.Context, mctx *store.MessageContext) error {
			return btnTestSendNative(ctx, mctx)
		}},
		{"btntest phone", func(ctx context.Context, mctx *store.MessageContext) error {
			return btnTestSend(ctx, mctx, btnTestPhoneBubble(), btnTestBizExtra(), "minta nomor")
		}},
		{"btntest signup", func(ctx context.Context, mctx *store.MessageContext) error {
			return btnTestSend(ctx, mctx, btnTestNativeWithExtra(
				btnTestNativeFlowButton("inapp_signup", "{}"),
			), btnTestBizExtra(), "bubble signup")
		}},
		{"btntest widget", func(ctx context.Context, mctx *store.MessageContext) error {
			return btnTestSend(ctx, mctx, btnTestWidgetBubble(), btnTestBizExtra(), "bubble widget")
		}},
	}

	book := []devTestStep{
		{"booktest penuh", func(ctx context.Context, mctx *store.MessageContext) error {
			return booktestSend(ctx, mctx, "", "Uji Booking Confirmation")
		}},
		{"booktest minimal", func(ctx context.Context, mctx *store.MessageContext) error {
			return booktestSend(ctx, mctx, "minimal", "Uji Booking Confirmation")
		}},
		{"booktest tanpa-kelola", func(ctx context.Context, mctx *store.MessageContext) error {
			return booktestSend(ctx, mctx, "tanpa-kelola", "Uji Booking Confirmation")
		}},
		{"booktest tanpa-peta", func(ctx context.Context, mctx *store.MessageContext) error {
			return booktestSend(ctx, mctx, "tanpa-peta", "Uji Booking Confirmation")
		}},
		{"booktest tanpa-kontak", func(ctx context.Context, mctx *store.MessageContext) error {
			return booktestSend(ctx, mctx, "tanpa-kontak", "Uji Booking Confirmation")
		}},
	}

	carousel := []devTestStep{
		{"carouseltest url", func(ctx context.Context, mctx *store.MessageContext) error {
			return carouselSend(ctx, mctx, "url", "Uji Carousel")
		}},
		{"carouseltest reply", func(ctx context.Context, mctx *store.MessageContext) error {
			return carouselSend(ctx, mctx, "reply", "Uji Carousel")
		}},
	}

	switch group {
	case "btn", "btntest":
		return btn
	case "book", "booktest":
		return book
	case "carousel":
		return carousel
	default:
		all := make([]devTestStep, 0, len(btn)+len(book)+len(carousel))
		all = append(all, btn...)
		all = append(all, book...)
		all = append(all, carousel...)
		return all
	}
}

func handleDevTest(ctx context.Context, mctx *store.MessageContext) error {
	group := ""
	if len(mctx.Args) > 0 {
		group = strings.ToLower(strings.TrimSpace(mctx.Args[0]))
	}
	switch group {
	case "", "all", "btn", "btntest", "book", "booktest", "carousel":
		// dikenali
	default:
		return store.ReplyText(ctx, mctx, "Bentuk: .devtest [all|btn|book|carousel]")
	}

	steps := devTestSteps(group)
	// Dijalankan di goroutine supaya handler pesan tak diblokir selama jeda
	// antar-bubble; hasil ringkasannya dikirim di akhir.
	go runDevTest(ctx, mctx, steps)
	return store.ReplyText(ctx, mctx, fmt.Sprintf(
		"Mengirim %d bubble uji berurutan (~%d detik). Cek tiap bubble di HP lalu laporkan yang tampil/hilang.",
		len(steps), int(devTestGap.Seconds())*len(steps)))
}

// runDevTest mengirim tiap langkah berurutan dengan jeda, lalu membalas
// ringkasan (checklist apa yang harus terlihat per bubble).
func runDevTest(ctx context.Context, mctx *store.MessageContext, steps []devTestStep) {
	var sent []string
	var failed []string
	for i, s := range steps {
		if i > 0 {
			time.Sleep(devTestGap)
		}
		if err := s.run(ctx, mctx); err != nil {
			logfmt.Error("devtest langkah %q gagal: %v\n", s.label, err)
			failed = append(failed, s.label)
			continue
		}
		sent = append(sent, s.label)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("*Selesai* — %d/%d bubble terkirim.", len(sent), len(steps)))
	if len(sent) > 0 {
		sb.WriteString("\n\n*Terkirim:*\n")
		for _, l := range sent {
			sb.WriteString("• " + l + "\n")
		}
	}
	if len(failed) > 0 {
		sb.WriteString("\n*Gagal:*\n")
		for _, l := range failed {
			sb.WriteString("• " + l + "\n")
		}
	}
	sb.WriteString("\nCatatan: bubble native/booking/carousel adalah InteractiveMessage; " +
		"phone = minta nomor; widget = BloksWidget. Laporkan mana yang tampil, tombolnya ada, dan tap terbalas.")
	_ = store.ReplyText(context.Background(), mctx, sb.String())
}
