package plugins

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"wabot-go/internal/geminiapi"
	"wabot-go/internal/store"

	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types"
	"github.com/polymorfa/hypermeow/types/events"
	"google.golang.org/protobuf/proto"
)

// geminiResetForTest membersihkan keadaan sesi global antar tes.
func geminiResetForTest(t *testing.T) {
	t.Helper()
	geminiMu.Lock()
	geminiSessions = map[string]*geminiSession{}
	geminiBusy = map[string]bool{}
	geminiMu.Unlock()
	old := geminiNow
	t.Cleanup(func() { geminiNow = old })
}

func TestGeminiCutPrefix(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"gemini halo", "halo", true},
		{"Gemini: apa kabar", "apa kabar", true},
		{"gemini, ringkas ya", "ringkas ya", true},
		{"gemini - jelaskan", "jelaskan", true},
		{"  gemini   spasi  ", "spasi", true},
		{"gemini", "", true},
		{"gemini2 bukan", "", false},
		{"bukan gemini", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := geminiCutPrefix(c.in)
		if ok != c.ok || got != c.want {
			t.Fatalf("geminiCutPrefix(%q) = (%q,%v), mau (%q,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestGeminiSplitReplyShort(t *testing.T) {
	chunks := geminiSplitReply("  halo dunia  ", geminiBubbleRunes)
	if len(chunks) != 1 || chunks[0] != "halo dunia" {
		t.Fatalf("chunks = %v", chunks)
	}
	if got := geminiSplitReply("   ", geminiBubbleRunes); len(got) != 0 {
		t.Fatalf("teks kosong harus jadi 0 potongan, dapat %v", got)
	}
}

func TestGeminiSplitReplyNewline(t *testing.T) {
	// Newline dalam rentang [500, 1000)
	text := strings.Repeat("a", 700) + "\n" + strings.Repeat("b", 800)
	chunks := geminiSplitReply(text, geminiBubbleRunes)
	if len(chunks) != 2 {
		t.Fatalf("len(chunks) = %d, mau 2", len(chunks))
	}
	if chunks[0] != strings.Repeat("a", 700) {
		t.Fatalf("chunk[0] panjang %d, mau 700", utf8.RuneCountInString(chunks[0]))
	}
	if chunks[1] != strings.Repeat("b", 800) {
		t.Fatalf("chunk[1] panjang %d, mau 800", utf8.RuneCountInString(chunks[1]))
	}
	for i, c := range chunks {
		if utf8.RuneCountInString(c) > geminiBubbleRunes {
			t.Fatalf("chunk[%d] > %d: %d", i, geminiBubbleRunes, utf8.RuneCountInString(c))
		}
	}
}

func TestGeminiSplitReplySpaceFallback(t *testing.T) {
	// Spasi dalam rentang [500, 1000) saat tanpa newline
	text := strings.Repeat("x", 650) + " " + strings.Repeat("y", 750)
	chunks := geminiSplitReply(text, geminiBubbleRunes)
	if len(chunks) != 2 {
		t.Fatalf("len(chunks) = %d, mau 2", len(chunks))
	}
	if chunks[0] != strings.Repeat("x", 650) {
		t.Fatalf("chunk[0] panjang %d, mau 650", utf8.RuneCountInString(chunks[0]))
	}
	if chunks[1] != strings.Repeat("y", 750) {
		t.Fatalf("chunk[1] panjang %d, mau 750", utf8.RuneCountInString(chunks[1]))
	}
	for i, c := range chunks {
		if utf8.RuneCountInString(c) > geminiBubbleRunes {
			t.Fatalf("chunk[%d] > %d: %d", i, geminiBubbleRunes, utf8.RuneCountInString(c))
		}
	}
}

func TestGeminiSplitReplyForceCutAndAllDelivered(t *testing.T) {
	// Teks panjang tanpa spasi & newline: potong paksa di 1000, semua isi terkirim, tiap bubble <= 1000
	text := strings.Repeat("z", 2500)
	chunks := geminiSplitReply(text, geminiBubbleRunes)
	if len(chunks) != 3 {
		t.Fatalf("len(chunks) = %d, mau 3", len(chunks))
	}
	if utf8.RuneCountInString(chunks[0]) != 1000 || utf8.RuneCountInString(chunks[1]) != 1000 || utf8.RuneCountInString(chunks[2]) != 500 {
		t.Fatalf("panjang potongan: %d, %d, %d", utf8.RuneCountInString(chunks[0]), utf8.RuneCountInString(chunks[1]), utf8.RuneCountInString(chunks[2]))
	}
	for i, c := range chunks {
		if utf8.RuneCountInString(c) > geminiBubbleRunes {
			t.Fatalf("chunk[%d] > %d: %d", i, geminiBubbleRunes, utf8.RuneCountInString(c))
		}
	}
	joined := strings.Join(chunks, "")
	if joined != text {
		t.Fatalf("semua isi harus terkirim tanpa perubahan")
	}
}

func TestGeminiSplitReplyKeepsHeadingWithParagraph(t *testing.T) {
	// Paragraf pembuka + item 1 yang panjang sehingga item 2 berada di sekitar 950 rune
	pembuka := strings.Repeat("A", 400) + "\n\n"
	item1 := "* *Dominasi Jaringan 5G, 6G, dan Internet Cepat Merata*\n" + strings.Repeat("B", 450) + "\n\n"
	heading2 := "* *Perkembangan *Internet of Things* (IoT) dan Rumah Pintar*"
	desc2 := "\nSemakin banyak perangkat sehari-hari—mulai dari kendaraan, peralatan rumah tangga—akan terhubung ke internet. " + strings.Repeat("C", 300)
	fullText := pembuka + item1 + heading2 + desc2

	chunks := geminiSplitReply(fullText, geminiBubbleRunes)
	if len(chunks) < 2 {
		t.Fatalf("harus terpecah minimal 2 bubble, dapat %d", len(chunks))
	}

	// Bubble pertama TIDAK boleh berakhir dengan baris heading2
	lastLineChunk0 := getLastNonEmptyLine(chunks[0])
	if strings.Contains(lastLineChunk0, "Perkembangan") {
		t.Fatalf("Bubble 0 terpotong di judul tanpa paragrafnya! last line: %s", lastLineChunk0)
	}

	// Bubble kedua harus memuat heading2 dan awal kalimat penjelasannya
	if !strings.HasPrefix(chunks[1], heading2) {
		t.Fatalf("Bubble 1 harus diawali dengan heading2, dapat:\n%s", chunks[1])
	}
	if !strings.Contains(chunks[1], "Semakin banyak perangkat") {
		t.Fatalf("Bubble 1 harus menyertakan paragraf penjelasnya!")
	}

	for i, c := range chunks {
		if utf8.RuneCountInString(c) > geminiBubbleRunes {
			t.Fatalf("chunk[%d] melebihi %d: %d", i, geminiBubbleRunes, utf8.RuneCountInString(c))
		}
	}
}

func TestGeminiSplitReplyNumberedHeadingKept(t *testing.T) {
	intro := strings.Repeat("X", 550) + "\n\n"
	heading := "1. *Keamanan Siber dan Privasi Data*"
	body := "\nIni adalah penjelasan detail mengenai keamanan data siber yang sangat penting. " + strings.Repeat("Y", 600)
	text := intro + heading + body

	chunks := geminiSplitReply(text, geminiBubbleRunes)
	if len(chunks) < 2 {
		t.Fatalf("harus terpecah minimal 2 bubble, dapat %d", len(chunks))
	}

	// Heading tidak boleh tertinggal di akhir chunk 0
	if strings.Contains(getLastNonEmptyLine(chunks[0]), "Keamanan Siber") {
		t.Fatalf("Heading tertinggal di akhir bubble pertama")
	}

	// Heading dan paragraf harus bersama di chunk 1
	if !strings.Contains(chunks[1], heading) || !strings.Contains(chunks[1], "Ini adalah penjelasan") {
		t.Fatalf("Heading dan penjelasannya harus berada di chunk yang sama!")
	}
}

func TestGeminiSystemRequest(t *testing.T) {
	sys := "Kamu adalah asisten pintar."
	q := "Apa ibukota Indonesia?"
	got := geminiSystemRequest(sys, q)
	want := "[Instruksi sistem]\nKamu adalah asisten pintar.\n\n[Pertanyaan]\nApa ibukota Indonesia?"
	if got != want {
		t.Fatalf("geminiSystemRequest() =\n%q\nmau:\n%q", got, want)
	}

	if gotEmpty := geminiSystemRequest("", q); gotEmpty != q {
		t.Fatalf("sys kosong: got %q, mau %q", gotEmpty, q)
	}
}

func TestGeminiSystemAction(t *testing.T) {
	// show
	for _, in := range []string{"", "   ", "\t\n"} {
		if act := geminiSystemAction(in); act != "show" {
			t.Fatalf("geminiSystemAction(%q) = %q, mau show", in, act)
		}
	}

	// reset
	for _, in := range []string{"reset", "RESET", "hapus", "Hapus", "off", "OFF", "clear", "CLEAR"} {
		if act := geminiSystemAction(in); act != "reset" {
			t.Fatalf("geminiSystemAction(%q) = %q, mau reset", in, act)
		}
	}

	// set (termasuk yang diawali kata "reset ")
	casesSet := []string{
		"kamu adalah asisten",
		"reset semua aturan sebelumnya dan jadilah kucing",
		"hapus ingatanmu dan mulai dari awal",
		"clear your mind and think",
		"off with their heads",
	}
	for _, in := range casesSet {
		if act := geminiSystemAction(in); act != "set" {
			t.Fatalf("geminiSystemAction(%q) = %q, mau set", in, act)
		}
	}
}

func TestGeminiPinnedTurnSurvivesPruning(t *testing.T) {
	pinnedUser := geminiSystemRequest("Sistem penting", strings.Repeat("u", geminiTurnFieldRunes*2))
	// 1. Giliran pinned tidak dipotong 1200 rune
	history := geminiAppendTurn(nil, geminiTurn{User: pinnedUser, Bot: "bot jawab", Pinned: true})
	if len(history) != 1 {
		t.Fatalf("len(history) = %d", len(history))
	}
	if history[0].User != pinnedUser {
		t.Fatalf("giliran pinned terpotong: len=%d, mau=%d", utf8.RuneCountInString(history[0].User), utf8.RuneCountInString(pinnedUser))
	}

	// 2. Giliran non-pinned tetap terpotong 1200 rune
	longNonPinned := strings.Repeat("x", geminiTurnFieldRunes*2)
	history = geminiAppendTurn(history, geminiTurn{User: longNonPinned, Bot: longNonPinned, Pinned: false})
	if len(history) != 2 {
		t.Fatalf("len(history) = %d", len(history))
	}
	if utf8.RuneCountInString(history[1].User) > geminiTurnFieldRunes+1 {
		t.Fatalf("giliran non-pinned tidak terpotong: %d", utf8.RuneCountInString(history[1].User))
	}

	// 3. Tambahkan banyak giliran non-pinned sampai melebihi geminiMaxTurns (8).
	// Giliran pinned (di history[0]) harus bertahan!
	for i := 0; i < 10; i++ {
		history = geminiAppendTurn(history, geminiTurn{User: "pertanyaan", Bot: "jawaban"})
	}
	if len(history) != geminiMaxTurns {
		t.Fatalf("len(history) = %d, mau %d", len(history), geminiMaxTurns)
	}
	if !history[0].Pinned || history[0].User != pinnedUser {
		t.Fatalf("giliran pinned hilang saat pemangkasan!")
	}

	// 4. geminiBuildPrompt juga tidak membuang giliran pinned saat riwayat kepanjangan
	bigNonPinned := strings.Repeat("y", geminiTurnFieldRunes)
	var crowdedHistory []geminiTurn
	crowdedHistory = append(crowdedHistory, geminiTurn{User: pinnedUser, Bot: "bot 1", Pinned: true})
	for i := 0; i < geminiMaxTurns; i++ {
		crowdedHistory = append(crowdedHistory, geminiTurn{User: bigNonPinned, Bot: bigNonPinned})
	}

	built := geminiBuildPrompt(crowdedHistory, "tanya lagi")
	if !strings.Contains(built, "Sistem penting") {
		t.Fatalf("geminiBuildPrompt membuang giliran pinned!")
	}
	if !strings.Contains(built, "tanya lagi") {
		t.Fatalf("geminiBuildPrompt menghilangkan pertanyaan user!")
	}
}

// TestGeminiAppendTurnTrimsFields memastikan satu jawaban raksasa tidak
// memenuhi riwayat: tiap field dipotong, bukan dibuang seluruhnya.
func TestGeminiAppendTurnTrimsFields(t *testing.T) {
	long := strings.Repeat("a", geminiTurnFieldRunes*2)
	history := geminiAppendTurn(nil, geminiTurn{User: long, Bot: long})
	if len(history) != 1 {
		t.Fatalf("giliran = %d, mau 1", len(history))
	}
	if got := utf8.RuneCountInString(history[0].User); got > geminiTurnFieldRunes+1 {
		t.Fatalf("field user = %d rune, mau ≤ %d", got, geminiTurnFieldRunes+1)
	}
}

func TestGeminiAppendTurnMaxTurns(t *testing.T) {
	var history []geminiTurn
	for i := 0; i < geminiMaxTurns+4; i++ {
		history = geminiAppendTurn(history, geminiTurn{User: "tanya", Bot: "jawab"})
	}
	if len(history) != geminiMaxTurns {
		t.Fatalf("giliran = %d, mau %d", len(history), geminiMaxTurns)
	}
}

func TestGeminiAppendTurnMaxRunes(t *testing.T) {
	big := strings.Repeat("b", geminiTurnFieldRunes)
	var history []geminiTurn
	for i := 0; i < geminiMaxTurns; i++ {
		history = geminiAppendTurn(history, geminiTurn{User: big, Bot: big})
	}
	if got := geminiHistoryRunes(history); got > geminiMaxHistoryRunes {
		t.Fatalf("total riwayat = %d rune, mau ≤ %d", got, geminiMaxHistoryRunes)
	}
	if len(history) == 0 {
		t.Fatal("riwayat tidak boleh kosong")
	}
}

// TestGeminiBuildPromptIncludesHistory memastikan konteks percakapan ikut
// terkirim — mode tamu tidak menyimpan riwayat di server.
func TestGeminiBuildPromptIncludesHistory(t *testing.T) {
	history := []geminiTurn{{User: "Nama saya Raka.", Bot: "Halo Raka."}}
	got := geminiBuildPrompt(history, "Siapa nama saya?")
	for _, want := range []string{"Nama saya Raka.", "Halo Raka.", "Siapa nama saya?"} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt %q tidak memuat %q", got, want)
		}
	}
}

func TestGeminiBuildPromptWithoutHistory(t *testing.T) {
	if got := geminiBuildPrompt(nil, "  halo  "); got != "halo" {
		t.Fatalf("prompt = %q", got)
	}
}

// TestGeminiBuildPromptFitsLimit memastikan giliran tertua dibuang sampai
// prompt + riwayat muat dalam batas.
func TestGeminiBuildPromptFitsLimit(t *testing.T) {
	big := strings.Repeat("c", geminiTurnFieldRunes)
	var history []geminiTurn
	for i := 0; i < geminiMaxTurns; i++ {
		history = append(history, geminiTurn{User: big, Bot: big})
	}
	got := geminiBuildPrompt(history, "pertanyaan terakhir")
	if utf8.RuneCountInString(got) > geminiMaxPromptRunes {
		t.Fatalf("prompt = %d rune, mau ≤ %d", utf8.RuneCountInString(got), geminiMaxPromptRunes)
	}
	if !strings.Contains(got, "pertanyaan terakhir") {
		t.Fatal("pertanyaan terbaru hilang")
	}
}

// TestGeminiSessionTTL memastikan sesi (dan riwayatnya) mati setelah 10 menit
// tanpa pesan.
func TestGeminiSessionTTL(t *testing.T) {
	geminiResetForTest(t)
	now := time.Unix(1_700_000_000, 0)
	geminiNow = func() time.Time { return now }

	key := "chat|user"
	geminiSessionEnsure(key)
	geminiSessionAddTurn(key, geminiTurn{User: "halo", Bot: "hai"})

	now = now.Add(geminiSessionTTL - time.Second)
	if turns, _, active := geminiSessionInfo(key); !active || turns != 1 {
		t.Fatalf("sesi harus masih aktif: turns=%d active=%v", turns, active)
	}

	now = now.Add(2 * time.Second)
	if _, active := geminiSessionGet(key); active {
		t.Fatal("sesi harus mati setelah TTL")
	}
	if _, _, active := geminiSessionInfo(key); active {
		t.Fatal("info sesi harus nonaktif setelah TTL")
	}
}

// TestGeminiSessionClearKeepsActive memastikan `.gemini new` membuang riwayat
// tapi sesi tetap hidup.
func TestGeminiSessionClearKeepsActive(t *testing.T) {
	geminiResetForTest(t)
	key := "chat|user"
	geminiSessionEnsure(key)
	geminiSessionAddTurn(key, geminiTurn{User: "a", Bot: "b"})

	if !geminiSessionClear(key) {
		t.Fatal("clear harus true")
	}
	turns, _, active := geminiSessionInfo(key)
	if !active || turns != 0 {
		t.Fatalf("setelah clear: active=%v turns=%d", active, turns)
	}
}

// TestGeminiKeyPerSender memastikan riwayat dipisah per pengirim, bukan per grup.
func TestGeminiKeyPerSender(t *testing.T) {
	chat := types.JID{User: "123@g.us", Server: types.GroupServer}
	a := geminiKey(nil, chat, types.JID{User: "111", Server: types.DefaultUserServer}, types.JID{})
	b := geminiKey(nil, chat, types.JID{User: "222", Server: types.DefaultUserServer}, types.JID{})
	if a == b {
		t.Fatalf("kunci pengirim berbeda harus beda: %q", a)
	}
	other := geminiKey(nil, types.JID{User: "456@g.us", Server: types.GroupServer}, types.JID{User: "111", Server: types.DefaultUserServer}, types.JID{})
	if a == other {
		t.Fatalf("kunci chat berbeda harus beda: %q", a)
	}
}

// TestGeminiTriggerOf memastikan aturan pemicu sempit: kata gemini selalu
// memicu, reply hanya saat sesi aktif, dan lampiran tidak didukung.
func TestGeminiTriggerOf(t *testing.T) {
	evt := &events.Message{
		Info: types.MessageInfo{
			Chat:   types.JID{User: "123@g.us", Server: types.GroupServer},
			Sender: types.JID{User: "111", Server: types.DefaultUserServer},
		},
		Message: &waE2E.Message{
			ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("halo")},
		},
	}
	if p, ok := geminiTriggerOf(evt, "gemini halo", false); !ok || p != "halo" {
		t.Fatalf("prefix: (%q,%v)", p, ok)
	}
	if _, ok := geminiTriggerOf(evt, "halo biasa", false); ok {
		t.Fatal("teks biasa tidak boleh memicu")
	}
	if _, ok := geminiTriggerOf(evt, "", false); ok {
		t.Fatal("teks kosong tidak boleh memicu")
	}
}

// TestGeminiTriggerOfReply memastikan reply ke bubble bot hanya memicu saat
// sesi masih hidup.
func TestGeminiTriggerOfReply(t *testing.T) {
	const botID = "GEMINI-REPLY-1"
	store.TrackBotMessageID(botID, "lanjut")
	evt := &events.Message{
		Info: types.MessageInfo{
			Chat:   types.JID{User: "123@g.us", Server: types.GroupServer},
			Sender: types.JID{User: "111", Server: types.DefaultUserServer},
		},
		Message: &waE2E.Message{
			ExtendedTextMessage: &waE2E.ExtendedTextMessage{
				Text:        proto.String("lanjut"),
				ContextInfo: &waE2E.ContextInfo{StanzaID: proto.String(botID)},
			},
		},
	}
	if _, ok := geminiTriggerOf(evt, "lanjut", false); ok {
		t.Fatal("reply tanpa sesi aktif tidak boleh memicu")
	}
	if p, ok := geminiTriggerOf(evt, "lanjut", true); !ok || p != "lanjut" {
		t.Fatalf("reply saat aktif: (%q,%v)", p, ok)
	}
}

func TestGeminiDiagVerdict(t *testing.T) {
	healthy := geminiapi.Diag{
		Cookie: geminiapi.Stage{OK: true},
		Meta:   geminiapi.Stage{OK: true},
		Chat:   geminiapi.Stage{OK: true},
	}
	if got := geminiDiagVerdict(&healthy); !strings.Contains(got, "sehat") {
		t.Fatalf("verdict sehat = %q", got)
	}

	noCookie := geminiapi.Diag{Cookie: geminiapi.Stage{Err: geminiapi.ErrGuestCookie}}
	if got := geminiDiagVerdict(&noCookie); !strings.Contains(got, "Cookie") {
		t.Fatalf("verdict cookie = %q", got)
	}

	noMeta := geminiapi.Diag{Cookie: geminiapi.Stage{OK: true}, Meta: geminiapi.Stage{Err: geminiapi.ErrMeta}}
	if got := geminiDiagVerdict(&noMeta); !strings.Contains(got, "protokol") {
		t.Fatalf("verdict meta = %q", got)
	}

	limit := geminiapi.Diag{
		Cookie: geminiapi.Stage{OK: true},
		Meta:   geminiapi.Stage{OK: true},
		Chat:   geminiapi.Stage{Err: geminiapi.ErrLimit},
	}
	if got := geminiDiagVerdict(&limit); !strings.Contains(got, "batas") {
		t.Fatalf("verdict limit = %q", got)
	}
}

// TestGeminiDiagVerdictMissingNID mengunci temuan probe panel (Nov 2026): sesi
// tamu tanpa cookie NID tetap bisa satu giliran, tapi percakapan lanjutan
// ditolak server (`1097`). Diagnosa harus menyebut NID, bukan menyimpulkan
// "sehat" lalu menyalahkan tiket.
func TestGeminiDiagVerdictMissingNID(t *testing.T) {
	healthy := geminiapi.Diag{
		Cookie: geminiapi.Stage{OK: true},
		Meta:   geminiapi.Stage{OK: true},
		Chat:   geminiapi.Stage{OK: true},
		Ticket: geminiapi.Resume{ConversationID: "c_x"},
	}
	if got := geminiDiagVerdictFor(&healthy, []string{"SOCS", "COMPASS"}, false); !strings.Contains(got, "NID") {
		t.Fatalf("verdict tanpa NID = %q", got)
	}
	if got := geminiDiagVerdictFor(&healthy, []string{"SOCS", "NID"}, false); !strings.Contains(got, "sehat") {
		t.Fatalf("verdict dengan NID = %q", got)
	}
}

func TestGeminiErrorText(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{geminiapi.ErrGuestCookie, "sesi tamu"},
		{geminiapi.ErrMeta, "protokol"},
		{geminiapi.ErrLimit, "Batas"},
		{geminiapi.ErrStaleConv, "new"},
		{geminiapi.ErrEmpty, "tidak terbaca"},
	}
	for _, c := range cases {
		got := geminiErrorText(c.err)
		if !strings.Contains(strings.ToLower(got), strings.ToLower(c.want)) {
			t.Fatalf("geminiErrorText(%v) = %q, mau memuat %q", c.err, got, c.want)
		}
	}
	if got := geminiErrorText(nil); got != "" {
		t.Fatalf("nil = %q", got)
	}
}

func TestGeminiHelpBlockMentionsTTL(t *testing.T) {
	help := geminiHelpBlock()
	want := fmt.Sprintf("%d menit", int(geminiSessionTTL.Minutes()))
	if !strings.Contains(help, want) {
		t.Fatalf("help tidak menyebut TTL %q: %s", want, help)
	}
	if strings.Contains(strings.ToLower(help), "login <cookie>") {
		t.Fatalf("help masih menawarkan login cookie: %s", help)
	}
}

func TestGeminiCleanAnswerIndentedList(t *testing.T) {
	input := `Halo Mitra! Berikut adalah beberapa pilihan penjual es pisang ijo di Kendari yang bisa kamu kunjungi:

* *Kedai Es Pisang Ijo Daun*
 * *Alamat:* Jalan Komjen Dr.H.M Jasin, Mokoau, Kota Kendari
* *Pisang Ijo AMANDA Wua-wua*
 * *Alamat:* Jalan Jenderal M.T. Haryono, Bende, Kota Kendari

Apakah ada tempat di atas yang ingin kamu pilih untuk dicarikan rutenya, Mitra?`

	got := geminiCleanAnswer(input)

	if strings.Contains(got, " * *Alamat:*") {
		t.Fatalf("baris alamat masih memiliki spasi sebelum bintang: %q", got)
	}
	if !strings.Contains(got, "\n* *Alamat:* Jalan Komjen") {
		t.Fatalf("baris alamat harus rata kiri '* *Alamat:*', got:\n%s", got)
	}
}

func TestGeminiCleanAnswerAsteriskInParentheses(t *testing.T) {
	in := "*1. Integrasi AI yang Lebih Dalam (*Artificial Intelligence*)*"
	want := "*1. Integrasi AI yang Lebih Dalam (Artificial Intelligence)*"
	got := geminiCleanAnswer(in)
	if got != want {
		t.Fatalf("geminiCleanAnswer() =\n%q\nmau:\n%q", got, want)
	}
}

// TestGeminiSessionTTLIsTenMinutes mengunci umur sesi yang disepakati (10
// menit): lebih lama dari versi awal (5 menit), tetap pendek supaya obrolan
// lama tidak terbawa ke topik baru.
func TestGeminiSessionTTLIsTenMinutes(t *testing.T) {
	if geminiSessionTTL != 10*time.Minute {
		t.Fatalf("geminiSessionTTL = %v, mau 10 menit", geminiSessionTTL)
	}
}

// TestGeminiSessionClearDropsTicket memastikan `.gemini new` membuang tiket
// percakapan juga, bukan hanya riwayat di memori.
func TestGeminiSessionClearDropsTicket(t *testing.T) {
	geminiResetForTest(t)
	key := "chat|user"
	geminiSessionEnsure(key)

	ticket := geminiapi.Resume{ConversationID: "c_1", ResponseID: "r_1", ReplyID: "rc_1"}
	geminiSessionSetResume(key, ticket, "UID-1")
	if got, uid := geminiSessionResume(key); got != ticket || uid != "UID-1" {
		t.Fatalf("tiket tersimpan = %+v uid=%q", got, uid)
	}

	geminiSessionClear(key)
	if got, uid := geminiSessionResume(key); !got.Empty() || uid != "" {
		t.Fatalf("setelah clear tiket masih ada: %+v uid=%q", got, uid)
	}
}

// TestGeminiStateKeyStable memastikan kunci penyimpanan riwayat pendek, stabil,
// dan tidak memuat JID mentah.
func TestGeminiStateKeyStable(t *testing.T) {
	a := geminiStateKey("628123@s.whatsapp.net|628123@s.whatsapp.net")
	if !strings.HasPrefix(a, geminiHistoryPrefix) {
		t.Fatalf("kunci = %q, mau berawalan %q", a, geminiHistoryPrefix)
	}
	if a != geminiStateKey("628123@s.whatsapp.net|628123@s.whatsapp.net") {
		t.Fatal("kunci harus stabil untuk sesi yang sama")
	}
	if a == geminiStateKey("628999@s.whatsapp.net|628123@s.whatsapp.net") {
		t.Fatal("sesi berbeda harus punya kunci berbeda")
	}
	if strings.Contains(a, "@") || strings.Contains(a, "|") {
		t.Fatalf("kunci tidak boleh memuat JID mentah: %q", a)
	}
	if len(a) != len(geminiHistoryPrefix)+16 {
		t.Fatalf("panjang kunci = %d, mau %d", len(a), len(geminiHistoryPrefix)+16)
	}
}

// TestGeminiTicketMatchesCookie mengunci arti "tiket masih berlaku": tiket
// kosong tidak pernah cocok, tiket tanpa sidik jari (versi lama) dianggap cocok,
// dan tiket bersidik jari hanya cocok bila cookie tamu yang sekarang sama.
func TestGeminiTicketMatchesCookie(t *testing.T) {
	geminiResetForTest(t)
	if geminiTicketMatchesCookie(geminiapi.Resume{}) {
		t.Fatal("tiket kosong tidak boleh dianggap berlaku")
	}
	if !geminiTicketMatchesCookie(geminiapi.Resume{ConversationID: "c_1"}) {
		t.Fatal("tiket tanpa sidik jari harus dianggap berlaku")
	}
	// Klien tes tidak pernah mengambil cookie, jadi CookieID kosong: tiket
	// tidak boleh dinyatakan basi hanya karena identitas tamu belum diketahui.
	if !geminiTicketMatchesCookie(geminiapi.Resume{ConversationID: "c_1", CookieID: "sidik"}) {
		t.Fatal("tanpa cookie di memori, tiket tidak boleh dianggap basi")
	}
}

// TestGeminiHistorySurvivesRestart mengunci inti perbaikan "tahan restart":
// riwayat giliran harus bisa ditulis, dibaca kembali, dan ditolak setelah
// lewat geminiSessionTTL. Tiket percakapan TIDAK ikut disimpan, karena sesi
// tamu terikat cookie `NID` yang selalu baru setelah proses restart.
func TestGeminiHistorySurvivesRestart(t *testing.T) {
	geminiResetForTest(t)
	now := time.Date(2026, 11, 11, 13, 0, 0, 0, time.UTC)
	geminiNow = func() time.Time { return now }

	turns := []geminiTurn{
		{User: "ingat angka 4711, jawab siap.", Bot: "Siap."},
		{User: "angka berapa tadi?", Bot: "4711"},
	}
	raw, ok := geminiEncodeHistory(turns, now)
	if !ok {
		t.Fatal("riwayat tidak ter-encode")
	}
	if strings.Contains(raw, "NID") || strings.Contains(raw, "cookie") {
		t.Fatalf("state riwayat tidak boleh memuat kredensial: %s", raw)
	}

	got := geminiDecodeHistory(raw, now.Add(9*time.Minute))
	if len(got) != 2 || got[1].Bot != "4711" {
		t.Fatalf("riwayat terbaca = %+v", got)
	}

	if got := geminiDecodeHistory(raw, now.Add(geminiSessionTTL+time.Second)); got != nil {
		t.Fatalf("riwayat kedaluwarsa harus dibuang, dapat %+v", got)
	}
	if got := geminiDecodeHistory("{rusak", now); got != nil {
		t.Fatalf("riwayat rusak harus diabaikan, dapat %+v", got)
	}
	if _, ok := geminiEncodeHistory(nil, now); ok {
		t.Fatal("riwayat kosong tidak perlu disimpan")
	}
}

// TestGeminiPromptForTurn memastikan prompt hanya membawa riwayat saat tiket
// percakapan tidak tersedia; dengan tiket, server yang mengingat konteks.
func TestGeminiPromptForTurn(t *testing.T) {
	history := []geminiTurn{{User: "siapa presiden pertama", Bot: "Ir. Soekarno"}}

	withTicket := geminiPromptForTurn(history, "  dan wakilnya?  ",
		geminiapi.Resume{ConversationID: "c_1", ResponseID: "r_1"})
	if withTicket != "dan wakilnya?" {
		t.Fatalf("prompt bertiket = %q, mau pertanyaan apa adanya", withTicket)
	}

	withoutTicket := geminiPromptForTurn(history, "dan wakilnya?", geminiapi.Resume{})
	if !strings.Contains(withoutTicket, "siapa presiden pertama") {
		t.Fatalf("prompt tanpa tiket harus memuat riwayat:\n%s", withoutTicket)
	}
	if !strings.Contains(withoutTicket, "dan wakilnya?") {
		t.Fatalf("prompt tanpa tiket harus memuat pertanyaan:\n%s", withoutTicket)
	}
}

// --- konteks waktu & lokasi ---

// TestGeminiContextBlock mengunci isi blok konteks: waktu lokal, zona, lokasi
// pengguna, dan larangan memakai lokasi server. Blok inilah yang membantah
// tebakan Gemini bahwa pengguna berada di Jerman.
func TestGeminiContextBlock(t *testing.T) {
	now := time.Date(2026, 11, 11, 13, 5, 0, 0, time.UTC)

	withLoc := geminiContextBlock(now, "Makassar, Sulawesi Selatan")
	for _, want := range []string{
		"11 Nov 2026 13:05",
		"UTC+00:00",
		"Lokasi pengguna: Makassar, Sulawesi Selatan.",
		"Jangan memakai lokasi server",
		"Jangan mengarang nama tempat",
	} {
		if !strings.Contains(withLoc, want) {
			t.Fatalf("blok konteks tidak memuat %q:\n%s", want, withLoc)
		}
	}

	noLoc := geminiContextBlock(now, "")
	if !strings.Contains(noLoc, "belum disetel") {
		t.Fatalf("tanpa lokasi, blok harus meminta kota/daerah:\n%s", noLoc)
	}
	if strings.Contains(noLoc, "Lokasi pengguna: .") {
		t.Fatalf("lokasi kosong tidak boleh jadi titik kosong:\n%s", noLoc)
	}
}

// TestGeminiTurnContext mengunci baris pendek yang ditempel tiap giliran:
// jam tetap segar dan lokasi ikut berubah tanpa `.gemini new`.
func TestGeminiTurnContext(t *testing.T) {
	now := time.Date(2026, 11, 11, 13, 5, 0, 0, time.UTC)

	withLoc := geminiTurnContext(now, "Makassar")
	if !strings.Contains(withLoc, "11 Nov 2026 13:05") || !strings.Contains(withLoc, "· Makassar") {
		t.Fatalf("baris konteks = %q", withLoc)
	}
	if !strings.HasPrefix(withLoc, "[Sekarang: ") || !strings.HasSuffix(withLoc, "]") {
		t.Fatalf("bentuk baris konteks salah: %q", withLoc)
	}

	noLoc := geminiTurnContext(now, "  ")
	if strings.Contains(noLoc, "·") {
		t.Fatalf("tanpa lokasi tidak boleh ada pemisah: %q", noLoc)
	}
}

// TestGeminiMergeSystem memastikan blok konteks tidak menimpa prompt sistem
// owner, dan tetap ada walau owner belum menyetel apa pun.
func TestGeminiMergeSystem(t *testing.T) {
	block := geminiContextBlock(time.Date(2026, 11, 11, 13, 5, 0, 0, time.UTC), "Makassar")

	merged := geminiMergeSystem("Jawab singkat.", block)
	if !strings.HasPrefix(merged, "Jawab singkat.") || !strings.Contains(merged, "[Konteks]") {
		t.Fatalf("gabungan salah:\n%s", merged)
	}
	if got := geminiMergeSystem("", block); got != block {
		t.Fatalf("tanpa sistem owner, hasil harus blok konteks saja: %q", got)
	}
	if got := geminiMergeSystem("Jawab singkat.", ""); got != "Jawab singkat." {
		t.Fatalf("tanpa blok, hasil harus teks owner: %q", got)
	}
}

// TestGeminiStripAgentTags mengunci pembersihan penanda media internal Gemini
// (`<Image .../>`): src-nya id internal, bukan gambar, jadi harus dibuang.
func TestGeminiStripAgentTags(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "tag satu baris",
			in:   "Rekomendasi:\n\n<Image alt=\"Masjid Al Alam\" caption=\"Masjid di teluk\" src=\"image_agent_tag_1634513376826124782\"/>\n\n1. *Masjid Al-Alam*",
			want: "Rekomendasi:\n\n1. *Masjid Al-Alam*",
		},
		{
			name: "tag sendiri tanpa baris kosong",
			in:   "Daftar tempat:\n<Image src=\"image_agent_tag_9\"/>\n1. *Masjid Al-Alam*",
			want: "Daftar tempat:\n1. *Masjid Al-Alam*",
		},
		{
			name: "tag di baris terakhir",
			in:   "Selesai.\n<Image src=\"image_agent_tag_10\"/>",
			want: "Selesai.",
		},
		{
			name: "tag di tengah kalimat",
			in:   "Lihat ini <Image alt=\"x\" src=\"image_agent_tag_1\"/> sekarang.",
			want: "Lihat ini sekarang.",
		},
		{
			name: "atribut memuat > di dalam kutip",
			in:   "A <Image caption=\"a > b\" src=\"image_agent_tag_2\"/> B",
			want: "A B",
		},
		{
			name: "huruf besar dan img",
			in:   "A <IMAGE src=\"image_agent_tag_3\">B<img src=\"image_agent_tag_4\"/> C",
			want: "A B C",
		},
		{
			name: "video dengan penanda agent",
			in:   "A <Video src=\"video_agent_tag_5\"/> B",
			want: "A B",
		},
		{
			name: "tag tanpa penanda agent dibiarkan",
			in:   "Tulis <Video tutorial> kalau perlu.",
			want: "Tulis <Video tutorial> kalau perlu.",
		},
		{
			name: "teks biasa dengan kurung sudut",
			in:   "1 < 3 > 2",
			want: "1 < 3 > 2",
		},
	}

	for _, tc := range cases {
		if got := geminiStripAgentTags(tc.in); got != tc.want {
			t.Fatalf("%s:\ndapat %q\nmau   %q", tc.name, got, tc.want)
		}
	}
}

// TestGeminiCleanAnswerDropsImageTag memastikan pembersihan akhir (yang dipakai
// jalur kirim) benar-benar bebas dari tag media dan tidak menyisakan sampah.
func TestGeminiCleanAnswerDropsImageTag(t *testing.T) {
	in := "Rekomendasi tempat wisata:\n\n<Image alt=\"Masjid Al Alam Kendari di tengah laut\" caption=\"Masjid Al Alam di Teluk Kendari\" src=\"image_agent_tag_1634513376826124782\"/>\n\n1. *Masjid Al-Alam*"
	got := geminiCleanAnswer(in)
	if strings.Contains(got, "image_agent_tag") || strings.Contains(got, "<Image") {
		t.Fatalf("tag masih tersisa:\n%s", got)
	}
	if !strings.Contains(got, "Rekomendasi tempat wisata") || !strings.Contains(got, "*Masjid Al-Alam*") {
		t.Fatalf("teks penting ikut hilang:\n%s", got)
	}
}
