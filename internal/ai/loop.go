package ai

import (
	"context"
	"fmt"
	"strings"
	"time"

	"wabot-go/internal/timex"
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
	loopStart := time.Now()
	sessionKey := BuildSessionKey(actx.IsGroup, actx.ChatJID, actx.SenderJID)

	for i := 0; i < MaxLoops; i++ {
		logRoundRequest(sessionKey, i, current, systemPrompt)
		resp, err := provider.Generate(actx.Context, GenerateParams{
			History:      current,
			SystemPrompt: systemPrompt,
			Tools:        GetToolDeclarations(),
		})
		if err != nil {
			return "", err
		}
		logRoundResponse(sessionKey, i, resp)

		// Tidak ada tool call → ini jawaban final.
		if len(resp.ToolCalls) == 0 {
			logLoopDone(sessionKey, i+1, loopStart, resp)
			return resp.Text, nil
		}

		// Tambahkan giliran model (berisi permintaan tool) ke history,
		// supaya provider "ingat" dia baru saja meminta tool tersebut.
		// PENTING ID (di FunctionCall) WAJIB diikutsertakan apa adanya
		// persis seperti yang dikembalikan model: provider OpenAI-compatible
		// (NexaRouter/Ciora) mensyaratkan tool_call_id pada balasan
		// role="tool" cocok PERSIS dengan id yang diterbitkan di sini
		// (lihat types.go, tag json:"-").
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
			var output, status string
			if seen[key] {
				output = "[DILEWATI] Tool ini sudah dipanggil dengan argumen yang sama pada giliran ini."
				status = "dilewati"
				logToolFull(sessionKey, tc, output, i, 0, status)
			} else {
				seen[key] = true
				toolStart := time.Now()
				result, execErr := CallTool(actx, tc.Name, tc.Args)
				toolMs := time.Since(toolStart).Milliseconds()
				if execErr != nil {
					output = fmt.Sprintf("[TOOL_GAGAL] %s", execErr.Error())
					status = "gagal"
				} else {
					output = result
					status = "ok"
				}
				logToolFull(sessionKey, tc, output, i, toolMs, status)
			}
			responseParts = append(responseParts, Part{
				FunctionResponse: &FunctionResponse{
					// ID di-echo balik dari tc.ID supaya adapter
					// OpenAI-compatible bisa memasangkan balasan ini ke
					// tool_call yang benar lewat tool_call_id asli.
					// Lihat openai_common.go.
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

// logToolFull mencatat satu eksekusi tool UTUH (format blok MESSAGE).
// Mode ringkas (AI_LOG_FULL=0) kembali ke satu baris ala format lama.
func logToolFull(sessionKey string, tc ToolCall, output string, round int, toolMs int64, status string) {
	if !AILogFullMode() {
		fmt.Printf("%s │ ● TOOL %s(%s) → %s\n",
			timex.Now().Format("15:04"),
			tc.Name,
			compactOneLine(fmt.Sprintf("%v", tc.Args), 80),
			compactOneLine(output, 100),
		)
		return
	}
	id := tc.ID
	if id == "" {
		id = "-"
	}
	aiBlockNew("AI_TOOL", []AIField{
		{"name", tc.Name},
		{"at", fmt.Sprintf("putaran=%d sesi=%s", round+1, sessionKey)},
		{"status", fmt.Sprintf("%s %dms", status, toolMs)},
		{"id", id},
	}, []AISection{
		{"args", aiJSON(tc.Args)},
		{"output", output},
	})
}

// logRoundRequest mencatat request tiap putaran (format blok MESSAGE).
func logRoundRequest(sessionKey string, round int, current []Content, systemPrompt string) {
	if !AILogFullMode() {
		return
	}
	decls := GetToolDeclarations()
	names := make([]string, 0, len(decls))
	for _, d := range decls {
		names = append(names, d.Name)
	}
	aiBlockNew("AI_REQ", []AIField{
		{"at", fmt.Sprintf("putaran=%d sesi=%s", round+1, sessionKey)},
		{"konten", fmt.Sprintf("%d", len(current))},
		{"tools", fmt.Sprintf("%d (%s)", len(names), strings.Join(names, ", "))},
	}, []AISection{
		{"system", systemPrompt},
		{"history", aiJSON(current)},
	})
}

// logRoundResponse mencatat respons tiap putaran (format blok MESSAGE).
func logRoundResponse(sessionKey string, round int, resp *AgentResponse) {
	if !AILogFullMode() {
		return
	}
	var calls strings.Builder
	for _, tc := range resp.ToolCalls {
		fmt.Fprintf(&calls, "- %s id=%s args=%s\n", tc.Name, tc.ID, aiJSON(tc.Args))
	}
	text := resp.Text
	if strings.TrimSpace(text) == "" {
		text = "(kosong — model minta tool)"
	}
	aiBlockNew("AI_RESP", []AIField{
		{"at", fmt.Sprintf("putaran=%d sesi=%s", round+1, sessionKey)},
		{"provider", resp.Provider},
		{"durasi", fmt.Sprintf("%dms", resp.DurationMs)},
		{"finish", resp.FinishReason},
		{"usage", fmt.Sprintf("%d/%d/%d", resp.Usage.Prompt, resp.Usage.Completion, resp.Usage.Total)},
	}, []AISection{
		{"calls", strings.TrimRight(calls.String(), "\n")},
		{"text", text},
	})
}

// logLoopDone mencatat selesainya loop (format blok MESSAGE).
func logLoopDone(sessionKey string, rounds int, start time.Time, resp *AgentResponse) {
	if !AILogFullMode() {
		return
	}
	aiBlockNew("AI_LOOP", []AIField{
		{"at", "sesi=" + sessionKey},
		{"putaran", fmt.Sprintf("%d", rounds)},
		{"total", fmt.Sprintf("%dms", time.Since(start).Milliseconds())},
		{"usage", fmt.Sprintf("%d/%d/%d", resp.Usage.Prompt, resp.Usage.Completion, resp.Usage.Total)},
	}, nil)
}

// compactOneLine memadatkan teks jadi satu baris dan memotongnya ke max
// rune (tambah "..." kalau dipotong). Dipakai hanya mode ringkas.
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
