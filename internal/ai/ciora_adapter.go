package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"wabot-go/internal/logfmt"
)

const cioraEndpoint = "https://ciora.id/v1/chat/completions"

// CioraAdapter adalah implementasi ProviderAdapter untuk Ciora AI
// (https://ciora.id), provider utama Flora. API-nya OpenAI-compatible
// (endpoint /v1/chat/completions, format messages/tool_calls ala OpenAI,
// auth "Authorization: Bearer <key>") dan dokumentasinya menyatakan
// mendukung tool calling standar OpenAI — jadi adapter ini REUSE seluruh
// struct & fungsi konversi bersama (openAIRequest, openAIMessage,
// toOpenAIMessages, toOpenAITools, fromOpenAIMessage — semua di
// openai_common.go), sama seperti yang dilakukan NexaRouterAdapter.
type CioraAdapter struct {
	APIKey     string
	Model      string
	HTTPClient *http.Client
}

// NewCioraAdapter membuat adapter dengan timeout default 60 detik,
// sama seperti adapter provider lain.
func NewCioraAdapter(apiKey, model string) *CioraAdapter {
	return &CioraAdapter{
		APIKey: apiKey,
		Model:  model,
		HTTPClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

func (c *CioraAdapter) Generate(ctx context.Context, params GenerateParams) (*AgentResponse, error) {
	if c.APIKey == "" {
		return nil, fmt.Errorf("ciora: API key kosong")
	}
	if c.Model == "" {
		return nil, fmt.Errorf("ciora: model kosong")
	}

	// Field "tools" DIKIRIM (Ciora menyatakan mendukung function calling
	// standar OpenAI). Kalau ternyata server menolak request KARENA field
	// tools (400 + pesan menyinggung tools), otomatis dicoba ulang SEKALI
	// tanpa tools (fallback mode chat teks) — pola yang sama dengan
	// NexaRouterAdapter.
	tools := toOpenAITools(params.Tools)
	start := time.Now()
	parsed, toolsRejected, err := c.postChat(ctx, params, tools)
	if err != nil {
		return nil, err
	}
	if toolsRejected {
		logfmt.Warn("ciora: server menolak field tools — fallback mode teks (tanpa tool).\n")
		parsed, _, err = c.postChat(ctx, params, nil)
		if err != nil {
			return nil, err
		}
	}
	durationMs := time.Since(start).Milliseconds()

	if len(parsed.Choices) == 0 {
		return nil, fmt.Errorf("ciora: tidak ada choice dalam response")
	}

	return fillOpenAIResponse(fromOpenAIMessage(parsed.Choices[0].Message), parsed, "ciora", c.Model, durationMs), nil
}

// postChat mengirim satu request chat ke Ciora AI. toolsRejected bernilai
// true HANYA kalau server mengembalikan 400 yang berkaitan dengan field
// tools — satu-satunya kondisi di mana pemanggil boleh mencoba ulang tanpa
// tools. Error lain (auth, saldo, model, network) diteruskan apa adanya.
func (c *CioraAdapter) postChat(ctx context.Context, params GenerateParams, tools []openAITool) (*openAIResponse, bool, error) {
	reqBody := openAIRequest{
		Model:    c.Model,
		Messages: toOpenAIMessages(params.SystemPrompt, params.History),
		Tools:    tools,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, false, fmt.Errorf("ciora: gagal marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, cioraEndpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, false, fmt.Errorf("ciora: gagal bangun request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, false, fmt.Errorf("ciora: request gagal: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, fmt.Errorf("ciora: gagal baca response: %w", err)
	}

	var parsed openAIResponse
	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		// Body bukan JSON + status 400 saat tools dikirim = sangat mungkin
		// penolakan terhadap field tools (format error tak dikenal).
		if resp.StatusCode == http.StatusBadRequest && len(tools) > 0 {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("ciora: gagal parse response (status %d): %w", resp.StatusCode, err)
	}

	if resp.StatusCode == http.StatusBadRequest && len(tools) > 0 && parsed.Error != nil &&
		strings.Contains(strings.ToLower(parsed.Error.Message), "tool") {
		return nil, true, nil
	}

	if parsed.Error != nil {
		return nil, false, fmt.Errorf("ciora: API error (%v): %s", parsed.Error.Code, parsed.Error.Message)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("ciora: status HTTP %d, body: %s", resp.StatusCode, string(respBytes))
	}

	return &parsed, false, nil
}
