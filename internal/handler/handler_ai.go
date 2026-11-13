package handler

import (
	"context"
	"fmt"

	"wabot-go/internal/ai"
	"wabot-go/internal/logfmt"
	"wabot-go/internal/store"
	"wabot-go/plugins"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/types"
	"github.com/polymorfa/hypermeow/types/events"
)

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
	// Flora hanya milik bot utama (keputusan jadibot): pesan lewat clone
	// tidak pernah menyentuh jalur AI — termasuk lock/unlock dan trust.
	if !store.IsMainClient(client) {
		return
	}
	// Script playground (#playground) bukan pesan untuk Flora: isinya bebas,
	// termasuk kata "flora" di title/footer. Hanya milik owner.
	if plugins.IsPlaygroundScript(evt) {
		return
	}
	// sessionKey dibangun lewat ai.BuildSessionKey yang sama dipakai
	// plugins/ai.go (HandleAIMessage/processAITurn) — WAJIB identik supaya
	// HasActiveSession di bawah mengecek kunci yang benar-benar dipakai
	// GlobalSession.Append untuk menyimpan histori sesi ini. Sebelum
	// perbaikan ini, di sini selalu memakai sender polos meski histori
	// sungguhan disimpan dengan kunci gabungan chatJID:senderJID untuk
	// pesan grup — akibatnya HasActive selalu salah (selalu dianggap
	// idle) untuk sesi di dalam grup.
	sessionKey := ai.BuildSessionKey(evt.Info.IsGroup, evt.Info.Chat.String(), evt.Info.Sender.String())

	// Perintah lock/unlock ("lock flo", "unlock flora", "lock @flora")
	// dicek PALING DULU — bahkan saat sedang terkunci — supaya unlock
	// selalu bisa. Hanya owner yang boleh; non-owner diabaikan diam-diam.
	if action := ai.ParseLockCommand(text, isBotMentioned(client, evt)); action != ai.LockNone {
		if !plugins.IsOwnerEvent(evt) {
			return
		}
		mctx := &store.MessageContext{
			Client: client, Chat: evt.Info.Chat, Sender: evt.Info.Sender, RawEvt: evt,
		}
		bg := context.Background()
		if action == ai.LockOn {
			if err := store.Groups().SetAILock(bg, true); err != nil {
				_ = store.ReplyText(bg, mctx, fmt.Sprintf("Gagal mengunci: %v", err))
				return
			}
			_ = store.ReplyText(bg, mctx, "Flora di-lock — trigger natural dimatikan di semua chat. Buka lagi dengan unlock flo/flora.")
		} else {
			if err := store.Groups().SetAILock(bg, false); err != nil {
				_ = store.ReplyText(bg, mctx, fmt.Sprintf("Gagal membuka kunci: %v", err))
				return
			}
			_ = store.ReplyText(bg, mctx, "Flora aktif lagi — trigger natural menyala.")
		}
		return
	}

	// Gerbang lock global: saat terkunci, semua trigger natural di semua
	// chat diabaikan diam-diam. Command admin .agent TIDAK lewat sini
	// sehingga tetap jalan.
	if locked, err := store.Groups().IsAILocked(context.Background()); err != nil {
		logfmt.Warn("Gagal cek lock AI: %v\n", err)
	} else if locked {
		return
	}

	// Gerbang trust Flora: trigger natural hanya melayani owner + user
	// terpercaya (lihat store.GroupDB.TrustUser). Non-trusted diabaikan
	// diam-diam — tanpa bocoran bahwa Flora terkunci untuk mereka.
	if !plugins.IsOwnerOrTrustedEvent(context.Background(), client, evt) {
		return
	}

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

	// Pemrosesan AI jalan di goroutine sendiri supaya tidak memblokir event
	// loop whatsmeow. HandleAIMessage butuh waktu lama (HTTP ke provider +
	// delay ketik antar-bubble, total bisa belasan detik) — kalau dijalankan
	// sinkron di sini, whatsmeow tidak bisa memproses stanza berikutnya dan
	// memunculkan warning "Node handling took Xs". Aman dari balapan karena
	// sesi yang sama sudah diserialkan via ai.WithSenderLock di dalamnya.
	go func() {
		if err := plugins.HandleAIMessage(context.Background(), ctx, finalText); err != nil {
			logfmt.Error("Error memproses trigger AI (alasan: %s): %v\n", trigger.Reason, err)
			_ = store.ReplyText(context.Background(), ctx, "Maaf, ada gangguan saat memproses ini.")
		}
	}()
}

// isBotMentioned true kalau pesan menandai bot sendiri — delegasi ke
// store.IsBotJID (satu sumber kebenaran dual-format LID/PN, sama dengan
// IsBotAdmin), karena mention di grup mode LID datang sebagai @lid.
func isBotMentioned(client *whatsmeow.Client, evt *events.Message) bool {
	for _, raw := range store.ExtractMentionedJIDs(evt) {
		parsed, err := types.ParseJID(raw)
		if err != nil {
			continue
		}
		if store.IsBotJID(client, parsed) {
			return true
		}
	}
	return false
}
