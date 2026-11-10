package handler

import (
	"context"
	"strings"

	"wabot-go/internal/ai"
	"wabot-go/internal/diag"
	"wabot-go/internal/logfmt"
	"wabot-go/internal/metrics"
	"wabot-go/internal/store"
	"wabot-go/plugins"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/types/events"
)

// classifyAim menentukan baris aim: untuk log pesan masuk (lihat
// store.LogIncoming). "command (.x)" bila teks berprefix command/spesial,
// "agent" bila pemicu AI cocok, "none" bila keduanya tidak.
// Catatan jujur: ini MAKSUD pesan, bukan hasilnya — gerbang ban/lock/trust
// bisa tetap menggugurkan pesan ber-aim agent/command setelahnya. Input
// game angka polos tidak diklasifikasikan di sini (CheckGameInput punya
// efek samping konsumsi, tak boleh dipanggil dua kali) — ia tercatat
// mengikuti hasil trigger seperti pesan biasa.
func classifyAim(client *whatsmeow.Client, evt *events.Message, text string) string {
	if text != "" {
		if strings.HasPrefix(text, CommandPrefix) {
			name := text
			if f := strings.Fields(strings.TrimPrefix(text, CommandPrefix)); len(f) > 0 {
				name = f[0]
			}
			return "command (." + strings.ToLower(name) + ")"
		}
		if _, ok := cutSpecialPrefix(text, "$"); ok {
			return "command ($)"
		}
		if _, ok := cutSpecialPrefix(text, "\\"); ok {
			return "command (\\)"
		}
	}
	sessionKey := ai.BuildSessionKey(evt.Info.IsGroup, evt.Info.Chat.String(), evt.Info.Sender.String())
	trigger := ai.DetectTrigger(ai.TriggerInput{
		Text:             text,
		IsReplyToBot:     store.IsReplyToBot(evt),
		IsStickerMsg:     store.IsStickerMessage(evt),
		IsMediaMsg:       store.IsMediaMessage(evt),
		HasActiveSession: ai.GlobalSession.HasActive(sessionKey),
	})
	if trigger.Matched {
		return "agent"
	}
	return "none"
}

// cutSpecialPrefix memotong prefix spesial di awal teks. Mengembalikan sisa
// teks (boleh kosong — pemanggil yang memutuskan) dan true kalau cocok.
func cutSpecialPrefix(text, prefix string) (string, bool) {
	if text == "" || !strings.HasPrefix(text, prefix) {
		return "", false
	}
	return strings.TrimPrefix(text, prefix), true
}

// handleCommand memproses pesan yang diawali prefix command seperti sebelumnya.
func handleCommand(client *whatsmeow.Client, registry *plugins.Registry, evt *events.Message, text string) {
	// Parse "command arg1 arg2" menjadi nama command + argumen
	withoutPrefix := strings.TrimPrefix(text, CommandPrefix)
	parts := strings.Fields(withoutPrefix)
	if len(parts) == 0 {
		return
	}

	cmdName := strings.ToLower(parts[0])
	args := parts[1:]

	// RawArgs: teks mentah setelah nama command dengan baris baru utuh.
	// Diambil langsung dari teks asli (tanpa lewat strings.Fields yang
	// menghancurkan whitespace) — potong di whitespace pertama setelah
	// nama command, lalu trim tepi saja supaya baris baru di TENGAH utuh.
	rawArgs := ""
	if i := strings.IndexAny(withoutPrefix, " \t\n\r"); i >= 0 {
		rawArgs = strings.TrimSpace(withoutPrefix[i+1:])
	}

	executeNamedCommand(client, registry, evt, cmdName, args, rawArgs)
}

// executeNamedCommand menjalankan command yang sudah diketahui namanya
// (dari prefix "." maupun prefix spesial "$"/"\"). Dipisah dari
// handleCommand supaya semua jalur routing berbagi penanganan error yang
// sama. args boleh kosong; rawArgs adalah sisa teks mentah (baris baru
// utuh) untuk command yang membutuhkannya (shell, eval, template).
func executeNamedCommand(client *whatsmeow.Client, registry *plugins.Registry, evt *events.Message, cmdName string, args []string, rawArgs string) {
	if rawArgs == "" && len(args) > 0 {
		rawArgs = strings.Join(args, " ")
	}

	cmd, ok := registry.Get(cmdName)
	if !ok {
		return // command tidak ditemukan, diamkan saja (bisa diubah untuk balas "command tidak dikenal")
	}

	// Catat waktu selesai command (dipakai idle memory release). Defer supaya
	// tetap tercatat walau handler error/return lebih awal.
	defer diag.NoteCommandFinished()

	metrics.IncCommand(cmdName)

	// Gerbang clone (jadibot): command main-only (flag MainOnly atau
	// kategori Owner/Hidden/AI/Absensi) ditolak eksplisit di clone supaya
	// user tahu harus ke bot utama — bukan diam-diam.
	if cmd.CloneBlocked() && !store.IsMainClient(client) {
		mctx := &store.MessageContext{
			Client: client, Chat: evt.Info.Chat, Sender: evt.Info.Sender, RawEvt: evt,
		}
		_ = store.ReplyText(context.Background(), mctx, "Perintah ini hanya tersedia di bot utama.")
		return
	}

	ctx := &store.MessageContext{
		Client:  client,
		Chat:    evt.Info.Chat,
		Sender:  evt.Info.Sender,
		RawEvt:  evt,
		Args:    args,
		RawArgs: rawArgs,
	}

	if err := cmd.Handler(context.Background(), ctx); err != nil {
		logfmt.Error("Error menjalankan command '%s': %v\n", cmdName, err)
		_ = store.ReplyText(context.Background(), ctx, "Terjadi kesalahan saat menjalankan perintah.")
	}
}
