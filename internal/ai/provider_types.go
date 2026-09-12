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
type AgentResponse struct {
	Text      string
	ToolCalls []ToolCall
}

// ToolCall merepresentasikan satu permintaan pemanggilan tool dari model.
//
// ThoughtSignature dibawa apa adanya dari response provider (kalau ada)
// supaya loop.go bisa echo balik field ini saat menyusun ulang history —
// wajib untuk Gemini model seri 2.5/3, lihat catatan di types.go pada Part.
type ToolCall struct {
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
