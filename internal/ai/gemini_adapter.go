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

const geminiEndpointFmt = "https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent"

// GeminiAdapter adalah implementasi ProviderAdapter untuk Gemini API.
// Sejak Fase 3: mendukung native function-calling (Tools di request,
// FunctionCall di-parse dari response, termasuk ThoughtSignature-nya).
type GeminiAdapter struct {
	APIKey     string
	Model      string
	HTTPClient *http.Client
}

// NewGeminiAdapter membuat adapter dengan timeout default 60 detik.
func NewGeminiAdapter(apiKey, model string) *GeminiAdapter {
	return &GeminiAdapter{
		APIKey: apiKey,
		Model:  model,
		HTTPClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

func (g *GeminiAdapter) Generate(ctx context.Context, params GenerateParams) (*AgentResponse, error) {
	if g.APIKey == "" {
		return nil, fmt.Errorf("gemini: API key kosong")
	}
	if g.Model == "" {
		return nil, fmt.Errorf("gemini: model kosong")
	}

	reqBody := GenerateRequest{
		Contents: params.History,
	}
	if params.SystemPrompt != "" {
		reqBody.SystemInstruction = &Content{
			Parts: []Part{{Text: params.SystemPrompt}},
		}
	}
	if len(params.Tools) > 0 {
		reqBody.Tools = []ToolDeclaration{
			{FunctionDeclarations: params.Tools},
		}
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("gemini: gagal marshal request: %w", err)
	}

	url := fmt.Sprintf(geminiEndpointFmt, g.Model)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("gemini: gagal bangun request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-goog-api-key", g.APIKey)

	resp, err := g.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("gemini: request gagal: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("gemini: gagal baca response: %w", err)
	}

	var parsed generateResponse
	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		return nil, fmt.Errorf("gemini: gagal parse response (status %d): %w", resp.StatusCode, err)
	}

	if parsed.Error != nil {
		return nil, fmt.Errorf("gemini: API error %d (%s): %s", parsed.Error.Code, parsed.Error.Status, parsed.Error.Message)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gemini: status HTTP %d, body: %s", resp.StatusCode, string(respBytes))
	}

	if len(parsed.Candidates) == 0 {
		return nil, fmt.Errorf("gemini: tidak ada candidate dalam response (kemungkinan diblokir safety filter)")
	}

	text := ""
	var toolCalls []ToolCall
	for _, part := range parsed.Candidates[0].Content.Parts {
		if part.FunctionCall != nil {
			toolCalls = append(toolCalls, ToolCall{
				Name:             part.FunctionCall.Name,
				Args:             part.FunctionCall.Args,
				ThoughtSignature: part.ThoughtSignature,
			})
			continue
		}
		text += part.Text
	}

	return &AgentResponse{
		Text:      text,
		ToolCalls: toolCalls,
	}, nil
}
