package plugins

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"wabot-go/internal/ai"
	"wabot-go/internal/deepchat"
	"wabot-go/internal/identity"
	"wabot-go/internal/logfmt"
	"wabot-go/internal/messaging"
	"wabot-go/internal/store"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types"
	"github.com/polymorfa/hypermeow/types/events"
	"google.golang.org/protobuf/proto"
)

func init() {
	registerPlugin(RegisterDeepChat)
}

const (
	deepTrigger = "deep"

	deepSessionTTL    = 10 * time.Minute
	deepMaxTurns      = 10
	deepMaxPromptRune = 8000
	deepBubbleRunes   = 1000
	deepBubbleDelay   = 3 * time.Second

	deepAckEmoji  = "💬"
	deepWaitEmoji = "⏳"
)

type deepSession struct {
	history []deepchat.ConversationTurn
	touched time.Time
}

var (
	deepMu       sync.Mutex
	deepSessions = map[string]*deepSession{}
	deepBusy     = map[string]bool{}

	deepClientOnce sync.Once
	deepClientInst *deepchat.Client
)

func deepAPI() *deepchat.Client {
	deepClientOnce.Do(func() {
		deepClientInst = deepchat.New()
	})
	return deepClientInst
}

// RegisterDeepChat mendaftarkan command .deep (kategori Owner).
func RegisterDeepChat(r *Registry) {
	r.Register(Command{
		Name:        "deep",
		Aliases:     []string{"deepchat"},
		Description: "DeepChat AI: chat, /humanize, /imagine, /edit, /swap (owner)",
		Category:    CategoryOwner,
		Handler:     handleDeepChatCommand,
	})
}

func handleDeepChatCommand(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}

	raw := strings.TrimSpace(mctx.RawArgs)
	if raw == "" {
		return store.ReplyText(ctx, mctx, deepHelpBlock())
	}

	sub, rest := deepSplitFirst(raw)
	switch strings.ToLower(sub) {
	case "on":
		key := deepKeyOf(mctx)
		deepSessionEnsure(key)
		return store.ReplyText(ctx, mctx, "*DeepChat*\n\n```Mode : aktif```\n\nSesi obrolan dibuka. Silakan mulai chat dengan deep <pesan>.")
	case "off":
		key := deepKeyOf(mctx)
		deepSessionDrop(key)
		return store.ReplyText(ctx, mctx, "*DeepChat*\n\n```Mode    : nonaktif\nRiwayat : dibuang```\n\nSesi obrolan dihentikan.")
	case "new", "reset":
		key := deepKeyOf(mctx)
		deepSessionClear(key)
		return store.ReplyText(ctx, mctx, "*DeepChat*\n\n```Mode    : aktif\nRiwayat : dibersihkan```\n\nPesan berikutnya memulai percakapan tanpa konteks lama.")
	case "/humanize", "humanize":
		go deepRunHumanize(mctx.Client, mctx.RawEvt, rest)
		return nil
	case "/imagine", "imagine":
		go deepRunImagine(mctx.Client, mctx.RawEvt, rest)
		return nil
	case "/edit", "edit":
		go deepRunEdit(mctx.Client, mctx.RawEvt, rest)
		return nil
	case "/swap", "swap", "/faceswap", "faceswap":
		go deepRunFaceSwap(mctx.Client, mctx.RawEvt, rest)
		return nil
	case "/opt", "opt", "/optimize", "optimize":
		go deepRunOptimizePrompt(mctx.Client, mctx.RawEvt, rest)
		return nil
	default:
		go deepRunChat(mctx.Client, mctx.RawEvt, raw)
		return nil
	}
}

func deepSplitFirst(s string) (first, rest string) {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, " \t\r\n"); i >= 0 {
		return s[:i], strings.TrimSpace(s[i+1:])
	}
	return s, ""
}

func deepHelpBlock() string {
	var b strings.Builder
	b.WriteString("*Chat DeepChat*\n\n")
	b.WriteString(fmt.Sprintf("Layanan AI multi-fitur tanpa login. Sesi dibuang setelah %d menit tanpa pesan.\n\n", int(deepSessionTTL.Minutes())))
	b.WriteString("`Mulai`\n")
	b.WriteString("Format : deep <pertanyaan>\n")
	b.WriteString("Contoh : deep buatkan fungsi binary search di Python\n\n")
	b.WriteString("> Setelah sesi hidup, reply bubble bot juga memicu jawaban.\n\n")

	b.WriteString("`Fitur Khusus`\n")
	b.WriteString("Format :\n")
	b.WriteString("- deep /humanize <teks>\n")
	b.WriteString("- deep /imagine <prompt>\n")
	b.WriteString("- deep /edit <prompt>\n")
	b.WriteString("- deep /swap\n")
	b.WriteString("- deep /opt <prompt>\n\n")
	b.WriteString("```humanize : ubah gaya teks agar lebih manusiawi\n")
	b.WriteString("imagine  : buat gambar dengan Flux\n")
	b.WriteString("edit     : edit/inpaint foto (lampirkan/reply foto)\n")
	b.WriteString("swap     : face swap (reply foto target + kirim foto wajah)\n")
	b.WriteString("opt      : optimasi prompt AI```\n\n")

	b.WriteString("`Sesi`\n")
	b.WriteString("Format :\n")
	b.WriteString("- .deep on\n")
	b.WriteString("- .deep off\n")
	b.WriteString("- .deep new\n\n")
	b.WriteString("```on/off   : saklar sesi obrolan\n")
	b.WriteString("new      : bersihkan riwayat obrolan aktif```")
	return b.String()
}

// CheckDeepChatSession dipanggil handler untuk pesan non-command / pasif.
func CheckDeepChatSession(client *whatsmeow.Client, evt *events.Message, text string) bool {
	if evt == nil || !store.IsMainClient(client) {
		return false
	}
	if !IsOwnerEvent(evt) {
		return false
	}

	key := deepKey(client, evt.Info.Chat, evt.Info.Sender, evt.Info.SenderAlt)
	active := deepSessionIsActive(key)

	cmd, arg, triggered := deepTriggerOf(evt, text, active)
	if !triggered {
		return false
	}

	switch cmd {
	case "/humanize", "humanize":
		go deepRunHumanize(client, evt, arg)
		return true
	case "/imagine", "imagine":
		go deepRunImagine(client, evt, arg)
		return true
	case "/edit", "edit":
		go deepRunEdit(client, evt, arg)
		return true
	case "/swap", "swap", "/faceswap", "faceswap":
		go deepRunFaceSwap(client, evt, arg)
		return true
	case "/opt", "opt", "/optimize", "optimize":
		go deepRunOptimizePrompt(client, evt, arg)
		return true
	case "chat":
		if strings.TrimSpace(arg) == "" {
			go func() {
				mctx := deepContext(client, evt)
				_ = store.ReplyText(context.Background(), mctx, ErrMsg("DeepChat", "Tulis pertanyaan setelah awalan deep, atau balas bubble bot."))
			}()
			return true
		}
		go deepRunChat(client, evt, arg)
		return true
	default:
		return false
	}
}

func deepTriggerOf(evt *events.Message, text string, active bool) (cmd, arg string, ok bool) {
	text = strings.TrimSpace(text)
	if rest, cut := deepCutPrefix(text); cut {
		sub, srest := deepSplitFirst(rest)
		switch strings.ToLower(sub) {
		case "/humanize", "humanize":
			return "/humanize", srest, true
		case "/imagine", "imagine":
			return "/imagine", srest, true
		case "/edit", "edit":
			return "/edit", srest, true
		case "/swap", "swap", "/faceswap", "faceswap":
			return "/swap", srest, true
		case "/opt", "opt", "/optimize", "optimize":
			return "/opt", srest, true
		default:
			return "chat", rest, true
		}
	}

	if active && text != "" && evt != nil && store.IsReplyToBot(evt) {
		return "chat", text, true
	}

	return "", "", false
}

func deepCutPrefix(text string) (string, bool) {
	t := strings.TrimSpace(text)
	if len(t) < len(deepTrigger) {
		return "", false
	}
	if !strings.EqualFold(t[:len(deepTrigger)], deepTrigger) {
		return "", false
	}
	rest := t[len(deepTrigger):]
	if rest == "" {
		return "", true
	}
	switch rest[0] {
	case ' ', '\t', '\r', '\n', ':', ',', '-':
		return strings.TrimLeft(rest, " \t\r\n:,-"), true
	}
	return "", false
}

// --- Session helpers ---

func deepKey(client *whatsmeow.Client, chat, sender, senderAlt types.JID) string {
	chatStr := chat.String()
	userStr := identity.CanonicalUser(context.Background(), client, sender, senderAlt)
	return chatStr + "|" + userStr
}

func deepKeyOf(mctx *store.MessageContext) string {
	var senderAlt types.JID
	if mctx.RawEvt != nil {
		senderAlt = mctx.RawEvt.Info.SenderAlt
	}
	return deepKey(mctx.Client, mctx.Chat, mctx.Sender, senderAlt)
}

func deepSessionIsActive(key string) bool {
	deepMu.Lock()
	defer deepMu.Unlock()
	deepPruneLocked()
	st := deepSessions[key]
	return st != nil
}

func deepSessionEnsure(key string) {
	deepMu.Lock()
	defer deepMu.Unlock()
	deepPruneLocked()
	st := deepSessions[key]
	if st == nil {
		st = &deepSession{}
		deepSessions[key] = st
	}
	st.touched = time.Now()
}

func deepSessionTouch(key string) {
	deepMu.Lock()
	defer deepMu.Unlock()
	if st := deepSessions[key]; st != nil {
		st.touched = time.Now()
	}
}

func deepSessionDrop(key string) {
	deepMu.Lock()
	defer deepMu.Unlock()
	delete(deepSessions, key)
}

func deepSessionClear(key string) {
	deepMu.Lock()
	defer deepMu.Unlock()
	st := deepSessions[key]
	if st != nil {
		st.history = nil
		st.touched = time.Now()
	} else {
		deepSessions[key] = &deepSession{touched: time.Now()}
	}
}

func deepSessionGetHistory(key string) []deepchat.ConversationTurn {
	deepMu.Lock()
	defer deepMu.Unlock()
	deepPruneLocked()
	st := deepSessions[key]
	if st == nil {
		return nil
	}
	st.touched = time.Now()
	res := make([]deepchat.ConversationTurn, len(st.history))
	copy(res, st.history)
	return res
}

func deepSessionAppendTurn(key string, userMsg, botReply string) {
	deepMu.Lock()
	defer deepMu.Unlock()
	st := deepSessions[key]
	if st == nil {
		st = &deepSession{}
		deepSessions[key] = st
	}
	st.touched = time.Now()
	st.history = append(st.history,
		deepchat.ConversationTurn{Role: "user", Content: userMsg},
		deepchat.ConversationTurn{Role: "assistant", Content: botReply},
	)
	if len(st.history) > deepMaxTurns*2 {
		st.history = st.history[len(st.history)-deepMaxTurns*2:]
	}
}

func deepPruneLocked() {
	now := time.Now()
	for k, st := range deepSessions {
		if now.Sub(st.touched) > deepSessionTTL {
			delete(deepSessions, k)
		}
	}
}

func deepChatClaim(chat string) bool {
	deepMu.Lock()
	defer deepMu.Unlock()
	if deepBusy[chat] {
		return false
	}
	deepBusy[chat] = true
	return true
}

func deepChatRelease(chat string) {
	deepMu.Lock()
	defer deepMu.Unlock()
	delete(deepBusy, chat)
}

func deepContext(client *whatsmeow.Client, evt *events.Message) *store.MessageContext {
	return &store.MessageContext{
		Client: client,
		Chat:   evt.Info.Chat,
		Sender: evt.Info.Sender,
		RawEvt: evt,
	}
}

// --- Multi-bubble dan Text Sanitizer (mirip Gemini) ---

func deepCleanAnswer(text string) string {
	text = geminiInvisibleRe.ReplaceAllString(text, "")
	text = geminiCtrlTagRe.ReplaceAllString(text, "")
	text = geminiStripAgentTags(text)
	return strings.TrimSpace(ai.SanitizeForWhatsApp(text))
}

func deepSendAnswer(ctx context.Context, mctx *store.MessageContext, text string) error {
	text = deepCleanAnswer(text)
	chunks := geminiSplitReply(text, deepBubbleRunes)
	if len(chunks) == 0 {
		return fmt.Errorf("jawaban kosong")
	}

	sent := false
	for _, chunk := range chunks {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		if sent {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(deepBubbleDelay):
			}
			if err := store.SendText(ctx, mctx, chunk); err != nil {
				return err
			}
		} else {
			if err := store.ReplyText(ctx, mctx, chunk); err != nil {
				return err
			}
			sent = true
		}
	}
	return nil
}

func deepErrorText(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "Waktu habis menunggu balasan server. Coba lagi."
	default:
		return fmt.Sprintf("Gagal memproses permintaan: %v", err)
	}
}

// --- Asynchronous Execution Handlers ---

func deepRunChat(client *whatsmeow.Client, evt *events.Message, prompt string) {
	mctx := deepContext(client, evt)
	chatStr := evt.Info.Chat.String()

	if !deepChatClaim(chatStr) {
		_ = store.ReplyText(context.Background(), mctx, ErrMsg("DeepChat", "Masih memproses pesan sebelumnya di chat ini. Tunggu sebentar ya."))
		return
	}
	defer deepChatRelease(chatStr)

	key := deepKeyOf(mctx)
	deepSessionEnsure(key)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	_ = store.SendReaction(ctx, mctx, deepAckEmoji)
	turnDone := make(chan struct{})
	defer close(turnDone)
	go func() {
		select {
		case <-time.After(5 * time.Second):
			rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer rcancel()
			_ = store.SendReaction(rctx, mctx, deepWaitEmoji)
		case <-turnDone:
		}
	}()

	if utf8.RuneCountInString(prompt) > deepMaxPromptRune {
		_ = store.ReplyText(ctx, mctx, ErrMsg("DeepChat", fmt.Sprintf("Pesan terlalu panjang (maks %d karakter).", deepMaxPromptRune)))
		return
	}

	history := deepSessionGetHistory(key)
	opts := &deepchat.ChatOptions{
		ConversationHistory: history,
	}

	if img := deepExtractImage(mctx); img != nil {
		if imgData, err := client.Download(ctx, img); err == nil && len(imgData) > 0 {
			opts.ImageBase64 = base64.StdEncoding.EncodeToString(imgData)
			opts.ImageMimeType = "image/jpeg"
		}
	}

	res, err := deepAPI().Chat(ctx, prompt, opts)
	if err != nil {
		logfmt.Warn("deepchat chat error: %v\n", err)
		_ = store.ReplyText(ctx, mctx, ErrMsg("DeepChat", deepErrorText(err)))
		return
	}

	reply := strings.TrimSpace(res.Reply)
	if reply == "" {
		_ = store.ReplyText(ctx, mctx, ErrMsg("DeepChat", "AI merespon tanpa teks."))
		return
	}

	deepSessionAppendTurn(key, prompt, reply)
	if err := deepSendAnswer(ctx, mctx, reply); err != nil {
		logfmt.Warn("deepchat kirim balasan: %v\n", err)
		_ = store.ReplyText(ctx, mctx, ErrMsg("DeepChat", err.Error()))
		return
	}
	deepSessionTouch(key)
}

func deepRunHumanize(client *whatsmeow.Client, evt *events.Message, text string) {
	mctx := deepContext(client, evt)
	text = strings.TrimSpace(text)

	if text == "" {
		if q := store.ExtractQuotedMessage(evt); q != nil {
			if conv := q.GetConversation(); conv != "" {
				text = conv
			} else if ext := q.GetExtendedTextMessage(); ext != nil {
				text = ext.GetText()
			}
		}
	}

	if text == "" {
		_ = store.ReplyText(context.Background(), mctx, ErrMsg("Humanize", "Sertakan teks atau reply pesan teks yang ingin di-humanize:\ndeep /humanize <teks>"))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	_ = store.SendReaction(ctx, mctx, deepWaitEmoji)
	res, err := deepAPI().Humanize(ctx, text, "medium")
	if err != nil {
		logfmt.Warn("deepchat humanize error: %v\n", err)
		_ = store.ReplyText(ctx, mctx, ErrMsg("Humanize", deepErrorText(err)))
		return
	}

	cleaned := deepCleanAnswer(res.HumanizedText)
	if err := deepSendAnswer(ctx, mctx, cleaned); err != nil {
		_ = store.ReplyText(ctx, mctx, ErrMsg("Humanize", err.Error()))
	}
}

func deepRunOptimizePrompt(client *whatsmeow.Client, evt *events.Message, prompt string) {
	mctx := deepContext(client, evt)
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		_ = store.ReplyText(context.Background(), mctx, ErrMsg("Optimize Prompt", "Sertakan prompt yang ingin dioptimasi:\ndeep /opt <prompt>"))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	_ = store.SendReaction(ctx, mctx, deepWaitEmoji)
	res, err := deepAPI().OptimizePrompt(ctx, prompt)
	if err != nil {
		logfmt.Warn("deepchat optimize prompt error: %v\n", err)
		_ = store.ReplyText(ctx, mctx, ErrMsg("Optimize Prompt", deepErrorText(err)))
		return
	}

	cleaned := deepCleanAnswer(res.OptimizedPrompt)
	if err := deepSendAnswer(ctx, mctx, cleaned); err != nil {
		_ = store.ReplyText(ctx, mctx, ErrMsg("Optimize Prompt", err.Error()))
	}
}

func deepRunImagine(client *whatsmeow.Client, evt *events.Message, prompt string) {
	mctx := deepContext(client, evt)
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		_ = store.ReplyText(context.Background(), mctx, ErrMsg("Imagine", "Sertakan prompt gambar:\ndeep /imagine <deskripsi gambar>"))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	_ = store.SendText(ctx, mctx, "Sedang membuat gambar, tunggu sebentar...")
	_ = store.SendReaction(ctx, mctx, deepWaitEmoji)

	res, err := deepAPI().GenerateImage(ctx, prompt, nil)
	if err != nil {
		logfmt.Warn("deepchat imagine error: %v\n", err)
		_ = store.ReplyText(ctx, mctx, ErrMsg("Imagine", deepErrorText(err)))
		return
	}

	if len(res.Images) == 0 || res.Images[0].URL == "" {
		_ = store.ReplyText(ctx, mctx, ErrMsg("Imagine", "Server tidak mengembalikan URL gambar."))
		return
	}

	imgData, err := deepDownloadMedia(ctx, res.Images[0].URL)
	if err != nil {
		logfmt.Warn("deepchat download img error: %v\n", err)
		_ = store.ReplyText(ctx, mctx, ErrMsg("Imagine", fmt.Sprintf("Gagal mengunduh hasil gambar: %v", err)))
		return
	}

	caption := seedreamCaption("", res.Model, prompt)
	if err := deepSendImage(ctx, mctx, imgData, caption); err != nil {
		logfmt.Warn("deepchat send img error: %v\n", err)
		_ = store.ReplyText(ctx, mctx, ErrMsg("Imagine", fmt.Sprintf("Gagal mengirim gambar: %v", err)))
	}
}

func deepRunEdit(client *whatsmeow.Client, evt *events.Message, prompt string) {
	mctx := deepContext(client, evt)
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		_ = store.ReplyText(context.Background(), mctx, ErrMsg("Edit Foto", "Sertakan instruksi edit pada caption atau reply foto:\ndeep /edit <instruksi edit>"))
		return
	}

	imgMsg := deepExtractImage(mctx)
	if imgMsg == nil {
		_ = store.ReplyText(context.Background(), mctx, ErrMsg("Edit Foto", "Kirim foto dengan caption deep /edit <instruksi> atau reply sebuah foto."))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	_ = store.SendText(ctx, mctx, "Sedang mengedit foto, tunggu sebentar...")
	_ = store.SendReaction(ctx, mctx, deepWaitEmoji)

	rawBytes, err := client.Download(ctx, imgMsg)
	if err != nil {
		_ = store.ReplyText(ctx, mctx, ErrMsg("Edit Foto", fmt.Sprintf("Gagal mengunduh foto sumber: %v", err)))
		return
	}

	base64Img := base64.StdEncoding.EncodeToString(rawBytes)
	res, err := deepAPI().EditImage(ctx, prompt, base64Img, nil)
	if err != nil {
		logfmt.Warn("deepchat edit error: %v\n", err)
		_ = store.ReplyText(ctx, mctx, ErrMsg("Edit Foto", deepErrorText(err)))
		return
	}

	if len(res.Images) == 0 || res.Images[0].URL == "" {
		_ = store.ReplyText(ctx, mctx, ErrMsg("Edit Foto", "Server tidak mengembalikan URL gambar hasil edit."))
		return
	}

	imgData, err := deepDownloadMedia(ctx, res.Images[0].URL)
	if err != nil {
		_ = store.ReplyText(ctx, mctx, ErrMsg("Edit Foto", fmt.Sprintf("Gagal mengunduh gambar hasil edit: %v", err)))
		return
	}

	caption := seedreamCaption("edit", res.Model, prompt)
	if err := deepSendImage(ctx, mctx, imgData, caption); err != nil {
		_ = store.ReplyText(ctx, mctx, ErrMsg("Edit Foto", fmt.Sprintf("Gagal mengirim gambar: %v", err)))
	}
}

func deepRunFaceSwap(client *whatsmeow.Client, evt *events.Message, rest string) {
	mctx := deepContext(client, evt)

	var srcImg, faceImg *waE2E.ImageMessage
	if direct := store.UnwrapMessage(evt.Message); direct != nil {
		faceImg = direct.GetImageMessage()
	}
	if quoted := store.UnwrapMessage(store.ExtractQuotedMessage(evt)); quoted != nil {
		srcImg = quoted.GetImageMessage()
	}

	if srcImg == nil || faceImg == nil {
		_ = store.ReplyText(context.Background(), mctx, ErrMsg("Face Swap",
			"Cara pakai Face Swap:\n"+
				"1. Reply foto target (badan/lokasi foto).\n"+
				"2. Kirim foto wajah baru dengan caption deep /swap."))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	_ = store.SendText(ctx, mctx, "Sedang menukar wajah, tunggu sebentar...")
	_ = store.SendReaction(ctx, mctx, deepWaitEmoji)

	srcBytes, err := client.Download(ctx, srcImg)
	if err != nil {
		_ = store.ReplyText(ctx, mctx, ErrMsg("Face Swap", "Gagal mengunduh foto target."))
		return
	}
	faceBytes, err := client.Download(ctx, faceImg)
	if err != nil {
		_ = store.ReplyText(ctx, mctx, ErrMsg("Face Swap", "Gagal mengunduh foto wajah."))
		return
	}

	srcB64 := base64.StdEncoding.EncodeToString(srcBytes)
	faceB64 := base64.StdEncoding.EncodeToString(faceBytes)

	res, err := deepAPI().FaceSwap(ctx, srcB64, faceB64)
	if err != nil {
		logfmt.Warn("deepchat faceswap error: %v\n", err)
		_ = store.ReplyText(ctx, mctx, ErrMsg("Face Swap", deepErrorText(err)))
		return
	}

	targetURL := res.Variant
	if targetURL == "" {
		targetURL = res.Original
	}
	if targetURL == "" {
		_ = store.ReplyText(ctx, mctx, ErrMsg("Face Swap", "Server tidak mengembalikan URL foto hasil swap."))
		return
	}

	imgData, err := deepDownloadMedia(ctx, targetURL)
	if err != nil {
		_ = store.ReplyText(ctx, mctx, ErrMsg("Face Swap", fmt.Sprintf("Gagal mengunduh hasil foto swap: %v", err)))
		return
	}

	caption := "Face Swap Selesai"
	if err := deepSendImage(ctx, mctx, imgData, caption); err != nil {
		_ = store.ReplyText(ctx, mctx, ErrMsg("Face Swap", fmt.Sprintf("Gagal mengirim hasil: %v", err)))
	}
}

// --- Media Helpers ---

func deepExtractImage(mctx *store.MessageContext) *waE2E.ImageMessage {
	if msg := store.UnwrapMessage(mctx.RawEvt.Message); msg != nil {
		if img := msg.GetImageMessage(); img != nil {
			return img
		}
	}
	if quoted := store.UnwrapMessage(store.ExtractQuotedMessage(mctx.RawEvt)); quoted != nil {
		if img := quoted.GetImageMessage(); img != nil {
			return img
		}
	}
	return nil
}

func deepDownloadMedia(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}

func deepSendImage(ctx context.Context, mctx *store.MessageContext, data []byte, caption string) error {
	if err := messaging.CheckMediaBytes(data); err != nil {
		return fmt.Errorf("media bytes tidak valid: %v", err)
	}
	uploaded, err := mctx.Client.Upload(ctx, data, whatsmeow.MediaImage)
	if err != nil {
		return fmt.Errorf("upload gambar: %v", err)
	}

	im := &waE2E.ImageMessage{
		URL:               proto.String(uploaded.URL),
		DirectPath:        proto.String(uploaded.DirectPath),
		MediaKey:          uploaded.MediaKey,
		MediaKeyTimestamp: proto.Int64(time.Now().Unix()),
		Mimetype:          proto.String("image/jpeg"),
		FileEncSHA256:     uploaded.FileEncSHA256,
		FileSHA256:        uploaded.FileSHA256,
		FileLength:        proto.Uint64(uploaded.FileLength),
		Caption:           proto.String(caption),
		JPEGThumbnail:     messaging.InlineThumbForImage(data),
		ContextInfo: &waE2E.ContextInfo{
			StanzaID:      proto.String(mctx.RawEvt.Info.ID),
			Participant:   proto.String(mctx.Sender.String()),
			QuotedMessage: mctx.RawEvt.Message,
		},
	}
	if cfg, _, cerr := image.DecodeConfig(bytes.NewReader(data)); cerr == nil && cfg.Width > 0 {
		im.Width = proto.Uint32(uint32(cfg.Width))
		im.Height = proto.Uint32(uint32(cfg.Height))
	}

	msg := &waE2E.Message{ImageMessage: im}
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, msg)
	if err != nil {
		return err
	}
	store.LogOutgoing(mctx, "deepchat", caption)
	store.TrackBotMedia(string(resp.ID), msg, caption)
	return nil
}
