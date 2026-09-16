package handler

import (
	"context"
	"fmt"
	"strings"

	"wabot-go/internal/ai"
	"wabot-go/internal/store"
	"wabot-go/plugins"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types/events"
)

// Prefix command bot, misal ".ping" atau "!ping". Ubah sesuai selera.
const CommandPrefix = "."

// NewEventHandler mengembalikan fungsi handler yang didaftarkan ke whatsmeow.
// Ini dipisah dari main.go supaya main.go tetap ringkas (cuma bootstrap).
func NewEventHandler(client *whatsmeow.Client) func(interface{}) {
	// Registry semua plugin/command didaftarkan sekali di sini.
	registry := plugins.NewRegistry()
	plugins.RegisterAll(registry)

	return func(rawEvt interface{}) {
		switch evt := rawEvt.(type) {
		case *events.Message:
			handleMessage(client, registry, evt)
		case *events.Connected:
			fmt.Println("🔌 Terhubung ke server WhatsApp")
		case *events.Disconnected:
			fmt.Println("⚠️  Terputus dari server WhatsApp")
		case *events.LoggedOut:
			fmt.Println("🚪 Bot logout dari WhatsApp. Hapus folder data/ lalu login ulang jika perlu.")
		}
	}
}

// handleMessage memproses tiap pesan masuk, mengekstrak teks & command,
// lalu memanggil plugin yang cocok. Pesan yang BUKAN command diteruskan
// ke pengecekan trigger AI (lihat handleAITrigger).
func handleMessage(client *whatsmeow.Client, registry *plugins.Registry, evt *events.Message) {
	// Abaikan pesan dari bot sendiri untuk mencegah loop
	if evt.Info.IsFromMe {
		return
	}

	store.LogIncoming(evt)

	text := extractText(evt)

	// Cek apakah pesan diawali prefix command
	if text != "" && strings.HasPrefix(text, CommandPrefix) {
		handleCommand(client, registry, evt, text)
		return
	}

	// Bukan command (termasuk pesan media tanpa caption) → cek trigger AI.
	handleAITrigger(client, evt, text)
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

	cmd, ok := registry.Get(cmdName)
	if !ok {
		return // command tidak ditemukan, diamkan saja (bisa diubah untuk balas "command tidak dikenal")
	}

	ctx := &store.MessageContext{
		Client: client,
		Chat:   evt.Info.Chat,
		Sender: evt.Info.Sender,
		RawEvt: evt,
		Args:   args,
	}

	if err := cmd.Handler(context.Background(), ctx); err != nil {
		fmt.Printf("❌ Error menjalankan command '%s': %v\n", cmdName, err)
		_ = store.ReplyText(context.Background(), ctx, "Terjadi kesalahan saat menjalankan perintah.")
	}
}

// handleAITrigger mengecek apakah pesan non-command ini memicu AI
// (kata kunci nama bot, reply ke pesan bot, sticker-reply, atau
// media-reply tanpa caption), dan kalau ya, memprosesnya lewat
// plugins.HandleAIMessage (lihat plugins/ai.go).
//
// Kalau pesan ini adalah reply ke salah satu bubble bot (lihat
// store.GetQuotedBotText), teks bubble yang di-reply disertakan sebagai
// konteks ke AI — supaya user tidak perlu mengulang topik yang ingin
// ditanyakan lebih lanjut.
func handleAITrigger(client *whatsmeow.Client, evt *events.Message, text string) {
	// sessionKey dibangun lewat ai.BuildSessionKey yang sama dipakai
	// plugins/ai.go (HandleAIMessage/processAITurn) — WAJIB identik supaya
	// HasActiveSession di bawah mengecek kunci yang benar-benar dipakai
	// GlobalSession.Append untuk menyimpan histori sesi ini. Sebelum
	// perbaikan ini, di sini selalu memakai sender polos meski histori
	// sungguhan disimpan dengan kunci gabungan chatJID:senderJID untuk
	// pesan grup — akibatnya HasActive selalu salah (selalu dianggap
	// idle) untuk sesi di dalam grup.
	sessionKey := ai.BuildSessionKey(evt.Info.IsGroup, evt.Info.Chat.String(), evt.Info.Sender.String())

	trigger := ai.DetectTrigger(ai.TriggerInput{
		Text:             text,
		IsReplyToBot:     store.IsReplyToBot(evt),
		IsStickerMsg:     store.IsStickerMessage(evt),
		IsMediaMsg:       store.IsMediaMessage(evt),
		HasActiveSession: ai.GlobalSession.HasActive(sessionKey),
	})

	if !trigger.Matched {
		return
	}

	quotedText := store.GetQuotedBotText(evt)
	finalText := ai.BuildTextWithQuotedContext(quotedText, text)

	ctx := &store.MessageContext{
		Client: client,
		Chat:   evt.Info.Chat,
		Sender: evt.Info.Sender,
		RawEvt: evt,
		Args:   nil, // trigger natural tidak berbasis command prefix, teks ada di 'text'
	}

	if err := plugins.HandleAIMessage(context.Background(), ctx, finalText); err != nil {
		fmt.Printf("❌ Error memproses trigger AI (alasan: %s): %v\n", trigger.Reason, err)
		_ = store.ReplyText(context.Background(), ctx, "Maaf, ada gangguan saat memproses ini.")
	}
}

// extractText mengambil teks dari berbagai tipe pesan (teks biasa, caption gambar, dll).
//
// Pesan di-unwrap dulu lewat store.UnwrapMessage — WhatsApp membungkus isi
// pesan asli satu-tiga layer di dalam EphemeralMessage ("Pesan Sementara" di
// grup) atau ViewOnceMessage (foto/video sekali lihat), sehingga tanpa
// unwrap ini semua getter di bawah akan selalu kosong untuk pesan bertipe
// tersebut dan bot terlihat "diam" di grup yang mengaktifkan fitur itu.
// Fungsi unwrap yang sama juga dipakai oleh ExtractStanzaID/
// IsStickerMessage/IsMediaMessage di internal/store/trigger_extract.go
// supaya perilakunya konsisten di semua jalur.
func extractText(evt *events.Message) string {
	msg := store.UnwrapMessage(evt.Message)
	if msg == nil {
		return ""
	}

	if msg.GetConversation() != "" {
		return msg.GetConversation()
	}
	if ext := msg.GetExtendedTextMessage(); ext != nil {
		return ext.GetText()
	}
	if img := msg.GetImageMessage(); img != nil {
		return img.GetCaption()
	}
	if vid := msg.GetVideoMessage(); vid != nil {
		return vid.GetCaption()
	}

	return ""
}
