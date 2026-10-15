package plugins

import (
	"context"
	"strings"

	"wabot-go/internal/store"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/types/events"
)

// CheckGameInput mencegat pesan NON-command yang merupakan input game
// aktif (angka tictactoe / kata menyerah). Dipanggil handler SEBELUM
// handleAITrigger; true = sudah ditangani game (setop, jangan teruskan
// ke AI maupun command lain).
//
// Guard ketat supaya chat biasa tidak terganggu: input hanya dikonsumsi
// kalau (1) ada game tictactoe aktif di chat ini, DAN (2) pengirim adalah
// pemain yang SEDANG dapat giliran (atau pemain mana pun untuk menyerah).
// Teks lain, pengirim lain, atau chat tanpa game → false (AI tetap jalan).
func CheckGameInput(client *whatsmeow.Client, evt *events.Message, text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))

	surrender := t == "nyerah" || t == "surrender" || t == "menyerah"
	pos := -1
	if !surrender {
		// Hanya SATU digit 1-9 — "10", "1 ", "a1" bukan input game.
		if p, ok := ttParsePos(t); !ok {
			return false
		} else {
			pos = p
		}
	}

	key := evt.Info.Chat.String()
	ttMu.Lock()
	defer ttMu.Unlock()

	g, ok := ttGames[key]
	if !ok {
		return false
	}
	sender, alt := evt.Info.Sender, evt.Info.SenderAlt

	// Jawaban tantangan: exact match "terima"/"tolak", HANYA dari yang
	// ditantang dan HANYA saat pending. "Terima kasih" tidak match (bukan
	// exact), jadi chat biasa aman.
	if t == "terima" || t == "tolak" {
		if !g.pending || !ttSamePlayer(g.playerO, sender, alt) {
			return false
		}
		mctx := &store.MessageContext{Client: client, Chat: evt.Info.Chat, Sender: sender, RawEvt: evt}
		_ = ttAnswerChallengeLocked(context.Background(), mctx, g, key, t == "terima")
		return true
	}

	if surrender {
		// Menyerah boleh dari pemain mana pun (bukan cuma yang dapat
		// giliran) — tapi tetap harus pemain game ini.
		if !ttIsPlayer(g, sender, alt) {
			return false
		}
		mctx := &store.MessageContext{Client: client, Chat: evt.Info.Chat, Sender: sender, RawEvt: evt}
		_ = ttFinishSurrender(context.Background(), mctx, g, key, sender, alt)
		return true
	}

	// Langkah angka: HARUS giliran pengirim. Bukan gilirannya = konsumsi
	// diam-diam (return true tanpa balasan, pola referensi) supaya tidak
	// bocor ke AI sebagai chat biasa.
	if !ttIsTurn(g, sender, alt) {
		if ttIsPlayer(g, sender, alt) {
			return true
		}
		return false
	}
	mctx := &store.MessageContext{Client: client, Chat: evt.Info.Chat, Sender: sender, RawEvt: evt}
	_ = ttDoMoveLocked(context.Background(), mctx, g, key, pos)
	return true
}
