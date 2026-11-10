package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"wabot-go/internal/logfmt"
	"wabot-go/internal/metrics"
	"wabot-go/internal/netx"
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
	// streamHTTPClient khusus jalur streaming: transport yang sama tetapi
	// dengan ResponseHeaderTimeout pendek (lihat streamHeaderTimeout).
	streamHTTPClient *http.Client
}

// defaultRequestTimeout adalah batas tunggu satu request ke provider kalau
// AI_REQUEST_TIMEOUT tidak diisi. 150 detik dipilih dari pengukuran nyata
// (ciora/gemini-3.8-flash: ±89 token/detik termasuk reasoning tersembunyi) —
// 60 detik terlalu ketat dan membuat request yang sebenarnya sehat ikut
// dianggap gagal.
const defaultRequestTimeout = 150 * time.Second

// requestTimeout membaca batas tunggu per request dari env
// AI_REQUEST_TIMEOUT (format durasi Go: "150s", "3m"). Nilai kosong/rusak/
// tidak positif = default + peringatan (jangan gagalkan bot karena salah
// tulis env).
func requestTimeout() time.Duration {
	raw := strings.TrimSpace(os.Getenv("AI_REQUEST_TIMEOUT"))
	if raw == "" {
		return defaultRequestTimeout
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		logfmt.Warn("AI_REQUEST_TIMEOUT %q tidak valid (contoh: 150s atau 3m) — pakai default %s.\n", raw, defaultRequestTimeout)
		return defaultRequestTimeout
	}
	return d
}

// streamHeaderTimeout membatasi tunggu HEADER respons di jalur streaming.
// Saat sehat, gateway mengirim byte pertama dalam ±0,06–0,24 detik (terukur
// pada ciora). Kalau 25 detik belum ada header, gateway hampir pasti
// tersangkut dan Cloudflare akan memutusnya dengan 524 di sekitar 125 detik —
// lebih baik gagal cepat lalu retry sekali (murah) daripada menunggu 2 menit.
// Var (bukan const) supaya tes bisa memendekkannya.
var streamHeaderTimeout = 60 * time.Second

// slowStreamHeader adalah ambang "header lambat" yang dicatat ke log sebagai
// peringatan diagnostik (gejala awal gateway antre sebelum akhirnya 524).
const slowStreamHeader = 5 * time.Second

// NewGenericOpenAIAdapter membuat adapter dengan timeout dari
// AI_REQUEST_TIMEOUT (default 150 detik, lihat defaultRequestTimeout).
func NewGenericOpenAIAdapter(name, endpoint, apiKey, model string) *GenericOpenAIAdapter {
	client := &http.Client{Timeout: requestTimeout(), Transport: netx.TunedTransport()}
	return &GenericOpenAIAdapter{
		Name:             name,
		Endpoint:         endpoint,
		APIKey:           apiKey,
		Model:            model,
		HTTPClient:       client,
		streamHTTPClient: withHeaderTimeout(client, streamHeaderTimeout),
	}
}

// withHeaderTimeout menyalin client + transport lalu memasang
// ResponseHeaderTimeout. Transport di-clone (bukan dimutasi) supaya jalur
// streaming punya batas header sendiri tanpa mengubah jalur non-streaming,
// yang memang bisa butuh puluhan detik sebelum header dikirim. Clone tetap
// mewarisi setelan proxy/TLS dari transport asal.
func withHeaderTimeout(base *http.Client, d time.Duration) *http.Client {
	if base == nil {
		base = http.DefaultClient
	}
	c := *base
	var tr *http.Transport
	switch t := base.Transport.(type) {
	case *http.Transport:
		if t != nil {
			tr = t.Clone()
		}
	case nil:
		if dt, ok := http.DefaultTransport.(*http.Transport); ok {
			tr = dt.Clone()
		}
	}
	if tr != nil {
		tr.ResponseHeaderTimeout = d
		c.Transport = tr
	}
	return &c
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
// concurrent_limit dan retry hanya membakar waktu. ciora: 2 slot — free
// tier juga membatasi concurrency; tanpa gerbang, beberapa chat + ekstraksi
// fakta + ringkasan sesi menembak bersamaan sampai semuanya timeout 150s.
var providerConcurrency = map[string]int{"openagentic": 1, "ciora": 2}

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

func (g *GenericOpenAIAdapter) Generate(ctx context.Context, params GenerateParams) (resp *AgentResponse, err error) {
	start := time.Now()
	defer func() {
		metrics.ObserveAI(g.Name, time.Since(start), err)
	}()
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
	// start sudah dicatat di atas (termasuk antre slot) untuk metrik.

	// Streaming dipakai lebih dulu: gateway mengalirkan data sejak ±0,2 detik
	// sehingga Cloudflare tidak lagi memutus request panjang dengan 524
	// ("origin tidak mengirim respons"). Kalau gateway menolak/mengabaikan
	// stream, otomatis jatuh ke jalur non-streaming sekali — perilaku lama.
	parsed, toolsRejected, err := g.requestChat(ctx, params, tools, true)
	if errors.Is(err, errStreamUnsupported) || errors.Is(err, errStreamEmpty) {
		logfmt.Warn("%s: %v — fallback ke non-streaming.\n", g.Name, err)
		parsed, toolsRejected, err = g.requestChat(ctx, params, tools, false)
	}
	if err != nil {
		return nil, err
	}
	if toolsRejected {
		logfmt.Warn("%s: server menolak field tools — fallback mode teks (tanpa tool).\n", g.Name)
		parsed, _, err = g.requestChat(ctx, params, nil, true)
		if errors.Is(err, errStreamUnsupported) || errors.Is(err, errStreamEmpty) {
			parsed, _, err = g.requestChat(ctx, params, nil, false)
		}
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

// errStreamUnsupported menandai gateway yang menolak field "stream" (400
// yang menyebut stream). Pemanggil boleh mencoba ulang lewat jalur non-stream.
var errStreamUnsupported = errors.New("gateway menolak streaming")

// errStreamEmpty menandai stream yang selesai tanpa teks maupun tool call
// (mis. gateway membalas 200 + "[DONE]" saja karena mengabaikan stream).
// Diperlakukan sama seperti errStreamUnsupported: fallback non-stream sekali.
var errStreamEmpty = errors.New("streaming selesai tanpa isi")

// requestChat memilih jalur streaming atau non-streaming.
func (g *GenericOpenAIAdapter) requestChat(ctx context.Context, params GenerateParams, tools []openAITool, stream bool) (*openAIResponse, bool, error) {
	if stream {
		return g.postChatStream(ctx, params, tools)
	}
	return g.postChat(ctx, params, tools)
}

// doRequest mengirim POST ke endpoint provider. client dipilih pemanggil
// (streaming memakai klien berbatas header, non-streaming memakai klien
// biasa); stream hanya menambah header Accept SSE; start dipakai untuk
// laporan durasi gagal koneksi.
func (g *GenericOpenAIAdapter) doRequest(ctx context.Context, client *http.Client, body []byte, stream bool, start time.Time) (*http.Response, error) {
	if client == nil {
		client = g.HTTPClient
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, g.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%s: gagal bangun request: %w", g.Name, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+g.APIKey)
	if stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		// Durasi + ukuran payload disertakan supaya laporan ke owner
		// langsung bisa membedakan "provider lambat" dari "kiriman
		// kebesaran" tanpa perlu reproduksi manual.
		return nil, fmt.Errorf("%s/%s: request gagal setelah %s (payload %d KB): %w",
			g.Name, g.Model, time.Since(start).Round(time.Millisecond), len(body)/1024, err)
	}
	return resp, nil
}

// sleepCtx tidur selama d (mis. menunggu rate limit) tapi tetap menghormati
// pembatalan context.
func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// classifyChatResponse menerjemahkan satu respons HTTP provider menjadi
// openAIResponse. toolsRejected bernilai true HANYA kalau server membalas 400
// yang berkaitan dengan field tools — satu-satunya kondisi di mana pemanggil
// boleh mencoba ulang tanpa tools.
//
// Status ≥400 dengan body non-JSON (mis. Cloudflare "error code: 524")
// dilaporkan apa adanya sebagai `status HTTP <kode>` + durasi + ukuran
// payload + cuplikan body, BUKAN sebagai "gagal parse response" yang
// menyesatkan. stream menandai apakah request tadi meminta SSE, supaya
// penolakan field stream bisa dibedakan dari penolakan tools.
func (g *GenericOpenAIAdapter) classifyChatResponse(resp *http.Response, respBytes []byte, tools []openAITool, stream bool, elapsed time.Duration, payloadKB int) (*openAIResponse, bool, error) {
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
			// 400 yang menyebut "stream" saat kita memang meminta stream =
			// gateway tidak mendukung SSE, bukan penolakan tools.
			if stream && resp.StatusCode == http.StatusBadRequest {
				low := strings.ToLower(bodyStr)
				if strings.Contains(low, "stream") && !strings.Contains(low, "tool") {
					logfmt.Warn("%s: gateway menolak streaming (400); body: %s\n", g.Name, bodySnippet(bodyStr))
					return nil, false, errStreamUnsupported
				}
			}
			// Body bukan JSON + status 400 saat tools dikirim = sangat mungkin
			// penolakan terhadap field tools (format error tak dikenal).
			if resp.StatusCode == http.StatusBadRequest && len(tools) > 0 {
				logfmt.Warn("%s: 400 non-JSON saat tools dikirim (dugaan tolak tools); body: %s\n", g.Name, bodySnippet(bodyStr))
				return nil, true, nil
			}
			if resp.StatusCode >= http.StatusBadRequest {
				return nil, false, fmt.Errorf("%s/%s: status HTTP %d setelah %s (payload %d KB, body %d B): %s",
					g.Name, g.Model, resp.StatusCode, elapsed.Round(time.Millisecond), payloadKB, len(respBytes), bodySnippet(bodyStr))
			}
			// Sertakan cuplikan body (ada provider yang balas 200 non-JSON:
			// SSE, HTML gateway, debug text) supaya penyebabnya terbaca dari
			// log, bukan tebak-tebakan.
			return nil, false, fmt.Errorf("%s: gagal parse response (status %d): %w; body: %s", g.Name, resp.StatusCode, err, bodySnippet(bodyStr))
		}
	}

	if stream && resp.StatusCode == http.StatusBadRequest && parsed.Error != nil {
		low := strings.ToLower(parsed.Error.Message)
		if strings.Contains(low, "stream") && !strings.Contains(low, "tool") {
			logfmt.Warn("%s: gateway menolak streaming (400): %s\n", g.Name, parsed.Error.Message)
			return nil, false, errStreamUnsupported
		}
	}

	if resp.StatusCode == http.StatusBadRequest && len(tools) > 0 && parsed.Error != nil &&
		strings.Contains(strings.ToLower(parsed.Error.Message), "tool") {
		logfmt.Warn("%s: 400 menyebut tools (dugaan tolak tools); body: %s\n", g.Name, bodySnippet(bodyStr))
		return nil, true, nil
	}

	if parsed.Error != nil {
		return nil, false, fmt.Errorf("%s/%s: API error (%v): %s", g.Name, g.Model, parsed.Error.Code, parsed.Error.Message)
	}

	if resp.StatusCode >= http.StatusBadRequest {
		return nil, false, fmt.Errorf("%s/%s: status HTTP %d setelah %s (payload %d KB): %s",
			g.Name, g.Model, resp.StatusCode, elapsed.Round(time.Millisecond), payloadKB, bodySnippet(bodyStr))
	}

	return &parsed, false, nil
}

// postChat mengirim satu request chat NON-streaming ke endpoint provider.
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

	start := time.Now()
	resp, err := g.doRequest(ctx, g.HTTPClient, bodyBytes, false, start)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	payloadKB := len(bodyBytes) / 1024

	if resp.StatusCode == http.StatusTooManyRequests {
		respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if wait := rateLimitWait(resp.Header, string(respBytes)); wait > 0 && wait <= maxRateLimitWait {
			logfmt.Warn("%s: 429, tidur %s sesuai petunjuk rate limit lalu coba sekali lagi.\n", g.Name, wait.Round(time.Second))
			if err := sleepCtx(ctx, wait); err != nil {
				return nil, false, fmt.Errorf("%s: dibatalkan saat tunggu rate limit: %w", g.Name, err)
			}
			resp.Body.Close()
			resp2, err := g.doRequest(ctx, g.HTTPClient, bodyBytes, false, time.Now())
			if err != nil {
				return nil, false, err
			}
			resp = resp2
			defer resp.Body.Close()
		} else {
			return g.classifyChatResponse(resp, respBytes, tools, false, time.Since(start), payloadKB)
		}
	}

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, fmt.Errorf("%s/%s: gagal baca response setelah %s: %w",
			g.Name, g.Model, time.Since(start).Round(time.Millisecond), err)
	}
	return g.classifyChatResponse(resp, respBytes, tools, false, time.Since(start), payloadKB)
}

// postChatStream mengirim request dengan "stream": true lalu membaca SSE
// (text/event-stream) baris per baris. Ini jalur utama adapter: respons mulai
// mengalir dalam ±0,2 detik sehingga gateway/Cloudflare tidak memutus request
// panjang dengan 524 karena "origin tidak mengirim apa pun".
//
// Bila gateway mengabaikan stream dan membalas JSON biasa, respons dibaca
// lewat jalur non-stream (classifyChatResponse). Bila gateway menolak field
// stream atau stream selesai tanpa isi, pemanggil mendapat
// errStreamUnsupported/errStreamEmpty dan fallback sekali ke non-streaming.
func (g *GenericOpenAIAdapter) postChatStream(ctx context.Context, params GenerateParams, tools []openAITool) (*openAIResponse, bool, error) {
	reqBody := openAIRequest{
		Model:         g.Model,
		Messages:      toOpenAIMessages(params.SystemPrompt, params.History),
		Tools:         tools,
		Stream:        true,
		StreamOptions: &openAIStreamOptions{IncludeUsage: true},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, false, fmt.Errorf("%s: gagal marshal request: %w", g.Name, err)
	}
	payloadKB := len(bodyBytes) / 1024

	start := time.Now()
	resp, err := g.doRequest(ctx, g.streamHTTPClient, bodyBytes, true, start)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()

	// Diagnostik: kalau header saja lama, gateway sedang antre — gejala awal
	// yang dulu berakhir 524 tanpa jejak. Saat sehat nilainya <1 detik.
	if ttfb := time.Since(start); ttfb >= slowStreamHeader {
		logfmt.Warn("%s/%s: header stream baru tiba setelah %s (payload %d KB) — gateway lambat/antre.\n",
			g.Name, g.Model, ttfb.Round(time.Millisecond), payloadKB)
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if wait := rateLimitWait(resp.Header, string(respBytes)); wait > 0 && wait <= maxRateLimitWait {
			logfmt.Warn("%s: 429 saat streaming, tidur %s lalu coba sekali lagi.\n", g.Name, wait.Round(time.Second))
			if err := sleepCtx(ctx, wait); err != nil {
				return nil, false, fmt.Errorf("%s: dibatalkan saat tunggu rate limit: %w", g.Name, err)
			}
			resp.Body.Close()
			resp2, err := g.doRequest(ctx, g.streamHTTPClient, bodyBytes, true, time.Now())
			if err != nil {
				return nil, false, err
			}
			resp = resp2
			defer resp.Body.Close()
		} else {
			return g.classifyChatResponse(resp, respBytes, tools, true, time.Since(start), payloadKB)
		}
	}

	if resp.StatusCode != http.StatusOK {
		respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return g.classifyChatResponse(resp, respBytes, tools, true, time.Since(start), payloadKB)
	}

	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	body := bufio.NewReaderSize(resp.Body, 64<<10)
	if !strings.Contains(contentType, "text/event-stream") {
		// Content-Type bisa kosong/keliru walau body-nya SSE. Intip awal body
		// sebelum memutuskan, supaya stream yang benar tidak salah dibaca
		// sebagai JSON lalu gagal parse.
		head, _ := body.Peek(64)
		headStr := strings.TrimSpace(string(head))
		if !strings.HasPrefix(headStr, "data:") && !strings.HasPrefix(headStr, ":") {
			respBytes, err := io.ReadAll(body)
			if err != nil {
				return nil, false, fmt.Errorf("%s/%s: gagal baca response setelah %s: %w",
					g.Name, g.Model, time.Since(start).Round(time.Millisecond), err)
			}
			logfmt.Warn("%s: stream diminta tapi balasan bukan SSE (Content-Type %q) — dibaca sebagai JSON.\n", g.Name, contentType)
			return g.classifyChatResponse(resp, respBytes, tools, false, time.Since(start), payloadKB)
		}
	}

	parsed, toolsRejected, err := g.readSSE(body)
	if err != nil {
		return nil, false, err
	}
	if len(parsed.Choices) == 0 {
		return nil, false, errStreamEmpty
	}
	return parsed, toolsRejected, nil
}

// openAIStreamChunk adalah satu potongan SSE gaya OpenAI. Potongan tool call
// datang bertahap: id/nama biasanya di potongan pertama, argumen tersambung
// di potongan-potongan berikutnya, semuanya dirujuk oleh Index yang sama.
type openAIStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *openAIUsage `json:"usage,omitempty"`
	Error *openAIError `json:"error,omitempty"`
}

// readSSE merakit potongan SSE menjadi satu openAIResponse: teks digabung,
// tool_calls dikelompokkan per Index (id/nama/argumen disatukan), usage dan
// finish_reason diambil yang terakhir dikirim. Baris selain "data:" (komentar
// ": keep-alive", "event:", baris kosong) diabaikan.
func (g *GenericOpenAIAdapter) readSSE(r io.Reader) (*openAIResponse, bool, error) {
	reader := bufio.NewReaderSize(r, 64<<10)

	var text strings.Builder
	type callAcc struct {
		id, typ, name string
		args          strings.Builder
	}
	calls := map[int]*callAcc{}
	var order []int
	var usage *openAIUsage
	finishReason := ""

	for {
		line, readErr := reader.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if payload, ok := strings.CutPrefix(line, "data:"); ok {
			payload = strings.TrimSpace(payload)
			if payload == "[DONE]" {
				break
			}
			if payload != "" {
				var chunk openAIStreamChunk
				if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
					logfmt.Warn("%s: baris SSE tak terbaca (%v): %s\n", g.Name, err, bodySnippet(payload))
				} else {
					if chunk.Error != nil {
						return nil, false, fmt.Errorf("%s/%s: API error saat streaming (%v): %s",
							g.Name, g.Model, chunk.Error.Code, chunk.Error.Message)
					}
					if chunk.Usage != nil {
						usage = chunk.Usage
					}
					for _, ch := range chunk.Choices {
						if ch.Delta.Content != "" {
							text.WriteString(ch.Delta.Content)
						}
						for _, d := range ch.Delta.ToolCalls {
							acc := calls[d.Index]
							if acc == nil {
								acc = &callAcc{typ: "function"}
								calls[d.Index] = acc
								order = append(order, d.Index)
							}
							if d.ID != "" {
								acc.id = d.ID
							}
							if d.Type != "" {
								acc.typ = d.Type
							}
							acc.name += d.Function.Name
							acc.args.WriteString(d.Function.Arguments)
						}
						if ch.FinishReason != "" {
							finishReason = ch.FinishReason
						}
					}
				}
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return nil, false, fmt.Errorf("%s/%s: gagal membaca stream: %w", g.Name, g.Model, readErr)
		}
	}

	msg := openAIMessage{Role: "assistant"}
	if text.Len() > 0 {
		msg.Content = text.String()
	}
	for _, idx := range order {
		acc := calls[idx]
		msg.ToolCalls = append(msg.ToolCalls, openAIToolCall{
			ID:   acc.id,
			Type: acc.typ,
			Function: openAIFunctionCall{
				Name:      acc.name,
				Arguments: acc.args.String(),
			},
		})
	}
	if text.Len() == 0 && len(msg.ToolCalls) == 0 {
		return nil, false, errStreamEmpty
	}

	return &openAIResponse{
		Choices: []openAIChoice{{Message: msg, FinishReason: finishReason}},
		Usage:   usage,
	}, false, nil
}
