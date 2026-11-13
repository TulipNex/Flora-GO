package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// sseServer membuat server yang membalas SSE dengan baris-baris yang diberikan
// (tiap string dikirim sebagai satu baris "data: ...").
func sseServer(t *testing.T, lines ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, line := range lines {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", line)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestGenerateStreamingText: potongan delta.content digabung jadi satu jawaban,
// usage + finish_reason dari chunk terakhir ikut terbaca.
func TestGenerateStreamingText(t *testing.T) {
	srv := sseServer(t,
		`{"choices":[{"delta":{"role":"assistant"}}]}`,
		`{"choices":[{"delta":{"content":"Halo "}}]}`,
		`{"choices":[{"delta":{"content":"dunia"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":3,"total_tokens":13}}`,
		`[DONE]`,
	)
	g := NewGenericOpenAIAdapter("tes", srv.URL, "k", "m")

	resp, err := g.Generate(context.Background(), GenerateParams{})
	if err != nil {
		t.Fatalf("Generate error: %v", err)
	}
	if resp.Text != "Halo dunia" {
		t.Fatalf("teks = %q, mau %q", resp.Text, "Halo dunia")
	}
	if resp.FinishReason != "stop" {
		t.Fatalf("finish_reason = %q", resp.FinishReason)
	}
	if resp.Usage.Total != 13 {
		t.Fatalf("usage total = %d, mau 13", resp.Usage.Total)
	}
}

// TestGenerateStreamingToolCalls: argumen tool yang terpecah beberapa chunk
// dan dua tool sekaligus (index 0 dan 1) dirangkai utuh.
func TestGenerateStreamingToolCalls(t *testing.T) {
	srv := sseServer(t,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"reminder","arguments":"{\"pesan\":"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"bayar\"}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"baca_keuangan","arguments":"{}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`[DONE]`,
	)
	g := NewGenericOpenAIAdapter("tes", srv.URL, "k", "m")

	resp, err := g.Generate(context.Background(), GenerateParams{})
	if err != nil {
		t.Fatalf("Generate error: %v", err)
	}
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("tool calls = %d, mau 2: %+v", len(resp.ToolCalls), resp.ToolCalls)
	}
	first := resp.ToolCalls[0]
	if first.ID != "call_a" || first.Name != "reminder" {
		t.Fatalf("tool call pertama salah: %+v", first)
	}
	if got := first.Args["pesan"]; got != "bayar" {
		t.Fatalf("args pesan = %v, mau bayar", got)
	}
	if second := resp.ToolCalls[1]; second.ID != "call_b" || second.Name != "baca_keuangan" {
		t.Fatalf("tool call kedua salah: %+v", second)
	}
}

// TestGenerateStreamIgnoredFallsBackToJSON: gateway mengabaikan "stream" dan
// membalas JSON biasa (tanpa Content-Type SSE) — tetap terbaca normal.
func TestGenerateStreamIgnoredFallsBackToJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"json biasa"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	g := NewGenericOpenAIAdapter("tes", srv.URL, "k", "m")
	resp, err := g.Generate(context.Background(), GenerateParams{})
	if err != nil {
		t.Fatalf("Generate error: %v", err)
	}
	if resp.Text != "json biasa" {
		t.Fatalf("teks = %q", resp.Text)
	}
}

// TestGenerateStreamRejectedFallback: gateway membalas 400 karena field stream
// tidak dikenal; adapter mencoba ulang tanpa stream dan berhasil.
func TestGenerateStreamRejectedFallback(t *testing.T) {
	var streamCalls, plainCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Stream bool `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Stream {
			streamCalls++
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"unknown field stream"}}`))
			return
		}
		plainCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"tanpa stream"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	g := NewGenericOpenAIAdapter("tes", srv.URL, "k", "m")
	resp, err := g.Generate(context.Background(), GenerateParams{})
	if err != nil {
		t.Fatalf("Generate error: %v", err)
	}
	if resp.Text != "tanpa stream" {
		t.Fatalf("teks = %q", resp.Text)
	}
	if streamCalls != 1 || plainCalls != 1 {
		t.Fatalf("streamCalls=%d plainCalls=%d, mau 1/1", streamCalls, plainCalls)
	}
}

// TestGenerateEmptyStreamFallback: stream 200 tapi hanya "[DONE]" — dianggap
// tidak menghasilkan isi, lalu fallback non-stream sekali.
func TestGenerateEmptyStreamFallback(t *testing.T) {
	var plainCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Stream bool `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			return
		}
		plainCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"isi cadangan"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	g := NewGenericOpenAIAdapter("tes", srv.URL, "k", "m")
	resp, err := g.Generate(context.Background(), GenerateParams{})
	if err != nil {
		t.Fatalf("Generate error: %v", err)
	}
	if resp.Text != "isi cadangan" || plainCalls != 1 {
		t.Fatalf("teks=%q plainCalls=%d", resp.Text, plainCalls)
	}
}

// TestGenerateStreamWithoutContentType: SSE dengan Content-Type keliru/kosong
// (Go menebak text/plain) tetap dibaca sebagai stream, bukan JSON.
func TestGenerateStreamWithoutContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"keliru tipe\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()

	g := NewGenericOpenAIAdapter("tes", srv.URL, "k", "m")
	resp, err := g.Generate(context.Background(), GenerateParams{})
	if err != nil {
		t.Fatalf("Generate error: %v", err)
	}
	if resp.Text != "keliru tipe" {
		t.Fatalf("teks = %q", resp.Text)
	}
}

// TestGenerateStreamRequestsUsage: jalur streaming wajib meminta
// stream_options.include_usage (didukung resmi gateway) supaya rekap token
// tetap diterima, bukan bergantung pada gateway yang mengirim usage sendiri.
func TestGenerateStreamRequestsUsage(t *testing.T) {
	type streamOptions struct {
		IncludeUsage bool `json:"include_usage"`
	}
	var got struct {
		Stream        bool          `json:"stream"`
		StreamOptions streamOptions `json:"stream_options"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()

	g := NewGenericOpenAIAdapter("tes", srv.URL, "k", "m")
	if _, err := g.Generate(context.Background(), GenerateParams{}); err != nil {
		t.Fatalf("Generate error: %v", err)
	}
	if !got.Stream {
		t.Fatal("request harus meminta stream=true")
	}
	if !got.StreamOptions.IncludeUsage {
		t.Fatal("request harus meminta stream_options.include_usage=true")
	}
}

// TestWithHeaderTimeoutSetsTransport: transport klien streaming mendapat
// ResponseHeaderTimeout tanpa mengubah klien dasarnya (jalur non-streaming
// tetap boleh menunggu lama sampai header dikirim).
func TestWithHeaderTimeoutSetsTransport(t *testing.T) {
	base := &http.Client{}
	c := withHeaderTimeout(base, 3*time.Second)
	tr, ok := c.Transport.(*http.Transport)
	if !ok || tr.ResponseHeaderTimeout != 3*time.Second {
		t.Fatalf("ResponseHeaderTimeout tidak terpasang: %#v", c.Transport)
	}
	if base.Transport != nil {
		t.Fatal("klien dasar tidak boleh ikut berubah")
	}
	if c == base {
		t.Fatal("harus menyalin client, bukan memutasi pointer yang sama")
	}
}

// TestGenerateStreamHeaderTimeoutFailsFast: gateway yang tidak mengirim header
// diputus setelah streamHeaderTimeout (bukan menunggu ~125 dtk sampai
// Cloudflare membalas 524). Errornya harus terbaca sebagai timeout supaya
// kebijakan retry (sekali) berlaku.
func TestGenerateStreamHeaderTimeoutFailsFast(t *testing.T) {
	oldTimeout := streamHeaderTimeout
	streamHeaderTimeout = 120 * time.Millisecond
	t.Cleanup(func() { streamHeaderTimeout = oldTimeout })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(400 * time.Millisecond)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	g := NewGenericOpenAIAdapter("tes", srv.URL, "k", "m")
	start := time.Now()
	_, err := g.Generate(context.Background(), GenerateParams{})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("harus gagal karena header tidak kunjung tiba")
	}
	if elapsed > 350*time.Millisecond {
		t.Fatalf("gagal terlalu lama (%v) — ResponseHeaderTimeout tidak bekerja", elapsed)
	}
	if !isTimeoutError(err) {
		t.Fatalf("harus dikenali sebagai timeout: %v", err)
	}
	if !strings.Contains(err.Error(), "payload") {
		t.Fatalf("error harus memuat ukuran payload: %v", err)
	}
}

// TestGenerateGatewayTimeoutNonJSON: 524 dengan body non-JSON dilaporkan
// sebagai status HTTP 524 (bukan "gagal parse response"), dan tidak dianggap
// error timeout biasa (tidak memicu retry timeout).
func TestGenerateGatewayTimeoutNonJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(524)
		_, _ = w.Write([]byte("error code: 524"))
	}))
	defer srv.Close()

	g := NewGenericOpenAIAdapter("tes", srv.URL, "k", "m")
	_, err := g.Generate(context.Background(), GenerateParams{})
	if err == nil {
		t.Fatal("harus error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "status HTTP 524") {
		t.Fatalf("error harus menyebut status HTTP 524: %v", err)
	}
	if strings.Contains(msg, "gagal parse") {
		t.Fatalf("error 524 tidak boleh dilaporkan sebagai gagal parse: %v", err)
	}
	if !isTransientError(err) {
		t.Fatalf("5xx harus transient secara umum: %v", err)
	}
	if !isGatewayTimeoutError(err) {
		t.Fatalf("harus dikenali sebagai 524: %v", err)
	}
}

// TestRetryGatewayTimeoutNotRepeated: 524 tidak diulang sama sekali (setiap
// percobaan menghabiskan waktu yang sama panjang tanpa peluang lebih baik).
func TestRetryGatewayTimeoutNotRepeated(t *testing.T) {
	withFastRetry(t, 10*time.Second)
	err524 := errors.New("tes/m: status HTTP 524 setelah 2m5s (body 15 B): error code: 524")
	p := &scriptedProvider{errs: []error{err524, err524, err524}}
	if _, err := RunAgentLoopWithRetry(newTestAgentContext(), p, nil, "sys"); err == nil {
		t.Fatal("harus error")
	}
	if p.calls != 1 {
		t.Fatalf("jumlah panggilan = %d, mau 1 (524 tidak diulang)", p.calls)
	}
}
