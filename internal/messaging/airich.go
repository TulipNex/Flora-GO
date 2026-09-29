package messaging

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"wabot-go/internal/store"

	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/proto/waAICommon"
	"go.mau.fi/whatsmeow/proto/waAICommonDeprecated"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// EKSPERIMEN SEMENTARA (lihat .richtest): builder AI Rich minimal untuk
// mencari kombinasi yang tampil sebagai rich di klien. Referensi struktur:
// YuukiBot (Riflxz) messagebuilder.go (NIXCODE port) — lisensinya mewajibkan
// credit, maka dicatat di sini; implementasi ditulis ulang clean-room dari
// definisi proto, bukan salinan kode.
//
// FAKTA YANG SUDAH DIKETAHUI: port AIRich mentah (RichResponseMessage saja)
// tampil sebagai TEKS BIASA di klien (percobaan lama, dihapus). Referensi
// YuukiBot mengirim AIRich mentah juga — jadi referensi TIDAK membuktikan
// render. Matriks varian .richtest (mentah/wrapper-viewonce/biz-node/keduanya)
// yang akan memutuskan secara empiris.

// RichTextBuilder merakit SATU blok teks markdown GenAI (cukup untuk
// eksperimen render; table/code/dll ditambah HANYA bila ada varian menang).
type RichTextBuilder struct {
	text  string
	title string
}

// NewRichTextBuilder membuat builder teks rich.
func NewRichTextBuilder(text string) *RichTextBuilder {
	return &RichTextBuilder{text: text}
}

// SetTitle mengisi disclaimer title BotMetadata.
func (b *RichTextBuilder) SetTitle(t string) *RichTextBuilder {
	b.title = t
	return b
}

// richForwardedContext adalah ContextInfo forward META_AI (cermin referensi:
// ForwardingScore + ForwardedAIBotMessageInfo BotJID 0@bot + ForwardOrigin).
func richForwardedContext() *waE2E.ContextInfo {
	return &waE2E.ContextInfo{
		ForwardingScore: proto.Uint32(1),
		IsForwarded:     proto.Bool(true),
		ForwardedAiBotMessageInfo: &waAICommon.ForwardedAIBotMessageInfo{
			BotJID: proto.String("0@bot"),
		},
		ForwardOrigin: waE2E.ContextInfo_META_AI.Enum(),
	}
}

// richUnifiedResponse adalah payload JSON unified (satu section, satu
// primitif markdown) yang di-base64 — bentuk yang dibaca klien Meta AI.
func (b *RichTextBuilder) richUnifiedResponse() *waAICommon.AIRichResponseUnifiedResponse {
	unified := map[string]any{
		"response_id": fmt.Sprintf("resp-%d", time.Now().UnixNano()),
		"sections": []any{
			map[string]any{
				"view_model": map[string]any{
					"__typename": "GenAISingleLayoutViewModel",
					"primitive": map[string]any{
						"text":       b.text,
						"__typename": "GenAIMarkdownTextUXPrimitive",
					},
				},
			},
		},
	}
	data, _ := json.Marshal(unified)
	return &waAICommon.AIRichResponseUnifiedResponse{
		Data: []byte(base64.StdEncoding.EncodeToString(data)),
	}
}

// richCore merakit inti RichResponseMessage (submessage teks + unified).
func (b *RichTextBuilder) richCore() *waE2E.AIRichResponseMessage {
	return &waE2E.AIRichResponseMessage{
		MessageType: waAICommonDeprecated.AIRichResponseMessageType_AI_RICH_RESPONSE_TYPE_STANDARD.Enum(),
		Submessages: []*waAICommonDeprecated.AIRichResponseSubMessage{
			{
				MessageType: waAICommonDeprecated.AIRichResponseSubMessageType_AI_RICH_RESPONSE_TEXT.Enum(),
				MessageText: proto.String(b.text),
			},
		},
		UnifiedResponse: b.richUnifiedResponse(),
		ContextInfo:     richForwardedContext(),
	}
}

// richEnvelope membungkus inti seperti pesan bot Meta AI: MessageContextInfo
// (BotMetadata + disclaimer) + BotForwardedMessage.
func (b *RichTextBuilder) richEnvelope() *waE2E.Message {
	return &waE2E.Message{
		MessageContextInfo: &waE2E.MessageContextInfo{
			DeviceListMetadata:        &waE2E.DeviceListMetadata{},
			DeviceListMetadataVersion: proto.Int32(2),
			BotMetadata: &waAICommon.BotMetadata{
				MessageDisclaimerText: proto.String(b.title),
			},
		},
		BotForwardedMessage: &waE2E.FutureProofMessage{
			Message: &waE2E.Message{
				RichResponseMessage: b.richCore(),
			},
		},
	}
}

// RichBizNodes adalah additional node stanza untuk pesan interaktif
// (cermin YuukiBot buildBizNodes: biz/native_flow + bot{biz_bot=1} untuk
// non-grup). Dibutuhkan via SendRequestExtra.AdditionalNodes.
func RichBizNodes(chat types.JID) []waBinary.Node {
	nodes := []waBinary.Node{
		{
			Tag:   "biz",
			Attrs: waBinary.Attrs{},
			Content: []waBinary.Node{
				{
					Tag: "interactive",
					Attrs: waBinary.Attrs{
						"type": "native_flow",
						"v":    "1",
					},
					Content: []waBinary.Node{
						{
							Tag: "native_flow",
							Attrs: waBinary.Attrs{
								"v":    "9",
								"name": "mixed",
							},
						},
					},
				},
			},
		},
	}
	if chat.Server != types.GroupServer {
		nodes = append(nodes, waBinary.Node{
			Tag:   "bot",
			Attrs: waBinary.Attrs{"biz_bot": "1"},
		})
	}
	return nodes
}

// SendRichText mengirim teks rich dengan varian eksperimen:
// 1 = mentah, 2 = bungkus ViewOnceMessage, 3 = +biz nodes, 4 = keduanya.
// Mengembalikan ID pesan (untuk korelasi laporan) + error.
func (b *RichTextBuilder) SendRichText(ctx context.Context, mctx *store.MessageContext, variant int) (types.MessageID, error) {
	msg := b.richEnvelope()
	if variant == 2 || variant == 4 {
		msg = &waE2E.Message{
			ViewOnceMessage: &waE2E.FutureProofMessage{Message: msg},
		}
	}
	if variant == 3 || variant == 4 {
		nodes := RichBizNodes(mctx.Chat)
		resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, msg, whatsmeow.SendRequestExtra{
			AdditionalNodes: &nodes,
		})
		if err != nil {
			return "", err
		}
		return resp.ID, nil
	}
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, msg)
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}
