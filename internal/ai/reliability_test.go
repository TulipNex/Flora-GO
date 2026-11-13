package ai

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// scriptedProvider adalah ProviderAdapter palsu untuk menguji kebijakan
// retry tanpa jaringan: errs[i] adalah error pada panggilan ke-i (nil =
// sukses dengan Text).
type scriptedProvider struct {
	errs  []error
	text  string
	delay time.Duration
	calls int
}

func (p *scriptedProvider) Generate(ctx context.Context, _ GenerateParams) (*AgentResponse, error) {
	call := p.calls
	p.calls++
	if p.delay > 0 {
		select {
		case <-time.After(p.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if call < len(p.errs) && p.errs[call] != nil {
		return nil, p.errs[call]
	}
	return &AgentResponse{Text: p.text}, nil
}

// withFastRetry memendekkan backoff + anggaran giliran selama satu tes.
func withFastRetry(t *testing.T, budget time.Duration) {
	t.Helper()
	oldBackoff, oldBudget := retryBackoffBase, turnBudget
	retryBackoffBase = time.Millisecond
	turnBudget = budget
	t.Cleanup(func() {
		retryBackoffBase = oldBackoff
		turnBudget = oldBudget
	})
}

func newTestAgentContext() *AgentContext {
	return NewAgentContext(context.Background(), "628@s.whatsapp.net", "628@s.whatsapp.net", false)
}

// TestRetryTimeoutOnlyOnce: timeout hanya diulang SEKALI walau MaxRetries=3.
func TestRetryTimeoutOnlyOnce(t *testing.T) {
	withFastRetry(t, 10*time.Second)
	timeoutErr := errors.New("context deadline exceeded (Client.Timeout exceeded while awaiting headers)")
	p := &scriptedProvider{errs: []error{timeoutErr, timeoutErr, timeoutErr}}
	if _, err := RunAgentLoopWithRetry(newTestAgentContext(), p, nil, "sys"); err == nil {
		t.Fatal("harus error setelah timeout berulang")
	}
	if p.calls != 2 {
		t.Fatalf("jumlah panggilan = %d, mau 2 (1 percobaan + 1 ulang)", p.calls)
	}
}

// TestRetryServerErrorUpToMax: error 5xx tetap dicoba sampai MaxRetries.
func TestRetryServerErrorUpToMax(t *testing.T) {
	withFastRetry(t, 10*time.Second)
	err500 := errors.New("status HTTP 500, body: boom")
	p := &scriptedProvider{errs: []error{err500, err500, nil}, text: "halo"}
	text, err := RunAgentLoopWithRetry(newTestAgentContext(), p, nil, "sys")
	if err != nil {
		t.Fatalf("harus sukses di percobaan ketiga: %v", err)
	}
	if text != "halo" || p.calls != 3 {
		t.Fatalf("text=%q calls=%d, mau halo/3", text, p.calls)
	}
}

// TestRetryPermanentErrorStops: error permanen tidak diulang sama sekali.
func TestRetryPermanentErrorStops(t *testing.T) {
	withFastRetry(t, 10*time.Second)
	p := &scriptedProvider{errs: []error{errors.New("API key kosong")}}
	if _, err := RunAgentLoopWithRetry(newTestAgentContext(), p, nil, "sys"); err == nil {
		t.Fatal("harus error")
	}
	if p.calls != 1 {
		t.Fatalf("jumlah panggilan = %d, mau 1", p.calls)
	}
}

// TestTurnErrorCollectsAllAttempts: semua percobaan gagal harus tersimpan di
// TurnError — bukan hanya error terakhir — supaya laporan ke owner memuat
// riwayat 500/timeout yang sebenarnya terjadi.
func TestTurnErrorCollectsAllAttempts(t *testing.T) {
	withFastRetry(t, 10*time.Second)
	err500 := errors.New("tes/m: status HTTP 500 setelah 1s: boom")
	p := &scriptedProvider{errs: []error{err500, err500, err500}}
	_, err := RunAgentLoopWithRetry(newTestAgentContext(), p, nil, "sys")
	if err == nil {
		t.Fatal("harus error setelah 3 percobaan")
	}
	var turnErr *TurnError
	if !errors.As(err, &turnErr) {
		t.Fatalf("error harus *TurnError, dapat %T: %v", err, err)
	}
	if len(turnErr.Attempts) != 3 {
		t.Fatalf("jumlah percobaan tercatat = %d, mau 3", len(turnErr.Attempts))
	}
	for i, a := range turnErr.Attempts {
		if a.Attempt != i+1 {
			t.Fatalf("percobaan ke-%d bernomor %d", i, a.Attempt)
		}
		if a.Err == nil {
			t.Fatalf("percobaan %d tanpa error", a.Attempt)
		}
		if !a.Transient {
			t.Fatalf("percobaan %d (500) harus transient", a.Attempt)
		}
	}
	if unwrapped := errors.Unwrap(turnErr); unwrapped == nil {
		t.Fatal("Unwrap harus meneruskan error akhir")
	}
	if !errors.Is(err, err500) {
		t.Fatal("errors.Is harus menembus TurnError ke error penyebab")
	}
}

// TestTurnErrorPermanentSingleAttempt: error permanen berhenti di percobaan
// pertama, dan TurnError tetap mencatat satu percobaan itu.
func TestTurnErrorPermanentSingleAttempt(t *testing.T) {
	withFastRetry(t, 10*time.Second)
	perm := errors.New("API key kosong: CIORA_API_KEY")
	p := &scriptedProvider{errs: []error{perm}}
	_, err := RunAgentLoopWithRetry(newTestAgentContext(), p, nil, "sys")
	var turnErr *TurnError
	if !errors.As(err, &turnErr) {
		t.Fatalf("error harus *TurnError, dapat %T: %v", err, err)
	}
	if len(turnErr.Attempts) != 1 {
		t.Fatalf("jumlah percobaan tercatat = %d, mau 1", len(turnErr.Attempts))
	}
	if turnErr.Attempts[0].Transient {
		t.Fatal("error permanen tidak boleh ditandai transient")
	}
	if p.calls != 1 {
		t.Fatalf("provider dipanggil %d kali, mau 1", p.calls)
	}
}

// TestFormatOwnerErrorReport: laporan wajib memuat provider, model, SEMUA
// percobaan, dan hasil akhir — serta tidak membocorkan nilai API key (hanya
// nama env yang muncul dari pesan error).
func TestFormatOwnerErrorReport(t *testing.T) {
	attempts := []AttemptFailure{
		{Attempt: 1, Transient: true, Elapsed: 1500 * time.Millisecond, Err: errors.New("tes/m: status HTTP 500 setelah 1.5s (payload 12 KB): boom")},
		{Attempt: 2, Transient: true, Elapsed: 300 * time.Millisecond, Err: errors.New("tes/m: status HTTP 502 setelah 0.3s: bad gateway")},
		{Attempt: 3, Transient: false, Elapsed: 50 * time.Millisecond, Err: errors.New("API key kosong: CIORA_API_KEY")},
	}
	err := &TurnError{
		Attempts: attempts,
		Err:      fmt.Errorf("gagal setelah beberapa percobaan: %w", attempts[2].Err),
	}
	report := FormatOwnerErrorReport("628111@s.whatsapp.net", "628222@g.us", "ciora", "ciora/gemini-3.8-flash", 2*time.Minute, err)

	for _, want := range []string{
		"[AI ERROR]",
		"Provider: ciora",
		"Model: ciora/gemini-3.8-flash",
		"Percobaan: 3",
		"1. transient",
		"2. transient",
		"3. permanen",
		"status HTTP 500",
		"status HTTP 502",
		"CIORA_API_KEY",
		"Hasil akhir: gagal setelah beberapa percobaan",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("laporan tidak memuat %q:\n%s", want, report)
		}
	}
	if strings.Contains(report, "sk-") {
		t.Fatalf("laporan tidak boleh memuat nilai API key:\n%s", report)
	}
	if len([]rune(report)) > maxOwnerReportChars+1 {
		t.Fatalf("laporan terlalu panjang: %d rune", len([]rune(report)))
	}
}

// TestFormatOwnerErrorReportConfigError: kegagalan sebelum retry loop (API key
// kosong, model kosong) tetap dilaporkan, ditandai 0 percobaan.
func TestFormatOwnerErrorReportConfigError(t *testing.T) {
	report := FormatOwnerErrorReport("628@s.whatsapp.net", "628@g.us", "", "", 0, errors.New("API key kosong: CIORA_API_KEY"))
	if !strings.Contains(report, "Percobaan: 0 (gagal sebelum request dikirim)") {
		t.Fatalf("laporan harus menandai 0 percobaan:\n%s", report)
	}
	if !strings.Contains(report, "Provider: -") || !strings.Contains(report, "Model: -") {
		t.Fatalf("provider/model kosong harus tampil sebagai '-':\n%s", report)
	}
	if !strings.Contains(report, "API key kosong") {
		t.Fatalf("pesan error konfigurasi harus ikut:\n%s", report)
	}
}

// TestTruncateOwnerErrOneLine memastikan cuplikan body multi-baris tidak
// merusak struktur laporan (nomor percobaan) dan dipotong di batas rune.
func TestTruncateOwnerErrOneLine(t *testing.T) {
	long := strings.Repeat("é", maxOwnerAttemptChars+50)
	got := truncateOwnerErr(errors.New("baris satu\nbaris dua\n\n" + long))
	if strings.ContainsAny(got, "\n\r") {
		t.Fatalf("error harus jadi satu baris: %q", got)
	}
	if !strings.Contains(got, "baris satu baris dua") {
		t.Fatalf("baris baru harus jadi spasi: %q", got)
	}
	if r := []rune(got); len(r) != maxOwnerAttemptChars+1 {
		t.Fatalf("panjang hasil = %d rune, mau %d (+ elipsis)", len(r), maxOwnerAttemptChars+1)
	}
}

// TestTurnBudgetStopsLoop: permintaan yang melewati anggaran giliran tidak
// diulang dan tidak menggantung.
func TestTurnBudgetStopsLoop(t *testing.T) {
	withFastRetry(t, 30*time.Millisecond)
	p := &scriptedProvider{delay: 500 * time.Millisecond, text: "telat"}
	start := time.Now()
	if _, err := RunAgentLoopWithRetry(newTestAgentContext(), p, nil, "sys"); err == nil {
		t.Fatal("harus error karena anggaran giliran habis")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("berhenti terlalu lama: %v", elapsed)
	}
	if p.calls != 1 {
		t.Fatalf("jumlah panggilan = %d, mau 1", p.calls)
	}
}
