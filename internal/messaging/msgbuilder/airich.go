package msgbuilder

import (
	"crypto/rand"
	"encoding/json"
	"fmt"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waAICommon"
	"github.com/polymorfa/hypermeow/proto/waAICommonDeprecated"
	"github.com/polymorfa/hypermeow/proto/waCommon"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

// AIRichBuilder menyediakan fluent method chaining untuk merakit
// pesan balasan Meta AI tingkat lanjut (AIRichResponseMessage) sesuai spesifikasi MessageBuilderV4.7.
type AIRichBuilder struct {
	responseID  string
	botJID      string
	title       string
	footer      string
	sections    []map[string]any
	screens     []map[string]any
	submessages []*waAICommonDeprecated.AIRichResponseSubMessage
}

// NewAIRich membuat instance builder baru untuk Meta AI Rich Response.
func NewAIRich() *AIRichBuilder {
	return &AIRichBuilder{
		responseID:  whatsmeow.GenerateMessageID(),
		botJID:      "0@bot",
		sections:    make([]map[string]any, 0),
		screens:     make([]map[string]any, 0),
		submessages: make([]*waAICommonDeprecated.AIRichResponseSubMessage, 0),
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

// SetTitle mengatur judul disclaimer pada botMetadata.
func (b *AIRichBuilder) SetTitle(title string) *AIRichBuilder {
	b.title = title
	return b
}

// SetFooter mengatur teks penutup footer (GenAIMetadataTextPrimitive).
func (b *AIRichBuilder) SetFooter(footer string) *AIRichBuilder {
	b.footer = footer
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
	b.submessages = append(b.submessages, &waAICommonDeprecated.AIRichResponseSubMessage{
		MessageType: waAICommonDeprecated.AIRichResponseSubMessageType_AI_RICH_RESPONSE_TEXT.Enum(),
		MessageText: proto.String(text),
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
	b.submessages = append(b.submessages, &waAICommonDeprecated.AIRichResponseSubMessage{
		MessageType: waAICommonDeprecated.AIRichResponseSubMessageType_AI_RICH_RESPONSE_TEXT.Enum(),
		MessageText: proto.String(text),
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
	b.submessages = append(b.submessages, &waAICommonDeprecated.AIRichResponseSubMessage{
		MessageType: waAICommonDeprecated.AIRichResponseSubMessageType_AI_RICH_RESPONSE_TEXT.Enum(),
		MessageText: proto.String(text),
	})
	return b
}

// AddCode menambahkan blok kode dengan syntax highlighting ter-tokenisasi.
func (b *AIRichBuilder) AddCode(language, code string) *AIRichBuilder {
	protoBlocks, unifiedBlocks := TokenizeCode(language, code)

	b.sections = append(b.sections, map[string]any{
		"view_model": map[string]any{
			"__typename": "GenAISingleLayoutViewModel",
			"primitive": map[string]any{
				"__typename":  "GenAICodeUXPrimitive",
				"language":    language,
				"code_blocks": unifiedBlocks,
			},
		},
	})

	b.submessages = append(b.submessages, &waAICommonDeprecated.AIRichResponseSubMessage{
		MessageType: waAICommonDeprecated.AIRichResponseSubMessageType_AI_RICH_RESPONSE_CODE.Enum(),
		CodeMetadata: &waAICommonDeprecated.AIRichResponseCodeMetadata{
			CodeLanguage: proto.String(language),
			CodeBlocks:   protoBlocks,
		},
	})
	return b
}

// AddTable menambahkan tabel 2D dengan baris header dan baris data terstruktur.
func (b *AIRichBuilder) AddTable(headers []string, rows [][]string) *AIRichBuilder {
	maxCols := len(headers)
	for _, r := range rows {
		if len(r) > maxCols {
			maxCols = len(r)
		}
	}

	normalize := func(r []string) []string {
		res := make([]string, maxCols)
		copy(res, r)
		for i := len(r); i < maxCols; i++ {
			res[i] = ""
		}
		return res
	}

	normHeader := normalize(headers)
	unifiedRows := []map[string]any{
		{
			"is_header": true,
			"cells":     normHeader,
		},
	}
	protoRows := []*waAICommonDeprecated.AIRichResponseTableMetadata_AIRichResponseTableRow{
		{
			IsHeading: proto.Bool(true),
			Items:     normHeader,
		},
	}

	for _, r := range rows {
		normR := normalize(r)
		unifiedRows = append(unifiedRows, map[string]any{
			"is_header": false,
			"cells":     normR,
		})
		protoRows = append(protoRows, &waAICommonDeprecated.AIRichResponseTableMetadata_AIRichResponseTableRow{
			IsHeading: proto.Bool(false),
			Items:     normR,
		})
	}

	b.sections = append(b.sections, map[string]any{
		"view_model": map[string]any{
			"__typename": "GenAISingleLayoutViewModel",
			"primitive": map[string]any{
				"__typename": "GenATableUXPrimitive",
				"rows":       unifiedRows,
			},
		},
	})

	b.submessages = append(b.submessages, &waAICommonDeprecated.AIRichResponseSubMessage{
		MessageType: waAICommonDeprecated.AIRichResponseSubMessageType_AI_RICH_RESPONSE_TABLE.Enum(),
		TableMetadata: &waAICommonDeprecated.AIRichResponseTableMetadata{
			Title: proto.String(""),
			Rows:  protoRows,
		},
	})
	return b
}

// AddSuggest menambahkan pil saran prompt interaktif di bagian bawah respons AI.
func (b *AIRichBuilder) AddSuggest(prompts ...string) *AIRichBuilder {
	return b.AddSuggestLayout("", prompts...)
}

// AddSuggestLayout menambahkan pil saran dengan layout tertentu ("Single", "HScroll", atau "ActionRow").
func (b *AIRichBuilder) AddSuggestLayout(layout string, prompts ...string) *AIRichBuilder {
	if len(prompts) == 0 {
		return b
	}

	pills := make([]map[string]any, 0, len(prompts))
	for _, p := range prompts {
		pills = append(pills, map[string]any{
			"__typename":  "GenAIFollowUpSuggestionPillPrimitive",
			"prompt_text": p,
			"prompt_type": "SUGGESTED_PROMPT",
		})
	}

	if layout == "" {
		if len(prompts) == 1 {
			layout = "Single"
		} else {
			layout = "HScroll"
		}
	}

	viewModel := map[string]any{
		"__typename": fmt.Sprintf("GenAI%sLayoutViewModel", layout),
	}
	if layout == "Single" {
		viewModel["primitive"] = pills[0]
	} else {
		viewModel["primitives"] = pills
	}

	b.sections = append(b.sections, map[string]any{
		"__typename": "GenAIUnifiedResponseSection",
		"view_model": viewModel,
	})
	return b
}

// AddTip menambahkan teks tip/bantuan dengan simbol ⓘ (GenAIMetadataTextPrimitive).
func (b *AIRichBuilder) AddTip(text string) *AIRichBuilder {
	tipText := "ⓘ " + text
	b.sections = append(b.sections, map[string]any{
		"view_model": map[string]any{
			"__typename": "GenAISingleLayoutViewModel",
			"primitive": map[string]any{
				"__typename": "GenAIMetadataTextPrimitive",
				"text":       tipText,
			},
		},
	})
	b.submessages = append(b.submessages, &waAICommonDeprecated.AIRichResponseSubMessage{
		MessageType: waAICommonDeprecated.AIRichResponseSubMessageType_AI_RICH_RESPONSE_TEXT.Enum(),
		MessageText: proto.String(tipText),
	})
	return b
}

// AddMetadata menambahkan teks metadata kecil abu-abu di bagian respons AI.
func (b *AIRichBuilder) AddMetadata(text string) *AIRichBuilder {
	b.sections = append(b.sections, map[string]any{
		"view_model": map[string]any{
			"__typename": "GenAISingleLayoutViewModel",
			"primitive": map[string]any{
				"__typename": "GenAIMetadataTextPrimitive",
				"text":       text,
			},
		},
	})
	b.submessages = append(b.submessages, &waAICommonDeprecated.AIRichResponseSubMessage{
		MessageType: waAICommonDeprecated.AIRichResponseSubMessageType_AI_RICH_RESPONSE_TEXT.Enum(),
		MessageText: proto.String(text),
	})
	return b
}

// AddImagine menambahkan media generator gambar dengan status "READY" atau "GENERATING" (progressive loading).
func (b *AIRichBuilder) AddImagine(previewURL, fullURL, imagineType, status, updateText string) *AIRichBuilder {
	if fullURL == "" {
		fullURL = previewURL
	}
	if imagineType == "" {
		imagineType = "IMAGE"
	}
	if status == "" {
		status = "READY"
	}

	primitive := map[string]any{
		"__typename":   "GenAIImaginePrimitive",
		"imagine_type": imagineType,
		"status": map[string]any{
			"status":      status,
			"update_text": updateText,
		},
	}
	if previewURL != "" {
		primitive["media"] = map[string]any{
			"__typename": "GenAIMediaItem",
			"mime_type":  "image/jpeg",
			"url":        previewURL,
		}
	}

	b.sections = append(b.sections, map[string]any{
		"view_model": map[string]any{
			"__typename": "GenAISingleLayoutViewModel",
			"primitive":  primitive,
		},
	})

	if previewURL != "" {
		b.submessages = append(b.submessages, &waAICommonDeprecated.AIRichResponseSubMessage{
			MessageType: waAICommonDeprecated.AIRichResponseSubMessageType_AI_RICH_RESPONSE_GRID_IMAGE.Enum(),
			GridImageMetadata: &waAICommonDeprecated.AIRichResponseGridImageMetadata{
				GridImageURL: &waAICommonDeprecated.AIRichResponseImageURL{
					ImagePreviewURL: proto.String(previewURL),
				},
				ImageURLs: []*waAICommonDeprecated.AIRichResponseImageURL{
					{
						ImagePreviewURL: proto.String(previewURL),
						ImageHighResURL: proto.String(fullURL),
						SourceURL:       proto.String(fullURL),
					},
				},
			},
		})
	}
	return b
}

// AddSource menambahkan kartu sumber/referensi pencarian web.
func (b *AIRichBuilder) AddSource(title, subtitle, url, iconURL string) *AIRichBuilder {
	primitive := map[string]any{
		"__typename":             "GenAISearchCitationItem",
		"reference_title":        title,
		"reference_display_name": subtitle,
		"reference_url":          url,
		"sources":                []any{},
	}
	if iconURL != "" {
		primitive["reference_icon"] = iconURL
	}
	b.sections = append(b.sections, map[string]any{
		"view_model": map[string]any{
			"__typename": "GenAISingleLayoutViewModel",
			"primitive":  primitive,
		},
	})
	return b
}

// AddImage menambahkan blok gambar AI standar dengan URL gambar preview dan full image.
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
	b.submessages = append(b.submessages, &waAICommonDeprecated.AIRichResponseSubMessage{
		MessageType: waAICommonDeprecated.AIRichResponseSubMessageType_AI_RICH_RESPONSE_GRID_IMAGE.Enum(),
		GridImageMetadata: &waAICommonDeprecated.AIRichResponseGridImageMetadata{
			GridImageURL: &waAICommonDeprecated.AIRichResponseImageURL{
				ImagePreviewURL: proto.String(previewURL),
			},
			ImageURLs: []*waAICommonDeprecated.AIRichResponseImageURL{
				{
					ImagePreviewURL: proto.String(previewURL),
					ImageHighResURL: proto.String(fullURL),
					SourceURL:       proto.String(fullURL),
				},
			},
		},
	})
	return b
}

// AddFooterLinks menambahkan tombol aksi tautan URL di bagian footer.
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

// GenerateVerificationMetadata menghasilkan payload verifikasi Meta AI V4.7
// (signature 64-byte dan certificate chain 684/892 byte).
func GenerateVerificationMetadata(version string) *waAICommon.BotSignatureVerificationMetadata {
	if version == "" {
		version = "4.7"
	}
	sigPrefix := []byte(fmt.Sprintf("NIXEL.MessageBuilderV%s-VerificationSignature.Metadata", version))
	certPrefix := []byte(fmt.Sprintf("NIXEL.MessageBuilderV%s-CertificateChain.Metadata", version))

	sig := make([]byte, 64)
	copy(sig, sigPrefix)
	if len(sigPrefix) < 64 {
		_, _ = rand.Read(sig[len(sigPrefix):])
	}

	cert1 := make([]byte, 684)
	copy(cert1, certPrefix)
	if len(certPrefix) < 684 {
		_, _ = rand.Read(cert1[len(certPrefix):])
	}

	cert2 := make([]byte, 892)
	copy(cert2, certPrefix)
	if len(certPrefix) < 892 {
		_, _ = rand.Read(cert2[len(certPrefix):])
	}

	return &waAICommon.BotSignatureVerificationMetadata{
		Proofs: []*waAICommon.BotSignatureVerificationUseCaseProof{
			{
				Version:          proto.Int32(1),
				UseCase:          waAICommon.BotSignatureVerificationUseCaseProof_WA_BOT_MSG.Enum(),
				Signature:        sig,
				CertificateChain: [][]byte{cert1, cert2},
			},
		},
	}
}

// Build merakit seluruh payload menjadi *waE2E.Message siap kirim.
func (b *AIRichBuilder) Build() *waE2E.Message {
	sections := make([]map[string]any, len(b.sections))
	copy(sections, b.sections)

	if b.footer != "" {
		sections = append(sections, map[string]any{
			"view_model": map[string]any{
				"__typename": "GenAISingleLayoutViewModel",
				"primitive": map[string]any{
					"__typename": "GenAIMetadataTextPrimitive",
					"text":       b.footer,
				},
			},
		})
	}

	data := map[string]any{
		"response_id": b.responseID,
		"sections":    sections,
	}
	if len(b.screens) > 0 {
		data["embedded_screens"] = b.screens
	}

	jsonBytes, _ := json.Marshal(data)

	richMsg := &waE2E.AIRichResponseMessage{
		MessageType: waAICommonDeprecated.AIRichResponseMessageType_AI_RICH_RESPONSE_TYPE_STANDARD.Enum(),
		Submessages: b.submessages,
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

	botMetadata := &waAICommon.BotMetadata{
		BotResponseID:        proto.String(b.responseID),
		VerificationMetadata: GenerateVerificationMetadata("4.7"),
	}
	if b.title != "" {
		botMetadata.MessageDisclaimerText = proto.String(b.title)
	}

	return &waE2E.Message{
		MessageContextInfo: &waE2E.MessageContextInfo{
			DeviceListMetadataVersion: proto.Int32(2),
			BotMetadata:               botMetadata,
		},
		BotForwardedMessage: &waE2E.FutureProofMessage{
			Message: &waE2E.Message{
				RichResponseMessage: richMsg,
			},
		},
	}
}

// BuildEdit membungkus pesan AIRich yang sudah dibangun ke dalam ProtocolMessage tipe 14 (MESSAGE_EDIT)
// di dalam BotForwardedMessage untuk melakukan progressive update pada pesan yang sudah terkirim.
func (b *AIRichBuilder) BuildEdit(targetID string) *waE2E.Message {
	editedMsg := b.Build()
	return &waE2E.Message{
		BotForwardedMessage: &waE2E.FutureProofMessage{
			Message: &waE2E.Message{
				ProtocolMessage: &waE2E.ProtocolMessage{
					Key: &waCommon.MessageKey{
						FromMe: proto.Bool(true),
						ID:     proto.String(targetID),
					},
					Type:          waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
					EditedMessage: editedMsg.GetBotForwardedMessage().GetMessage(),
				},
			},
		},
	}
}
