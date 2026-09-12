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

const nexaRouterEndpoint = "https://router.nexaworks.web.id/v1/chat/completions"

// NexaRouterAdapter adalah implementasi ProviderAdapter untuk NexaRouter
// (https://router.nexaworks.web.id), provider ketiga di samping Gemini &
// OpenRouter. API-nya JUGA OpenAI-compatible persis seperti OpenRouter
// (endpoint /v1/chat/completions, format messages/tool_calls ala OpenAI,
// auth "Authorization: Bearer <key>") — HANYA beda endpoint URL, sehingga
// adapter ini REUSE seluruh struct & fungsi konversi yang sudah ditulis
// untuk OpenRouter (openAIRequest, openAIMessage, toOpenAIMessages,
// toOpenAITools, fromOpenAIMessage — semua di openrouter_adapter.go),
// tidak menduplikasi logic konversi Content/Part <-> format OpenAI.
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

	// PENTING: field "tools" SENGAJA TIDAK disertakan, apapun isi
	// params.Tools. Dokumentasi resmi NexaRouter (router.nexaworks.web.id)
	// hanya mendaftarkan model, messages, stream, temperature, max_tokens
	// sebagai parameter yang didukung — tidak ada tools/function-calling
	// sama sekali. Percobaan nyata mengirim field tools menghasilkan
	// 400 invalid_request_error ("Request tidak valid"). Konsekuensinya:
	// saat NexaRouter jadi provider aktif, AI TIDAK BISA memicu tool
	// (kick_member, promote_member, dll) — hanya bisa chat teks biasa.
	// Ini batasan provider itu sendiri, bukan bug adapter.
	reqBody := openAIRequest{
		Model:    n.Model,
		Messages: toOpenAIMessages(params.SystemPrompt, params.History),
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("nexarouter: gagal marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, nexaRouterEndpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("nexarouter: gagal bangun request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+n.APIKey)

	resp, err := n.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("nexarouter: request gagal: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("nexarouter: gagal baca response: %w", err)
	}

	var parsed openAIResponse
	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		return nil, fmt.Errorf("nexarouter: gagal parse response (status %d): %w", resp.StatusCode, err)
	}

	if parsed.Error != nil {
		return nil, fmt.Errorf("nexarouter: API error (%v): %s", parsed.Error.Code, parsed.Error.Message)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("nexarouter: status HTTP %d, body: %s", resp.StatusCode, string(respBytes))
	}

	if len(parsed.Choices) == 0 {
		return nil, fmt.Errorf("nexarouter: tidak ada choice dalam response")
	}

	return fromOpenAIMessage(parsed.Choices[0].Message), nil
}
