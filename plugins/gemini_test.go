package plugins

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"wabot-go/internal/geminiapi"
)

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
	chunks := geminiSplitReply("  halo dunia  ", geminiReplyChunk)
	if len(chunks) != 1 || chunks[0] != "halo dunia" {
		t.Fatalf("chunks = %v", chunks)
	}
	if got := geminiSplitReply("   ", geminiReplyChunk); len(got) != 0 {
		t.Fatalf("teks kosong harus tanpa chunk: %v", got)
	}
}

func TestGeminiSplitReplyLong(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 800; i++ {
		sb.WriteString("baris panjang untuk uji pemecahan jawaban gemini\n")
	}
	text := sb.String()
	chunks := geminiSplitReply(text, geminiReplyChunk)
	if len(chunks) < 2 {
		t.Fatalf("harus terpecah, dapat %d chunk", len(chunks))
	}
	if len(chunks) > geminiMaxChunks {
		t.Fatalf("chunk %d melebihi batas %d", len(chunks), geminiMaxChunks)
	}
	for _, c := range chunks {
		if n := utf8.RuneCountInString(c); n > geminiReplyChunk {
			t.Fatalf("chunk %d rune melebihi %d", n, geminiReplyChunk)
		}
	}
	if !strings.Contains(chunks[len(chunks)-1], "dipotong") {
		t.Fatalf("chunk terakhir harus bertanda dipotong: %q", chunks[len(chunks)-1])
	}
}

func TestGeminiNormalizeCookieString(t *testing.T) {
	cases := map[string]string{
		"Cookie: __Secure-1PSID=abc; NID=1":               "__Secure-1PSID=abc; NID=1",
		"__Secure-1PSID=abc\nNID=1\r\n":                   "__Secure-1PSID=abc; NID=1",
		"  __Secure-1PSID=abc;  ":                         "__Secure-1PSID=abc",
		"__Secure-1PSID=abc\n\nNID=1\n__Secure-1PSIDTS=2": "__Secure-1PSID=abc; NID=1; __Secure-1PSIDTS=2",
	}
	for in, want := range cases {
		if got := geminiNormalizeCookieString(in); got != want {
			t.Fatalf("normalize(%q) = %q, mau %q", in, got, want)
		}
	}
}

func TestGeminiHelpText(t *testing.T) {
	if !strings.Contains(geminiHelpBlock(), ".gemini on") {
		t.Fatal("blok petunjuk harus memuat .gemini on")
	}
	if !strings.Contains(geminiHelpBlock(), ".gemini diag") {
		t.Fatal("blok petunjuk harus memuat .gemini diag")
	}
	if !strings.Contains(geminiHelpBlock(), ".gemini diag image") {
		t.Fatal("blok petunjuk harus memuat .gemini diag image")
	}
}

// TestGeminiDiagProbe memastikan baris diagnosa membedakan tiga keadaan yang
// sering tertukar: token ada, halaman login, dan halaman tanpa keduanya.
func TestGeminiDiagProbe(t *testing.T) {
	cases := []struct {
		name  string
		probe geminiapi.AppProbe
		want  string
	}{
		{"token", geminiapi.AppProbe{Status: 200, Bytes: 2048, HasToken: true}, "token ada"},
		{"login", geminiapi.AppProbe{Status: 200, Bytes: 2048, HasLogin: true}, "halaman login"},
		{"tanpa", geminiapi.AppProbe{Status: 200, Bytes: 2048}, "token tidak ada"},
		{"gagal", geminiapi.AppProbe{Err: geminiapi.ErrNoCookie}, "gagal"},
	}
	for _, tc := range cases {
		if got := geminiDiagProbe(tc.probe); !strings.Contains(got, tc.want) {
			t.Fatalf("%s: %q tidak memuat %q", tc.name, got, tc.want)
		}
	}
}

// TestGeminiDiagVerdict memastikan kesimpulan tidak menyuruh login ulang saat
// masalahnya hanya cookie volatile.
func TestGeminiDiagVerdict(t *testing.T) {
	dead := &geminiapi.Diag{
		FileNames:    []string{"__Secure-1PSID"},
		AppOnly1PSID: geminiapi.AppProbe{Status: 200, HasLogin: true},
	}
	if got := geminiDiagVerdict(dead); !strings.Contains(got, "Login ulang") {
		t.Fatalf("sesi mati: %q", got)
	}
	volatile := &geminiapi.Diag{
		FileNames: []string{"__Secure-1PSID"},
		AppNoTS:   geminiapi.AppProbe{Status: 200, HasToken: true},
	}
	if got := geminiDiagVerdict(volatile); strings.Contains(got, "Login ulang") {
		t.Fatalf("TS basi tidak boleh menyuruh login ulang: %q", got)
	}
	empty := &geminiapi.Diag{}
	if got := geminiDiagVerdict(empty); !strings.Contains(got, ".gemini login") {
		t.Fatalf("berkas kosong: %q", got)
	}
}

// TestGeminiEscalateAck memastikan reaksi ⏳ hanya dikirim bila giliran belum
// selesai: jawaban yang cepat tidak boleh meninggalkan ⏳ di pesan prompt.
func TestGeminiEscalateAck(t *testing.T) {
	calls := make(chan struct{}, 2)
	send := func() error {
		calls <- struct{}{}
		return nil
	}

	done := make(chan struct{})
	close(done)
	geminiEscalateAck(30*time.Millisecond, done, send)
	select {
	case <-calls:
		t.Fatal("reaksi tunggu tidak boleh dikirim setelah giliran selesai")
	case <-time.After(100 * time.Millisecond):
	}

	running := make(chan struct{})
	defer close(running)
	geminiEscalateAck(20*time.Millisecond, running, send)
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("reaksi tunggu harus dikirim saat giliran masih berjalan")
	}
	select {
	case <-calls:
		t.Fatal("reaksi tunggu tidak boleh dikirim dua kali")
	case <-time.After(100 * time.Millisecond):
	}

	// send nil = tidak ada yang dikirim dan tidak ada panic.
	geminiEscalateAck(time.Millisecond, make(chan struct{}), nil)
}

// TestGeminiDiagImageVerdict memastikan diagnosa gambar membedakan masalah
// generasi, 403 (dengan instruksi uji incognito), dan keberhasilan, tanpa
// menyebut URL.
func TestGeminiDiagImageVerdict(t *testing.T) {
	noChat := geminiapi.ImageProbe{Images: 0}
	if got := geminiDiagImageVerdict(noChat); !strings.Contains(got, "generasi") {
		t.Fatalf("tanpa gambar: %q", got)
	}

	denied := geminiapi.ImageProbe{
		Images: 1,
		Media: []geminiapi.MediaReport{{
			Host: "lh3.googleusercontent.com",
			Attempts: []geminiapi.MediaAttempt{
				{Name: "sesi", Status: 403},
				{Name: "publik", Status: 403},
				{Name: "sesi-tanpa-ukuran", Status: 403},
			},
		}},
	}
	if got := geminiDiagImageVerdict(denied); !strings.Contains(got, "403") || !strings.Contains(got, "incognito") {
		t.Fatalf("403: %q", got)
	}

	ok := geminiapi.ImageProbe{
		Images: 1,
		Media:  []geminiapi.MediaReport{{Data: []byte("x"), Used: "sesi"}},
	}
	if got := geminiDiagImageVerdict(ok); !strings.Contains(got, "sehat") {
		t.Fatalf("sukses: %q", got)
	}

	if got := geminiDiagImageVerdict(geminiapi.ImageProbe{ChatErr: geminiapi.ErrCookieRejected}); !strings.Contains(got, "Login") {
		t.Fatalf("cookie: %q", got)
	}
}

// TestGeminiMediaAttemptLine memastikan baris percobaan unduh tidak
// menampilkan URL dan menyertakan status/tipe saat gagal.
func TestGeminiMediaAttemptLine(t *testing.T) {
	if got := geminiMediaAttemptLine(geminiapi.MediaAttempt{Name: "sesi", Status: 403, ContentType: "text/html"}); !strings.Contains(got, "403") || !strings.Contains(got, "text/html") {
		t.Fatalf("gagal: %q", got)
	}
	if got := geminiMediaAttemptLine(geminiapi.MediaAttempt{Name: "sesi", Status: 200, ContentType: "text/html"}); !strings.Contains(got, "bukan gambar") {
		t.Fatalf("html 200: %q", got)
	}
	if got := geminiMediaAttemptLine(geminiapi.MediaAttempt{Name: "sesi", OK: true}); got != "ok" {
		t.Fatalf("sukses: %q", got)
	}
}

// TestGeminiCleanAnswer memastikan jawaban dirapikan seperti balasan flora
// agent: bold Markdown jadi bold WhatsApp, heading/pemisah dibuang, dan
// karakter tak terlihat hilang.
func TestGeminiCleanAnswer(t *testing.T) {
	in := "### Judul\n\n**tebal** dan \u200bteks\n---\n[cite: 1] akhir  "
	out := geminiCleanAnswer(in)
	if strings.Contains(out, "**") || strings.Contains(out, "###") || strings.Contains(out, "---") {
		t.Fatalf("simbol Markdown tersisa: %q", out)
	}
	if !strings.Contains(out, "*tebal*") {
		t.Fatalf("bold WhatsApp tidak terbentuk: %q", out)
	}
	if strings.Contains(out, "\u200b") {
		t.Fatalf("zero-width tersisa: %q", out)
	}
	if !strings.Contains(out, "*Judul*") {
		t.Fatalf("heading tidak jadi bold: %q", out)
	}
}
