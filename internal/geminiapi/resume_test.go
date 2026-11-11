package geminiapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// decodeChatForm membongkar f.req (bentuk [null, "<json>"]) menjadi payload
// dalam supaya tes bisa memeriksa slot satu per satu.
func decodeChatForm(t *testing.T, form string) []any {
	t.Helper()
	var outer []any
	if err := json.Unmarshal([]byte(form), &outer); err != nil {
		t.Fatalf("f.req bukan JSON: %v (%q)", err, form)
	}
	if len(outer) != 2 {
		t.Fatalf("f.req harus 2 slot luar, dapat %d", len(outer))
	}
	raw, ok := outer[1].(string)
	if !ok {
		t.Fatalf("slot kedua f.req harus string JSON, dapat %T", outer[1])
	}
	var inner []any
	if err := json.Unmarshal([]byte(raw), &inner); err != nil {
		t.Fatalf("payload dalam bukan JSON: %v", err)
	}
	return inner
}

// TestBuildChatRequestFullShape mengunci bentuk 80 slot: nomor slot, isi
// prompt, bahasa, tiket percakapan, dan identitas klien.
func TestBuildChatRequestFullShape(t *testing.T) {
	ticket := Resume{ConversationID: "c_abc", ResponseID: "r_def", ReplyID: "rc_1"}
	inner := decodeChatForm(t, buildChatRequestFull("halo", "id-ID", ticket, "UID-TES"))

	if len(inner) != 80 {
		t.Fatalf("panjang payload = %d, mau 80", len(inner))
	}

	prompt, ok := inner[0].([]any)
	if !ok || len(prompt) == 0 || prompt[0] != "halo" {
		t.Fatalf("slot 0 (prompt) = %v", inner[0])
	}
	lang, ok := inner[1].([]any)
	if !ok || len(lang) == 0 || lang[0] != "id-ID" {
		t.Fatalf("slot 1 (bahasa) = %v", inner[1])
	}

	resume, ok := inner[2].([]any)
	if !ok || len(resume) != 10 {
		t.Fatalf("slot 2 (tiket) harus 10 elemen, dapat %v", inner[2])
	}
	if resume[0] != "c_abc" || resume[1] != "r_def" || resume[2] != "rc_1" {
		t.Fatalf("id tiket tidak di slot 0-2: %v", resume)
	}
	if resume[9] != "" {
		t.Fatalf("slot 9 tiket harus string kosong, dapat %v", resume[9])
	}

	if inner[59] != "UID-TES" {
		t.Fatalf("slot 59 (uid) = %v", inner[59])
	}
	if s, ok := inner[4].(string); !ok || len(s) != 32 {
		t.Fatalf("slot 4 harus token hex 16 byte, dapat %v", inner[4])
	}
	if inner[3] != "" {
		t.Fatalf("slot 3 harus string kosong, dapat %v", inner[3])
	}
	if inner[61] == nil {
		t.Fatal("slot 61 harus array kosong, bukan null")
	}
}

// TestBuildChatRequestMinimalShape memastikan jalur cadangan tetap tiga slot.
func TestBuildChatRequestMinimalShape(t *testing.T) {
	inner := decodeChatForm(t, buildChatRequest("halo", ""))
	if len(inner) != 3 {
		t.Fatalf("panjang payload minimal = %d, mau 3", len(inner))
	}
	lang, ok := inner[1].([]any)
	if !ok || len(lang) == 0 || lang[0] != defaultLang {
		t.Fatalf("bahasa cadangan = %v, mau %q", inner[1], defaultLang)
	}
	resume, ok := inner[2].([]any)
	if !ok || len(resume) != 0 {
		t.Fatalf("slot tiket harus kosong, dapat %v", inner[2])
	}
}

func TestResumeArrayShape(t *testing.T) {
	arr := Resume{ConversationID: "c", ResponseID: "r", ReplyID: "rc"}.array()
	if len(arr) != 10 {
		t.Fatalf("panjang = %d, mau 10", len(arr))
	}
	if arr[0] != "c" || arr[1] != "r" || arr[2] != "rc" || arr[9] != "" {
		t.Fatalf("isi tidak sesuai: %v", arr)
	}
	for i := 3; i <= 8; i++ {
		if arr[i] != nil {
			t.Fatalf("slot %d harus null, dapat %v", i, arr[i])
		}
	}
}

func TestResumeEmpty(t *testing.T) {
	if !(Resume{}).Empty() {
		t.Fatal("Resume{} harus Empty")
	}
	if (Resume{ReplyID: "rc"}).Empty() {
		t.Fatal("Resume dengan satu id bukan Empty")
	}
}

// TestExtractPayloadIDs memastikan tiket percakapan diambil dari balasan.
func TestExtractPayloadIDs(t *testing.T) {
	res := &Result{}
	handleLine(buildStreamLine(t, streamInner([]string{"Halo"})), res)
	if res.Text != "Halo" {
		t.Fatalf("teks = %q", res.Text)
	}
	if res.ConversationID != "c_abc" || res.ResponseID != "r_def" || res.ReplyID != "rc_1" {
		t.Fatalf("tiket = %+v", res.Resume())
	}
}

// TestExtractPayloadIDsFirstPayloadWins mengunci temuan uji live (Nov 2026):
// satu balasan memuat LEBIH DARI SATU id percakapan, dan hanya id dari payload
// PERTAMA yang diterima server (id lain dijawab `1097`). Dulu id terakhir
// menimpa yang pertama, sehingga setiap giliran ditolak dan kuota terpakai dua
// kali.
func TestExtractPayloadIDsFirstPayloadWins(t *testing.T) {
	res := &Result{}

	benar := []any{
		nil,
		[]any{"c_benar", "r_benar"},
		nil, nil,
		[]any{[]any{"rc_balasan", []any{"Halo"}}},
	}
	salah := []any{
		nil,
		[]any{"c_salah", "r_salah"},
		nil, nil,
		[]any{[]any{"rc_salah", []any{"Halo, ada lagi?"}}},
	}

	handleLine(buildStreamLine(t, benar), res)
	handleLine(buildStreamLine(t, salah), res)

	if res.ConversationID != "c_benar" || res.ResponseID != "r_benar" {
		t.Fatalf("tiket tertimpa payload berikutnya: %+v", res.Resume())
	}
	if res.ReplyID != "rc_balasan" {
		t.Fatalf("id balasan = %q, mau rc_balasan", res.ReplyID)
	}
	if res.Text != "Halo, ada lagi?" {
		t.Fatalf("teks = %q, mau versi terpanjang", res.Text)
	}
}

// TestTicketRejectedStopsResending memastikan tiket yang ditolak server tidak
// dikirim ulang di setiap giliran: satu permintaan per balasan, bukan dua.
// Setelah masa jeda (cookie tamu diperbarui) tiket dicoba sekali lagi.
func TestTicketRejectedStopsResending(t *testing.T) {
	f := newFakeGemini(t, "")
	f.mu.Lock()
	f.chatBody = `[["e",4,null,null,131]]` + "\n" + `[["BardErrorInfo",[1097,null,"x"]]]` + "\n"
	f.mu.Unlock()

	base := time.Now()
	oldNow := ticketNow
	ticketNow = func() time.Time { return base }
	defer func() { ticketNow = oldNow }()

	c := New()
	ticket := Resume{ConversationID: "c_lama", ResponseID: "r_lama", ReplyID: "rc_lama"}
	if _, err := c.AskWith(context.Background(), "halo", ticket); !errors.Is(err, ErrStaleConv) {
		t.Fatalf("error = %v, mau ErrStaleConv", err)
	}
	if !c.TicketRejected() || c.TicketEnabled() {
		t.Fatalf("tiket harus dimatikan: rejected=%v enabled=%v", c.TicketRejected(), c.TicketEnabled())
	}
	if got := atomic.LoadInt32(&f.chatCalls); got != 1 {
		t.Fatalf("chat dipanggil %d kali, mau 1", got)
	}

	// Giliran berikutnya: tiket tidak dikirim lagi (satu permintaan, bukan dua).
	f.mu.Lock()
	f.chatBody = buildStreamLine(t, streamInner([]string{"Halo"})) + "\n"
	f.mu.Unlock()
	if _, err := c.AskWith(context.Background(), "lagi", ticket); err != nil {
		t.Fatalf("ask: %v", err)
	}
	f.mu.Lock()
	inner := decodeChatForm(t, f.lastForm)
	f.mu.Unlock()
	if conv := ticketConv(inner); conv != "" {
		t.Fatalf("tiket tidak boleh dikirim saat dimatikan: %q", conv)
	}
	if got := atomic.LoadInt32(&f.chatCalls); got != 2 {
		t.Fatalf("chat dipanggil %d kali, mau 2 (satu per giliran)", got)
	}

	// Masa jeda lewat = sesi tamu diperbarui: tiket dicoba sekali lagi.
	ticketNow = func() time.Time { return base.Add(ticketCooldown + time.Second) }
	if !c.TicketEnabled() {
		t.Fatal("tiket harus dicoba lagi setelah masa jeda")
	}
	if _, err := c.AskWith(context.Background(), "lagi", ticket); err != nil {
		t.Fatalf("ask: %v", err)
	}
	f.mu.Lock()
	inner = decodeChatForm(t, f.lastForm)
	f.mu.Unlock()
	if conv := ticketConv(inner); conv != "c_lama" {
		t.Fatalf("tiket harus dikirim lagi setelah masa jeda, dapat %q", conv)
	}
}

// ticketConv mengambil id percakapan dari slot tiket (slot 2) f.req. Bentuk
// lengkap selalu mengisi slot itu dengan larik 10 elemen, jadi id kosong
// diperiksa dari elemen pertamanya.
func ticketConv(inner []any) string {
	if len(inner) < 3 {
		return ""
	}
	arr, ok := inner[2].([]any)
	if !ok || len(arr) == 0 {
		return ""
	}
	s, _ := arr[0].(string)
	return s
}

// TestAskSendsTicketAndIdentity memastikan tiket + uid benar-benar dikirim dan
// header internal hanya muncul pada bentuk penuh.
func TestAskSendsTicketAndIdentity(t *testing.T) {
	f := newFakeGemini(t, buildStreamLine(t, streamInner([]string{"Lanjut"})))
	c := New()

	ticket := Resume{ConversationID: "c_abc", ResponseID: "r_def", ReplyID: "rc_1"}
	res, err := c.AskRequest(context.Background(), AskRequest{
		Prompt: "lagi",
		Resume: ticket,
		UID:    "UID-TES",
	})
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if got := res.Resume(); got != ticket {
		t.Fatalf("tiket hasil = %+v, mau %+v", got, ticket)
	}

	f.mu.Lock()
	inner := decodeChatForm(t, f.lastForm)
	header := f.lastHeader.Clone()
	f.mu.Unlock()

	if inner[59] != "UID-TES" {
		t.Fatalf("uid tidak dikirim: %v", inner[59])
	}
	resume, _ := inner[2].([]any)
	if len(resume) != 10 || resume[0] != "c_abc" {
		t.Fatalf("tiket tidak dikirim: %v", inner[2])
	}
	if !strings.Contains(header.Get("x-goog-ext-525005358-jspb"), "UID-TES") {
		t.Fatalf("header identitas = %q", header.Get("x-goog-ext-525005358-jspb"))
	}
	if header.Get("x-goog-ext-525001261-jspb") == "" {
		t.Fatal("header model tidak dikirim")
	}
	if !strings.Contains(header.Get("x-goog-ext-525001261-jspb"), "UID-TES") {
		t.Fatalf("header model harus memuat uid yang sama: %q", header.Get("x-goog-ext-525001261-jspb"))
	}
	if header.Get("x-goog-ext-73010990-jspb") != "[0,0,0]" {
		t.Fatalf("header ui 1 = %q", header.Get("x-goog-ext-73010990-jspb"))
	}
	if header.Get("x-goog-ext-73010989-jspb") != "[0]" {
		t.Fatalf("header ui 2 = %q", header.Get("x-goog-ext-73010989-jspb"))
	}
}

// TestAskFallsBackToMinimalShape memastikan bentuk penuh yang ditolak (HTTP
// 400) tidak menggagalkan jawaban: sekali ulang dengan bentuk minimal, lalu
// bentuk minimal dipakai untuk sisa proses.
func TestAskFallsBackToMinimalShape(t *testing.T) {
	f := newFakeGemini(t, buildStreamLine(t, streamInner([]string{"Pulih"})))
	f.mu.Lock()
	f.firstChat = 400
	f.mu.Unlock()
	c := New()

	res, err := c.Ask(context.Background(), "halo")
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if res.Text != "Pulih" {
		t.Fatalf("teks = %q", res.Text)
	}
	if got := atomic.LoadInt32(&f.chatCalls); got != 2 {
		t.Fatalf("chat dipanggil %d kali, mau 2", got)
	}
	if !c.payloadFallback.Load() {
		t.Fatal("bentuk penuh harus ditandai gagal setelah 400")
	}

	f.mu.Lock()
	inner := decodeChatForm(t, f.lastForm)
	f.mu.Unlock()
	if len(inner) != 3 {
		t.Fatalf("percobaan kedua harus bentuk minimal, dapat %d slot", len(inner))
	}

	// Giliran berikutnya langsung memakai bentuk minimal.
	if _, err := c.Ask(context.Background(), "lagi"); err != nil {
		t.Fatalf("ask kedua: %v", err)
	}
	f.mu.Lock()
	inner = decodeChatForm(t, f.lastForm)
	header := f.lastHeader.Clone()
	f.mu.Unlock()
	if len(inner) != 3 {
		t.Fatalf("setelah fallback harus tetap minimal, dapat %d slot", len(inner))
	}
	if header.Get("x-goog-ext-525005358-jspb") != "" {
		t.Fatal("header internal tidak boleh dikirim pada bentuk minimal")
	}
}

// TestAskStaleConvWithTicketNotRetried memastikan tiket yang ditolak tidak
// membakar kuota untuk percobaan kredensial baru: error dikembalikan supaya
// pemanggil memakai riwayat prompt.
func TestAskStaleConvWithTicketNotRetried(t *testing.T) {
	f := newFakeGemini(t, "")
	f.mu.Lock()
	f.chatBody = `[["e",4,null,null,131]]` + "\n" + `[["BardErrorInfo",[1097,null,"x"]]]` + "\n"
	f.mu.Unlock()
	c := New()

	_, err := c.AskWith(context.Background(), "halo",
		Resume{ConversationID: "c_lama", ResponseID: "r_lama", ReplyID: "rc_lama"})
	if !errors.Is(err, ErrStaleConv) {
		t.Fatalf("error = %v, mau ErrStaleConv", err)
	}
	if got := atomic.LoadInt32(&f.chatCalls); got != 1 {
		t.Fatalf("chat dipanggil %d kali, mau 1 (tanpa ulang)", got)
	}
}

// TestAskStaleConvWithoutTicketRetriesOnce menjaga perilaku lama: tanpa tiket,
// percakapan basi diulang sekali dengan kredensial baru.
func TestAskStaleConvWithoutTicketRetriesOnce(t *testing.T) {
	f := newFakeGemini(t, "")
	f.mu.Lock()
	f.chatBody = `[["e",4,null,null,131]]` + "\n" + `[["BardErrorInfo",[1096,null,"x"]]]` + "\n"
	f.mu.Unlock()
	c := New()

	_, err := c.Ask(context.Background(), "halo")
	if !errors.Is(err, ErrStaleConv) {
		t.Fatalf("error = %v, mau ErrStaleConv", err)
	}
	if got := atomic.LoadInt32(&f.chatCalls); got != 2 {
		t.Fatalf("chat dipanggil %d kali, mau 2", got)
	}
}

// TestTicketEnabledFollowsShape memastikan pemanggil tahu kapan tiket tidak
// dikirim server: hanya bentuk lengkap yang membawanya.
func TestTicketEnabledFollowsShape(t *testing.T) {
	if !New().TicketEnabled() {
		t.Fatal("tiket harus aktif secara default")
	}

	t.Setenv("GEMINI_FULL_PAYLOAD", "0")
	c := New()
	if c.TicketEnabled() {
		t.Fatal("GEMINI_FULL_PAYLOAD=0 harus mematikan tiket")
	}

	t.Setenv("GEMINI_FULL_PAYLOAD", "1")
	c = New()
	if !c.TicketEnabled() {
		t.Fatal("GEMINI_FULL_PAYLOAD=1 harus menghidupkan tiket")
	}
	c.payloadFallback.Store(true)
	if c.TicketEnabled() {
		t.Fatal("setelah bentuk penuh ditolak, tiket tidak dipakai lagi")
	}
}

// TestAskDisabledShapeIgnoresTicket memastikan saklar GEMINI_FULL_PAYLOAD=0
// benar-benar kembali ke bentuk minimal walau pemanggil mengirim tiket.
func TestAskDisabledShapeIgnoresTicket(t *testing.T) {
	t.Setenv("GEMINI_FULL_PAYLOAD", "0")
	f := newFakeGemini(t, buildStreamLine(t, streamInner([]string{"Halo"})))
	c := New()

	if _, err := c.AskWith(context.Background(), "halo",
		Resume{ConversationID: "c_1", ResponseID: "r_1", ReplyID: "rc_1"}); err != nil {
		t.Fatalf("ask: %v", err)
	}

	f.mu.Lock()
	inner := decodeChatForm(t, f.lastForm)
	header := f.lastHeader.Clone()
	f.mu.Unlock()
	if len(inner) != 3 {
		t.Fatalf("payload harus bentuk minimal, dapat %d slot", len(inner))
	}
	if header.Get("x-goog-ext-525005358-jspb") != "" {
		t.Fatal("header identitas tidak boleh dikirim pada bentuk minimal")
	}
}

// TestDiagReportsShapeAndTicket memastikan diagnosa melaporkan bentuk
// permintaan dan tiket percakapan yang diterima server.
func TestDiagReportsShapeAndTicket(t *testing.T) {
	newFakeGemini(t, buildStreamLine(t, streamInner([]string{"siap"})))
	d := New().Diag(context.Background())
	if !d.Cookie.OK || !d.Meta.OK || !d.Chat.OK {
		t.Fatalf("diag gagal: cookie=%v meta=%v chat=%v", d.Cookie.OK, d.Meta.OK, d.Chat.OK)
	}
	if d.Shape != "lengkap" {
		t.Fatalf("bentuk = %q, mau lengkap", d.Shape)
	}
	if d.Ticket.ConversationID != "c_abc" {
		t.Fatalf("tiket = %+v, mau c_abc", d.Ticket)
	}
}

// TestDiagMinimalShapeNoTicket memastikan bentuk minimal tidak melaporkan
// tiket (karena tiketnya memang tidak dikirim maupun dipakai).
func TestDiagMinimalShapeNoTicket(t *testing.T) {
	t.Setenv("GEMINI_FULL_PAYLOAD", "0")
	newFakeGemini(t, buildStreamLine(t, streamInner([]string{"siap"})))
	d := New().Diag(context.Background())
	if !d.Chat.OK {
		t.Fatalf("chat gagal: %v", d.Chat.Err)
	}
	if d.Shape != "minimal" {
		t.Fatalf("bentuk = %q, mau minimal", d.Shape)
	}
	if !d.Ticket.Empty() {
		t.Fatalf("bentuk minimal tidak boleh melaporkan tiket: %+v", d.Ticket)
	}
}

// TestNewUIDShape memastikan identitas klien berbentuk UUID v4 huruf besar.
func TestNewUIDShape(t *testing.T) {
	uid := newUID()
	if len(uid) != 36 {
		t.Fatalf("panjang uid = %d (%q)", len(uid), uid)
	}
	if uid[14] != '4' {
		t.Fatalf("versi uuid = %c, mau 4", uid[14])
	}
	switch uid[19] {
	case '8', '9', 'A', 'B':
	default:
		t.Fatalf("varian uuid = %c, mau 8/9/A/B", uid[19])
	}
	if newUID() == uid {
		t.Fatal("uid harus berbeda setiap dibuat")
	}
}
