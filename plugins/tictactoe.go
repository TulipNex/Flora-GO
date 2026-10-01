package plugins

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	"wabot-go/internal/store"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

func init() {
	registerPlugin(RegisterTicTacToe)
}

// Pola menang bitboard: sel 0-8 dipetakan ke bit 1<<i (baris atas = bit
// 0-2). Pola di bawah = 3 baris + 3 kolom + 2 diagonal. Tie = semua bit
// terisi (x|o == 0x1FF == 511). Sama seperti implementasi referensi
// (game-tictac_toe.js) — ringkas dan tanpa tabel if-else.
var ttWinMasks = []int{0x07, 0x38, 0x1C0, 0x49, 0x92, 0x124, 0x111, 0x54}

// ttCellEmoji untuk sel kosong (angka pilihan), mengikuti referensi.
var ttCellEmoji = []string{"1️⃣", "2️⃣", "3️⃣", "4️⃣", "5️⃣", "6️⃣", "7️⃣", "8️⃣", "9️⃣"}

// ttGame adalah satu sesi tictactoe. Hidup di memori (restart bot =
// game bubar) — disengaja untuk v1, supaya tidak perlu skema DB baru
// untuk state yang sifatnya sementara (pola sama: dlSem, senderLocks).
type ttGame struct {
	playerX, playerO types.JID
	xBits, oBits     int
	turnX            bool
	vsBot            bool
	// pending true = tantangan belum dijawab yang ditantang (papan kosong,
	// belum ada giliran). sampai dijawab / timeout / dibatalkan.
	pending bool
}

// ttChallengeTimeout adalah tenggat jawaban tantangan: tak dijawab =
// otomatis batal + umumkan. Const (bukan magic number) supaya gampang
// diubah tanpa menyentuh logic timer.
const ttChallengeTimeout = 60 * time.Second

// ttMu mengunci SEMUA akses state game, termasuk saat mengirim pesan hasil
// (jaringan). Skala bot ini kecil sehingga blokir antar-chat sepersekian
// detik bisa diterima — imbalannya tidak ada race dua langkah bersamaan
// yang lolos validasi giliran.
var (
	ttMu    sync.Mutex
	ttGames = map[string]*ttGame{}
)

func RegisterTicTacToe(r *Registry) {
	r.Register(Command{
		Name:        "tictactoe",
		Aliases:     []string{"ttt", "tc"},
		Description: "Tictactoe (.ttt @user tantang, .ttt bot, .ttt 5 langkah, .ttt stop)",
		Category:    CategoryGame,
		Handler:     handleTicTacToe,
	})
}

func ttUsage() string {
	return "Tictactoe:\n" +
		".ttt @user — tantang member (grup, perlu diterima dulu)\n" +
		".ttt terima / .ttt tolak — jawab tantangan (yang ditantang)\n" +
		".ttt bot — lawan bot\n" +
		".ttt 5 — taruh di kotak 5 (atau ketik angka polos saat giliranmu)\n" +
		".ttt papan — tampilkan papan\n" +
		".ttt nyerah — menyerah\n" +
		".ttt stop — hentikan game"
}

func handleTicTacToe(ctx context.Context, mctx *store.MessageContext) error {
	key := mctx.Chat.String()
	arg := ""
	if len(mctx.Args) > 0 {
		arg = strings.ToLower(strings.TrimSpace(mctx.Args[0]))
	}

	ttMu.Lock()
	defer ttMu.Unlock()

	switch arg {
	case "":
		return store.ReplyText(ctx, mctx, ttUsage())
	case "stop":
		if _, ok := ttGames[key]; !ok {
			return store.ReplyText(ctx, mctx, "Tidak ada game tictactoe di chat ini.")
		}
		delete(ttGames, key)
		return store.ReplyText(ctx, mctx, "Game tictactoe dihentikan.")
	case "papan", "board":
		g, ok := ttGames[key]
		if !ok {
			return store.ReplyText(ctx, mctx, "Tidak ada game aktif. "+ttUsage())
		}
		if g.pending {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Tantangan belum dijawab @%s.", displayTTUser(g.playerO)))
		}
		return ttSendBoard(ctx, mctx, g, "")
	case "terima":
		g, ok := ttGames[key]
		if !ok || !g.pending {
			return store.ReplyText(ctx, mctx, "Tidak ada tantangan yang perlu dijawab.")
		}
		sender, alt := mctx.Sender, mctx.RawEvt.Info.SenderAlt
		if !ttSamePlayer(g.playerO, sender, alt) {
			return store.ReplyText(ctx, mctx, "Hanya yang ditantang yang bisa menjawab.")
		}
		return ttAnswerChallengeLocked(ctx, mctx, g, key, true)
	case "tolak":
		g, ok := ttGames[key]
		if !ok || !g.pending {
			return store.ReplyText(ctx, mctx, "Tidak ada tantangan yang perlu dijawab.")
		}
		sender, alt := mctx.Sender, mctx.RawEvt.Info.SenderAlt
		if !ttSamePlayer(g.playerO, sender, alt) {
			return store.ReplyText(ctx, mctx, "Hanya yang ditantang yang bisa menjawab.")
		}
		return ttAnswerChallengeLocked(ctx, mctx, g, key, false)
	case "nyerah", "surrender", "menyerah":
		g, ok := ttGames[key]
		if !ok {
			return store.ReplyText(ctx, mctx, "Tidak ada game aktif.")
		}
		if g.pending {
			return store.ReplyText(ctx, mctx, "Game belum mulai. Batalkan tantangan: .ttt stop.")
		}
		sender, alt := mctx.Sender, mctx.RawEvt.Info.SenderAlt
		if !ttIsPlayer(g, sender, alt) {
			return store.ReplyText(ctx, mctx, "Kamu bukan pemain game ini.")
		}
		return ttFinishSurrender(ctx, mctx, g, key, sender, alt)
	case "bot":
		if _, ok := ttGames[key]; ok {
			return store.ReplyText(ctx, mctx, "Sudah ada game berjalan di chat ini (.ttt stop dulu).")
		}
		g := &ttGame{playerX: mctx.Sender, turnX: true, vsBot: true}
		ttGames[key] = g
		return ttSendBoard(ctx, mctx, g, "Game dimulai! Kamu ❌, bot ⭕.")
	default:
		// Angka 1-9 = langkah via command.
		if pos, ok := ttParsePos(arg); ok {
			g, exists := ttGames[key]
			if !exists {
				return store.ReplyText(ctx, mctx, "Tidak ada game aktif. Tantang dulu: .ttt @user atau .ttt bot.")
			}
			return ttDoMoveLocked(ctx, mctx, g, key, pos)
		}
		// Selain itu = tantangan ke member (mention / reply).
		return ttChallengeLocked(ctx, mctx, key)
	}
}

// ttChallengeLocked membuat tantangan (BELUM mulai main): yang ditantang
// di-tag dan diminta menjawab `.ttt terima` / `.ttt tolak` dalam
// ttChallengeTimeout. Timer otomatis membatalkan tantangan yang tak
// dijawab. Sengaja tidak langsung mulai (keputusan owner) supaya tidak
// ada game yang berjalan tanpa persetujuan lawan.
func ttChallengeLocked(ctx context.Context, mctx *store.MessageContext, key string) error {
	if _, ok := ttGames[key]; ok {
		return store.ReplyText(ctx, mctx, "Sudah ada game/tantangan di chat ini (.ttt stop dulu).")
	}
	if !mctx.IsGroup() {
		return store.ReplyText(ctx, mctx, "Tantang member hanya bisa di grup. Di private chat, lawan bot: .ttt bot.")
	}
	targetStr := ""
	if mentioned := mctx.MentionedJIDs(); len(mentioned) > 0 {
		targetStr = mentioned[0]
	} else if quoted := mctx.QuotedParticipant(); quoted != "" {
		targetStr = quoted
	}
	if targetStr == "" {
		return store.ReplyText(ctx, mctx, "Tag / reply member yang ditantang. "+ttUsage())
	}
	target, err := types.ParseJID(targetStr)
	if err != nil || target.User == "" {
		return store.ReplyText(ctx, mctx, "JID target tidak valid. Tag / reply member yang ditantang.")
	}
	sender, alt := mctx.Sender, mctx.RawEvt.Info.SenderAlt
	if ttSamePlayer(target, sender, alt) {
		return store.ReplyText(ctx, mctx, "Tidak bisa menantang diri sendiri. Lawan bot saja: .ttt bot.")
	}
	g := &ttGame{playerX: sender, playerO: target, turnX: true, pending: true}
	ttGames[key] = g
	ttArmChallengeTimeout(mctx.Client, mctx.Chat, key, g)
	return ttSendBoardWithMentions(ctx, mctx, g,
		fmt.Sprintf("@%s menantang @%s main tictactoe!\n@%s, ketik `.ttt terima` untuk main atau `.ttt tolak` (%d detik).",
			displayTTUser(sender), displayTTUser(target), displayTTUser(target), int(ttChallengeTimeout.Seconds())),
		[]types.JID{target})
}

// ttAnswerChallengeLocked memproses jawaban tantangan (accept=true mulai
// game, false bubar). HARUS dipanggil dengan ttMu terkunci dan g.pending
// true (sudah dicek pemanggil).
func ttAnswerChallengeLocked(ctx context.Context, mctx *store.MessageContext, g *ttGame, key string, accept bool) error {
	sender := mctx.Sender
	if !accept {
		delete(ttGames, key)
		return ttSendBoardWithMentions(ctx, mctx, g,
			fmt.Sprintf("@%s menolak tantangan. Mungkin lain waktu!", displayTTUser(sender)),
			nil)
	}
	g.pending = false
	return ttSendBoard(ctx, mctx, g, fmt.Sprintf("Tantangan diterima! @%s ❌ vs @%s ⭕.",
		displayTTUser(g.playerX), displayTTUser(g.playerO)))
}

// ttArmChallengeTimeout memasang timer sekali-pakai untuk tantangan: tak
// dijawab dalam ttChallengeTimeout = otomatis batal + umumkan ke chat.
// Cek pointer (ttGames[key] != g) mengabaikan timer basi — mis. tantangan
// sudah dijawab/dihentikan sebelum timer menyala (pola armScheduledCall
// di call.go). Pengumuman lewat store.SendText (bukan ReplyText) karena
// timer tidak punya pesan asal untuk di-quote.
func ttArmChallengeTimeout(client *whatsmeow.Client, chat types.JID, key string, g *ttGame) {
	time.AfterFunc(ttChallengeTimeout, func() {
		ttMu.Lock()
		defer ttMu.Unlock()
		if ttGames[key] != g || !g.pending {
			return
		}
		challenged := g.playerO
		delete(ttGames, key)
		mctx := &store.MessageContext{Client: client, Chat: chat}
		_ = store.SendText(context.Background(), mctx,
			fmt.Sprintf("Tantangan tictactoe untuk @%s kedaluwarsa (tidak dijawab %d detik).",
				displayTTUser(challenged), int(ttChallengeTimeout.Seconds())))
	})
}

// ttDoMoveLocked mengeksekusi satu langkah (pos 0-8) dari pengirim pesan.
// HARUS dipanggil dengan ttMu terkunci. Termasuk giliran bot kalau mode
// vsBot dan game belum selesai.
func ttDoMoveLocked(ctx context.Context, mctx *store.MessageContext, g *ttGame, key string, pos int) error {
	if g.pending {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Tantangan belum dijawab @%s.", displayTTUser(g.playerO)))
	}
	sender, alt := mctx.Sender, mctx.RawEvt.Info.SenderAlt
	if !ttIsPlayer(g, sender, alt) {
		return store.ReplyText(ctx, mctx, "Kamu bukan pemain game ini.")
	}
	if !ttIsTurn(g, sender, alt) {
		return store.ReplyText(ctx, mctx, "Belum giliranmu.")
	}
	if errMsg := ttPlace(g, pos); errMsg != "" {
		return store.ReplyText(ctx, mctx, errMsg)
	}
	if done, msg, mentions := ttAfterMove(g); done {
		delete(ttGames, key)
		return ttSendBoardWithMentions(ctx, mctx, g, msg, mentions)
	}
	// Lawan bot: bot langsung melangkah setelah pemain.
	if g.vsBot {
		empties := ttEmptyCells(g)
		if len(empties) > 0 {
			ttPlaceUnchecked(g, empties[rand.Intn(len(empties))], false)
		}
		if done, msg, mentions := ttAfterMove(g); done {
			delete(ttGames, key)
			return ttSendBoardWithMentions(ctx, mctx, g, msg, mentions)
		}
	}
	return ttSendBoard(ctx, mctx, g, "")
}

// ttFinishSurrender memenangkan lawan dari pengirim yang menyerah. HARUS
// dipanggil dengan ttMu terkunci.
func ttFinishSurrender(ctx context.Context, mctx *store.MessageContext, g *ttGame, key string, sender, alt types.JID) error {
	var winner types.JID
	xIsSender := ttSamePlayer(g.playerX, sender, alt)
	if g.vsBot {
		if !xIsSender {
			return store.ReplyText(ctx, mctx, "Kamu bukan pemain game ini.")
		}
		winner = types.JID{} // bot menang — tidak ada JID untuk di-mention
	} else if xIsSender {
		winner = g.playerO
	} else {
		winner = g.playerX
	}
	delete(ttGames, key)
	if winner.IsEmpty() {
		return ttSendBoard(ctx, mctx, g, "Kamu menyerah. Bot ⭕ menang! 🤖")
	}
	return ttSendBoardWithMentions(ctx, mctx, g,
		fmt.Sprintf("@%s menyerah. 🏆 @%s menang!", displayTTUser(sender), displayTTUser(winner)),
		[]types.JID{winner})
}

// ttPlace menaruh bidak pemain yang sedang dapat giliran ke pos (0-8).
// Mengembalikan pesan error kalau sel terisi (sudah pasti giliran benar
// karena dicek pemanggil).
func ttPlace(g *ttGame, pos int) string {
	mask := 1 << pos
	if g.xBits&mask != 0 || g.oBits&mask != 0 {
		return fmt.Sprintf("Kotak %d sudah terisi, pilih yang lain.", pos+1)
	}
	ttPlaceUnchecked(g, pos, g.turnX)
	return ""
}

// ttPlaceUnchecked menaruh bidak tanpa validasi (untuk bot / internal).
func ttPlaceUnchecked(g *ttGame, pos int, isX bool) {
	if isX {
		g.xBits |= 1 << pos
	} else {
		g.oBits |= 1 << pos
	}
	g.turnX = !g.turnX
}

// ttAfterMove mengecek akhir game setelah satu langkah: menang/seri.
// Mengembalikan done + teks hasil + JID untuk di-mention.
func ttAfterMove(g *ttGame) (bool, string, []types.JID) {
	for _, m := range ttWinMasks {
		if g.xBits&m == m {
			return true, fmt.Sprintf("🏆 @%s (❌) menang!", displayTTUser(g.playerX)), []types.JID{g.playerX}
		}
		if g.oBits&m == m {
			if g.vsBot {
				return true, "Bot ⭕ menang! 🤖 Coba lagi: .ttt bot", nil
			}
			return true, fmt.Sprintf("🏆 @%s (⭕) menang!", displayTTUser(g.playerO)), []types.JID{g.playerO}
		}
	}
	if g.xBits|g.oBits == 0x1FF {
		return true, "Seri! Tidak ada yang menang. Rematch? .ttt @user", nil
	}
	return false, "", nil
}

// ttEmptyCells mengembalikan indeks sel kosong untuk langkah bot.
func ttEmptyCells(g *ttGame) []int {
	var out []int
	for i := 0; i < 9; i++ {
		if g.xBits&(1<<i) == 0 && g.oBits&(1<<i) == 0 {
			out = append(out, i)
		}
	}
	return out
}

// ttIsPlayer true kalau pengirim adalah salah satu pemain game.
func ttIsPlayer(g *ttGame, sender, alt types.JID) bool {
	return ttSamePlayer(g.playerX, sender, alt) ||
		(!g.vsBot && ttSamePlayer(g.playerO, sender, alt))
}

// ttIsTurn true kalau pengirim adalah pemain yang sedang dapat giliran.
// Giliran bot (vsBot + turnX false) selalu false — bot tidak mengetik.
// Game pending (tantangan belum dijawab) belum punya giliran: false untuk
// semua, sehingga angka polos saat pending tidak dikonsumsi sebagai langkah.
func ttIsTurn(g *ttGame, sender, alt types.JID) bool {
	if g.pending {
		return false
	}
	if g.turnX {
		return ttSamePlayer(g.playerX, sender, alt)
	}
	return !g.vsBot && ttSamePlayer(g.playerO, sender, alt)
}

// ttSamePlayer membandingkan JID tersimpan dengan pengirim, tahan terhadap
// dual-format LID vs nomor telepon: cocok kalau string full-nya sama ATAU
// user-part-nya sama, dicek ke Sender maupun SenderAlt (filosofi sama
// dengan IsUserBanned di internal/store/ownerdb.go).
func ttSamePlayer(stored, sender, alt types.JID) bool {
	if stored.IsEmpty() {
		return false
	}
	s := stored.ToNonAD()
	for _, c := range []types.JID{sender, alt} {
		if c.IsEmpty() {
			continue
		}
		c = c.ToNonAD()
		if c.String() == s.String() {
			return true
		}
		if c.User != "" && s.User != "" && c.User == s.User {
			return true
		}
	}
	return false
}

// ttParsePos mengubah "1"-"9" jadi indeks 0-8.
func ttParsePos(s string) (int, bool) {
	if len(s) != 1 || s[0] < '1' || s[0] > '9' {
		return 0, false
	}
	return int(s[0] - '1'), true
}

// displayTTUser mengambil bagian user JID untuk teks mention (@...).
// Klien WhatsApp me-render-nya jadi nama kontak (lihat ReplyTextWithMentions).
func displayTTUser(jid types.JID) string {
	return jid.ToNonAD().User
}

// ttRenderBoard menggambar papan 3x3: bidak atau emoji angka pilihan.
func ttRenderBoard(g *ttGame) string {
	cells := make([]string, 9)
	for i := 0; i < 9; i++ {
		switch {
		case g.xBits&(1<<i) != 0:
			cells[i] = "❌"
		case g.oBits&(1<<i) != 0:
			cells[i] = "⭕"
		default:
			cells[i] = ttCellEmoji[i]
		}
	}
	return cells[0] + cells[1] + cells[2] + "\n" +
		cells[3] + cells[4] + cells[5] + "\n" +
		cells[6] + cells[7] + cells[8]
}

// ttSendBoard mengirim papan + baris giliran (mention pemain yang jalan).
func ttSendBoard(ctx context.Context, mctx *store.MessageContext, g *ttGame, header string) error {
	var turn types.JID
	var turnMark string
	if g.turnX {
		turn, turnMark = g.playerX, "❌"
	} else if !g.vsBot {
		turn, turnMark = g.playerO, "⭕"
	}
	var sb strings.Builder
	if header != "" {
		sb.WriteString(header + "\n\n")
	}
	sb.WriteString(ttRenderBoard(g))
	var mentions []types.JID
	if !turn.IsEmpty() {
		sb.WriteString(fmt.Sprintf("\n\nGiliran %s @%s", turnMark, displayTTUser(turn)))
		mentions = append(mentions, turn)
	} else {
		sb.WriteString("\n\nGiliran bot ⭕...")
	}
	sb.WriteString("\nKetik angka 1-9 (atau .ttt 5)")
	return ttSendBoardWithMentions(ctx, mctx, g, sb.String(), mentions)
}

// ttSendBoardWithMentions mengirim teks papan + legenda pemain + mentions.
// Legenda selalu menyertakan kedua pemain supaya jelas siapa X dan siapa O.
// Daftar mention = kedua pemain valid + tambahan, didedup berdasarkan JID.
func ttSendBoardWithMentions(ctx context.Context, mctx *store.MessageContext, g *ttGame, text string, mentions []types.JID) error {
	text += fmt.Sprintf("\n❌: @%s", displayTTUser(g.playerX))
	if !g.vsBot && !g.playerO.IsEmpty() {
		text += fmt.Sprintf("\n⭕: @%s", displayTTUser(g.playerO))
	} else {
		text += "\n⭕: bot 🤖"
	}
	seen := map[string]bool{}
	all := make([]types.JID, 0, 4)
	addMention := func(j types.JID) {
		if j.IsEmpty() {
			return
		}
		k := j.ToNonAD().String()
		if !seen[k] {
			seen[k] = true
			all = append(all, j)
		}
	}
	addMention(g.playerX)
	if !g.vsBot {
		addMention(g.playerO)
	}
	for _, m := range mentions {
		addMention(m)
	}
	if len(all) == 0 {
		return store.ReplyText(ctx, mctx, text)
	}
	return store.ReplyTextWithMentions(ctx, mctx, text, all)
}
