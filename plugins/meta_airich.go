package plugins

import (
	"context"
	"encoding/json"
	"fmt"

	"wabot-go/internal/store"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waAICommon"
	"github.com/polymorfa/hypermeow/proto/waAICommonDeprecated"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func init() {
	registerPlugin(RegisterMetaAIRich)
}

// RegisterMetaAIRich mendaftarkan command `.airich` (alias metarich):
// Mengirim pesan antarmuka kaya balasan Meta AI (AIRichResponseMessage / BotForwardedMessage)
// yang memuat judul, teks markdown, gambar preview, horizontal scroll card (carousel), dan tombol CTA link.
func RegisterMetaAIRich(r *Registry) {
	r.Register(Command{
		Name:        "airich",
		Aliases:     []string{"metarich"},
		Description: "Tampilkan widget antarmuka kaya Meta AI (AIRichResponse)",
		Category:    CategoryDev,
		Handler:     handleMetaAIRich,
	})
}

func handleMetaAIRich(ctx context.Context, mctx *store.MessageContext) error {
	// Command uji pengembang: hanya owner. Pesan ini memalsukan "diteruskan
	// dari Meta AI" (ForwardOrigin META_AI), jadi tidak boleh bisa dipicu
	// sembarang user di grup.
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	msg := buildMetaAIRichMessage()
	extra := whatsmeow.SendRequestExtra{ID: whatsmeow.GenerateMessageID()}

	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, msg, extra)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal kirim Meta AI Rich: %v", err))
	}

	store.LogOutgoing(mctx, "airich", "Meta AI Rich Response")
	store.TrackBotMessageID(resp.ID, "airich")
	return nil
}

func buildMetaAIRichMessage() *waE2E.Message {
	payload := map[string]any{
		"response_id": "742a451a-0c33-45ca-a205-42c2b1666bca",
		"sections": []map[string]any{
			{
				"view_model": map[string]any{
					"primitive": map[string]any{
						"__typename": "FOATextPrimitive",
						"text":       "# let me know~",
					},
					"__typename": "GenAISingleLayoutViewModel",
				},
			},
			{
				"view_model": map[string]any{
					"primitive": map[string]any{
						"text":       "im here~",
						"__typename": "GenAIMarkdownTextUXPrimitive",
					},
					"__typename": "GenAISingleLayoutViewModel",
				},
			},
			{
				"view_model": map[string]any{
					"primitive": map[string]any{
						"__typename": "GenAIImagePrimitive",
						"preview_image": map[string]any{
							"__typename": "GenAIMediaItem",
							"mime_type":  "image/jpeg",
							"url":        "https://cdn.ornzora.eu.cc/f4ec8425-c846-4937-b838-9291bf0514e2-FIORA.jpg",
						},
						"full_image": map[string]any{
							"__typename": "GenAIMediaItem",
							"mime_type":  "image/jpeg",
							"url":        "https://cdn.ornzora.eu.cc/2fa0763e-011f-4d18-b69b-32dd24282393-FIORA.jpg",
						},
					},
					"__typename": "GenAISingleLayoutViewModel",
				},
			},
			{
				"view_model": map[string]any{
					"primitives": []map[string]any{
						{
							"__typename": "GenAI3PExtWidgetPrimitive",
							"header": map[string]any{
								"__typename": "GenAI3PExtWidgetStandardHeader",
								"title":      "LIST-X",
							},
							"body": map[string]any{
								"__typename": "GenAI3PExtCalendarEventList",
								"sections":   []any{},
								"ctas": []map[string]any{
									{
										"__typename":   "GenAI3PExtWidgetCTA",
										"label":        "menu",
										"state":        "PENDING",
										"kind":         "OTHER",
										"tool_call_id": "00",
										"toast": map[string]any{
											"__typename": "GenAI3PExtWidgetToast",
											"label":      "NIX",
										},
									},
									{
										"__typename":   "GenAI3PExtWidgetCTA",
										"label":        "profile",
										"state":        "PENDING",
										"kind":         "OTHER",
										"tool_call_id": "01",
										"toast": map[string]any{
											"__typename": "GenAI3PExtWidgetToast",
											"label":      "NIX",
										},
									},
									{
										"__typename":   "GenAI3PExtWidgetCTA",
										"label":        "script",
										"state":        "PENDING",
										"kind":         "OTHER",
										"tool_call_id": "02",
										"toast": map[string]any{
											"__typename": "GenAI3PExtWidgetToast",
											"label":      "NIX",
										},
									},
								},
							},
						},
						{
							"__typename": "GenAI3PExtWidgetPrimitive",
							"header": map[string]any{
								"__typename": "GenAI3PExtWidgetStandardHeader",
								"title":      "NX-T",
							},
							"body": map[string]any{
								"__typename": "GenAI3PExtCalendarEventList",
								"sections":   []any{},
								"ctas": []map[string]any{
									{
										"__typename":   "GenAI3PExtWidgetCTA",
										"label":        "NIXCODE",
										"state":        "PENDING",
										"kind":         "OTHER",
										"tool_call_id": "10",
										"toast": map[string]any{
											"__typename": "GenAI3PExtWidgetToast",
											"label":      "NIX",
										},
									},
									{
										"__typename":   "GenAI3PExtWidgetCTA",
										"label":        "NIXEL",
										"state":        "PENDING",
										"kind":         "OTHER",
										"tool_call_id": "11",
										"toast": map[string]any{
											"__typename": "GenAI3PExtWidgetToast",
											"label":      "NIX",
										},
									},
									{
										"__typename":   "GenAI3PExtWidgetCTA",
										"label":        "FIORA",
										"state":        "PENDING",
										"kind":         "OTHER",
										"tool_call_id": "12",
										"toast": map[string]any{
											"__typename": "GenAI3PExtWidgetToast",
											"label":      "NIX",
										},
									},
								},
							},
						},
					},
					"__typename": "GenAIHScrollLayoutViewModel",
				},
			},
			{
				"view_model": map[string]any{
					"primitives": []map[string]any{
						{
							"__typename": "GenAIFooterActionPrimitive",
							"cta_text":   "WhatsApp Group",
							"cta_type":   "OPEN_URL",
							"cta_url":    "https://chat.whatsapp.com/J7OzqKB7Bl2AGIcNEYsdch?s=cl&p=a&ilr=0",
						},
						{
							"__typename": "GenAIFooterActionPrimitive",
							"cta_text":   "WhatsApp Channel",
							"cta_type":   "OPEN_URL",
							"cta_url":    "https://whatsapp.com/channel/0029VbCV1ck8fewpdNb2TY2k",
						},
					},
					"__typename": "GenAIHScrollLayoutViewModel",
				},
			},
		},
	}

	jsonBytes, _ := json.Marshal(payload)

	richMsg := &waE2E.AIRichResponseMessage{
		MessageType: waAICommonDeprecated.AIRichResponseMessageType_AI_RICH_RESPONSE_TYPE_STANDARD.Enum(),
		UnifiedResponse: &waAICommon.AIRichResponseUnifiedResponse{
			Data: jsonBytes,
		},
		ContextInfo: &waE2E.ContextInfo{
			ForwardingScore: proto.Uint32(1),
			IsForwarded:     proto.Bool(true),
			ForwardOrigin:   waE2E.ContextInfo_META_AI.Enum(),
		},
	}

	return &waE2E.Message{
		BotForwardedMessage: &waE2E.FutureProofMessage{
			Message: &waE2E.Message{
				RichResponseMessage: richMsg,
			},
		},
	}
}
