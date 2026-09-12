package bridge

import (
	"fmt"

	"wabot-go/internal/ai"
	"wabot-go/internal/store"
	"wabot-go/plugins"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// extractMctx mengambil *store.MessageContext asli dari AgentContext.Extra,
// yang diisi plugins/ai.go (HandleAIMessage) sebelum agent loop dijalankan.
// Ini jalan keluar generik supaya internal/ai/ sendiri tidak perlu import
// whatsmeow — hanya tool bridge (yang memang butuh whatsmeow client) yang
// melakukan type assertion ini.
func extractMctx(actx *ai.AgentContext) (*store.MessageContext, error) {
	raw, ok := actx.Extra["mctx"]
	if !ok {
		return nil, fmt.Errorf("konteks pesan tidak tersedia untuk tool ini")
	}
	mctx, ok := raw.(*store.MessageContext)
	if !ok {
		return nil, fmt.Errorf("konteks pesan tidak valid untuk tool ini")
	}
	return mctx, nil
}

func init() {
	ai.RegisterTool(ai.ToolDefinition{
		Name:        "kick_member",
		Description: "Keluarkan seorang member dari grup WhatsApp ini berdasarkan nomor teleponnya. Hanya bisa dipakai di dalam grup, dan hanya kalau pengirim perintah adalah admin grup serta bot juga admin grup.",
		Parameters: ai.ParameterSchema{
			Type: "OBJECT",
			Properties: map[string]ai.PropertySchema{
				"phone_number": {
					Type:        "STRING",
					Description: "Nomor telepon member yang mau dikeluarkan, format internasional tanpa tanda +, contoh: 6281234567890",
				},
			},
			Required: []string{"phone_number"},
		},
		Execute: func(actx *ai.AgentContext, args map[string]interface{}) (string, error) {
			return executeGroupChange(actx, args, whatsmeow.ParticipantChangeRemove, "dikeluarkan dari grup")
		},
	})

	ai.RegisterTool(ai.ToolDefinition{
		Name:        "promote_member",
		Description: "Jadikan seorang member sebagai admin grup WhatsApp ini berdasarkan nomor teleponnya. Hanya bisa dipakai di dalam grup, dan hanya kalau pengirim perintah adalah admin grup serta bot juga admin grup.",
		Parameters: ai.ParameterSchema{
			Type: "OBJECT",
			Properties: map[string]ai.PropertySchema{
				"phone_number": {
					Type:        "STRING",
					Description: "Nomor telepon member yang mau dijadikan admin, format internasional tanpa tanda +, contoh: 6281234567890",
				},
			},
			Required: []string{"phone_number"},
		},
		Execute: func(actx *ai.AgentContext, args map[string]interface{}) (string, error) {
			return executeGroupChange(actx, args, whatsmeow.ParticipantChangePromote, "dijadikan admin grup")
		},
	})
}

// executeGroupChange adalah logic bersama kick_member & promote_member —
// keduanya cuma beda di ParticipantChange apa yang dipanggil dan kata kerja
// di pesan hasil.
func executeGroupChange(actx *ai.AgentContext, args map[string]interface{}, change whatsmeow.ParticipantChange, successVerb string) (string, error) {
	mctx, err := extractMctx(actx)
	if err != nil {
		return "", err
	}

	// Reuse validasi admin yang SAMA PERSIS dengan command .kick/.promote manual.
	if msg := plugins.RequireGroupAdminGate(actx.Context, mctx); msg != "" {
		return "", fmt.Errorf("%s", msg)
	}

	phoneRaw, _ := args["phone_number"].(string)
	target, err := plugins.ParsePhoneToJID(phoneRaw)
	if err != nil {
		return "", err
	}

	_, err = mctx.Client.UpdateGroupParticipants(actx.Context, mctx.Chat, []types.JID{target}, change)
	if err != nil {
		return "", fmt.Errorf("gagal ubah status member: %w", err)
	}

	return fmt.Sprintf("Berhasil: %s telah %s.", phoneRaw, successVerb), nil
}
