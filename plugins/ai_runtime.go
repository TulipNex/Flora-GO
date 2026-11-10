package plugins

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types"
	"google.golang.org/protobuf/proto"

	"wabot-go/internal/ai"
	"wabot-go/internal/config"
	"wabot-go/internal/logfmt"
	"wabot-go/internal/memlog"
	"wabot-go/internal/store"
	"wabot-go/internal/timex"
)

// HandleAIMessage adalah logic inti pemrosesan AI — sekarang HANYA dipanggil
// dari trigger natural (kata kunci/reply/sticker, lihat
// internal/handler/handler.go); tanya-jawab via command sudah dihapus,
// command .agent murni admin.
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
func HandleAIMessage(ctx context.Context, mctx *store.MessageContext, text string) (retErr error) {
	// Satu baris riwayat memori per giliran AI: adapter provider membuat objek
	// baru tiap request, jadi alokasi per giliran inilah yang perlu
	// dibandingkan dari hari ke hari. Giliran yang ditolak spam/lock tercatat
	// dengan alloc ~0 (murah, tidak mengganggu peringkat .memlog top).
	op := memlog.Begin("ai", "turn")
	defer func() { op.End(retErr == nil, "") }()

	isGroup := mctx.IsGroup()
	sessionKey := ai.BuildSessionKey(isGroup, mctx.Chat.String(), mctx.Sender.String())

	// Anti-spam: request yang datang terlalu cepat dari sender yang sama
	// diabaikan diam-diam, tidak diproses dan tidak membalas apa-apa —
	// mencegah AI "nyasar" merespons pesan beruntun yang sebenarnya satu
	// maksud yang sama.
	if ai.IsSpam(sessionKey) {
		return nil
	}

	// Lock per-sesi (non-blocking): pesan yang datang saat sesi yang sama
	// masih diproses ditolak diam-diam — tidak antre serial, tidak ada LLM
	// call tambahan, tidak ada bubble. Mencegah burst menumpuk giliran +
	// bubble berurutan yang terlihat seperti spam.
	resultText, ok, err := ai.TrySenderLock(sessionKey, func() (string, error) {
		return processAITurn(ctx, mctx, sessionKey, text)
	})
	if !ok {
		logfmt.Skip("Giliran AI sesi %s ditolak: sesi sibuk.\n", sessionKey)
		return nil
	}
	if err != nil {
		// Fase 4: pesan ramah ke user, detail teknis sudah dikirim ke owner
		// oleh notifyOwner di dalam processAITurn. Balasan ramah ini
		// di-cooldown per chat (lihat sendFriendlyAIError): saat provider
		// down + chat ramai, tiap giliran gagal tidak boleh membanjiri chat
		// dengan bubble error berulang.
		return sendFriendlyAIError(ctx, mctx, err)
	}

	return sendFloraReply(ctx, mctx, resultText)
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
			err = store.ReplyTextAgent(ctx, mctx, bubble)
		} else {
			err = store.SendTextAgent(ctx, mctx, bubble)
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

// sendFloraReply mengirim balasan final Flora lewat jalur yang sesuai
// mode: voice note UTUH bila `.agent voice on`, teks bubble bila OFF.
// Satu-satunya pintu keluar balasan AI (HandleAIMessage) lewat sini.
func sendFloraReply(ctx context.Context, mctx *store.MessageContext, text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if ai.IsVoiceReplyOn(ctx) {
		if err := sendVoiceReply(ctx, mctx, text); err != nil {
			// Fail-soft: TTS gagal = kirim sebagai teks biasa. Balasan
			// user tidak boleh hilang gara-gara layanan suara bermasalah.
			logfmt.Warn("voice reply gagal (%v), fallback teks.\n", err)
		} else {
			return nil
		}
	}
	return sendBubbles(ctx, mctx, text)
}

// sendVoiceReply mengirim SELURUH teks balasan sebagai SATU voice note
// (tanpa pecah bubble, tanpa batas 500 char): teks dipecah jadi chunk di
// dalam SynthesizeLong, MP3 digabung, sekali convert Opus, sekali kirim.
// Suara fixed Gadis (identitas suara Flora). Presence composing dipakai
// selama menyiapkan (whatsmeow tidak punya presence "recording").
func sendVoiceReply(ctx context.Context, mctx *store.MessageContext, text string) error {
	_ = mctx.Client.SendChatPresence(ctx, mctx.Chat, types.ChatPresenceComposing, types.ChatPresenceMediaText)
	defer func() {
		_ = mctx.Client.SendChatPresence(ctx, mctx.Chat, types.ChatPresencePaused, types.ChatPresenceMediaText)
	}()

	mp3Bytes, err := SynthesizeLong(ctx, strings.TrimSpace(text), ttsDefaultVoice)
	if err != nil {
		return err
	}
	if len(mp3Bytes) == 0 {
		return fmt.Errorf("hasil sintesis kosong")
	}
	opusBytes, err := runFfmpegConvert(ctx, mp3Bytes, ".mp3", "out.opus", nil,
		"-c:a", "libopus", "-b:a", "48k")
	if err != nil || len(opusBytes) == 0 {
		return fmt.Errorf("gagal siapkan voice note: %v", err)
	}
	return sendVoiceNote(ctx, mctx, opusBytes, true)
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
	// memori maupun database. Provider non-vision mengabaikan
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
	// SEMUA provider bisa membacanya dan aman disimpan
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
	// Backstop hemat-token: kirim maks 40 giliran terakhir. Penyimpanan
	// (memori + SQLite) tetap utuh — yang dipangkas hanya kiriman.
	history = ai.CapHistoryForSend(history, 40)
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
	// Acuan tanggal: model tidak punya jam internal — tanpa baris ini,
	// kata relatif seperti "kemarin"/"minggu ini" ditebak dari pengetahuan
	// model yang bisa basi. Zona aktif timex (BOT_TIMEZONE, nama IANA).
	nowLocal := timex.Now()
	systemPrompt = systemPrompt + "\n\nHari ini (zona " + timex.Name() + "): " +
		nowLocal.Format("Monday, 2 January 2006") +
		" — format tanggal tool YYYY-MM-DD: " + nowLocal.Format("2006-01-02") + ". " +
		"Hitung tanggal relatif (kemarin, minggu/bulan ini) dari tanggal ini."
	// Pengganti max_tokens (field itu diabaikan gateway ciora — sudah diuji):
	// minta jawaban ringkas supaya waktu tunggu dan token tidak membengkak.
	systemPrompt = systemPrompt + "\n\nGaya jawaban: ringkas dan langsung ke inti. " +
		"Obrolan biasa cukup 1–3 paragraf pendek; jangan mengulang pertanyaan user, " +
		"jangan menulis pengantar atau penutup panjang."
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

	// Ingatan hemat: ringkasan FIFO sesi-sesi sebelumnya (maks 4) disuntik
	// sebagai konteks. Fail-soft seperti fakta — chat tetap jalan tanpanya.
	if summaries, err := store.AI().ListSessionSummaries(ctx, sessionKey); err == nil {
		if sp := ai.FormatSummariesForPrompt(summaries); sp != "" {
			systemPrompt = systemPrompt + "\n\n" + sp
		}
	}

	notifyOwner := func(err error) {
		notifyOwnerOfAIError(mctx, err)
	}

	logAITurnStart(ctx, mctx, sessionKey, history, text)
	turnStart := time.Now()
	resultText, err := ai.RunAgentLoopWithRetry(actx, provider, history, systemPrompt, notifyOwner)
	logAITurnDone(sessionKey, resultText, err, time.Since(turnStart))
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
	// yang sama — ekstraksi fakta ikut lewat provider yang sedang aktif,
	// supaya tidak diam-diam tetap memanggil provider lain di belakang layar
	// meski user sudah pindah provider.
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
		logfmt.Warn("vision: gagal download %s: %v\n", c.label, err)
		return nil, "", fmt.Sprintf("[Catatan: ada %s terlampir tapi tidak bisa dibaca.]", c.label)
	}
	if len(data) > maxVisionImageBytes {
		logfmt.Warn("vision: %s terlalu besar (%d byte), dilewati.\n", c.label, len(data))
		return nil, "", fmt.Sprintf("[Catatan: ada %s terlampir tapi terlalu besar untuk dibaca.]", c.label)
	}
	mime := c.mime
	if mime == "" {
		mime = c.defaultMime
	}
	return data, mime, ""
}

// ownerNotifyCooldown: notifikasi error AI ke owner dibatasi max 1 per
// jendela ini — saat provider down + grup ramai, ratusan giliran gagal
// tidak boleh membanjiri DM owner. Error yang ditahan dihitung (suppressed)
// dan dilaporkan pada notifikasi berikutnya. Mutex karena dipanggil dari
// banyak goroutine trigger konkuren.
var (
	ownerNotifyMu         sync.Mutex
	ownerNotifyLast       time.Time
	ownerNotifySuppressed int
)

const ownerNotifyCooldown = 5 * time.Minute

// userErrorCooldown: balasan ramah error AI ke chat dibatasi max 1 per
// jendela ini PER CHAT — saat provider down + chat ramai, tiap giliran gagal
// tidak boleh membanjiri chat dengan bubble "Maaf, lagi ada gangguan"
// berulang. Error yang ditahan dihitung (suppressed) dan dilaporkan pada
// balasan berikutnya. Mutex karena dipanggil dari banyak goroutine trigger
// konkuren. Return nil saat ditahan supaya fallback di handler (yang juga
// mengirim pesan error bila HandleAIMessage gagal) tidak ikut mengirim ganda.
var (
	userErrMu         sync.Mutex
	userErrLast       = make(map[string]time.Time)
	userErrSuppressed = make(map[string]int)
)

const userErrorCooldown = 5 * time.Minute

// sendFriendlyAIError mengirim bubble ramah error AI dengan cooldown per chat.
// Saat masih dalam jendela cooldown: tidak kirim apa-apa ke chat (diam),
// cukup catat di console + hitung suppressed, return nil.
// Penyebab antrean provider (ai.ProviderBusyMarker) dibalas dengan pesan
// "lagi antre" yang jujur, bukan "gangguan".
func sendFriendlyAIError(ctx context.Context, mctx *store.MessageContext, err error) error {
	chat := mctx.Chat.String()

	userErrMu.Lock()
	if last, ok := userErrLast[chat]; ok && time.Since(last) < userErrorCooldown {
		userErrSuppressed[chat]++
		n := userErrSuppressed[chat]
		userErrMu.Unlock()
		logfmt.Skip("Balasan error AI ke %s ditahan (cooldown, +%d ditahan).\n", chat, n)
		return nil
	}
	userErrLast[chat] = time.Now()
	suppressed := userErrSuppressed[chat]
	userErrSuppressed[chat] = 0
	userErrMu.Unlock()

	msg := "Maaf, lagi ada gangguan nih. Coba lagi sebentar ya~"
	if err != nil && strings.Contains(err.Error(), ai.ProviderBusyMarker) {
		msg = "Flora lagi antre menjawab chat lain nih, coba lagi sebentar ya~"
	}
	if suppressed > 0 {
		msg += fmt.Sprintf("\n(+%d error serupa ditahan dalam 5 menit terakhir.)", suppressed)
	}
	return store.ReplyText(ctx, mctx, msg)
}

// logAITurnStart mencatat pembuka giliran AI UTUH (format blok MESSAGE).
// Mode ringkas (AI_LOG_FULL=0) tidak mencetak apa pun di sini.
func logAITurnStart(ctx context.Context, mctx *store.MessageContext, sessionKey string, history []ai.Content, text string) {
	if !ai.AILogFullMode() {
		return
	}
	at := "private " + mctx.Chat.User
	if mctx.IsGroup() {
		at = "group " + mctx.Chat.User
	}
	ai.LogTurnStart(
		mctx.Sender.String(), at,
		ai.GetActiveProviderName(ctx), ai.GetActiveModelName(ctx),
		sessionKey, len(history), text,
	)
}

// logAITurnDone mencatat penutup giliran (format blok MESSAGE).
func logAITurnDone(sessionKey, resultText string, err error, elapsed time.Duration) {
	if !ai.AILogFullMode() {
		return
	}
	if err != nil {
		ai.LogTurnDone("GAGAL "+elapsed.String(), err.Error())
		return
	}
	ai.LogTurnDone("selesai "+elapsed.String(), resultText)
}

// notifyOwnerOfAIError mengirim laporan error teknis ke nomor owner UTAMA
// (entri pertama OWNER_NUMBERS), terpisah dari pesan ramah yang dilihat user biasa.
// Kegagalan kirim notifikasi ini sendiri sengaja diabaikan (fail-soft) —
// tidak boleh sampai bikin proses utama gagal gara-gara notifikasi gagal.
func notifyOwnerOfAIError(mctx *store.MessageContext, err error) {
	ownerNotifyMu.Lock()
	if time.Since(ownerNotifyLast) < ownerNotifyCooldown {
		ownerNotifySuppressed++
		n := ownerNotifySuppressed
		ownerNotifyMu.Unlock()
		logfmt.Skip("Notifikasi error AI ke owner ditahan (cooldown, +%d ditahan).\n", n)
		return
	}
	ownerNotifyLast = time.Now()
	suppressed := ownerNotifySuppressed
	ownerNotifySuppressed = 0
	ownerNotifyMu.Unlock()

	ownerJID := types.NewJID(config.PrimaryOwnerNumber(), types.DefaultUserServer)
	report := ai.FormatOwnerErrorReport(mctx.Sender.String(), mctx.Chat.String(), err)
	if suppressed > 0 {
		report += fmt.Sprintf("\n(+%d error serupa ditahan dalam 5 menit terakhir.)", suppressed)
	}

	_, sendErr := mctx.Client.SendMessage(context.Background(), ownerJID, &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String(report),
		},
	})
	if sendErr != nil {
		logfmt.Error("Gagal kirim notifikasi error AI ke owner: %v\n", sendErr)
	}
}
