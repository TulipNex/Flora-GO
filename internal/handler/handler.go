package handler

import (
	"context"
	"fmt"
	"strings"

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
// lalu memanggil plugin yang cocok.
func handleMessage(client *whatsmeow.Client, registry *plugins.Registry, evt *events.Message) {
	// Abaikan pesan dari bot sendiri untuk mencegah loop
	if evt.Info.IsFromMe {
		return
	}

	store.LogIncoming(evt)

	text := extractText(evt)
	if text == "" {
		return
	}

	// Cek apakah pesan diawali prefix command
	if !strings.HasPrefix(text, CommandPrefix) {
		return
	}

	// Parse "командо arg1 arg2" menjadi nama command + argumen
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

// extractText mengambil teks dari berbagai tipe pesan (teks biasa, caption gambar, dll)
func extractText(evt *events.Message) string {
	msg := evt.Message
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
