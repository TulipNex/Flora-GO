package plugins

import (
	"context"
	"fmt"
	"strings"

	"wabot-go/internal/messaging/msgbuilder"
	"wabot-go/internal/store"

	whatsmeow "github.com/polymorfa/hypermeow"
)

func init() {
	registerPlugin(RegisterBuilderTest)
}

// RegisterBuilderTest mendaftarkan command `.buildertest` (alias btest):
// Menguji 4 tipe pesan MessageBuilder:
//  1. `.buildertest link`     -> Meta AI Rich dengan Inline Markdown Hyperlink
//  2. `.buildertest flow`     -> Native Flow Buttons (Quick Reply, Copy, URL)
//  3. `.buildertest carousel` -> Carousel geser horizontal dengan multi kartu
//  4. `.buildertest a2ui`     -> Meta Bloks A2UI dengan Card layout
func RegisterBuilderTest(r *Registry) {
	r.Register(Command{
		Name:        "buildertest",
		Aliases:     []string{"btest"},
		Description: "Uji coba komponen MessageBuilder (link|flow|carousel|a2ui)",
		Category:    CategoryDev,
		Handler:     handleBuilderTest,
	})
}

func handleBuilderTest(ctx context.Context, mctx *store.MessageContext) error {
	// Command uji pengembang: hanya owner.
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	sub := ""
	if len(mctx.Args) > 0 {
		sub = strings.ToLower(strings.TrimSpace(mctx.Args[0]))
	}

	switch sub {
	case "link", "hyperlink":
		return testBuilderHyperlink(ctx, mctx)
	case "flow", "button", "buttons":
		return testBuilderNativeFlow(ctx, mctx)
	case "carousel":
		return testBuilderCarousel(ctx, mctx)
	case "a2ui", "bloks":
		return testBuilderA2UI(ctx, mctx)
	default:
		menu := "*MessageBuilder Test Suite:*\n" +
			"• `.buildertest link` — Uji Inline Markdown Hyperlink Meta AI\n" +
			"• `.buildertest flow` — Uji Native Flow (Quick Reply, Copy, URL)\n" +
			"• `.buildertest carousel` — Uji Kartu Geser Horizontal (Carousel)\n" +
			"• `.buildertest a2ui` — Uji Meta Bloks A2UI Card Layout"
		return store.ReplyText(ctx, mctx, menu)
	}
}

// 1. Uji Hyperlink Meta AI
func testBuilderHyperlink(ctx context.Context, mctx *store.MessageContext) error {
	msg := msgbuilder.NewAIRich().
		AddHeading("# Portal Informasi Bot").
		AddMarkdownWithLinks(
			"Halo! Anda dapat mengunjungi [Google Search] untuk mencari informasi, membaca [Dokumentasi Go] untuk referensi pemrograman, atau bergabung ke [WhatsApp Channel] resmi kami.",
			msgbuilder.HyperlinkItem{
				Text:      "Google Search",
				URL:       "https://www.google.com",
				IsTrusted: true,
			},
			msgbuilder.HyperlinkItem{
				Text:      "Dokumentasi Go",
				URL:       "https://go.dev/doc/",
				IsTrusted: true,
			},
			msgbuilder.HyperlinkItem{
				Text:      "WhatsApp Channel",
				URL:       "https://whatsapp.com/channel/0029VbCV1ck8fewpdNb2TY2k",
				IsTrusted: true,
			},
		).
		AddFooterLinks(
			struct{ Label, URL string }{Label: "Kunjungi Situs", URL: "https://google.com"},
		).
		Build()

	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, msg, whatsmeow.SendRequestExtra{ID: whatsmeow.GenerateMessageID()})
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal kirim Hyperlink: %v", err))
	}
	store.LogOutgoing(mctx, "buildertest", "Hyperlink Test")
	store.TrackBotMessageID(resp.ID, "buildertest link")
	return nil
}

// 2. Uji Native Flow Buttons
func testBuilderNativeFlow(ctx context.Context, mctx *store.MessageContext) error {
	msg := msgbuilder.NewNativeFlow().
		SetTitle("Uji Native Flow MessageBuilder").
		SetSubtitle("Berbagai aksi tombol native").
		SetBody("Berikut adalah contoh tombol interaktif yang disusun melalui NativeFlowBuilder.").
		SetFooter("wabot-go • MessageBuilder").
		AddQuickReply("btest_qr_1", "Pilihan Satu").
		AddQuickReply("btest_qr_2", "Pilihan Dua").
		AddCopyButton("Salin Kode Promo", "FLORA-2026-DISCOUNT").
		AddURLButton("Buka Google", "https://www.google.com").
		Build()

	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, msg, whatsmeow.SendRequestExtra{ID: whatsmeow.GenerateMessageID()})
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal kirim Native Flow: %v", err))
	}
	store.LogOutgoing(mctx, "buildertest", "NativeFlow Test")
	store.TrackBotMessageID(resp.ID, "buildertest flow")
	return nil
}

// 3. Uji Carousel Multi-Card
func testBuilderCarousel(ctx context.Context, mctx *store.MessageContext) error {
	carousel := msgbuilder.NewCarousel().
		SetBody("Geser ke samping untuk melihat kartu produk:")

	card1 := msgbuilder.NewCard().
		SetTitle("Paket Basic").
		SetBody("Akses fitur standar bot 30 hari tanpa batas.").
		AddURLButton("Detail Basic", "https://google.com").
		AddQuickReply("order_basic", "Pilih Basic")

	card2 := msgbuilder.NewCard().
		SetTitle("Paket Premium").
		SetBody("Akses seluruh AI tools + generate image unlimited.").
		AddURLButton("Detail Premium", "https://google.com").
		AddQuickReply("order_premium", "Pilih Premium")

	card3 := msgbuilder.NewCard().
		SetTitle("Paket VIP").
		SetBody("Prioritas queue + grup support khusus owner.").
		AddURLButton("Detail VIP", "https://google.com").
		AddQuickReply("order_vip", "Pilih VIP")

	carousel.AddCard(card1).AddCard(card2).AddCard(card3)

	resp, err := carousel.Send(ctx, mctx.Client, mctx.Chat)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal kirim Carousel: %v", err))
	}
	store.LogOutgoing(mctx, "buildertest", "Carousel Test")
	store.TrackBotMessageID(resp.ID, "buildertest carousel")
	return nil
}

// 4. Uji A2UI Bloks Card
func testBuilderA2UI(ctx context.Context, mctx *store.MessageContext) error {
	msg := msgbuilder.NewA2UI().
		SetBody("Ringkasan Statistik Server").
		SetFooter("wabot-go • A2UI Bloks").
		AddHeading("h1_title", "Dashboard Status", "h1").
		AddCardBox("card_sys", "Sistem & Runtime", "Go 1.27 • Linux x86_64 • Uptime 99.9%").
		AddCardBox("card_memory", "Penggunaan Memori", "Alloc: 18.2 MB • RSS: 38.4 MB • GC Idle OK").
		AddURLButton("Buka Monitor", "https://google.com").
		Build()

	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, msg, whatsmeow.SendRequestExtra{ID: whatsmeow.GenerateMessageID()})
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal kirim A2UI: %v", err))
	}
	store.LogOutgoing(mctx, "buildertest", "A2UI Test")
	store.TrackBotMessageID(resp.ID, "buildertest a2ui")
	return nil
}
