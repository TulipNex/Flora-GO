package messaging

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"wabot-go/internal/store"

	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/proto/waAICommon"
	"go.mau.fi/whatsmeow/proto/waAICommonDeprecated"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// richMetaAIBotJID adalah JID bot Meta AI asli (dari referensi NIXCODE).
// Versi port sebelumnya memakai "0@bot" (dari port YuukiBot) — kemungkinan
// penyebab render ditolak klien yang memvalidasi identitas bot.
const richMetaAIBotJID = "867051314767696@bot"

// richAlertSubmessage adalah submessage placeholder "[ UNSUPPORTED_TYPE - X ]"
// (type TEXT) yang WAJIB menyertai section non-teks/tabel/kode (source,
// suggest, widget) menurut referensi — tanpanya section yatim diabaikan
// klien (pelajaran uji Sep 2026: source & suggest tak tampil).
func richAlertSubmessage(typename string) *waAICommonDeprecated.AIRichResponseSubMessage {
	return &waAICommonDeprecated.AIRichResponseSubMessage{
		MessageType: waAICommonDeprecated.AIRichResponseSubMessageType_AI_RICH_RESPONSE_TEXT.Enum(),
		MessageText: proto.String("[ UNSUPPORTED_TYPE - " + typename + "]"),
	}
}

// richCodeKeywords adalah kata kunci per bahasa untuk highlight (cermin
// referensi; bahasa tak dikenal = satu blok DEFAULT utuh).
var richCodeKeywords = map[string]map[string]bool{
	"go": {
		"break": true, "case": true, "chan": true, "const": true, "continue": true,
		"default": true, "defer": true, "else": true, "fallthrough": true, "for": true,
		"func": true, "go": true, "goto": true, "if": true, "import": true,
		"interface": true, "map": true, "package": true, "range": true, "return": true,
		"select": true, "struct": true, "switch": true, "type": true, "var": true,
	},
	"javascript": {
		"break": true, "case": true, "catch": true, "class": true, "const": true,
		"continue": true, "debugger": true, "default": true, "delete": true, "do": true,
		"else": true, "export": true, "extends": true, "finally": true, "for": true,
		"function": true, "if": true, "import": true, "in": true, "instanceof": true,
		"let": true, "new": true, "return": true, "static": true, "super": true,
		"switch": true, "this": true, "throw": true, "try": true, "typeof": true,
		"var": true, "void": true, "while": true, "with": true, "yield": true,
		"true": true, "false": true, "null": true, "undefined": true, "async": true,
		"await": true,
	},
	"python": {
		"False": true, "None": true, "True": true, "and": true, "as": true,
		"assert": true, "async": true, "await": true, "break": true, "class": true,
		"continue": true, "def": true, "del": true, "elif": true, "else": true,
		"except": true, "finally": true, "for": true, "from": true, "global": true,
		"if": true, "import": true, "in": true, "is": true, "lambda": true,
		"nonlocal": true, "not": true, "or": true, "pass": true, "raise": true,
		"return": true, "try": true, "while": true, "with": true, "yield": true,
	},
}

type richCodeHighlight = waAICommonDeprecated.AIRichResponseCodeMetadata_AIRichResponseCodeHighlightType

const (
	richHlDefault richCodeHighlight = waAICommonDeprecated.AIRichResponseCodeMetadata_AI_RICH_RESPONSE_CODE_HIGHLIGHT_DEFAULT
	richHlKeyword richCodeHighlight = waAICommonDeprecated.AIRichResponseCodeMetadata_AI_RICH_RESPONSE_CODE_HIGHLIGHT_KEYWORD
	richHlMethod  richCodeHighlight = waAICommonDeprecated.AIRichResponseCodeMetadata_AI_RICH_RESPONSE_CODE_HIGHLIGHT_METHOD
	richHlString  richCodeHighlight = waAICommonDeprecated.AIRichResponseCodeMetadata_AI_RICH_RESPONSE_CODE_HIGHLIGHT_STRING
	richHlNumber  richCodeHighlight = waAICommonDeprecated.AIRichResponseCodeMetadata_AI_RICH_RESPONSE_CODE_HIGHLIGHT_NUMBER
	richHlComment richCodeHighlight = waAICommonDeprecated.AIRichResponseCodeMetadata_AI_RICH_RESPONSE_CODE_HIGHLIGHT_COMMENT
)

// tokenizeRichCode memecah kode jadi blok highlight: komentar (//...),
// string ("..."), angka, keyword bahasa, method (kata + "("), sisanya default.
// Token se-tipe yang berurutan digabung (cermin referensi).
func tokenizeRichCode(code, lang string) []*waAICommonDeprecated.AIRichResponseCodeMetadata_AIRichResponseCodeBlock {
	keywords := richCodeKeywords[strings.ToLower(lang)]
	push := func(tokens []*waAICommonDeprecated.AIRichResponseCodeMetadata_AIRichResponseCodeBlock, content string, typ richCodeHighlight) []*waAICommonDeprecated.AIRichResponseCodeMetadata_AIRichResponseCodeBlock {
		if content == "" {
			return tokens
		}
		if n := len(tokens); n > 0 && tokens[n-1].GetHighlightType() == typ {
			tokens[n-1].CodeContent = proto.String(tokens[n-1].GetCodeContent() + content)
			return tokens
		}
		return append(tokens, &waAICommonDeprecated.AIRichResponseCodeMetadata_AIRichResponseCodeBlock{
			HighlightType: typ.Enum(),
			CodeContent:   proto.String(content),
		})
	}
	var tokens []*waAICommonDeprecated.AIRichResponseCodeMetadata_AIRichResponseCodeBlock
	isSpace := func(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }
	isWord := func(c byte) bool {
		return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '$'
	}
	i := 0
	for i < len(code) {
		c := code[i]
		switch {
		case isSpace(c):
			s := i
			for i < len(code) && isSpace(code[i]) {
				i++
			}
			tokens = push(tokens, code[s:i], richHlDefault)
		case c == '/' && i+1 < len(code) && code[i+1] == '/':
			s := i
			for i < len(code) && code[i] != '\n' {
				i++
			}
			tokens = push(tokens, code[s:i], richHlComment)
		case c == '"' || c == '\'' || c == '`':
			s, q := i, c
			i++
			for i < len(code) {
				if code[i] == '\\' && i+1 < len(code) {
					i += 2
				} else if code[i] == q {
					i++
					break
				} else {
					i++
				}
			}
			tokens = push(tokens, code[s:i], richHlString)
		case c >= '0' && c <= '9':
			s := i
			for i < len(code) && (code[i] >= '0' && code[i] <= '9' || code[i] == '.' || code[i] == '_') {
				i++
			}
			tokens = push(tokens, code[s:i], richHlNumber)
		case isWord(c):
			s := i
			for i < len(code) && isWord(code[i]) {
				i++
			}
			word := code[s:i]
			typ := richHlDefault
			if keywords[word] {
				typ = richHlKeyword
			} else {
				j := i
				for j < len(code) && (code[j] == ' ' || code[j] == '\t') {
					j++
				}
				if j < len(code) && code[j] == '(' {
					typ = richHlMethod
				}
			}
			tokens = push(tokens, word, typ)
		default:
			tokens = push(tokens, string(c), richHlDefault)
			i++
		}
	}
	if len(tokens) == 0 {
		tokens = push(tokens, code, richHlDefault)
	}
	return tokens
}

// unifiedRichCodeBlocks memetakan blok highlight ke bentuk unified
// ({content, type: DEFAULT|KEYWORD|METHOD|STR|NUMBER|COMMENT}).
func unifiedRichCodeBlocks(blocks []*waAICommonDeprecated.AIRichResponseCodeMetadata_AIRichResponseCodeBlock) []any {
	typeMap := map[richCodeHighlight]string{
		richHlDefault: "DEFAULT",
		richHlKeyword: "KEYWORD",
		richHlMethod:  "METHOD",
		richHlString:  "STR",
		richHlNumber:  "NUMBER",
		richHlComment: "COMMENT",
	}
	var out []any
	for _, b := range blocks {
		out = append(out, map[string]any{
			"content": b.GetCodeContent(),
			"type":    typeMap[b.GetHighlightType()],
		})
	}
	return out
}

// Builder AI Rich (pesan kaya ala bot Meta AI). Referensi struktur:
// YuukiBot (Riflxz) messagebuilder.go (NIXCODE port) — lisensinya mewajibkan
// credit, maka dicatat di sini; implementasi ditulis ulang clean-room dari
// definisi proto, bukan salinan kode.
//
// VONIS EKSPERIMEN (.richtest, Sep 2026): varian MENTAH (V1) tampil rich,
// +biz nodes (V3) tampil juga, bungkus ViewOnceMessage (V2/V4) HILANG TOTAL.
// Jadi: kirim mentah SELALU, tanpa wrapper. Kode varian + command uji sudah
// dihapus.

// AIRichBuilder merakit blok teks, tabel, kode, sumber, tip, dan saran
// GenAI. Pola tiap Add*: submessage proto + section unified. Referensi:
// YuukiBot messagebuilder.go (AIRich).
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
			BotJID: proto.String(richMetaAIBotJID),
		},
		ForwardOrigin: waE2E.ContextInfo_META_AI.Enum(),
	}
}

// AddCode menambah blok kode dengan highlight (GenAICodeUXPrimitive).
func (b *AIRichBuilder) AddCode(language, code string) *AIRichBuilder {
	blocks := tokenizeRichCode(code, language)
	b.submessages = append(b.submessages, &waAICommonDeprecated.AIRichResponseSubMessage{
		MessageType: waAICommonDeprecated.AIRichResponseSubMessageType_AI_RICH_RESPONSE_CODE.Enum(),
		CodeMetadata: &waAICommonDeprecated.AIRichResponseCodeMetadata{
			CodeLanguage: proto.String(language),
			CodeBlocks:   blocks,
		},
	})
	b.sections = append(b.sections, newRichLayout("Single", map[string]any{
		"language":    language,
		"code_blocks": unifiedRichCodeBlocks(blocks),
		"__typename":  "GenAICodeUXPrimitive",
	}))
	return b
}

// AddSource menambah kartu sumber referensi (GenAISearchResultPrimitive).
// Satu panggilan = satu sumber {iconURL, url, displayName}. CATATAN: versi
// whatsmeow terpasang tak punya field BotMetadata.RichResponseSourcesMetadata
// (ada di versi referensi, hilang di upstream) — sumber hanya dibawa di
// section unified (cukup untuk render; metadata proto-nya opsional).
func (b *AIRichBuilder) AddSource(iconURL, url, displayName string) *AIRichBuilder {
	b.submessages = append(b.submessages, richAlertSubmessage("GenAISearchResultPrimitive"))
	b.sections = append(b.sections, newRichLayout("Single", map[string]any{
		"sources": []any{
			map[string]any{
				"source_type":         "THIRD_PARTY",
				"source_display_name": displayName,
				"source_subtitle":     "AI",
				"source_url":          url,
				"favicon": map[string]any{
					"url":       iconURL,
					"mime_type": "image/jpeg",
					"width":     16,
					"height":    16,
				},
			},
		},
		"__typename": "GenAISearchResultPrimitive",
	}))
	return b
}

// AddTip menambah teks metadata kecil (GenAIMetadataTextPrimitive). Section
// memakai prefix "? " seperti referensi (tanpanya tip tak tampil).
func (b *AIRichBuilder) AddTip(text string) *AIRichBuilder {
	b.submessages = append(b.submessages, &waAICommonDeprecated.AIRichResponseSubMessage{
		MessageType: waAICommonDeprecated.AIRichResponseSubMessageType_AI_RICH_RESPONSE_TEXT.Enum(),
		MessageText: proto.String(text),
	})
	b.sections = append(b.sections, newRichLayout("Single", map[string]any{
		"text":       "? " + text,
		"__typename": "GenAIMetadataTextPrimitive",
	}))
	return b
}

// AddSuggest menambah saran follow-up (GenAIFollowUpSuggestionPillPrimitive):
// 1 saran = layout Single, >1 = HScroll — plus submessage alert WAJIB
// (tanpanya section yatim, pelajaran uji Sep 2026).
func (b *AIRichBuilder) AddSuggest(suggestions []string) *AIRichBuilder {
	var pills []any
	for _, s := range suggestions {
		pills = append(pills, map[string]any{
			"prompt_text": s,
			"prompt_type": "SUGGESTED_PROMPT",
			"__typename":  "GenAIFollowUpSuggestionPillPrimitive",
		})
	}
	b.submessages = append(b.submessages, richAlertSubmessage("GenAIFollowUpSuggestionPillPrimitive"))
	if len(pills) == 1 {
		b.sections = append(b.sections, newRichLayout("Single", pills[0]))
	} else {
		b.sections = append(b.sections, newRichLayout("HScroll", pills))
	}
	return b
}

// build merakit pesan rich LENGKAP (submessages + unified base64 +
// envelope bot + metadata sumber). Dipisah dari Send supaya bisa
// di-unit-test tanpa koneksi.
func (b *AIRichBuilder) build() *waE2E.Message {
	// response_id + botResponseId unik per kiriman (cermin referensi yang
	// me-refresh keduanya tiap pesan dinamis).
	respID := fmt.Sprintf("resp-%d", time.Now().UnixNano())
	unified := map[string]any{
		"response_id": respID,
		"sections":    b.sections,
	}
	data, _ := json.Marshal(unified)
	return &waE2E.Message{
		MessageContextInfo: &waE2E.MessageContextInfo{
			DeviceListMetadata:        &waE2E.DeviceListMetadata{},
			DeviceListMetadataVersion: proto.Int32(2),
			BotMetadata: &waAICommon.BotMetadata{
				MessageDisclaimerText: proto.String(b.title),
				BotResponseID:         proto.String(respID),
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
// ViewOnceMessage yang terbukti menghilangkan pesan), lalu mengirim EDIT
// SUSULAN berisi konten yang sama (protocolMessage type 14 dalam
// BotForwardedMessage — cermin referensi NIXCODE; diduga pemicu render
// klien). Edit biasanya sunyi (tanpa notifikasi kedua). Error = gagal kirim.
func (b *AIRichBuilder) Send(ctx context.Context, mctx *store.MessageContext) (types.MessageID, error) {
	if len(b.submessages) == 0 && len(b.sections) == 0 {
		return "", fmt.Errorf("AIRich butuh minimal satu blok (AddText/AddTable/AddCode/dll)")
	}
	msg := b.build()
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, msg)
	if err != nil {
		return "", err
	}
	editMsg := &waE2E.Message{
		BotForwardedMessage: &waE2E.FutureProofMessage{
			Message: &waE2E.Message{
				ProtocolMessage: &waE2E.ProtocolMessage{
					Key: &waCommon.MessageKey{
						RemoteJID: proto.String(mctx.Chat.String()),
						FromMe:    proto.Bool(true),
						ID:        proto.String(string(resp.ID)),
					},
					Type:          waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
					EditedMessage: msg,
				},
			},
		},
	}
	if _, err := mctx.Client.SendMessage(ctx, mctx.Chat, editMsg); err != nil {
		fmt.Printf("⚠️ airich: edit susulan gagal (pesan utama %s tetap terkirim): %v\n", resp.ID, err)
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
