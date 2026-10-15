package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"wabot-go/internal/logfmt"
)

// GenericOpenAIAdapter adalah implementasi ProviderAdapter untuk SEMUA
// provider OpenAI-compatible (endpoint /v1/chat/completions, format
// messages/tool_calls ala OpenAI, auth "Authorization: Bearer <key>").
// Provider dibedakan hanya oleh Name + Endpoint — tambah provider baru
// cukup lewat registry (command), tanpa file adapter baru. REUSE seluruh
// struct & fungsi konversi bersama (openAIRequest, openAIMessage,
// toOpenAIMessages, toOpenAITools, fromOpenAIMessage — semua di
// openai_common.go), tidak menduplikasi logic konversi Content/Part.
type GenericOpenAIAdapter struct {
	Name       string
	Endpoint   string
	APIKey     string
	Model      string
	HTTPClient *http.Client
}

// NewGenericOpenAIAdapter membuat adapter dengan timeout default 60 detik,
// sama seperti adapter lama (nexarouter/ciora) yang dileburnya.
func NewGenericOpenAIAdapter(name, endpoint, apiKey, model string) *GenericOpenAIAdapter {
	return &GenericOpenAIAdapter{
		Name:     name,
		Endpoint: endpoint,
		APIKey:   apiKey,
		Model:    model,
		HTTPClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// bodySnippet memotong body respons ke 500 karakter pertama untuk log
// diagnostik (pola dipakai error parse + penolakan tools).
func bodySnippet(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 500 {
		s = s[:500] + "..."
	}
	return s
}

// maxRateLimitWait membatasi tidur akibat 429: lebih lama dari ini =
// gagal cepat supaya chat tidak menggantung; user retry manual.
const maxRateLimitWait = 30 * time.Second

var resetAfterRe = regexp.MustCompile(`(?i)reset after (\d+)s`)

// rateLimitWait membaca petunjuk tunggu 429 sesuai dokumentasi provider:
// header X-RateLimit-Reset (unix timestamp, prioritas), fallback pola
// "reset after Ns" di body. 0 = tak ada petunjuk valid.
func rateLimitWait(header http.Header, body string) time.Duration {
	if ts, err := strconv.ParseInt(strings.TrimSpace(header.Get("X-RateLimit-Reset")), 10, 64); err == nil && ts > 0 {
		if wait := time.Until(time.Unix(ts, 0).Add(time.Second)); wait > 0 {
			return wait
		}
	}
	if m := resetAfterRe.FindStringSubmatch(body); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
			return time.Duration(n+1) * time.Second
		}
	}
	return 0
}

// ProviderBusyMarker menandai error antrean provider penuh. Dipakai
// plugins/ai.go untuk memilih balasan ramah "lagi antre" (bukan
// "gangguan"). Isinya SENGAJA tanpa kata "timeout"/kode HTTP supaya
// isTransientError tidak menganggapnya layak retry.
const ProviderBusyMarker = "provider sibuk"

// providerConcurrency membatasi request bersamaan per provider (slot sisi
// server). Provider tak terdaftar = tanpa batas. openagentic free tier:
// 1 slot — tanpa antrean lokal, chat konkuren langsung menabrak
// concurrent_limit dan retry hanya membakar waktu.
var providerConcurrency = map[string]int{"openagentic": 1}

// providerQueueTimeout adalah lama maksimal antre slot sebelum gagal ramah.
// var (bukan const) supaya tes bisa memendekkannya.
var providerQueueTimeout = 30 * time.Second

var (
	providerSlotsMu sync.Mutex
	providerSlots   = map[string]chan struct{}{}
)

// acquireProviderSlot antre satu slot eksekusi provider. Gagal ramah bila
// antrean melewati batas tunggu (non-transient: pemanggil tidak me-retry).
func acquireProviderSlot(ctx context.Context, provider string) (func(), error) {
	limit, gated := providerConcurrency[provider]
	if !gated || limit < 1 {
		return func() {}, nil
	}
	providerSlotsMu.Lock()
	ch, ok := providerSlots[provider]
	if !ok {
		ch = make(chan struct{}, limit)
		providerSlots[provider] = ch
	}
	providerSlotsMu.Unlock()
	timer := time.NewTimer(providerQueueTimeout)
	defer timer.Stop()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("%s: dibatalkan saat antre", provider)
	case <-timer.C:
		return nil, fmt.Errorf("%s: %s, antrean penuh — coba lagi sebentar", provider, ProviderBusyMarker)
	}
}

func (g *GenericOpenAIAdapter) Generate(ctx context.Context, params GenerateParams) (*AgentResponse, error) {
	if g.APIKey == "" {
		return nil, fmt.Errorf("%s: API key kosong", g.Name)
	}
	if g.Model == "" {
		return nil, fmt.Errorf("%s: model kosong", g.Name)
	}
	release, err := acquireProviderSlot(ctx, g.Name)
	if err != nil {
		return nil, err
	}
	defer release()

	// Field "tools" DIKIRIM. Kalau ternyata server menolak request KARENA
	// field tools (400 + pesan menyinggung tools), otomatis dicoba ulang
	// SEKALI tanpa tools (fallback mode chat teks). Jadi skenario terburuknya
	// sama seperti tanpa tools, tidak ada regresi.
	tools := toOpenAITools(params.Tools)
	start := time.Now()
	parsed, toolsRejected, err := g.postChat(ctx, params, tools)
	if err != nil {
		return nil, err
	}
	if toolsRejected {
		logfmt.Warn("%s: server menolak field tools — fallback mode teks (tanpa tool).\n", g.Name)
		parsed, _, err = g.postChat(ctx, params, nil)
		if err != nil {
			return nil, err
		}
	}
	durationMs := time.Since(start).Milliseconds()

	if len(parsed.Choices) == 0 {
		return nil, fmt.Errorf("%s: tidak ada choice dalam response", g.Name)
	}

	return fillOpenAIResponse(fromOpenAIMessage(parsed.Choices[0].Message), parsed, g.Name, g.Model, durationMs), nil
}

// postChat mengirim satu request chat ke endpoint provider. toolsRejected
// bernilai true HANYA kalau server mengembalikan 400 yang berkaitan dengan
// field tools — satu-satunya kondisi di mana pemanggil boleh mencoba ulang
// tanpa tools. Error lain (auth, saldo, model, network) diteruskan apa adanya.
//
// Khusus 429: bila respons membawa petunjuk tunggu resmi (header
// X-RateLimit-Reset / pola "reset after Ns") yang masih di bawah
// maxRateLimitWait, postChat tidur dulu lalu mengulang request SEKALI di
// sini (slot semaphore tetap dipegang selama tidur — tidak ada yang
// menyerobot). Gagal kedua = error apa adanya untuk retry loop luar.
func (g *GenericOpenAIAdapter) postChat(ctx context.Context, params GenerateParams, tools []openAITool) (*openAIResponse, bool, error) {
	reqBody := openAIRequest{
		Model:    g.Model,
		Messages: toOpenAIMessages(params.SystemPrompt, params.History),
		Tools:    tools,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, false, fmt.Errorf("%s: gagal marshal request: %w", g.Name, err)
	}

	doOnce := func() (*http.Response, []byte, error) {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, g.Endpoint, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, nil, fmt.Errorf("%s: gagal bangun request: %w", g.Name, err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+g.APIKey)

		resp, err := g.HTTPClient.Do(httpReq)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: request gagal: %w", g.Name, err)
		}
		defer resp.Body.Close()

		respBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: gagal baca response: %w", g.Name, err)
		}
		return resp, respBytes, nil
	}

	resp, respBytes, err := doOnce()
	if err != nil {
		return nil, false, err
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		if wait := rateLimitWait(resp.Header, string(respBytes)); wait > 0 && wait <= maxRateLimitWait {
			logfmt.Warn("%s: 429, tidur %s sesuai petunjuk rate limit lalu coba sekali lagi.\n", g.Name, wait.Round(time.Second))
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, false, fmt.Errorf("%s: dibatalkan saat tunggu rate limit: %w", g.Name, ctx.Err())
			case <-timer.C:
			}
			if resp2, bytes2, err := doOnce(); err == nil {
				resp, respBytes = resp2, bytes2
			} else {
				return nil, false, err
			}
		}
	}

	bodyStr := strings.TrimSpace(string(respBytes))
	// Kupas satu prefix SSE ("data:") bila ada — beberapa gateway
	// membungkus JSON dalam framing SSE walau stream tidak diminta.
	bodyStr = strings.TrimSpace(strings.TrimPrefix(bodyStr, "data:"))

	var parsed openAIResponse
	if err := json.Unmarshal([]byte(bodyStr), &parsed); err != nil {
		// Parse toleran: beberapa gateway menempelkan ekor (terminator
		// SSE "done"/"data:", objek kedua) setelah JSON valid — ambil
		// nilai pertama saja, ekornya dicatat sebagai bukti.
		if decErr := json.NewDecoder(strings.NewReader(bodyStr)).Decode(&parsed); decErr == nil && len(parsed.Choices) > 0 {
			tail := bodyStr
			if len(tail) > 300 {
				tail = "..." + tail[len(tail)-300:]
			}
			logfmt.Warn("%s: respons berekor non-JSON, dipakai nilai pertama; ekor: %s\n", g.Name, tail)
		} else {
			// Body bukan JSON + status 400 saat tools dikirim = sangat mungkin
			// penolakan terhadap field tools (format error tak dikenal).
			if resp.StatusCode == http.StatusBadRequest && len(tools) > 0 {
				logfmt.Warn("%s: 400 non-JSON saat tools dikirim (dugaan tolak tools); body: %s\n", g.Name, bodySnippet(bodyStr))
				return nil, true, nil
			}
			// Sertakan cuplikan body (ada provider yang balas 200 non-JSON:
			// SSE, HTML gateway, debug text) supaya penyebabnya terbaca dari
			// log, bukan tebak-tebakan.
			return nil, false, fmt.Errorf("%s: gagal parse response (status %d): %w; body: %s", g.Name, resp.StatusCode, err, bodySnippet(bodyStr))
		}
	}

	if resp.StatusCode == http.StatusBadRequest && len(tools) > 0 && parsed.Error != nil &&
		strings.Contains(strings.ToLower(parsed.Error.Message), "tool") {
		logfmt.Warn("%s: 400 menyebut tools (dugaan tolak tools); body: %s\n", g.Name, bodySnippet(bodyStr))
		return nil, true, nil
	}

	if parsed.Error != nil {
		return nil, false, fmt.Errorf("%s: API error (%v): %s", g.Name, parsed.Error.Code, parsed.Error.Message)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("%s: status HTTP %d, body: %s", g.Name, resp.StatusCode, string(respBytes))
	}

	return &parsed, false, nil
}
