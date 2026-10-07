package bridge

import (
	"fmt"

	"wabot-go/internal/ai"
	"wabot-go/internal/store"
	"wabot-go/plugins"
)

// requireSenderAdmin adalah gate tool yang butuh hak admin PENGIRIM tapi
// TIDAK butuh bot admin (kasus: tag_all — mengirim pesan biasa tidak butuh
// hak admin, sama seperti command .tagall). Owner selalu lolos.
func requireSenderAdmin(actx *ai.AgentContext, mctx *store.MessageContext) error {
	if !mctx.IsGroup() {
		return fmt.Errorf("tool ini hanya bisa dipakai di dalam grup")
	}
	if plugins.IsOwner(mctx) {
		return nil
	}
	admin, err := mctx.IsSenderAdmin(actx.Context)
	if err != nil {
		return fmt.Errorf("gagal cek status admin: %w", err)
	}
	if !admin {
		return fmt.Errorf("hanya admin grup yang boleh memakai aksi ini")
	}
	return nil
}

func init() {
	ai.RegisterTool(ai.ToolDefinition{
		Name:        "set_group_name",
		Description: "Ganti nama grup ini (perlu di grup; pengirim & bot harus admin).",
		Parameters: ai.ParameterSchema{
			Type: "OBJECT",
			Properties: map[string]ai.PropertySchema{
				"group_name": {
					Type:        "STRING",
					Description: "Nama baru grup, contoh: Keluarga Besar",
				},
			},
			Required: []string{"group_name"},
		},
		Execute: func(actx *ai.AgentContext, args map[string]interface{}) (string, error) {
			mctx, err := extractMctx(actx)
			if err != nil {
				return "", err
			}
			if msg := plugins.RequireGroupAdminGate(actx.Context, mctx); msg != "" {
				return "", fmt.Errorf("%s", msg)
			}
			name, _ := args["group_name"].(string)
			if name == "" {
				return "", fmt.Errorf("nama grup tidak boleh kosong")
			}
			if err := mctx.Client.SetGroupName(actx.Context, mctx.Chat, name); err != nil {
				return "", fmt.Errorf("gagal ganti nama grup: %w", err)
			}
			return fmt.Sprintf("Berhasil: nama grup diganti jadi %s.", name), nil
		},
	})

	ai.RegisterTool(ai.ToolDefinition{
		Name:        "tag_all",
		Description: "Tandai SEMUA member grup ini sekaligus, opsional dengan pengumuman (perlu di grup; pengirim harus admin).",
		Parameters: ai.ParameterSchema{
			Type: "OBJECT",
			Properties: map[string]ai.PropertySchema{
				"message": {
					Type:        "STRING",
					Description: "Pengumuman penyerta tag, cth: Rapat jam 8 malam ini. Boleh kosong.",
				},
			},
		},
		Execute: func(actx *ai.AgentContext, args map[string]interface{}) (string, error) {
			mctx, err := extractMctx(actx)
			if err != nil {
				return "", err
			}
			if err := requireSenderAdmin(actx, mctx); err != nil {
				return "", err
			}
			msg, _ := args["message"].(string)
			if msg == "" {
				msg = "Panggilan untuk semua member grup"
			}
			n, err := plugins.TagAllMembers(actx.Context, mctx, msg)
			if err != nil {
				return "", fmt.Errorf("gagal tag semua member: %w", err)
			}
			return fmt.Sprintf("Berhasil: menandai %d member grup.", n), nil
		},
	})

	ai.RegisterTool(ai.ToolDefinition{
		Name:        "group_link",
		Description: "Ambil link invite grup ini (perlu di grup; pengirim & bot harus admin).",
		Parameters: ai.ParameterSchema{
			Type:       "OBJECT",
			Properties: map[string]ai.PropertySchema{},
		},
		Execute: func(actx *ai.AgentContext, args map[string]interface{}) (string, error) {
			mctx, err := extractMctx(actx)
			if err != nil {
				return "", err
			}
			if msg := plugins.RequireGroupAdminGate(actx.Context, mctx); msg != "" {
				return "", fmt.Errorf("%s", msg)
			}
			link, err := mctx.Client.GetGroupInviteLink(actx.Context, mctx.Chat, false)
			if err != nil {
				return "", fmt.Errorf("gagal ambil link invite: %w", err)
			}
			return fmt.Sprintf("Link invite grup: %s", link), nil
		},
	})

	ai.RegisterTool(ai.ToolDefinition{
		Name:        "group_close",
		Description: "Kunci grup ini sehingga hanya admin bisa kirim pesan (perlu di grup; pengirim & bot harus admin).",
		Parameters: ai.ParameterSchema{
			Type:       "OBJECT",
			Properties: map[string]ai.PropertySchema{},
		},
		Execute: func(actx *ai.AgentContext, args map[string]interface{}) (string, error) {
			return setGroupLocked(actx, true)
		},
	})

	ai.RegisterTool(ai.ToolDefinition{
		Name:        "group_open",
		Description: "Buka kunci grup ini sehingga semua member boleh kirim pesan (perlu di grup; pengirim & bot harus admin).",
		Parameters: ai.ParameterSchema{
			Type:       "OBJECT",
			Properties: map[string]ai.PropertySchema{},
		},
		Execute: func(actx *ai.AgentContext, args map[string]interface{}) (string, error) {
			return setGroupLocked(actx, false)
		},
	})
}

// setGroupLocked adalah logic bersama group_close & group_open — mirror dari
// handleClose/handleOpen di plugins (aksi WhatsApp + catatan lokal), supaya
// perilaku tool dan command tetap identik.
func setGroupLocked(actx *ai.AgentContext, locked bool) (string, error) {
	mctx, err := extractMctx(actx)
	if err != nil {
		return "", err
	}
	if msg := plugins.RequireGroupAdminGate(actx.Context, mctx); msg != "" {
		return "", fmt.Errorf("%s", msg)
	}
	if err := mctx.Client.SetGroupAnnounce(actx.Context, mctx.Chat, locked); err != nil {
		if locked {
			return "", fmt.Errorf("gagal mengunci grup: %w", err)
		}
		return "", fmt.Errorf("gagal membuka grup: %w", err)
	}
	// Catatan lokal fail-soft: aksi WhatsApp sudah berhasil, jangan gagalkan
	// tool hanya karena catatan gagal (pola yang sama dengan command).
	if err := store.Groups().SetClosed(actx.Context, mctx.Chat.String(), locked); err != nil {
		if locked {
			return "Berhasil: grup dikunci, tapi catatan lokal gagal disimpan.", nil
		}
		return "Berhasil: grup dibuka, tapi catatan lokal gagal disimpan.", nil
	}
	if locked {
		return "Berhasil: grup dikunci, hanya admin yang bisa kirim pesan.", nil
	}
	return "Berhasil: grup dibuka, semua member boleh kirim pesan.", nil
}
