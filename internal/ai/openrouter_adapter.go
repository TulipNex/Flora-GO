package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const openRouterEndpoint = "https://openrouter.ai/api/v1/chat/completions"

// OpenRouterAdapter adalah implementasi ProviderAdapter untuk OpenRouter,
// yang API-nya OpenAI-compatible (endpoint /chat/completions, format
// messages/tool_calls ala OpenAI) — BERBEDA dari format Gemini
// (contents/parts) yang dipakai GeminiAdapter.
//
// Karena struct internal Content/Part (types.go) sudah dirancang meniru
// bentuk Gemini sejak Fase 1, adapter ini bertugas mengKONVERSI dua arah:
// Content/Part (universal, dipakai loop.go) <-> format OpenAI (dipakai
// saat komunikasi HTTP ke OpenRouter). Konversi terjadi HANYA di file
// ini — loop.go, plugins/ai.go, dan kode lain tidak perlu tahu bedanya.
type OpenRouterAdapter struct {
	APIKey     string
	Model      string
	HTTPClient *http.Client
}

// NewOpenRouterAdapter membuat adapter dengan timeout default 60 detik,
// sama seperti NewGeminiAdapter.
func NewOpenRouterAdapter(apiKey, model string) *OpenRouterAdapter {
	return &OpenRouterAdapter{
		APIKey: apiKey,
		Model:  model,
		HTTPClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// --- Struct request/response format OpenAI (khusus dipakai adapter ini) ---

type openAIRequest struct {
	Model    string          `json:"model"`
	Messages []openAIMessage `json:"messages"`
	Tools    []openAITool    `json:"tools,omitempty"`
}

// openAIMessage merepresentasikan satu baris "messages" ala OpenAI.
// Role bisa "system", "user", "assistant", atau "tool".
// Content bertipe any karena di wire OpenAI isinya BISA string polos
// (kasus umum) ATAU array blok multimodal [{type:text},{type:image_url}]
// saat pesan user membawa gambar (vision). ToolCalls dipakai saat
// assistant meminta pemanggilan tool; ToolCallID+Name dipakai saat
// role="tool" (hasil eksekusi tool dikirim balik).
type openAIMessage struct {
	Role       string           `json:"role"`
	Content    any              `json:"content,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	Name       string           `json:"name,omitempty"`
}

// openAIContentPart adalah satu blok dalam content multimodal:
// teks biasa atau gambar (data URL base64) untuk vision model.
type openAIContentPart struct {
	Type     string          `json:"type"` // "text" | "image_url"
	Text     string          `json:"text,omitempty"`
	ImageURL *openAIImageURL `json:"image_url,omitempty"`
}

// openAIImageURL membawa gambar sebagai data URL (bukan URL remote),
// supaya tidak perlu upload ke mana pun dulu — langsung ditempel base64.
type openAIImageURL struct {
	URL string `json:"url"` // format: data:<mime>;base64,<base64>
}

// openAIContentText membaca kembali Content balasan model jadi string.
// Balasan normal selalu string; array (sangat jarang untuk model teks)
// digabung dari blok-blok text-nya secara best-effort.
func openAIContentText(content any) string {
	switch c := content.(type) {
	case nil:
		return ""
	case string:
		return c
	case []any:
		var sb strings.Builder
		for _, item := range c {
			if m, ok := item.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					sb.WriteString(t)
				}
			}
		}
		return sb.String()
	default:
		return ""
	}
}

type openAIToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"` // selalu "function"
	Function openAIFunctionCall `json:"function"`
}

type openAIFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON-encoded string, BUKAN object langsung
}

type openAITool struct {
	Type     string             `json:"type"` // selalu "function"
	Function openAIToolFunction `json:"function"`
}

type openAIToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  ParameterSchema `json:"parameters"`
}

type openAIResponse struct {
	Choices []openAIChoice `json:"choices"`
	Usage   *openAIUsage   `json:"usage,omitempty"`
	Error   *openAIError   `json:"error,omitempty"`
}

// openAIUsage adalah pemakaian token gaya OpenAI (bila dikirim API).
type openAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type openAIChoice struct {
	Message      openAIMessage `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

type openAIError struct {
	Message string      `json:"message"`
	Type    string      `json:"type"`
	Code    interface{} `json:"code"`
}

// toolCallID mengembalikan ID asli sebuah FunctionCall/FunctionResponse
// kalau ada, fallback ke Name kalau ID kosong. Fallback ini HANYA jaring
// pengaman untuk histori lama yang tersimpan sebelum field ID ditambahkan
// (baris SQLite lama, atau Part yang datang dari GeminiAdapter yang
// memang tidak pernah mengisi ID) — untuk tool_call BARU dari OpenRouter/
// NexaRouter, ID asli selalu ada dan dipakai apa adanya.
func toolCallID(id, name string) string {
	if id != "" {
		return id
	}
	return name
}

// toOpenAIMessages mengkonversi []Content (format internal universal, dan
// systemPrompt terpisah) menjadi []openAIMessage ala OpenAI. Ini kebalikan
// dari GenerateRequest.SystemInstruction milik Gemini — OpenAI menaruh
// system prompt sebagai message pertama berrole "system", bukan field
// terpisah.
func toOpenAIMessages(systemPrompt string, history []Content) []openAIMessage {
	var messages []openAIMessage
	if systemPrompt != "" {
		messages = append(messages, openAIMessage{Role: "system", Content: systemPrompt})
	}

	for _, c := range history {
		switch c.Role {
		case "user":
			// Baris "user" bisa berisi teks biasa ATAU FunctionResponse
			// (hasil eksekusi tool yang dikirim balik, ditaruh loop.go
			// sebagai Content{Role: "user", Parts: [...FunctionResponse]}).
			// OpenAI membedakan ini lewat role terpisah ("tool"), jadi
			// perlu dipecah: FunctionResponse jadi message role="tool"
			// sendiri-sendiri, teks biasa jadi satu message role="user".
			var text string
			var images []Part
			for _, p := range c.Parts {
				if p.FunctionResponse != nil {
					result, _ := json.Marshal(p.FunctionResponse.Response)
					messages = append(messages, openAIMessage{
						Role:       "tool",
						Content:    string(result),
						ToolCallID: toolCallID(p.FunctionResponse.ID, p.FunctionResponse.Name),
						Name:       p.FunctionResponse.Name,
					})
					continue
				}
				text += p.Text
				if len(p.ImageBytes) > 0 {
					images = append(images, p)
				}
			}
			if len(images) == 0 {
				// Jalur lama tanpa gambar: content string polos, wire-nya
				// identik seperti sebelum fitur vision ada.
				if text != "" {
					messages = append(messages, openAIMessage{Role: "user", Content: text})
				}
				continue
			}
			// Jalur vision: content jadi array blok multimodal. Blok teks
			// selalu dulu (boleh kosong kalau user kirim gambar tanpa caption),
			// lalu satu blok image_url per gambar sebagai data URL base64.
			var blocks []openAIContentPart
			if text != "" {
				blocks = append(blocks, openAIContentPart{Type: "text", Text: text})
			}
			for _, p := range images {
				mime := p.ImageMime
				if mime == "" {
					mime = "image/jpeg"
				}
				blocks = append(blocks, openAIContentPart{
					Type: "image_url",
					ImageURL: &openAIImageURL{
						URL: "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(p.ImageBytes),
					},
				})
			}
			messages = append(messages, openAIMessage{Role: "user", Content: blocks})

		case "model":
			// Baris "model" bisa berisi teks biasa ATAU FunctionCall
			// (permintaan tool dari model, ditaruh loop.go sebagai
			// Content{Role: "model", Parts: [...FunctionCall]}).
			var text string
			var toolCalls []openAIToolCall
			for _, p := range c.Parts {
				if p.FunctionCall != nil {
					argsJSON, _ := json.Marshal(p.FunctionCall.Args)
					toolCalls = append(toolCalls, openAIToolCall{
						ID:   toolCallID(p.FunctionCall.ID, p.FunctionCall.Name),
						Type: "function",
						Function: openAIFunctionCall{
							Name:      p.FunctionCall.Name,
							Arguments: string(argsJSON),
						},
					})
					continue
				}
				text += p.Text
			}
			messages = append(messages, openAIMessage{
				Role:      "assistant",
				Content:   text,
				ToolCalls: toolCalls,
			})
		}
	}

	return messages
}

// toOpenAITools mengkonversi []FunctionDecl (format internal, dipakai
// bersama oleh Gemini & OpenRouter) menjadi []openAITool ala OpenAI.
func toOpenAITools(decls []FunctionDecl) []openAITool {
	if len(decls) == 0 {
		return nil
	}
	tools := make([]openAITool, 0, len(decls))
	for _, d := range decls {
		tools = append(tools, openAITool{
			Type: "function",
			Function: openAIToolFunction{
				Name:        d.Name,
				Description: d.Description,
				Parameters:  d.Parameters,
			},
		})
	}
	return tools
}

// fromOpenAIMessage mengkonversi satu openAIMessage hasil balasan model
// menjadi AgentResponse (format internal universal yang dipahami loop.go).
//
// ID tool_call asli (tc.ID, mis. "call_xyz...") disimpan apa adanya ke
// ToolCall.ID — TIDAK lagi dibuang/diganti Name seperti sebelumnya. ID ini
// nanti di-echo balik oleh loop.go saat menyusun ulang history (lewat
// FunctionCall.ID dan FunctionResponse.ID), lalu dibaca lagi oleh
// toOpenAIMessages di atas via toolCallID(). Ini memperbaiki potensi
// tool_call_id mismatch/HTTP 400 kalau suatu saat model minta tool yang
// sama 2x dengan argumen berbeda dalam satu giliran (dulu keduanya akan
// dapat ID = Name yang identik dan ambigu bagi provider).
func fromOpenAIMessage(msg openAIMessage) *AgentResponse {
	var toolCalls []ToolCall
	for _, tc := range msg.ToolCalls {
		var args map[string]interface{}
		_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
		toolCalls = append(toolCalls, ToolCall{
			ID:   tc.ID,
			Name: tc.Function.Name,
			Args: args,
			// ThoughtSignature sengaja dibiarkan kosong — OpenAI-compatible
			// API tidak punya konsep ini, hanya relevan untuk Gemini.
		})
	}
	return &AgentResponse{
		Text:      openAIContentText(msg.Content),
		ToolCalls: toolCalls,
	}
}

// fillOpenAIResponse melengkapi AgentResponse dengan usage/finish-reason
// dari respons API + durasi + identitas provider/model (observabilitas log).
func fillOpenAIResponse(resp *AgentResponse, parsed *openAIResponse, provider, model string, durationMs int64) *AgentResponse {
	if len(parsed.Choices) > 0 {
		resp.FinishReason = parsed.Choices[0].FinishReason
	}
	if parsed.Usage != nil {
		resp.Usage = TokenUsage{
			Prompt:     parsed.Usage.PromptTokens,
			Completion: parsed.Usage.CompletionTokens,
			Total:      parsed.Usage.TotalTokens,
		}
	}
	resp.DurationMs = durationMs
	resp.Provider = provider
	resp.Model = model
	return resp
}

func (o *OpenRouterAdapter) Generate(ctx context.Context, params GenerateParams) (*AgentResponse, error) {
	if o.APIKey == "" {
		return nil, fmt.Errorf("openrouter: API key kosong")
	}
	if o.Model == "" {
		return nil, fmt.Errorf("openrouter: model kosong")
	}

	reqBody := openAIRequest{
		Model:    o.Model,
		Messages: toOpenAIMessages(params.SystemPrompt, params.History),
		Tools:    toOpenAITools(params.Tools),
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("openrouter: gagal marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, openRouterEndpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("openrouter: gagal bangun request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+o.APIKey)

	start := time.Now()
	resp, err := o.HTTPClient.Do(httpReq)
	durationMs := time.Since(start).Milliseconds()
	if err != nil {
		return nil, fmt.Errorf("openrouter: request gagal: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("openrouter: gagal baca response: %w", err)
	}

	var parsed openAIResponse
	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		return nil, fmt.Errorf("openrouter: gagal parse response (status %d): %w", resp.StatusCode, err)
	}

	if parsed.Error != nil {
		return nil, fmt.Errorf("openrouter: API error (%v): %s", parsed.Error.Code, parsed.Error.Message)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openrouter: status HTTP %d, body: %s", resp.StatusCode, string(respBytes))
	}

	if len(parsed.Choices) == 0 {
		return nil, fmt.Errorf("openrouter: tidak ada choice dalam response")
	}

	return fillOpenAIResponse(fromOpenAIMessage(parsed.Choices[0].Message), &parsed, "openrouter", o.Model, durationMs), nil
}
