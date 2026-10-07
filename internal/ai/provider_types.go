package ai

import "context"

// GenerateParams adalah parameter yang dikirim ke provider AI manapun.
// Field Tools sejak Fase 3 dipakai untuk memberi tahu model tool apa
// saja yang tersedia — kosongkan (nil) untuk chat biasa tanpa tool.
type GenerateParams struct {
	History      []Content
	SystemPrompt string
	Tools        []FunctionDecl
}

// AgentResponse adalah hasil generate dari provider manapun.
// Kalau ToolCalls tidak kosong, berarti model minta tool dieksekusi
// dulu sebelum jawaban final (lihat loop.go). Kalau kosong, Text adalah
// jawaban final untuk dikirim ke user.
//
// Usage/FinishReason/DurationMs/Provider/Model diisi adapter untuk
// observabilitas (log + hitung token) — kosong bila provider tak memberi.
type AgentResponse struct {
	Text         string
	ToolCalls    []ToolCall
	Usage        TokenUsage
	FinishReason string
	DurationMs   int64
	Provider     string
	Model        string
}

// TokenUsage adalah pemakaian token satu panggilan Generate.
type TokenUsage struct {
	Prompt     int
	Completion int
	Total      int
}

// ToolCall merepresentasikan satu permintaan pemanggilan tool dari model.
//
// ThoughtSignature dibawa apa adanya dari response provider (kalau ada)
// supaya loop.go bisa echo balik field ini saat menyusun ulang history —
// wajib untuk Gemini model seri 2.5/3, lihat catatan di types.go pada Part.
//
// ID dibawa apa adanya dari provider ber-gaya OpenAI (OpenRouter,
// NexaRouter) yang menerbitkan tool_call id sendiri (mis. "call_xyz").
// WAJIB di-echo balik persis sama di tool_call_id pada balasan role="tool"
// — provider OpenAI-compatible yang ketat menolak permintaan berikutnya
// kalau ID tidak cocok. Gemini tidak mengenal konsep ini sama sekali,
// jadi untuk jalur Gemini field ini selalu kosong ("") — itu wajar dan
// tidak dipakai/dibaca oleh gemini_adapter.go maupun loop.go untuk apa pun
// selain diteruskan apa adanya (kalau kosong, adapter OpenAI-compatible
// fallback ke Name, lihat catatan di openrouter_adapter.go).
type ToolCall struct {
	ID               string
	Name             string
	Args             map[string]interface{}
	ThoughtSignature string
}

// ProviderAdapter adalah kontrak seragam untuk semua provider AI
// (Gemini sekarang, provider lain menyusul nanti tanpa mengubah
// agent loop atau pemanggil lain).
type ProviderAdapter interface {
	Generate(ctx context.Context, params GenerateParams) (*AgentResponse, error)
}
