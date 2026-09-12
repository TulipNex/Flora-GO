package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
// Content dipakai untuk teks biasa; ToolCalls dipakai saat assistant
// meminta pemanggilan tool; ToolCallID+Name dipakai saat role="tool"
// (hasil eksekusi tool dikirim balik).
type openAIMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	Name       string           `json:"name,omitempty"`
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
	Error   *openAIError   `json:"error,omitempty"`
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
			for _, p := range c.Parts {
				if p.FunctionResponse != nil {
					result, _ := json.Marshal(p.FunctionResponse.Response)
					messages = append(messages, openAIMessage{
						Role:       "tool",
						Content:    string(result),
						ToolCallID: p.FunctionResponse.Name, // lihat catatan di fromOpenAIMessage soal pemetaan ID
						Name:       p.FunctionResponse.Name,
					})
					continue
				}
				text += p.Text
			}
			if text != "" {
				messages = append(messages, openAIMessage{Role: "user", Content: text})
			}

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
						ID:   p.FunctionCall.Name, // lihat catatan di bawah
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

// Catatan soal ToolCallID: OpenAI mensyaratkan tool_call_id pada message
// role="tool" cocok PERSIS dengan id yang diterbitkan assistant di
// tool_calls sebelumnya. Struct ToolCall internal (provider.go) TIDAK
// membawa field ID terpisah (didesain generik utk Gemini yang tidak
// mengenal konsep ID ini) — jadi sebagai jembatan, adapter ini memakai
// tc.Name sebagai pengganti ID pada kedua sisi (request tool_calls DAN
// balasan tool result). Ini AMAN untuk pola pemakaian bot ini karena
// loop.go men-dedup pemanggilan tool yang sama persis dalam satu giliran
// (lihat seen[key] di loop.go) — tidak pernah ada dua tool_call dengan
// Name identik aktif bersamaan yang butuh dibedakan lewat ID asli.
// Kalau nanti loop.go direvisi untuk mengizinkan tool yang sama dipanggil
// paralel dengan argumen beda dalam satu giliran, jembatan Name-sebagai-ID
// ini perlu diganti dengan ID asli yang dibawa lewat field baru.

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
func fromOpenAIMessage(msg openAIMessage) *AgentResponse {
	var toolCalls []ToolCall
	for _, tc := range msg.ToolCalls {
		var args map[string]interface{}
		_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
		toolCalls = append(toolCalls, ToolCall{
			Name: tc.Function.Name,
			Args: args,
			// ThoughtSignature sengaja dibiarkan kosong — OpenAI-compatible
			// API tidak punya konsep ini, hanya relevan untuk Gemini.
		})
	}
	return &AgentResponse{
		Text:      msg.Content,
		ToolCalls: toolCalls,
	}
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

	resp, err := o.HTTPClient.Do(httpReq)
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

	return fromOpenAIMessage(parsed.Choices[0].Message), nil
}
