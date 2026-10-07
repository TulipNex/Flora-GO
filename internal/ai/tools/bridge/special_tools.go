package bridge

import (
	"fmt"
	"strings"

	"wabot-go/internal/ai"
	"wabot-go/internal/store"
	"wabot-go/internal/timex"
	"wabot-go/plugins"

	"go.mau.fi/whatsmeow/types"
)

// requireOwnerTool memastikan peminta tool adalah owner bot. Dipakai tool
// berdampak tinggi (telepon keluar, mode bot) yang tidak boleh dipicu orang
// lain lewat AI walau mereka admin grup. Pengecekan memakai plugins.IsOwner
// supaya LID vs PN tetap tertangani (cek SenderAlt juga).
func requireOwnerTool(mctx *store.MessageContext) error {
	if !plugins.IsOwner(mctx) {
		return fmt.Errorf("hanya owner bot yang boleh memakai aksi ini")
	}
	return nil
}

func init() {
	ai.RegisterTool(ai.ToolDefinition{
		Name:        "make_call",
		Description: "Melakukan panggilan WhatsApp keluar ke nomor telepon, langsung atau terjadwal. PENTING: hanya gunakan kalau OWNER bot yang eksplisit memintanya (mis. owner bilang 'telpon ...'), JANGAN pernah untuk permintaan orang lain — panggilan berdering di HP sungguhan.",
		Parameters: ai.ParameterSchema{
			Type: "OBJECT",
			Properties: map[string]ai.PropertySchema{
				"phone_number": {
					Type:        "STRING",
					Description: "Nomor telepon yang mau dihubungi (628xxx, 08xx, atau +62 8xx-xxxx; bukan nomor bot sendiri), atau LID member (dari mention @<angka> di grup). Contoh: 6281234567890",
				},
				"at": {
					Type:        "STRING",
					Description: "Opsional: jadwalkan panggilan pada jam format HH.MM zona waktu bot hari ini (atau besok kalau sudah lewat), contoh: 17.00. Jadwal tersimpan dan tahan restart. Kosongkan untuk menelpon langsung.",
				},
			},
			Required: []string{"phone_number"},
		},
		Execute: func(actx *ai.AgentContext, args map[string]interface{}) (string, error) {
			mctx, err := extractMctx(actx)
			if err != nil {
				return "", err
			}
			if err := requireOwnerTool(mctx); err != nil {
				return "", err
			}
			phoneRaw, _ := args["phone_number"].(string)
			// Validasi format dulu (sekaligus normalisasi +/spasi/strip dan
			// 0 lokal jadi 62). Lalu resolve LID→nomor HP lewat jalur yang
			// SAMA dengan command .call — AI sering menyerahkan LID dari
			// mention, yang tidak bisa langsung ditelpon.
			cleaned, err := plugins.ParsePhoneToJID(phoneRaw)
			if err != nil {
				return "", err
			}
			phone := cleaned.User
			if resolved, rerr := plugins.ResolveCallPhone(actx.Context, mctx, types.NewJID(cleaned.User, types.HiddenUserServer)); rerr == nil {
				phone = resolved
			}
			// Kalau resolve gagal (mis. chat private / nomor tidak dikenal
			// di grup), pakai nomor hasil parse apa adanya — perilaku lama
			// yang benar untuk nomor HP asli.
			if atRaw, _ := args["at"].(string); strings.TrimSpace(atRaw) != "" {
				at, err := plugins.ParseJamWITA(strings.TrimSpace(atRaw))
				if err != nil {
					return "", fmt.Errorf("format jam salah (%v), pakai HH.MM zona waktu bot contoh 17.00", err)
				}
				if _, err := plugins.ScheduleCall(mctx.Chat, phone, at); err != nil {
					return "", fmt.Errorf("gagal menyimpan jadwal: %w", err)
				}
				return fmt.Sprintf("Berhasil: panggilan ke %s dijadwalkan pukul %s %s, tersimpan dan tahan restart. Progresnya tampil di chat ini saat waktunya tiba.", phone, at.Format("15:04"), timex.Name()), nil
			}
			// Langsung: progres (memanggil → berdering → terhubung →
			// berakhir/ditolak/tidak dijawab) tampil di SATU bubble chat
			// ini yang diedit tiap status berubah.
			if err := plugins.CallWithProgress(actx.Context, mctx.Client, mctx.Chat, phone); err != nil {
				return "", fmt.Errorf("gagal melakukan panggilan: %w", err)
			}
			return fmt.Sprintf("Baik, menelpon %s sekarang — progresnya tampil di chat ini.", phone), nil
		},
	})

	ai.RegisterTool(ai.ToolDefinition{
		Name:        "bot_mode",
		Description: "Melihat atau mengganti mode bot: self (hanya owner yang dilayani) atau public (semua orang dilayani). PENTING: hanya gunakan kalau OWNER bot yang eksplisit memintanya, JANGAN untuk permintaan orang lain.",
		Parameters: ai.ParameterSchema{
			Type: "OBJECT",
			Properties: map[string]ai.PropertySchema{
				"mode": {
					Type:        "STRING",
					Description: "Mode baru: self atau public. Kosongkan untuk melihat mode saat ini.",
				},
			},
		},
		Execute: func(actx *ai.AgentContext, args map[string]interface{}) (string, error) {
			mctx, err := extractMctx(actx)
			if err != nil {
				return "", err
			}
			if err := requireOwnerTool(mctx); err != nil {
				return "", err
			}
			mode, _ := args["mode"].(string)
			mode = strings.ToLower(strings.TrimSpace(mode))
			if mode == "" {
				current, err := store.Groups().GetBotMode(actx.Context)
				if err != nil {
					return "", fmt.Errorf("gagal baca mode bot: %w", err)
				}
				return fmt.Sprintf("Mode bot saat ini: %s.", current), nil
			}
			if err := store.Groups().SetBotMode(actx.Context, mode); err != nil {
				return "", err
			}
			return fmt.Sprintf("Berhasil: mode bot diganti jadi %s.", mode), nil
		},
	})

	ai.RegisterTool(ai.ToolDefinition{
		Name:        "make_sticker",
		Description: "Ubah gambar/video yang dilampirkan atau di-reply user menjadi stiker WhatsApp lalu kirim ke chat ini. Gunakan kalau user meminta dibuatkan stiker dari gambar/video (mis. 'jadiin stiker', 'buatkan stiker dari ini'). Gambar/video sumbernya diambil dari pesan user yang memicu (lampiran langsung atau reply).",
		Parameters: ai.ParameterSchema{
			Type: "OBJECT",
			Properties: map[string]ai.PropertySchema{
				"packname": {
					Type:        "STRING",
					Description: "Nama pack stiker. Boleh kosong (pakai default).",
				},
				"author": {
					Type:        "STRING",
					Description: "Nama author stiker. Boleh kosong (pakai default).",
				},
			},
		},
		Execute: func(actx *ai.AgentContext, args map[string]interface{}) (string, error) {
			mctx, err := extractMctx(actx)
			if err != nil {
				return "", err
			}
			pack, _ := args["packname"].(string)
			author, _ := args["author"].(string)
			if err := plugins.BuildAndSendSticker(actx.Context, mctx, pack, author); err != nil {
				return "", err
			}
			return "Berhasil: stiker sudah dibuat dan dikirim ke chat.", nil
		},
	})
}
