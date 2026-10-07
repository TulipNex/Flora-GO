package plugins

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"wabot-go/internal/logfmt"
	"wabot-go/internal/store"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types/events"
)

func init() {
	registerPlugin(RegisterVote)
}

// Batas polling mengikuti batas klien WhatsApp: minimal 2 opsi (satu opsi
// bukan pilihan), maksimal 12. Panjang dipangkas rune-aman supaya tidak
// ditolak server.
const (
	voteMinOptions  = 2
	voteMaxOptions  = 12
	voteMaxQRunes   = 255
	voteMaxOptRunes = 100
)

// pollTally adalah tally satu polling buatan bot. Counts sejajar Options.
// Voters memetakan pengirim -> indeks opsi pilihan TERAKHIR-nya (vote ulang
// menimpa, meniru perilaku klien: satu suara berlaku per orang per opsi,
// dihitung dari vote terakhir).
type pollTally struct {
	Question string
	Options  []string
	Counts   []int
	Voters   map[string][]int
}

// pollTallies adalah registry in-memory antar pesan masuk dan command
// .voteresult. Kunci = ID pesan polling bot (resp.ID saat .vote terkirim).
// IN-MEMORY sesuai keputusan (hilang saat restart) — map + mutex, tanpa DB.
var pollTallies = struct {
	sync.Mutex
	m map[string]*pollTally
}{m: make(map[string]*pollTally)}

// RegisterVote mendaftarkan `.vote` (buat polling) + `.voteresult` (lihat
// hasil, reply ke bubble polling). Kategori Umum, semua user boleh memakai.
//
// Pembuatan memakai cli.BuildPollCreation — JANGAN rakit
// PollCreationMessage manual: helper itu yang membangkitkan EncKey 32-byte
// + MessageSecret; tanpa secret ini vote masuk TAK BISA didekripsi
// (DecryptPollVote butuh secret aslinya dari store).
//
// Format:
//
//	.vote Mau makan apa? | Nasi goreng | Mie ayam | Sate
//	.vote Pilih hari | Senin | Rabu | Jumat --multi 2
func RegisterVote(r *Registry) {
	r.Register(Command{
		Name:        "vote",
		Description: "Buat polling (.vote tanya? | opsi1 | opsi2 ...)",
		Category:    CategoryUmum,
		Handler:     handleVote,
	})
	r.Register(Command{
		Name:        "voteresult",
		Description: "Lihat hasil polling (reply ke bubble polling)",
		Category:    CategoryUmum,
		Handler:     handleVoteResult,
	})
}

// voteMultiRe mendeteksi flag "--multi N" / "--multi=N" di mana saja dalam
// argumen mentah. Dipisah dari pemecah "|" supaya opsi boleh mengandung
// spasi bebas.
var voteMultiRe = regexp.MustCompile(`(?i)(?:^|\s)--multi[= ](\d+)\s*`)

func handleVote(ctx context.Context, mctx *store.MessageContext) error {
	raw := strings.TrimSpace(mctx.RawArgs)
	multi := 1
	if m := voteMultiRe.FindStringSubmatch(raw); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 1 {
			multi = n
		}
		raw = strings.TrimSpace(voteMultiRe.ReplaceAllString(raw, " "))
	}
	segs := splitVoteSegments(raw)
	if len(segs) < 1+voteMinOptions {
		return store.ReplyText(ctx, mctx, "Format: .vote pertanyaan? | opsi1 | opsi2 [| opsi3 ...] [--multi N]\nContoh: .vote Mau makan apa? | Nasi goreng | Mie ayam\nMinimal 2 opsi, maksimal 12 opsi.")
	}
	question := truncateVoteRunes(segs[0], voteMaxQRunes)
	options := make([]string, 0, len(segs)-1)
	for _, s := range segs[1:] {
		if s = truncateVoteRunes(s, voteMaxOptRunes); s != "" {
			options = append(options, s)
		}
	}
	if len(options) < voteMinOptions {
		return store.ReplyText(ctx, mctx, "Polling butuh minimal 2 opsi yang tidak kosong.")
	}
	if len(options) > voteMaxOptions {
		options = options[:voteMaxOptions]
	}
	if multi > len(options) {
		multi = len(options)
	}

	msg := mctx.Client.BuildPollCreation(question, options, multi)
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, msg)
	if err != nil {
		logfmt.Warn("vote: gagal kirim polling (%v)\n", err)
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal buat polling: %v", err))
	}
	pollTallies.Lock()
	pollTallies.m[string(resp.ID)] = &pollTally{
		Question: question,
		Options:  options,
		Counts:   make([]int, len(options)),
		Voters:   make(map[string][]int),
	}
	pollTallies.Unlock()
	store.LogOutgoing(mctx, "poll", question)
	store.TrackBotMessageID(string(resp.ID), "poll: "+question)
	return nil
}

// splitVoteSegments memecah argumen mentah per "|" dan membuang yang kosong
// di tepi (toleran spasi: "a |b| c " tetap rapi).
func splitVoteSegments(raw string) []string {
	parts := strings.Split(raw, "|")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// truncateVoteRunes memotong rune-aman (tanpa "..." — opsi polling yang
// terpotong diam-diam lebih membingungkan daripada yang pendek).
func truncateVoteRunes(s string, max int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > max {
		return strings.TrimSpace(string(r[:max]))
	}
	return s
}

func handleVoteResult(ctx context.Context, mctx *store.MessageContext) error {
	quotedID := store.ExtractStanzaID(mctx.RawEvt)
	if quotedID == "" {
		return store.ReplyText(ctx, mctx, "Reply ke bubble polling bot, lalu .voteresult\nContoh: reply polling → .voteresult")
	}
	pollTallies.Lock()
	tally, ok := pollTallies.m[quotedID]
	pollTallies.Unlock()
	if !ok {
		return store.ReplyText(ctx, mctx, "Data polling tidak ditemukan (bukan polling bot, atau bot sudah restart sehingga tally hilang).")
	}
	return store.ReplyText(ctx, mctx, formatVoteResult(tally))
}

// formatVoteResult merangkai hasil urut suara terbanyak, lengkap dengan
// persen (pembagi = total suara opsi, bukan jumlah pemilih — konsisten
// dengan tampilan klien untuk polling multi-pilih).
func formatVoteResult(t *pollTally) string {
	total := 0
	for _, c := range t.Counts {
		total += c
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "*Hasil: %s*\nTotal %d suara\n\n", t.Question, total)
	order := make([]int, len(t.Options))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool {
		if t.Counts[order[a]] == t.Counts[order[b]] {
			return order[a] < order[b]
		}
		return t.Counts[order[a]] > t.Counts[order[b]]
	})
	for rank, i := range order {
		pct := 0.0
		if total > 0 {
			pct = float64(t.Counts[i]) * 100 / float64(total)
		}
		fmt.Fprintf(&sb, "%d. %s — %d (%.0f%%)\n", rank+1, t.Options[i], t.Counts[i], pct)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// RecordPollVote mencatat satu vote masuk ke tally polling bot. Dipanggil
// handler untuk SETIAP pesan non-command (dan command? tidak — vote tak
// berprefix, jadi cukup jalur non-command) setelah unwrap.
//
// Mengembalikan true bila pesan adalah PollUpdateMessage (ditangani —
// pemanggil WAJIB setop alur di sini, jangan teruskan ke AI/moderasi),
// false bila bukan vote. Kegagalan dekripsi/lookup TIDAK error ke user:
// vote adalah interaksi pasif, spam balasan tiap vote gagal justru
// mengganggu — cukup log console (fail-soft non-kritis).
func RecordPollVote(client *whatsmeow.Client, evt *events.Message) bool {
	msg := store.UnwrapMessage(evt.Message)
	if msg == nil || msg.GetPollUpdateMessage() == nil {
		return false
	}
	ctx := context.Background()
	vote, err := client.DecryptPollVote(ctx, evt)
	if err != nil {
		logfmt.Warn("vote: dekripsi vote gagal (%v)\n", err)
		return true
	}
	key := ""
	if k := msg.GetPollUpdateMessage().GetPollCreationMessageKey(); k != nil {
		key = k.GetID()
	}
	pollTallies.Lock()
	tally, ok := pollTallies.m[key]
	pollTallies.Unlock()
	if !ok {
		// Bukan polling bot (polling user lain / data hilang restart).
		return true
	}
	idxs := voteOptionIndexes(vote.GetSelectedOptions(), tally.Options)
	voter := evt.Info.Sender.String()
	pollTallies.Lock()
	if prev, voted := tally.Voters[voter]; voted {
		for _, i := range prev {
			if i >= 0 && i < len(tally.Counts) {
				tally.Counts[i]--
			}
		}
	}
	for _, i := range idxs {
		tally.Counts[i]++
	}
	tally.Voters[voter] = idxs
	pollTallies.Unlock()
	return true
}

// voteOptionIndexes memetakan hash SHA-256 pilihan (isi SelectedOptions)
// ke indeks opsi — meniru HashPollOptions milik whatsmeow (hash nama opsi
// mentah, tanpa normalisasi).
func voteOptionIndexes(hashes [][]byte, options []string) []int {
	var out []int
	for _, h := range hashes {
		for i, opt := range options {
			sum := sha256.Sum256([]byte(opt))
			if bytes.Equal(h, sum[:]) {
				out = append(out, i)
				break
			}
		}
	}
	return out
}
