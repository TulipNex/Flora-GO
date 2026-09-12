package ai

import (
	"context"
	"fmt"
	"sync"
)

// AgentContext dibawa ke setiap eksekusi tool, berisi info yang mungkin
// dibutuhkan tool (siapa yang chat, di chat mana, dst). Field Extra
// disediakan sebagai jalan keluar generik supaya package ai/ tidak perlu
// import whatsmeow secara langsung — tool bridge (yang butuh whatsmeow
// client, dsb) mengisi & membaca lewat Extra sesuai kebutuhannya sendiri.
type AgentContext struct {
	Context   context.Context
	SenderJID string
	ChatJID   string
	IsGroup   bool
	Extra     map[string]interface{}
}

// ToolDefinition adalah satu tool yang bisa dipanggil AI.
type ToolDefinition struct {
	Name        string
	Description string
	Parameters  ParameterSchema
	Execute     func(actx *AgentContext, args map[string]interface{}) (string, error)
}

var (
	registryMu sync.Mutex
	registry   = map[string]ToolDefinition{}
)

// RegisterTool mendaftarkan satu tool. Dipanggil dari init() di masing-masing
// file tool (internal/ai/tools/native/*.go, internal/ai/tools/bridge/*.go),
// persis pola self-registration yang sudah dipakai plugins/registry.go.
func RegisterTool(def ToolDefinition) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[def.Name] = def
}

// GetToolDeclarations mengembalikan skema semua tool terdaftar, dalam
// format yang siap dikirim ke Gemini sebagai GenerateParams.Tools.
func GetToolDeclarations() []FunctionDecl {
	registryMu.Lock()
	defer registryMu.Unlock()

	decls := make([]FunctionDecl, 0, len(registry))
	for _, t := range registry {
		decls = append(decls, FunctionDecl{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.Parameters,
		})
	}
	return decls
}

// CallTool mengeksekusi satu tool berdasarkan nama. Error dikembalikan
// apa adanya ke pemanggil (loop.go) — loop yang memutuskan cara
// melaporkannya balik ke model (ditandai [TOOL_GAGAL]).
func CallTool(actx *AgentContext, name string, args map[string]interface{}) (string, error) {
	registryMu.Lock()
	tool, ok := registry[name]
	registryMu.Unlock()

	if !ok {
		return "", fmt.Errorf("tool %q tidak terdaftar", name)
	}
	return tool.Execute(actx, args)
}
