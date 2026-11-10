package msgbuilder

import (
	"encoding/json"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waAICommon"
	"github.com/polymorfa/hypermeow/proto/waAICommonDeprecated"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

// AIRichBuilder menyediakan fluent method chaining untuk merakit
// pesan balasan Meta AI tingkat lanjut (AIRichResponseMessage).
type AIRichBuilder struct {
	responseID string
	botJID     string
	sections   []map[string]any
	screens    []map[string]any
}

// NewAIRich membuat instance builder baru untuk Meta AI Rich Response.
func NewAIRich() *AIRichBuilder {
	return &AIRichBuilder{
		responseID: whatsmeow.GenerateMessageID(),
		botJID:     "0@bot",
		sections:   make([]map[string]any, 0),
		screens:    make([]map[string]any, 0),
	}
}

// SetResponseID mengatur custom response ID.
func (b *AIRichBuilder) SetResponseID(id string) *AIRichBuilder {
	if id != "" {
		b.responseID = id
	}
	return b
}

// SetBotJID mengatur JID bot Meta AI pengirim (default: 0@bot).
func (b *AIRichBuilder) SetBotJID(jid string) *AIRichBuilder {
	if jid != "" {
		b.botJID = jid
	}
	return b
}

// AddHeading menambahkan judul teks besar (FOATextPrimitive).
func (b *AIRichBuilder) AddHeading(text string) *AIRichBuilder {
	b.sections = append(b.sections, map[string]any{
		"view_model": map[string]any{
			"__typename": "GenAISingleLayoutViewModel",
			"primitive": map[string]any{
				"__typename": "FOATextPrimitive",
				"text":       text,
			},
		},
	})
	return b
}

// AddMarkdown menambahkan teks markdown standar.
func (b *AIRichBuilder) AddMarkdown(text string) *AIRichBuilder {
	b.sections = append(b.sections, map[string]any{
		"view_model": map[string]any{
			"__typename": "GenAISingleLayoutViewModel",
			"primitive": map[string]any{
				"__typename": "GenAIMarkdownTextUXPrimitive",
				"text":       text,
			},
		},
	})
	return b
}

// HyperlinkItem mendefinisikan tautan inline di dalam teks markdown.
type HyperlinkItem struct {
	Text      string `json:"display_name"`
	URL       string `json:"url"`
	IsTrusted bool   `json:"is_trusted"`
}

// AddMarkdownWithLinks menambahkan teks markdown yang menyertakan inline hyperlinks
// sehingga anchor text di dalam tanda kurung siku [Anchor] dapat diklik langsung di WhatsApp.
func (b *AIRichBuilder) AddMarkdownWithLinks(text string, links ...HyperlinkItem) *AIRichBuilder {
	entities := make([]map[string]any, 0, len(links))
	for _, l := range links {
		entities = append(entities, map[string]any{
			"__typename":   "GenAIInlineLinkItem",
			"url":          l.URL,
			"display_name": l.Text,
			"is_trusted":   true,
		})
	}

	primitive := map[string]any{
		"__typename":      "GenAIMarkdownTextUXPrimitive",
		"text":            text,
		"inline_entities": entities,
	}

	b.sections = append(b.sections, map[string]any{
		"view_model": map[string]any{
			"__typename": "GenAISingleLayoutViewModel",
			"primitive":  primitive,
		},
	})
	return b
}

// AddImage menambahkan blok gambar AI dengan URL gambar preview dan full image.
func (b *AIRichBuilder) AddImage(previewURL, fullURL string) *AIRichBuilder {
	if fullURL == "" {
		fullURL = previewURL
	}
	b.sections = append(b.sections, map[string]any{
		"view_model": map[string]any{
			"__typename": "GenAISingleLayoutViewModel",
			"primitive": map[string]any{
				"__typename": "GenAIImagePrimitive",
				"preview_image": map[string]any{
					"__typename": "GenAIMediaItem",
					"mime_type":  "image/jpeg",
					"url":        previewURL,
				},
				"full_image": map[string]any{
					"__typename": "GenAIMediaItem",
					"mime_type":  "image/jpeg",
					"url":        fullURL,
				},
			},
		},
	})
	return b
}

// AddFooterLink menambahkan tombol aksi tautan URL di bagian footer.
func (b *AIRichBuilder) AddFooterLinks(links ...struct{ Label, URL string }) *AIRichBuilder {
	primitives := make([]map[string]any, 0, len(links))
	for _, l := range links {
		primitives = append(primitives, map[string]any{
			"__typename": "GenAIFooterActionPrimitive",
			"cta_text":   l.Label,
			"cta_type":   "OPEN_URL",
			"cta_url":    l.URL,
		})
	}
	b.sections = append(b.sections, map[string]any{
		"view_model": map[string]any{
			"__typename": "GenAIHScrollLayoutViewModel",
			"primitives": primitives,
		},
	})
	return b
}

// AddEmbeddedHTML menyematkan layar HTML webview ke dalam response Meta AI.
func (b *AIRichBuilder) AddEmbeddedHTML(tabTitle, htmlContent, pageURL string) *AIRichBuilder {
	if pageURL == "" {
		pageURL = "https://example.com"
	}
	b.screens = append(b.screens, map[string]any{
		"title": tabTitle,
		"content": []map[string]any{
			{
				"__typename": "FOAIDNixelButtonSheets",
				"tabs": []map[string]any{
					{
						"id":         "tab_0",
						"tab_header": tabTitle,
						"sections": []map[string]any{
							{
								"__typename": "GenAIUnifiedResponseSection",
								"view_model": map[string]any{
									"__typename": "GenAISingleLayoutViewModel",
									"primitive": map[string]any{
										"__typename":      "GenAIaeacdsnwHtmlPrimitive",
										"payload":         htmlContent,
										"url":             pageURL,
										"trusted_sources": []string{pageURL},
									},
								},
							},
						},
						"step_entries": []any{},
					},
				},
			},
		},
	})
	return b
}

// Build merakit seluruh payload menjadi *waE2E.Message siap kirim.
func (b *AIRichBuilder) Build() *waE2E.Message {
	data := map[string]any{
		"response_id": b.responseID,
		"sections":    b.sections,
	}
	if len(b.screens) > 0 {
		data["embedded_screens"] = b.screens
	}

	jsonBytes, _ := json.Marshal(data)

	richMsg := &waE2E.AIRichResponseMessage{
		MessageType: waAICommonDeprecated.AIRichResponseMessageType_AI_RICH_RESPONSE_TYPE_STANDARD.Enum(),
		UnifiedResponse: &waAICommon.AIRichResponseUnifiedResponse{
			Data: jsonBytes,
		},
		ContextInfo: &waE2E.ContextInfo{
			ForwardingScore: proto.Uint32(1),
			IsForwarded:     proto.Bool(true),
			ForwardOrigin:   waE2E.ContextInfo_META_AI.Enum(),
			ForwardedAiBotMessageInfo: &waAICommon.ForwardedAIBotMessageInfo{
				BotJID: proto.String(b.botJID),
			},
		},
	}

	return &waE2E.Message{
		MessageContextInfo: &waE2E.MessageContextInfo{
			DeviceListMetadataVersion: proto.Int32(2),
			BotMetadata:               &waAICommon.BotMetadata{},
		},
		BotForwardedMessage: &waE2E.FutureProofMessage{
			Message: &waE2E.Message{
				RichResponseMessage: richMsg,
			},
		},
	}
}
