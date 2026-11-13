package msgbuilder

import (
	"encoding/json"

	"wabot-go/internal/messaging"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

// A2UIBuilder menyediakan fluent method chaining untuk menyusun
// komponen Server-Driven UI deklaratif spesifikasi A2UI v0.9 (Meta Bloks).
type A2UIBuilder struct {
	surfaceID  string
	catalogID  string
	rootChild  []string
	components []map[string]any
	buttons    []*waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton
	bodyText   string
	footerText string
}

// NewA2UI membuat instance baru untuk menyusun widget Meta Bloks A2UI.
func NewA2UI() *A2UIBuilder {
	return &A2UIBuilder{
		surfaceID:  "starcore-widget=" + whatsmeow.GenerateMessageID(),
		catalogID:  "https://a2ui.org/specification/v0_9/catalogs/basic/catalog.json",
		rootChild:  make([]string, 0),
		components: make([]map[string]any, 0),
		buttons:    make([]*waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton, 0),
		bodyText:   "Interactive A2UI Widget",
		footerText: "• A2UI Bloks",
	}
}

// SetBody mengatur fallback text body pada bubble.
func (b *A2UIBuilder) SetBody(text string) *A2UIBuilder {
	b.bodyText = text
	return b
}

// SetFooter mengatur teks footer pada bubble.
func (b *A2UIBuilder) SetFooter(text string) *A2UIBuilder {
	b.footerText = text
	return b
}

// AddHeading menambahkan judul besar H1/H2/H3.
func (b *A2UIBuilder) AddHeading(id, text, variant string) *A2UIBuilder {
	if variant == "" {
		variant = "h1"
	}
	b.components = append(b.components, map[string]any{
		"id":        id,
		"component": "Text",
		"text":      text,
		"variant":   variant,
	})
	b.rootChild = append(b.rootChild, id)
	return b
}

// AddText menambahkan teks paragraf biasa.
func (b *A2UIBuilder) AddText(id, text, variant string) *A2UIBuilder {
	if variant == "" {
		variant = "body"
	}
	b.components = append(b.components, map[string]any{
		"id":        id,
		"component": "Text",
		"text":      text,
		"variant":   variant,
	})
	b.rootChild = append(b.rootChild, id)
	return b
}

// AddImage menambahkan gambar banner.
func (b *A2UIBuilder) AddImage(id, imageURL string) *A2UIBuilder {
	b.components = append(b.components, map[string]any{
		"id":        id,
		"component": "Image",
		"url":       imageURL,
		"variant":   "header",
		"fit":       "none",
	})
	b.rootChild = append(b.rootChild, id)
	return b
}

// AddCardBox menambahkan kotak kartu ber-padding yang menampung judul dan teks deskripsi.
func (b *A2UIBuilder) AddCardBox(cardID, title, description string) *A2UIBuilder {
	contentID := cardID + "_content"
	titleID := cardID + "_title"
	descID := cardID + "_desc"

	b.components = append(b.components,
		map[string]any{
			"id":        cardID,
			"component": "Card",
			"child":     contentID,
		},
		map[string]any{
			"id":        contentID,
			"component": "Column",
			"children":  []string{titleID, descID},
		},
		map[string]any{
			"id":        titleID,
			"component": "Text",
			"text":      title,
			"variant":   "h3",
		},
		map[string]any{
			"id":        descID,
			"component": "Text",
			"text":      description,
			"variant":   "body",
		},
	)
	b.rootChild = append(b.rootChild, cardID)
	return b
}

// AddURLButton menambahkan tombol URL di bawah kartu widget.
func (b *A2UIBuilder) AddURLButton(label, url string) *A2UIBuilder {
	params, _ := json.Marshal(map[string]string{
		"display_text": label,
		"url":          url,
	})
	b.buttons = append(b.buttons, &waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
		Name:             proto.String(messaging.NFCTAURL),
		ButtonParamsJSON: proto.String(string(params)),
	})
	return b
}

// SurfaceData mengembalikan JSON createSurface A2UI (tanpa BloksWidget) agar
// perakit yang menyusun InteractiveMessage sendiri — mis. playground yang
// menggabungkan widget dengan tombol/carousel — bisa menempelkannya ke field
// BloksWidget.
func (b *A2UIBuilder) SurfaceData() string {
	allComponents := []map[string]any{
		{
			"id":        "root",
			"component": "Column",
			"children":  b.rootChild,
		},
	}
	allComponents = append(allComponents, b.components...)

	a2uiData := map[string]any{
		"version": "v0.9",
		"createSurface": map[string]any{
			"surfaceId":  b.surfaceID,
			"catalogId":  b.catalogID,
			"components": allComponents,
		},
	}

	dataBytes, _ := json.Marshal(a2uiData)
	return string(dataBytes)
}

// Build merakit seluruh komponen menjadi *waE2E.Message siap kirim.
func (b *A2UIBuilder) Build() *waE2E.Message {
	return &waE2E.Message{
		InteractiveMessage: &waE2E.InteractiveMessage{
			Header: &waE2E.InteractiveMessage_Header{
				HasMediaAttachment: proto.Bool(false),
			},
			Body: &waE2E.InteractiveMessage_Body{
				Text: proto.String(b.bodyText),
			},
			Footer: &waE2E.InteractiveMessage_Footer{
				Text: proto.String(b.footerText),
			},
			InteractiveMessage: &waE2E.InteractiveMessage_NativeFlowMessage_{
				NativeFlowMessage: &waE2E.InteractiveMessage_NativeFlowMessage{
					Buttons:           b.buttons,
					MessageParamsJSON: proto.String("{}"),
					MessageVersion:    proto.Int32(1),
				},
			},
			BloksWidget: &waE2E.InteractiveMessage_BloksWidget{
				Uuid: proto.String(whatsmeow.GenerateMessageID()),
				Data: proto.String(b.SurfaceData()),
				Type: proto.String("im_a2ui"),
			},
			ContextInfo: &waE2E.ContextInfo{
				Expiration: proto.Uint32(7776000),
			},
		},
	}
}
