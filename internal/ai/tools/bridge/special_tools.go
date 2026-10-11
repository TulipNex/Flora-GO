package bridge

import (
	"context"
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

// selfCallWords adalah kata ganti diri yang berarti "telepon saya sendiri".
// Dicek case-insensitive setelah trim — model diinstruksikan mengosongkan
// phone_number untuk kasus ini, tapi kata-katanya tetap diterima sebagai
// jaring pengaman bila model menyalin ucapan user apa adanya.
var selfCallWords = map[string]bool{
	"saya": true, "aku": true, "gue": true, "gw": true,
	"saya sendiri": true, "aku sendiri": true,
	"diri saya": true, "diriku": true, "diri aku": true,
}

// normCallDigits menormalkan nomor ke digit kanonis (awalan 0 lokal jadi 62)
// untuk perbandingan self-call. Cermin normPhoneDigits di plugins/call.go
// yang tidak diekspor.
func normCallDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	d := b.String()
	if strings.HasPrefix(d, "0") {
		d = "62" + d[1:]
	}
	return d
}

// isSelfCall true bila nomor target sama dengan nomor peminta sendiri
// (perbandingan digit kanonis). Best-effort: peminta yang tak terpetakan
// (LID tanpa pemetaan) mengembalikan false — pemanggil memutuskan
// fail-closed atau fallback ke gerbang owner.
func isSelfCall(ctx context.Context, mctx *store.MessageContext, targetPhone string) bool {
	selfPhone, err := resolveOwnerPhone(ctx, mctx)
	if err != nil {
		return false
	}
	a, b := normCallDigits(targetPhone), normCallDigits(selfPhone)
	return a != "" && a == b
}

func init() {
	ai.RegisterTool(ai.ToolDefinition{
		Name:        "make_call",
		Description: "Melakukan panggilan WhatsApp keluar ke nomor telepon, langsung atau terjadwal. PENTING: hanya gunakan kalau peminta yang eksplisit memintanya (mis. owner bilang 'telpon ...'), JANGAN pernah untuk permintaan orang lain — panggilan berdering di HP sungguhan. Bila user minta menelepon DIRINYA SENDIRI ('telepon saya/aku/gue'), KOSONGKAN phone_number — JANGAN mengarang nomor, sistem memakai nomor pengirim pesan.",
		Parameters: ai.ParameterSchema{
			Type: "OBJECT",
			Properties: map[string]ai.PropertySchema{
				"phone_number": {
					Type:        "STRING",
					Description: "Nomor telepon yang mau dihubungi (628xxx, 08xx, atau +62 8xx-xxxx; bukan nomor bot sendiri), atau LID member (dari mention @<angka> di grup). Contoh: 6281234567890. KOSONGKAN bila user minta menelepon dirinya sendiri.",
				},
				"at": {
					Type:        "STRING",
					Description: "Opsional: jadwalkan panggilan pada jam format HH.MM zona waktu bot hari ini (atau besok kalau sudah lewat), contoh: 17.00. Jadwal tersimpan dan tahan restart. Kosongkan untuk menelpon langsung.",
				},
			},
		},
		Execute: func(actx *ai.AgentContext, args map[string]interface{}) (string, error) {
			mctx, err := extractMctx(actx)
			if err != nil {
				return "", err
			}
			ctx := actx.Context
			if ctx == nil {
				ctx = context.Background()
			}
			phoneRaw, _ := args["phone_number"].(string)
			trimmed := strings.TrimSpace(phoneRaw)

			var phone string
			if trimmed == "" || selfCallWords[strings.ToLower(trimmed)] {
				// Telepon diri sendiri: resolve nomor peminta (PN langsung,
				// LID via partisipan grup/GetAltJID — reuse resolveOwnerPhone
				// yang sama dipakai eskalasi reminder). Gagal = error jelas,
				// JANGAN pernah menelepon nomor tebakan.
				selfPhone, rerr := resolveOwnerPhone(ctx, mctx)
				if rerr != nil {
					return "", fmt.Errorf("nomormu tak terpetakan (%v) — sebutkan nomor eksplisit, contoh 6281234567890", rerr)
				}
				phone = selfPhone
			} else {
				// Validasi format dulu (sekaligus normalisasi +/spasi/strip dan
				// 0 lokal jadi 62). Lalu resolve LID→nomor HP lewat jalur yang
				// SAMA dengan command .call — AI sering menyerahkan LID dari
				// mention, yang tidak bisa langsung ditelpon.
				cleaned, err := plugins.ParsePhoneToJID(phoneRaw)
				if err != nil {
					return "", err
				}
				phone = cleaned.User
				if resolved, rerr := plugins.ResolveCallPhone(ctx, mctx, types.NewJID(cleaned.User, types.HiddenUserServer)); rerr == nil {
					phone = resolved
				}
				// Kalau resolve gagal (mis. chat private / nomor tidak dikenal
				// di grup), pakai nomor hasil parse apa adanya — perilaku lama
				// yang benar untuk nomor HP asli.
			}

			// Izin bertingkat: telepon diri sendiri boleh owner + trusted
			// (dibandingkan via digit kanonis supaya nomor sendiri yang
			// diketik eksplisit diperlakukan sama dengan "telepon saya");
			// telepon ORANG LAIN khusus owner.
			if isSelfCall(ctx, mctx, phone) {
				if !plugins.IsOwner(mctx) && !plugins.IsOwnerOrTrusted(ctx, mctx) {
					return "", fmt.Errorf("hanya owner dan user terpercaya yang boleh memakai aksi ini")
				}
			} else if err := requireOwnerTool(mctx); err != nil {
				return "", err
			}
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
			if err := plugins.CallWithProgress(ctx, mctx.Client, mctx.Chat, phone); err != nil {
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
