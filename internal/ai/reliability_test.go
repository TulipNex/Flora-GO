package ai

import (
	"context"
	"errors"
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
	if _, err := RunAgentLoopWithRetry(newTestAgentContext(), p, nil, "sys", nil); err == nil {
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
	text, err := RunAgentLoopWithRetry(newTestAgentContext(), p, nil, "sys", nil)
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
	if _, err := RunAgentLoopWithRetry(newTestAgentContext(), p, nil, "sys", nil); err == nil {
		t.Fatal("harus error")
	}
	if p.calls != 1 {
		t.Fatalf("jumlah panggilan = %d, mau 1", p.calls)
	}
}

// TestTurnBudgetStopsLoop: permintaan yang melewati anggaran giliran tidak
// diulang dan tidak menggantung.
func TestTurnBudgetStopsLoop(t *testing.T) {
	withFastRetry(t, 30*time.Millisecond)
	p := &scriptedProvider{delay: 500 * time.Millisecond, text: "telat"}
	start := time.Now()
	if _, err := RunAgentLoopWithRetry(newTestAgentContext(), p, nil, "sys", nil); err == nil {
		t.Fatal("harus error karena anggaran giliran habis")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("berhenti terlalu lama: %v", elapsed)
	}
	if p.calls != 1 {
		t.Fatalf("jumlah panggilan = %d, mau 1", p.calls)
	}
}
