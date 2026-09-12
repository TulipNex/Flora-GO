package plugins

import (
	"context"
	"fmt"
	"strings"
	"time"

	"wabot-go/internal/ai"
	"wabot-go/internal/config"
	"wabot-go/internal/store"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

func init() {
	registerPlugin(RegisterAI)
}

func RegisterAI(r *Registry) {
	r.Register(Command{
		Name:        "ai",
		Description: "Ngobrol dengan AI (.ai <pesan> / .ai reset / .ai provider / .ai model)",
		Handler:     handleAICommand,
	})
}

// handleAICommand menangani command eksplisit ".ai <pesan>" / ".ai reset" /
// ".ai provider [nama]" / ".ai model [nama]".
//
// Sejak fitur multi-provider: ".ai provider" tanpa argumen menampilkan
// daftar provider + yang aktif; ".ai provider <nama>" mengganti provider
// aktif. Pola sama untuk ".ai model" tapi daftarnya mengikuti provider
// yang SEDANG aktif. Pengaturan ini GLOBAL (berlaku untuk semua user,
// bukan per-sender) dan persist ke SQLite — lihat internal/ai/providers.go.
func handleAICommand(ctx context.Context, mctx *store.MessageContext) error {
	if len(mctx.Args) > 0 && strings.EqualFold(mctx.Args[0], "provider") {
		return handleProviderSubcommand(ctx, mctx)
	}
	if len(mctx.Args) > 0 && strings.EqualFold(mctx.Args[0], "model") {
		return handleModelSubcommand(ctx, mctx)
	}

	text := strings.TrimSpace(strings.Join(mctx.Args, " "))

	if text == "" {
		return store.ReplyText(ctx, mctx, "Mau ngobrol apa? Contoh: .ai halo, apa kabar?")
	}

	if strings.EqualFold(text, "reset") {
		ai.GlobalSession.Reset(mctx.Sender.String())
		return store.ReplyText(ctx, mctx, "Oke, sesi percakapan sudah direset.")
	}

	// Kalau command ".ai <pesan>" ini dikirim sebagai reply ke salah satu
	// bubble bot, sertakan teks bubble yang di-reply sebagai konteks —
	// sama seperti trigger natural (lihat handler.go handleAITrigger).
	quotedText := store.GetQuotedBotText(mctx.RawEvt)
	text = ai.BuildTextWithQuotedContext(quotedText, text)

	return HandleAIMessage(ctx, mctx, text)
}

// handleProviderSubcommand menangani ".ai provider" (tampilkan daftar) dan
// ".ai provider <nama>" (ganti provider aktif).
func handleProviderSubcommand(ctx context.Context, mctx *store.MessageContext) error {
	arg := strings.TrimSpace(strings.Join(mctx.Args[1:], " "))

	if arg == "" {
		return store.ReplyText(ctx, mctx, ai.FormatProviderList(ctx))
	}

	if err := ai.SetActiveProvider(ctx, arg); err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal ganti provider: %v\n\n%s", err, ai.FormatProviderList(ctx)))
	}

	return store.ReplyText(ctx, mctx, fmt.Sprintf("Oke, provider AI sekarang: *%s*", strings.ToLower(arg)))
}

// handleModelSubcommand menangani ".ai model" (tampilkan daftar model utk
// provider aktif) dan ".ai model <nama>" (ganti model aktif).
func handleModelSubcommand(ctx context.Context, mctx *store.MessageContext) error {
	arg := strings.TrimSpace(strings.Join(mctx.Args[1:], " "))

	if arg == "" {
		return store.ReplyText(ctx, mctx, ai.FormatModelList(ctx))
	}

	if err := ai.SetActiveModel(ctx, arg); err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal ganti model: %v\n\n%s", err, ai.FormatModelList(ctx)))
	}

	return store.ReplyText(ctx, mctx, fmt.Sprintf("Oke, model AI sekarang: *%s*", arg))
}

// HandleAIMessage adalah logic inti pemrosesan AI — dipanggil baik dari
// command ".ai" (handleAICommand di atas) maupun dari trigger natural
// (kata kunci/reply/sticker, lihat internal/handler/handler.go).
//
// Sejak Fase 4: dibungkus spam gate + lock per-sender, mood user disuntik
// sebagai tambahan system prompt, dan pemanggilan provider memakai retry
// otomatis + notifikasi owner saat gagal total (lihat internal/ai/reliability.go).
//
// Sejak Fase 5: balasan final dipecah jadi multi-bubble (lihat sendBubbles)
// alih-alih satu ReplyText tunggal, dan fact extractor dipicu fire-and-forget
// setiap beberapa giliran (lihat processAITurn).
func HandleAIMessage(ctx context.Context, mctx *store.MessageContext, text string) error {
	sessionKey := mctx.Sender.String()

	// Anti-spam: request yang datang terlalu cepat dari sender yang sama
	// diabaikan diam-diam, tidak diproses dan tidak membalas apa-apa —
	// mencegah AI "nyasar" merespons pesan beruntun yang sebenarnya satu
	// maksud yang sama.
	if ai.IsSpam(sessionKey) {
		return nil
	}

	// Lock per-sender: memastikan giliran percakapan sender yang sama tidak
	// pernah diproses tumpang tindih (mis. reply dua kali cepat beruntun).
	resultText, err := ai.WithSenderLock(sessionKey, func() (string, error) {
		return processAITurn(ctx, mctx, sessionKey, text)
	})
	if err != nil {
		// Fase 4: pesan ramah ke user, detail teknis sudah dikirim ke owner
		// oleh notifyOwner di dalam processAITurn.
		return store.ReplyText(ctx, mctx, "Maaf, lagi ada gangguan nih. Coba lagi sebentar ya~")
	}

	return sendBubbles(ctx, mctx, resultText)
}

// sendBubbles memecah balasan final jadi beberapa bubble (Fase 5) dan
// mengirimkannya berurutan dengan delay ketik simulasi di antaranya.
// Bubble pertama dikirim sebagai reply ke pesan user (ReplyText, sama
// seperti perilaku sebelum Fase 5); bubble ke-2 dst dikirim sebagai
// pesan lepas (SendText) supaya terasa natural, bukan reply bertumpuk.
//
// Delay ketik disimulasikan SEBELUM tiap bubble dikirim (termasuk bubble
// pertama), dan selama delay itu status "mengetik..." (chat presence
// composing) ditampilkan ke user lewat SendChatPresence — supaya jeda
// antar-bubble bukan cuma diam, tapi terlihat seperti orang benar-benar
// mengetik. Kegagalan kirim presence sengaja diabaikan (fail-soft): status
// mengetik cuma kosmetik, tidak boleh sampai menggagalkan pengiriman pesan
// utamanya kalau WhatsApp menolak/timeout presence-nya.
func sendBubbles(ctx context.Context, mctx *store.MessageContext, text string) error {
	bubbles := ai.SplitIntoBubbles(text)
	if len(bubbles) == 0 {
		return nil
	}

	for i, bubble := range bubbles {
		_ = mctx.Client.SendChatPresence(ctx, mctx.Chat, types.ChatPresenceComposing, types.ChatPresenceMediaText)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(ai.TypingDelayFor(bubble)):
		}

		var err error
		if i == 0 {
			err = store.ReplyText(ctx, mctx, bubble)
		} else {
			err = store.SendText(ctx, mctx, bubble)
		}
		if err != nil {
			return err
		}
	}

	// Setelah bubble terakhir terkirim, pastikan status "mengetik" tidak
	// nyangkut di layar user — WhatsApp biasanya membersihkannya otomatis
	// begitu pesan diterima, tapi dikirim eksplisit sebagai jaring pengaman.
	_ = mctx.Client.SendChatPresence(ctx, mctx.Chat, types.ChatPresencePaused, types.ChatPresenceMediaText)

	return nil
}

// processAITurn adalah isi satu giliran percakapan AI, dipisah dari
// HandleAIMessage supaya bisa dibungkus ai.WithSenderLock dengan bersih
// (closure murni menerima/mengembalikan (string, error), tanpa langsung
// mengirim balasan dari dalam lock).
func processAITurn(ctx context.Context, mctx *store.MessageContext, sessionKey, text string) (string, error) {
	provider, err := ai.GetActiveProvider(ctx)
	if err != nil {
		return "", err
	}

	isGroup := mctx.IsGroup()

	history := ai.GlobalSession.Get(sessionKey)
	history = append(history, ai.Content{
		Role:  "user",
		Parts: []ai.Part{{Text: text}},
	})

	actx := ai.NewAgentContext(ctx, mctx.Sender.String(), mctx.Chat.String(), isGroup)
	// Extra dipakai tool bridge yang butuh akses whatsmeow client/mctx asli
	// tanpa membuat package internal/ai import whatsmeow secara langsung.
	actx.Extra["client"] = mctx.Client
	actx.Extra["mctx"] = mctx

	// Mood user disuntik sebagai tambahan system prompt (versi sederhana:
	// hanya mood user, tanpa mood bot/crush detector, sesuai blueprint).
	mood := ai.DetectUserMood(text)
	systemPrompt := config.AISystemPromptBase
	if modifier := ai.MoodModifier(mood); modifier != "" {
		systemPrompt = systemPrompt + "\n\n" + modifier
	}

	// Fase 5: fakta jangka panjang tentang user (per sender JID, lintas
	// chat/grup) disuntik ke system prompt kalau ada. Kegagalan baca
	// fail-soft — chat tetap jalan tanpa fakta kalau DB bermasalah.
	if factsJSON, err := store.AI().GetFacts(ctx, mctx.Sender.String()); err == nil {
		if factsPrompt := ai.FormatFactsForPrompt(factsJSON); factsPrompt != "" {
			systemPrompt = systemPrompt + "\n\n" + factsPrompt
		}
	}

	notifyOwner := func(err error) {
		notifyOwnerOfAIError(mctx, err)
	}

	resultText, err := ai.RunAgentLoopWithRetry(actx, provider, history, systemPrompt, notifyOwner)
	if err != nil {
		return "", err
	}

	newTurn := []ai.Content{
		{Role: "user", Parts: []ai.Part{{Text: text}}},
		{Role: "model", Parts: []ai.Part{{Text: resultText}}},
	}
	ai.GlobalSession.Append(sessionKey, newTurn[0])
	ai.GlobalSession.Append(sessionKey, newTurn[1])

	// Fase 5: pemicu fact extractor fire-and-forget, memakai PROVIDER AKTIF
	// yang sama (bukan selalu Gemini) — kalau OpenRouter sedang aktif,
	// ekstraksi fakta ikut lewat OpenRouter juga, supaya tidak diam-diam
	// tetap memanggil Gemini di belakang layar meski user sudah pindah
	// provider. Sengaja pakai key SENDER (mctx.Sender.String()), bukan
	// sessionKey — kebetulan sama persis untuk saat ini (sessionKey memang
	// didefinisikan sebagai sender), tapi ditulis eksplisit supaya kalau
	// nanti sessionKey berubah makna (mis. jadi per-chat untuk grup),
	// fakta tetap konsisten per sender sesuai keputusan desain Fase 5.
	ai.TriggerFactExtraction(provider, mctx.Sender.String(), append(history, newTurn[1]))

	return resultText, nil
}

// notifyOwnerOfAIError mengirim laporan error teknis ke nomor owner
// (config.OwnerNumber), terpisah dari pesan ramah yang dilihat user biasa.
// Kegagalan kirim notifikasi ini sendiri sengaja diabaikan (fail-soft) —
// tidak boleh sampai bikin proses utama gagal gara-gara notifikasi gagal.
func notifyOwnerOfAIError(mctx *store.MessageContext, err error) {
	ownerJID := types.NewJID(config.OwnerNumber, types.DefaultUserServer)
	report := ai.FormatOwnerErrorReport(mctx.Sender.String(), mctx.Chat.String(), err)

	_, sendErr := mctx.Client.SendMessage(context.Background(), ownerJID, &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String(report),
		},
	})
	if sendErr != nil {
		fmt.Printf("❌ Gagal kirim notifikasi error AI ke owner: %v\n", sendErr)
	}
}
