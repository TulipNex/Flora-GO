package ai

import (
	"context"
	"fmt"
)

// MaxLoops adalah batas jumlah putaran tool-calling dalam satu giliran
// balasan, mencegah infinite loop kalau model terus-menerus minta tool
// tanpa pernah menghasilkan jawaban final.
const MaxLoops = 8

// RunAgentLoop menjalankan satu giliran percakapan penuh: kirim history
// ke provider, kalau provider minta tool maka dieksekusi dan hasilnya
// dikirim balik, diulang sampai provider menghasilkan teks final atau
// MaxLoops tercapai.
//
// history yang di-pass HARUS sudah termasuk pesan user terbaru — fungsi
// ini tidak menambahkannya sendiri, supaya pemanggil (plugins/ai.go)
// yang mengatur kapan pesan user ditambahkan (relevan untuk penyimpanan
// ke SessionStore/SQLite).
func RunAgentLoop(actx *AgentContext, provider ProviderAdapter, history []Content, systemPrompt string) (string, error) {
	current := append([]Content{}, history...)

	for i := 0; i < MaxLoops; i++ {
		resp, err := provider.Generate(actx.Context, GenerateParams{
			History:      current,
			SystemPrompt: systemPrompt,
			Tools:        GetToolDeclarations(),
		})
		if err != nil {
			return "", err
		}

		// Tidak ada tool call → ini jawaban final.
		if len(resp.ToolCalls) == 0 {
			return resp.Text, nil
		}

		// Tambahkan giliran model (berisi permintaan tool) ke history,
		// supaya Gemini "ingat" dia baru saja meminta tool tersebut.
		// PENTING: ThoughtSignature WAJIB diikutsertakan apa adanya persis
		// seperti yang dikembalikan model — Gemini (seri 2.5/3) menolak
		// request berikutnya dengan error 400 kalau field ini hilang.
		// Lihat catatan lengkap di types.go pada struct Part.
		modelParts := make([]Part, 0, len(resp.ToolCalls)+1)
		if resp.Text != "" {
			modelParts = append(modelParts, Part{Text: resp.Text})
		}
		for _, tc := range resp.ToolCalls {
			modelParts = append(modelParts, Part{
				FunctionCall:     &FunctionCall{Name: tc.Name, Args: tc.Args},
				ThoughtSignature: tc.ThoughtSignature,
			})
		}
		current = append(current, Content{Role: "model", Parts: modelParts})

		// Eksekusi tiap tool call, dedup dalam satu giliran supaya kalau
		// model minta tool yang sama persis 2x, tidak dieksekusi dobel.
		seen := map[string]bool{}
		var responseParts []Part
		for _, tc := range resp.ToolCalls {
			key := fmt.Sprintf("%s:%v", tc.Name, tc.Args)
			var output string
			if seen[key] {
				output = "[DILEWATI] Tool ini sudah dipanggil dengan argumen yang sama pada giliran ini."
			} else {
				seen[key] = true
				result, execErr := CallTool(actx, tc.Name, tc.Args)
				if execErr != nil {
					output = fmt.Sprintf("[TOOL_GAGAL] %s", execErr.Error())
				} else {
					output = result
				}
			}
			responseParts = append(responseParts, Part{
				FunctionResponse: &FunctionResponse{
					Name:     tc.Name,
					Response: map[string]interface{}{"result": output},
				},
			})
		}
		current = append(current, Content{Role: "user", Parts: responseParts})
		// loop lagi — model akan lihat hasil tool dan memutuskan langkah berikutnya
	}

	return "", fmt.Errorf("agent loop: melebihi %d putaran tanpa jawaban final", MaxLoops)
}

// NewAgentContext adalah helper kecil membangun AgentContext dari data minimal.
func NewAgentContext(ctx context.Context, senderJID, chatJID string, isGroup bool) *AgentContext {
	return &AgentContext{
		Context:   ctx,
		SenderJID: senderJID,
		ChatJID:   chatJID,
		IsGroup:   isGroup,
		Extra:     make(map[string]interface{}),
	}
}
