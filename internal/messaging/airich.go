package messaging

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"wabot-go/internal/store"

	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/proto/waAICommon"
	"go.mau.fi/whatsmeow/proto/waAICommonDeprecated"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// Builder AI Rich (pesan kaya ala bot Meta AI). Referensi struktur:
// YuukiBot (Riflxz) messagebuilder.go (NIXCODE port) — lisensinya mewajibkan
// credit, maka dicatat di sini; implementasi ditulis ulang clean-room dari
// definisi proto, bukan salinan kode.
//
// VONIS EKSPERIMEN (.richtest, Sep 2026): varian MENTAH (V1) tampil rich,
// +biz nodes (V3) tampil juga, bungkus ViewOnceMessage (V2/V4) HILANG TOTAL.
// Jadi: kirim mentah SELALU, tanpa wrapper. Kode varian + command uji sudah
// dihapus.

// AIRichBuilder merakit blok teks dan tabel GenAI.
type AIRichBuilder struct {
	title       string
	submessages []*waAICommonDeprecated.AIRichResponseSubMessage
	sections    []any
}

// NewAIRichBuilder membuat builder rich kosong.
func NewAIRichBuilder() *AIRichBuilder {
	return &AIRichBuilder{}
}

// SetTitle mengisi disclaimer title BotMetadata.
func (b *AIRichBuilder) SetTitle(t string) *AIRichBuilder {
	b.title = t
	return b
}

// AddText menambah blok teks markdown (GenAIMarkdownTextUXPrimitive).
func (b *AIRichBuilder) AddText(text string) *AIRichBuilder {
	b.submessages = append(b.submessages, &waAICommonDeprecated.AIRichResponseSubMessage{
		MessageType: waAICommonDeprecated.AIRichResponseSubMessageType_AI_RICH_RESPONSE_TEXT.Enum(),
		MessageText: proto.String(text),
	})
	b.sections = append(b.sections, newRichLayout("Single", map[string]any{
		"text":       text,
		"__typename": "GenAIMarkdownTextUXPrimitive",
	}))
	return b
}

// AddTable menambah tabel asli WhatsApp (GenATableUXPrimitive).
// Input: [][]string, baris pertama = header; baris pendek di-pad kosong.
func (b *AIRichBuilder) AddTable(table [][]string) *AIRichBuilder {
	if len(table) == 0 {
		return b
	}
	maxLen := 0
	for _, r := range table {
		if len(r) > maxLen {
			maxLen = len(r)
		}
	}
	normalize := func(r []string) []string {
		out := make([]string, maxLen)
		copy(out, r)
		return out
	}

	var metaRows []*waAICommonDeprecated.AIRichResponseTableMetadata_AIRichResponseTableRow
	var unifiedRows []any
	for i, r := range table {
		nr := normalize(r)
		metaRows = append(metaRows, &waAICommonDeprecated.AIRichResponseTableMetadata_AIRichResponseTableRow{
			Items:     nr,
			IsHeading: proto.Bool(i == 0),
		})
		unifiedRows = append(unifiedRows, map[string]any{
			"is_header": i == 0,
			"cells":     nr,
		})
	}
	b.submessages = append(b.submessages, &waAICommonDeprecated.AIRichResponseSubMessage{
		MessageType: waAICommonDeprecated.AIRichResponseSubMessageType_AI_RICH_RESPONSE_TABLE.Enum(),
		TableMetadata: &waAICommonDeprecated.AIRichResponseTableMetadata{
			Title: proto.String(""),
			Rows:  metaRows,
		},
	})
	b.sections = append(b.sections, newRichLayout("Single", map[string]any{
		"rows":       unifiedRows,
		"__typename": "GenATableUXPrimitive",
	}))
	return b
}

// newRichLayout membungkus primitif jadi view_model GenAI (Single =
// primitive tunggal; HScroll = primitives jamak).
func newRichLayout(name string, data any) map[string]any {
	vm := map[string]any{"__typename": "GenAI" + name + "LayoutViewModel"}
	if arr, ok := data.([]any); ok {
		vm["primitives"] = arr
	} else {
		vm["primitive"] = data
	}
	return map[string]any{"view_model": vm}
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

// build merakit pesan rich LENGKAP (submessages + unified base64 +
// envelope bot). Dipisah dari Send supaya bisa di-unit-test tanpa koneksi.
func (b *AIRichBuilder) build() *waE2E.Message {
	sections := b.sections
	unified := map[string]any{
		"response_id": fmt.Sprintf("resp-%d", time.Now().UnixNano()),
		"sections":    sections,
	}
	data, _ := json.Marshal(unified)
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
				RichResponseMessage: &waE2E.AIRichResponseMessage{
					MessageType: waAICommonDeprecated.AIRichResponseMessageType_AI_RICH_RESPONSE_TYPE_STANDARD.Enum(),
					Submessages: b.submessages,
					UnifiedResponse: &waAICommon.AIRichResponseUnifiedResponse{
						Data: []byte(base64.StdEncoding.EncodeToString(data)),
					},
					ContextInfo: richForwardedContext(),
				},
			},
		},
	}
}

// Send mengirim pesan rich MENTAH (varian menang V1 — tanpa wrapper
// ViewOnceMessage yang terbukti menghilangkan pesan). Error = gagal kirim.
func (b *AIRichBuilder) Send(ctx context.Context, mctx *store.MessageContext) (types.MessageID, error) {
	if len(b.submessages) == 0 {
		return "", fmt.Errorf("AIRich butuh minimal satu blok (AddText/AddTable)")
	}
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, b.build())
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

// RichBizNodes adalah additional node stanza untuk pesan interaktif
// (cermin YuukiBot buildBizNodes: biz/native_flow + bot{biz_bot=1} untuk
// non-grup). Disimpan untuk eksperimen tombol interaktif berikutnya —
// TIDAK dipakai pengiriman AIRich (varian biz terbukti tak menambah nilai
// di sini, mentah sudah tampil).
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
