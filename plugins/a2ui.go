package plugins

import (
	"context"
	"encoding/json"
	"fmt"

	"wabot-go/internal/messaging"
	"wabot-go/internal/store"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func init() {
	registerPlugin(RegisterA2UI)
	registerPlugin(RegisterNoPanda)
}

// RegisterA2UI mendaftarkan command `.a2ui` (alias panda/bloks):
// Menampilkan kartu antarmuka kaya Meta Bloks / A2UI Portofolio Panda interaktif.
func RegisterA2UI(r *Registry) {
	r.Register(Command{
		Name:        "a2ui",
		Aliases:     []string{"panda", "bloks"},
		Description: "Tampilkan widget interaktif Meta Bloks / A2UI",
		Category:    CategoryDev,
		Handler:     handleA2UI,
	})
}

// RegisterNoPanda mendaftarkan command `.nopanda`:
// Menampilkan widget interaktif Meta Bloks / A2UI Portofolio Panda TANPA aset gambar
// dan dibungkus dalam Card container agar memiliki margin/padding dari tepi bubble chat.
func RegisterNoPanda(r *Registry) {
	r.Register(Command{
		Name:        "nopanda",
		Description: "Tampilkan widget Meta Bloks / A2UI dengan padding margin tanpa gambar",
		Category:    CategoryDev,
		Handler:     handleNoPanda,
	})
}

func handleA2UI(ctx context.Context, mctx *store.MessageContext) error {
	// Command uji pengembang: hanya owner (CategoryDev tidak ditegakkan router).
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	msg := buildPandaPortfolioMessage(true, false)
	extra := whatsmeow.SendRequestExtra{ID: whatsmeow.GenerateMessageID()}

	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, msg, extra)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal kirim A2UI Widget: %v", err))
	}

	store.LogOutgoing(mctx, "a2ui", "Panda Portfolio Widget")
	store.TrackBotMessageID(resp.ID, "a2ui panda")
	return nil
}

func handleNoPanda(ctx context.Context, mctx *store.MessageContext) error {
	// Command uji pengembang: hanya owner.
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	msg := buildPandaPortfolioMessage(false, true)
	extra := whatsmeow.SendRequestExtra{ID: whatsmeow.GenerateMessageID()}

	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, msg, extra)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal kirim NoPanda Widget: %v", err))
	}

	store.LogOutgoing(mctx, "nopanda", "NoPanda Widget (Padded Card)")
	store.TrackBotMessageID(resp.ID, "nopanda")
	return nil
}

func buildPandaPortfolioMessage(includeImage bool, withCardMargin bool) *waE2E.Message {
	if withCardMargin {
		return buildNoPandaCardPaddedMessage()
	}

	// Layout bawaan dengan gambar
	profileChildren := []string{"profile_image", "profile"}
	components := []map[string]any{
		{
			"id":        "root",
			"component": "Column",
			"children": []string{
				"portfolio_header",
				"profile_section",
				"about_section",
				"skills",
				"projects_section",
				"contact",
			},
		},
		{
			"id":        "portfolio_header",
			"component": "Text",
			"text":      "Portofolio Panda Raksasa",
			"variant":   "h1",
		},
		{
			"id":        "profile_section",
			"component": "Column",
			"children":  profileChildren,
		},
		{
			"id":        "profile_image",
			"component": "Image",
			"url":       "https://files.catbox.moe/ms64an.jpeg",
			"variant":   "header",
			"fit":       "none",
		},
		{
			"id":        "profile",
			"component": "Text",
			"text":      "Panda raksasa dengan spesialisasi bambu, tidur siang, dan menjadi hewan paling menggemaskan di hutan.",
			"variant":   "body",
		},
		{
			"id":        "about_section",
			"component": "Column",
			"children": []string{
				"about",
				"about_content",
			},
		},
		{
			"id":        "about",
			"component": "Text",
			"text":      "Tentang Saya",
			"variant":   "h2",
		},
		{
			"id":        "about_content",
			"component": "Text",
			"text":      "Saya adalah panda raksasa yang menghabiskan sebagian besar waktu mencari bambu, makan, beristirahat, dan menjalani kehidupan dengan santai. Keahlian utama saya adalah bertahan hidup dengan wajah menggemaskan.",
			"variant":   "body",
		},
		{
			"id":        "skills",
			"component": "Text",
			"text":      "Keahlian: Makan Bambu • Memanjat Pohon • Berguling • Tidur Siang • Menggemaskan",
			"variant":   "caption",
		},
		{
			"id":        "projects_section",
			"component": "Column",
			"children": []string{
				"projects",
				"project_1",
				"project_2",
				"project_3",
			},
		},
		{
			"id":        "projects",
			"component": "Text",
			"text":      "Aktivitas Pilihan",
			"variant":   "h2",
		},
		{
			"id":        "project_1",
			"component": "Card",
			"child":     "project_1_content",
		},
		{
			"id":        "project_1_content",
			"component": "Column",
			"children": []string{
				"project_1_title",
				"project_1_description",
			},
		},
		{
			"id":        "project_1_title",
			"component": "Text",
			"text":      "Operasi Bambu",
			"variant":   "h3",
		},
		{
			"id":        "project_1_description",
			"component": "Text",
			"text":      "Mencari, memilih, dan menghabiskan bambu berkualitas tinggi setiap hari.",
			"variant":   "body",
		},
		{
			"id":        "project_2",
			"component": "Card",
			"child":     "project_2_content",
		},
		{
			"id":        "project_2_content",
			"component": "Column",
			"children": []string{
				"project_2_title",
				"project_2_description",
			},
		},
		{
			"id":        "project_2_title",
			"component": "Text",
			"text":      "Program Tidur Siang",
			"variant":   "h3",
		},
		{
			"id":        "project_2_description",
			"component": "Text",
			"text":      "Mengoptimalkan energi dengan tidur di tempat paling nyaman setelah makan.",
			"variant":   "body",
		},
		{
			"id":        "project_3",
			"component": "Card",
			"child":     "project_3_content",
		},
		{
			"id":        "project_3_content",
			"component": "Column",
			"children": []string{
				"project_3_title",
				"project_3_description",
			},
		},
		{
			"id":        "project_3_title",
			"component": "Text",
			"text":      "Duta Kegemasan",
			"variant":   "h3",
		},
		{
			"id":        "project_3_description",
			"component": "Text",
			"text":      "Meningkatkan kebahagiaan pengunjung hanya dengan duduk, makan, dan menatap kamera.",
			"variant":   "body",
		},
		{
			"id":        "contact",
			"component": "Text",
			"text":      "Status: Aktif • Lokasi: Hutan Bambu • Spesialisasi: Bambu",
			"variant":   "caption",
		},
	}

	return assembleA2UIMessage(components)
}

// buildNoPandaCardPaddedMessage menyusun konten panda di mana teks dan section
// dibungkus di dalam Card container sehingga memiliki jarak margin & padding
// otomatis dari tepi bubble chat WhatsApp.
func buildNoPandaCardPaddedMessage() *waE2E.Message {
	components := []map[string]any{
		{
			"id":        "root",
			"component": "Column",
			"children": []string{
				"header_card",
				"about_card",
				"projects_section",
				"contact_card",
			},
		},
		// Card 1: Header & Bio dengan padding internal
		{
			"id":        "header_card",
			"component": "Card",
			"child":     "header_content",
		},
		{
			"id":        "header_content",
			"component": "Column",
			"children": []string{
				"portfolio_header",
				"profile",
			},
		},
		{
			"id":        "portfolio_header",
			"component": "Text",
			"text":      "Portofolio Panda Raksasa",
			"variant":   "h1",
		},
		{
			"id":        "profile",
			"component": "Text",
			"text":      "Panda raksasa dengan spesialisasi bambu, tidur siang, dan menjadi hewan paling menggemaskan di hutan.",
			"variant":   "body",
		},
		// Card 2: Tentang Saya & Keahlian dengan padding internal
		{
			"id":        "about_card",
			"component": "Card",
			"child":     "about_content_box",
		},
		{
			"id":        "about_content_box",
			"component": "Column",
			"children": []string{
				"about_title",
				"about_text",
				"skills",
			},
		},
		{
			"id":        "about_title",
			"component": "Text",
			"text":      "Tentang Saya",
			"variant":   "h2",
		},
		{
			"id":        "about_text",
			"component": "Text",
			"text":      "Saya adalah panda raksasa yang menghabiskan sebagian besar waktu mencari bambu, makan, beristirahat, dan menjalani kehidupan dengan santai. Keahlian utama saya adalah bertahan hidup dengan wajah menggemaskan.",
			"variant":   "body",
		},
		{
			"id":        "skills",
			"component": "Text",
			"text":      "Keahlian: Makan Bambu • Memanjat Pohon • Berguling • Tidur Siang • Menggemaskan",
			"variant":   "caption",
		},
		// Section Proyek (Tiap Proyek sudah Card ber-padding)
		{
			"id":        "projects_section",
			"component": "Column",
			"children": []string{
				"projects_heading_card",
				"project_1",
				"project_2",
				"project_3",
			},
		},
		{
			"id":        "projects_heading_card",
			"component": "Card",
			"child":     "projects_heading",
		},
		{
			"id":        "projects_heading",
			"component": "Text",
			"text":      "Aktivitas Pilihan",
			"variant":   "h2",
		},
		{
			"id":        "project_1",
			"component": "Card",
			"child":     "project_1_content",
		},
		{
			"id":        "project_1_content",
			"component": "Column",
			"children": []string{
				"project_1_title",
				"project_1_description",
			},
		},
		{
			"id":        "project_1_title",
			"component": "Text",
			"text":      "Operasi Bambu",
			"variant":   "h3",
		},
		{
			"id":        "project_1_description",
			"component": "Text",
			"text":      "Mencari, memilih, dan menghabiskan bambu berkualitas tinggi setiap hari.",
			"variant":   "body",
		},
		{
			"id":        "project_2",
			"component": "Card",
			"child":     "project_2_content",
		},
		{
			"id":        "project_2_content",
			"component": "Column",
			"children": []string{
				"project_2_title",
				"project_2_description",
			},
		},
		{
			"id":        "project_2_title",
			"component": "Text",
			"text":      "Program Tidur Siang",
			"variant":   "h3",
		},
		{
			"id":        "project_2_description",
			"component": "Text",
			"text":      "Mengoptimalkan energi dengan tidur di tempat paling nyaman setelah makan.",
			"variant":   "body",
		},
		{
			"id":        "project_3",
			"component": "Card",
			"child":     "project_3_content",
		},
		{
			"id":        "project_3_content",
			"component": "Column",
			"children": []string{
				"project_3_title",
				"project_3_description",
			},
		},
		{
			"id":        "project_3_title",
			"component": "Text",
			"text":      "Duta Kegemasan",
			"variant":   "h3",
		},
		{
			"id":        "project_3_description",
			"component": "Text",
			"text":      "Meningkatkan kebahagiaan pengunjung hanya dengan duduk, makan, dan menatap kamera.",
			"variant":   "body",
		},
		// Card 4: Footer Kontak
		{
			"id":        "contact_card",
			"component": "Card",
			"child":     "contact",
		},
		{
			"id":        "contact",
			"component": "Text",
			"text":      "Status: Aktif • Lokasi: Hutan Bambu • Spesialisasi: Bambu",
			"variant":   "caption",
		},
	}

	return assembleA2UIMessage(components)
}

func assembleA2UIMessage(components []map[string]any) *waE2E.Message {
	a2uiData := map[string]any{
		"version": "v0.9",
		"createSurface": map[string]any{
			"surfaceId":  "starcore-widget=b392eacf-f086-48b0-b096-965be19d343a",
			"catalogId":  "https://a2ui.org/specification/v0_9/catalogs/basic/catalog.json",
			"components": components,
		},
	}

	dataBytes, _ := json.Marshal(a2uiData)

	buttons := []*waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
		{
			Name:             proto.String(messaging.NFCTAURL),
			ButtonParamsJSON: proto.String(`{"display_text":"Cek Panda","url":"https://www.google.com","merchant_url":"https://www.google.com"}`),
		},
		{
			Name:             proto.String(messaging.NFCTAURL),
			ButtonParamsJSON: proto.String(`{"display_text":"Cek Pulsa","url":"https://www.google.com","merchant_url":"https://www.google.com"}`),
		},
	}

	im := &waE2E.InteractiveMessage{
		Header: &waE2E.InteractiveMessage_Header{
			HasMediaAttachment: proto.Bool(false),
		},
		Body: &waE2E.InteractiveMessage_Body{
			Text: proto.String("Portofolio Panda Raksasa"),
		},
		Footer: &waE2E.InteractiveMessage_Footer{
			Text: proto.String("• Nebuluous"),
		},
		InteractiveMessage: &waE2E.InteractiveMessage_NativeFlowMessage_{
			NativeFlowMessage: &waE2E.InteractiveMessage_NativeFlowMessage{
				Buttons:           buttons,
				MessageParamsJSON: proto.String("{}"),
				MessageVersion:    proto.Int32(1),
			},
		},
		BloksWidget: &waE2E.InteractiveMessage_BloksWidget{
			Uuid: proto.String("b392eacf-f086-48b0-b096-965be19d343a"),
			Data: proto.String(string(dataBytes)),
			Type: proto.String("im_a2ui"),
		},
		ContextInfo: &waE2E.ContextInfo{
			Expiration: proto.Uint32(7776000),
		},
	}

	return &waE2E.Message{
		InteractiveMessage: im,
	}
}
