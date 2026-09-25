package ai

import (
	"context"
	"fmt"
	"strings"
	"time"
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
		// supaya provider "ingat" dia baru saja meminta tool tersebut.
		// PENTING dua hal WAJIB diikutsertakan apa adanya persis seperti
		// yang dikembalikan model:
		//   - ThoughtSignature (di Part): Gemini (seri 2.5/3) menolak
		//     request berikutnya dengan error 400 kalau field ini hilang.
		//   - ID (di FunctionCall): provider ber-gaya OpenAI
		//     (OpenRouter/NexaRouter) mensyaratkan tool_call_id pada balasan
		//     role="tool" cocok PERSIS dengan id yang diterbitkan di sini.
		//     Untuk Gemini, ID selalu kosong ("") dan memang tidak
		//     dipakai/tidak ikut termarshal ke request (lihat types.go,
		//     tag json:"-").
		// Lihat catatan lengkap di types.go pada Part/FunctionCall dan di
		// provider_types.go pada struct ToolCall.
		modelParts := make([]Part, 0, len(resp.ToolCalls)+1)
		if resp.Text != "" {
			modelParts = append(modelParts, Part{Text: resp.Text})
		}
		for _, tc := range resp.ToolCalls {
			modelParts = append(modelParts, Part{
				FunctionCall:     &FunctionCall{ID: tc.ID, Name: tc.Name, Args: tc.Args},
				ThoughtSignature: tc.ThoughtSignature,
			})
		}
		current = append(current, Content{Role: "model", Parts: modelParts})

		// Eksekusi tiap tool call, dedup dalam satu giliran supaya kalau
		// model minta tool yang sama persis 2x, tidak dieksekusi dobel.
		// Tiap eksekusi dicatat ke terminal (gaya timeline seperti log
		// pesan) supaya terlihat apa yang dilakukan agent — sebelumnya
		// eksekusi tool tidak meninggalkan jejak sama sekali di log.
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
			logToolCall(tc.Name, tc.Args, output)
			responseParts = append(responseParts, Part{
				FunctionResponse: &FunctionResponse{
					// ID di-echo balik dari tc.ID (bukan lagi memakai Name
					// sebagai pengganti ID) supaya adapter OpenAI-compatible
					// bisa memasangkan balasan ini ke tool_call yang benar
					// lewat tool_call_id asli. Lihat openrouter_adapter.go.
					ID:       tc.ID,
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

// logToolCall mencatat satu eksekusi tool ke terminal: nama, argumen
// ringkas, dan hasil ringkas (sukses/gagal). Argumen & hasil dipadatkan
// satu baris + dipotong 100 rune supaya tidak membanjiri log.
func logToolCall(name string, args map[string]interface{}, output string) {
	fmt.Printf("%s │ ● TOOL %s(%s) → %s\n",
		time.Now().Format("15:04"),
		name,
		compactOneLine(fmt.Sprintf("%v", args), 80),
		compactOneLine(output, 100),
	)
}

// compactOneLine memadatkan teks jadi satu baris dan memotongnya ke max
// rune (tambah "..." kalau dipotong).
func compactOneLine(text string, max int) string {
	oneLine := strings.Join(strings.Fields(text), " ")
	runes := []rune(oneLine)
	if len(runes) <= max {
		return oneLine
	}
	return string(runes[:max]) + "..."
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
