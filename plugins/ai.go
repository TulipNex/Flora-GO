package plugins

import (
	"context"
	"fmt"
	"strings"
	"time"

	"wabot-go/internal/ai"
	"wabot-go/internal/config"
	"wabot-go/internal/store"

	"go.mau.fi/whatsmeow"
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
		Description: "Ngobrol dengan AI (.ai <pesan> / .ai reset / .ai provider / .ai model / .ai status)",
		Category:    CategoryAI,
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
	if len(mctx.Args) > 0 && strings.EqualFold(mctx.Args[0], "status") {
		return handleStatusSubcommand(ctx, mctx)
	}

	text := strings.TrimSpace(strings.Join(mctx.Args, " "))

	if text == "" {
		return store.ReplyText(ctx, mctx, "Mau ngobrol apa? Contoh: .ai halo, apa kabar?")
	}

	if strings.EqualFold(text, "reset") {
		// Kunci sesi dibedakan private/grup (lihat ai.BuildSessionKey) —
		// ".ai reset" yang diketik di dalam grup HANYA mereset histori
		// sender itu di grup tersebut, tidak menyentuh histori private-nya
		// atau histori dia di grup lain.
		sessionKey := ai.BuildSessionKey(mctx.IsGroup(), mctx.Chat.String(), mctx.Sender.String())
		ai.GlobalSession.Reset(sessionKey)
		return store.ReplyText(ctx, mctx, "Oke, sesi percakapan sudah direset.")
	}

	// Kalau command ".ai <pesan>" ini dikirim sebagai reply ke salah satu
	// bubble bot, sertakan teks bubble yang di-reply sebagai konteks —
	// sama seperti trigger natural (lihat handler.go handleAITrigger).
	quotedText := store.GetQuotedBotText(mctx.RawEvt)
	text = ai.BuildTextWithQuotedContext(quotedText, text)

	// Sama seperti trigger natural: pemrosesan AI jalan async supaya tidak
	// memblokir event loop whatsmeow (lihat komentar di handleAITrigger).
	// HandleAIMessage sudah membalas sendiri ke user (bubble atau pesan
	// gangguan), jadi handler cukup mengembalikan nil.
	go func() {
		if err := HandleAIMessage(context.Background(), mctx, text); err != nil {
			fmt.Printf("❌ Error memproses command .ai: %v\n", err)
		}
	}()
	return nil
}

// handleProviderSubcommand menangani ".ai provider" (tampilkan daftar),
// ".ai provider <nama>" dan ".ai provider <nomor>" (ganti provider aktif,
// nomor sesuai urutan di daftar).
func handleProviderSubcommand(ctx context.Context, mctx *store.MessageContext) error {
	arg := strings.TrimSpace(strings.Join(mctx.Args[1:], " "))

	if arg == "" {
		return store.ReplyText(ctx, mctx, ai.FormatProviderList(ctx))
	}

	name := ai.ResolveProviderRef(arg)
	if err := ai.SetActiveProvider(ctx, name); err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal ganti provider: %v\n\n%s", err, ai.FormatProviderList(ctx)))
	}

	return store.ReplyText(ctx, mctx, fmt.Sprintf("Oke, provider AI sekarang: *%s*", name))
}

// handleModelSubcommand menangani ".ai model" (tampilkan daftar model utk
// provider aktif), ".ai model <nama>" dan ".ai model <nomor>" (ganti model
// aktif, nomor sesuai urutan di daftar).
func handleModelSubcommand(ctx context.Context, mctx *store.MessageContext) error {
	arg := strings.TrimSpace(strings.Join(mctx.Args[1:], " "))

	if arg == "" {
		return store.ReplyText(ctx, mctx, ai.FormatModelList(ctx))
	}

	name := ai.ResolveModelRef(ai.GetActiveProviderName(ctx), arg)
	if err := ai.SetActiveModel(ctx, name); err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal ganti model: %v\n\n%s", err, ai.FormatModelList(ctx)))
	}

	return store.ReplyText(ctx, mctx, fmt.Sprintf("Oke, model AI sekarang: *%s*", name))
}

// handleStatusSubcommand menangani ".ai status": menampilkan status lock
// trigger natural (global), provider + model aktif, dan status sesi chat
// ini (aktif kalau masih dalam idle timeout 30 menit, idle kalau sudah
// lama tidak chat). Sengaja jalan walau sedang terkunci — gunanya justru
// untuk mengecek status lock.
func handleStatusSubcommand(ctx context.Context, mctx *store.MessageContext) error {
	locked, err := store.Groups().IsAILocked(ctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal baca status lock: %v", err))
	}
	lockText := "aktif (trigger natural menyala)"
	if locked {
		lockText = "TERKUNCI (trigger natural mati, .ai tetap jalan)"
	}

	sessionKey := ai.BuildSessionKey(mctx.IsGroup(), mctx.Chat.String(), mctx.Sender.String())
	sessionText := "idle (sapa Flora untuk mulai)"
	if ai.GlobalSession.HasActive(sessionKey) {
		sessionText = "aktif"
	}

	return store.ReplyText(ctx, mctx, fmt.Sprintf(
		"Status Flora:\n- Lock: %s\n- Provider: %s\n- Model: %s\n- Sesi chat ini: %s",
		lockText, ai.GetActiveProviderName(ctx), ai.GetActiveModelName(ctx), sessionText,
	))
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
//
// Kunci sesi PERCAKAPAN (histori chat) dibangun lewat ai.BuildSessionKey,
// yang membedakan private vs grup: sender yang sama akan punya histori
// TERPISAH antara chat private-nya dan chat dia di sebuah grup (dan
// terpisah lagi per grup) — sebelumnya semua konteks ini keliru berbagi
// satu histori yang sama karena kunci sesi hanya memakai sender JID.
func HandleAIMessage(ctx context.Context, mctx *store.MessageContext, text string) error {
	isGroup := mctx.IsGroup()
	sessionKey := ai.BuildSessionKey(isGroup, mctx.Chat.String(), mctx.Sender.String())

	// Anti-spam: request yang datang terlalu cepat dari sender yang sama
	// diabaikan diam-diam, tidak diproses dan tidak membalas apa-apa —
	// mencegah AI "nyasar" merespons pesan beruntun yang sebenarnya satu
	// maksud yang sama.
	if ai.IsSpam(sessionKey) {
		return nil
	}

	// Lock per-sesi: memastikan giliran percakapan sesi yang sama tidak
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
//
// sessionKey di sini sudah berupa kunci gabungan private/grup (lihat
// ai.BuildSessionKey di HandleAIMessage) — dipakai untuk histori
// percakapan (ai.GlobalSession). Fakta jangka panjang (fact extractor)
// SENGAJA TETAP memakai mctx.Sender.String() murni, bukan sessionKey —
// lihat catatan di pemanggilan ai.TriggerFactExtraction di bawah.
func processAITurn(ctx context.Context, mctx *store.MessageContext, sessionKey, text string) (string, error) {
	provider, err := ai.GetActiveProvider(ctx)
	if err != nil {
		return "", err
	}

	isGroup := mctx.IsGroup()

	// Vision: kalau pesan pemicu membawa gambar (lampiran langsung atau
	// yang di-reply), unduh dan selipkan ke pesan user giliran ini supaya
	// model yang mendukung vision bisa "melihat"-nya. Byte gambar HANYA
	// hidup di slice history lokal ini (ephemeral) — baris yang disimpan
	// ke sesi/DB di bawah tetap teks murni, jadi tidak membengkakkan
	// memori maupun database. Provider non-vision (Gemini) mengabaikan
	// field gambar secara diam-diam.
	userPart := ai.Part{Text: text}
	if imgBytes, imgMime, imgNote := extractTriggerImage(ctx, mctx); len(imgBytes) > 0 {
		userPart.ImageBytes = imgBytes
		userPart.ImageMime = imgMime
	} else if imgNote != "" {
		text += "\n" + imgNote
		userPart.Text = text
	}

	// Dokumen: kalau pesan pemicu membawa file (PDF/DOCX/XLSX/teks —
	// lampiran langsung atau yang di-reply), ekstrak teksnya dan tempel ke
	// pesan user. Tidak seperti gambar vision, ini murni teks sehingga
	// SEMUA provider bisa membacanya (termasuk Gemini) dan aman disimpan
	// ke histori. Dokumen pendek (<= maxDocChars) ditempel utuh;
	// dokumen panjang dibaca bertahap via summarizeLongDocument supaya
	// tidak ada isi yang hilang (bukan dipotong seperti dulu).
	if docName, docText, docNote := readTriggerDocument(ctx, mctx); docNote != "" {
		text += "\n" + docNote
		userPart.Text = text
	} else if docText != "" {
		if len([]rune(docText)) <= maxDocChars {
			text += "\n" + formatDocBlock(docName, strings.TrimSpace(docText), false)
		} else {
			text += "\n" + summarizeLongDocument(ctx, provider, docName, docText)
		}
		userPart.Text = text
	}

	history := ai.GlobalSession.Get(sessionKey)
	history = append(history, ai.Content{
		Role:  "user",
		Parts: []ai.Part{userPart},
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
	// provider.
	//
	// SENGAJA memakai mctx.Sender.String() murni (bukan sessionKey yang
	// sudah digabung dengan chatJID untuk grup) — fakta jangka panjang
	// tentang seseorang memang didesain lintas-chat/grup (lihat keputusan
	// Fase 5: "fakta user disimpan per sender, lintas semua chat/grup"),
	// jadi harus TETAP satu kunci per orang meski histori PERCAKAPANNYA
	// sekarang sudah dipisah per private/grup lewat sessionKey.
	ai.TriggerFactExtraction(provider, mctx.Sender.String(), append(history, newTurn[1]))

	return resultText, nil
}

// maxVisionImageBytes adalah batas ukuran gambar untuk vision (4MB). Foto
// dari WhatsApp umumnya sudah dikompres (<1MB); di atas batas dilewati
// supaya payload base64 tidak meledak dan provider tidak menolak request
// karena kebesaran.
const maxVisionImageBytes = 4 << 20

// extractTriggerImage mengambil gambar dari pesan pemicu AI untuk vision:
// prioritas lampiran langsung di pesan itu, fallback ke pesan yang
// di-reply; tiap jalur mencoba ImageMessage dulu lalu StickerMessage.
// Stiker diperlakukan SAMA seperti foto (diunduh + dikirim sebagai
// image_url) supaya Flora "melihat" stiker yang diterimanya — stiker
// animasi dikirim apa adanya, backend vision umumnya memakai frame
// pertama. Mengembalikan byte+mime kalau berhasil; kalau pesan memang
// mengandung gambar/stiker tapi gagal diunduh/kebesaran, kembalikan note
// jujur untuk ditempel ke teks (supaya AI tidak mengarang isinya). Kalau
// tidak ada gambar sama sekali, kembalikan kosong. Semua kegagalan
// fail-soft: chat teks tetap jalan normal.
func extractTriggerImage(ctx context.Context, mctx *store.MessageContext) (imgBytes []byte, imgMime, note string) {
	// Getter protobuf aman dipanggil pada pesan nil (mengembalikan nil),
	// jadi tidak perlu cek nil hasil UnwrapMessage satu per satu.
	direct := store.UnwrapMessage(mctx.RawEvt.Message)
	quoted := store.UnwrapMessage(store.ExtractQuotedMessage(mctx.RawEvt))

	// Kumpulkan kandidat berurutan: gambar langsung, gambar reply, stiker
	// langsung, stiker reply. Yang pertama ketemu dipakai.
	type candidate struct {
		msg         whatsmeow.DownloadableMessage
		mime        string
		label       string
		defaultMime string
	}
	var candidates []candidate
	if img := direct.GetImageMessage(); img != nil {
		candidates = append(candidates, candidate{img, img.GetMimetype(), "gambar", "image/jpeg"})
	}
	if img := quoted.GetImageMessage(); img != nil {
		candidates = append(candidates, candidate{img, img.GetMimetype(), "gambar", "image/jpeg"})
	}
	if stk := direct.GetStickerMessage(); stk != nil {
		candidates = append(candidates, candidate{stk, stk.GetMimetype(), "stiker", "image/webp"})
	}
	if stk := quoted.GetStickerMessage(); stk != nil {
		candidates = append(candidates, candidate{stk, stk.GetMimetype(), "stiker", "image/webp"})
	}
	if len(candidates) == 0 {
		return nil, "", ""
	}

	c := candidates[0]
	data, err := mctx.Client.Download(ctx, c.msg)
	if err != nil {
		fmt.Printf("⚠️ vision: gagal download %s: %v\n", c.label, err)
		return nil, "", fmt.Sprintf("[Catatan: ada %s terlampir tapi tidak bisa dibaca.]", c.label)
	}
	if len(data) > maxVisionImageBytes {
		fmt.Printf("⚠️ vision: %s terlalu besar (%d byte), dilewati.\n", c.label, len(data))
		return nil, "", fmt.Sprintf("[Catatan: ada %s terlampir tapi terlalu besar untuk dibaca.]", c.label)
	}
	mime := c.mime
	if mime == "" {
		mime = c.defaultMime
	}
	return data, mime, ""
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
