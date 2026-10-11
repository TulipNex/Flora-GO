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

const nexaRouterEndpoint = "https://router.nexaworks.web.id/v1/chat/completions"

// NexaRouterAdapter adalah implementasi ProviderAdapter untuk NexaRouter
// (https://router.nexaworks.web.id). API-nya OpenAI-compatible
// (endpoint /v1/chat/completions, format messages/tool_calls ala OpenAI,
// auth "Authorization: Bearer <key>"), sehingga adapter ini REUSE seluruh
// struct & fungsi konversi bersama (openAIRequest, openAIMessage,
// toOpenAIMessages, toOpenAITools, fromOpenAIMessage — semua di
// openai_common.go), tidak menduplikasi logic konversi Content/Part <-> format OpenAI.
type NexaRouterAdapter struct {
	APIKey     string
	Model      string
	HTTPClient *http.Client
}

// NewNexaRouterAdapter membuat adapter dengan timeout default 60 detik,
// sama seperti adapter provider lain.
func NewNexaRouterAdapter(apiKey, model string) *NexaRouterAdapter {
	return &NexaRouterAdapter{
		APIKey: apiKey,
		Model:  model,
		HTTPClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

func (n *NexaRouterAdapter) Generate(ctx context.Context, params GenerateParams) (*AgentResponse, error) {
	if n.APIKey == "" {
		return nil, fmt.Errorf("nexarouter: API key kosong")
	}
	if n.Model == "" {
		return nil, fmt.Errorf("nexarouter: model kosong")
	}

	// Field "tools" DIKIRIM — dokumentasi resmi
	// NexaRouter menyebut "tools" sebagai parameter yang dikenal (lihat tabel
	// error 400-nya) dan wire Anthropic-nya mendukung tools, jadi ada peluang
	// endpoint OpenAI-compatible-nya juga mendukung. Kalau ternyata server
	// menolak request KARENA field tools (400 + pesan menyinggung tools),
	// otomatis dicoba ulang SEKALI tanpa tools (fallback mode chat teks,
	// perilaku lama). Jadi skenario terburuknya sama seperti sebelum
	// perubahan ini, tidak ada regresi.
	tools := toOpenAITools(params.Tools)
	start := time.Now()
	parsed, toolsRejected, err := n.postChat(ctx, params, tools)
	if err != nil {
		return nil, err
	}
	if toolsRejected {
		logfmt.Warn("nexarouter: server menolak field tools — fallback mode teks (tanpa tool).\n")
		parsed, _, err = n.postChat(ctx, params, nil)
		if err != nil {
			return nil, err
		}
	}
	durationMs := time.Since(start).Milliseconds()

	if len(parsed.Choices) == 0 {
		return nil, fmt.Errorf("nexarouter: tidak ada choice dalam response")
	}

	return fillOpenAIResponse(fromOpenAIMessage(parsed.Choices[0].Message), parsed, "nexarouter", n.Model, durationMs), nil
}

// postChat mengirim satu request chat ke NexaRouter. toolsRejected bernilai
// true HANYA kalau server mengembalikan 400 yang berkaitan dengan field
// tools — satu-satunya kondisi di mana pemanggil boleh mencoba ulang tanpa
// tools. Error lain (auth, saldo, model, network) diteruskan apa adanya.
func (n *NexaRouterAdapter) postChat(ctx context.Context, params GenerateParams, tools []openAITool) (*openAIResponse, bool, error) {
	reqBody := openAIRequest{
		Model:    n.Model,
		Messages: toOpenAIMessages(params.SystemPrompt, params.History),
		Tools:    tools,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, false, fmt.Errorf("nexarouter: gagal marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, nexaRouterEndpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, false, fmt.Errorf("nexarouter: gagal bangun request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+n.APIKey)

	resp, err := n.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, false, fmt.Errorf("nexarouter: request gagal: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, fmt.Errorf("nexarouter: gagal baca response: %w", err)
	}

	var parsed openAIResponse
	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		// Body bukan JSON + status 400 saat tools dikirim = sangat mungkin
		// penolakan terhadap field tools (format error tak dikenal).
		if resp.StatusCode == http.StatusBadRequest && len(tools) > 0 {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("nexarouter: gagal parse response (status %d): %w", resp.StatusCode, err)
	}

	if resp.StatusCode == http.StatusBadRequest && len(tools) > 0 && parsed.Error != nil &&
		strings.Contains(strings.ToLower(parsed.Error.Message), "tool") {
		return nil, true, nil
	}

	if parsed.Error != nil {
		return nil, false, fmt.Errorf("nexarouter: API error (%v): %s", parsed.Error.Code, parsed.Error.Message)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("nexarouter: status HTTP %d, body: %s", resp.StatusCode, string(respBytes))
	}

	return &parsed, false, nil
}
