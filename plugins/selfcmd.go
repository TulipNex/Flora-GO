package plugins

import (
	"context"
	"strings"

	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterSelfCmd)
}

// selfCmdName adalah nama command saklar — dipakai juga oleh guard handler
// (ShouldProcessSelfMessage) dan IsOwner di owneradmin.go.
const selfCmdName = "selfcmd"

// RegisterSelfCmd mendaftarkan command `.selfcmd` kategori Owner: saklar
// agar bot mengeksekusi command yang dikirim dari akunnya sendiri.
// `.selfcmd on|off` untuk menyalakan/mematikan, tanpa argumen = status.
func RegisterSelfCmd(r *Registry) {
	r.Register(Command{
		Name:        selfCmdName,
		Description: "Saklar eksekusi command dari akun bot sendiri (.selfcmd on|off)",
		Category:    CategoryOwner,
		Handler:     handleSelfCmd,
	})
}

// handleSelfCmd menyalakan/mematikan/menampilkan status saklar self-command.
// Gerbangnya bukan requireOwner biasa: pesan dari akun bot sendiri (IsFromMe)
// lolos bila memenuhi isSelfSender — perlu, karena saat toggle masih OFF
// justru dari akun sendiri inilah saklar harus bisa dinyalakan (bootstrap).
func handleSelfCmd(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" && !isSelfSender(mctx) {
		return store.ReplyText(ctx, mctx, msg)
	}
	arg := strings.ToLower(strings.TrimSpace(mctx.RawArgs))
	switch arg {
	case "on", "off":
		on := arg == "on"
		if err := store.Groups().SetSelfCommand(ctx, on); err != nil {
			return store.ReplyText(ctx, mctx, "Gagal menyimpan saklar: "+err.Error())
		}
		if on {
			return store.ReplyText(ctx, mctx, "Self-command ON: bot mengeksekusi command dari akun sendiri.")
		}
		return store.ReplyText(ctx, mctx, "Self-command OFF: pesan dari akun sendiri diabaikan.")
	default:
		on, err := store.Groups().IsSelfCommandEnabled(ctx)
		if err != nil {
			return store.ReplyText(ctx, mctx, "Gagal baca status: "+err.Error())
		}
		status := "OFF"
		if on {
			status = "ON"
		}
		return store.ReplyText(ctx, mctx, "Self-command: "+status+". Pakai `.selfcmd on|off`.")
	}
}

// isSelfSender true kalau pesan datang dari akun bot sendiri dan BUKAN
// output yang dikirim bot (anti-loop — ID output bot tercatat di
// store.TrackBotMessageID). Dipakai gerbang .selfcmd (bootstrap saat
// toggle OFF) dan IsOwner (command owner lain saat toggle ON).
func isSelfSender(mctx *store.MessageContext) bool {
	if mctx == nil || mctx.RawEvt == nil || !mctx.RawEvt.Info.IsFromMe {
		return false
	}
	return !store.IsBotMessageID(mctx.RawEvt.Info.ID)
}

// SelfCommandEnabled membaca status saklar, fail-closed (DB error = OFF)
// supaya gangguan DB tidak membuka eksekusi self-command diam-diam.
func SelfCommandEnabled() bool {
	on, err := store.Groups().IsSelfCommandEnabled(context.Background())
	if err != nil {
		return false
	}
	return on
}

// IsSelfCommandSwitch true kalau teks adalah pemanggilan saklar itu sendiri
// (".selfcmd ..."). Prefix "." di-hardcode di sini mengikuti CommandPrefix
// di internal/handler (handler mengimpor plugins, jadi konstanta itu tak
// bisa dipakai di sini tanpa import cycle).
func IsSelfCommandSwitch(text string) bool {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return false
	}
	return strings.ToLower(fields[0]) == "."+selfCmdName
}

// ShouldProcessSelfMessage memutuskan apakah pesan IsFromMe boleh masuk
// pipeline (dipanggil guard handler). Aturan berlapis, semuanya fail-closed:
//  1. Output bot sendiri tak pernah diproses ulang (anti-loop struktural).
//  2. Saklar itu sendiri selalu lolos (bootstrap: bisa dinyalakan dari
//     akun bot sendiri walau toggle masih OFF; output-nya teks biasa).
//  3. Command lain butuh toggle ON + teks berprefix command.
func ShouldProcessSelfMessage(text, id, commandPrefix string) bool {
	if store.IsBotMessageID(id) {
		return false
	}
	if text == "" {
		return false
	}
	if IsSelfCommandSwitch(text) {
		return true
	}
	if !SelfCommandEnabled() {
		return false
	}
	if strings.HasPrefix(text, commandPrefix) {
		return true
	}
	// Prefix spesial satu karakter ("$" shell, "\" eval) — lolos seperti
	// routing handler (cutSpecialPrefix cocok pada awalan saja).
	return strings.HasPrefix(text, "$") || strings.HasPrefix(text, "\\")
}
