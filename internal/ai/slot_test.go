package ai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestProviderSlotQueue: antrean openagentic (limit 1) menahan akuisisi
// kedua, melepas setelah release; provider lain tanpa batas; timeout
// antrean gagal ramah dan non-transient.
func TestProviderSlotQueue(t *testing.T) {
	ctx := context.Background()
	rel1, err := acquireProviderSlot(ctx, "openagentic")
	if err != nil {
		t.Fatalf("akuisisi 1: %v", err)
	}
	acquired := make(chan func(), 1)
	go func() {
		r, err := acquireProviderSlot(ctx, "openagentic")
		if err != nil {
			acquired <- nil
			return
		}
		acquired <- r
	}()
	select {
	case <-acquired:
		t.Fatal("akuisisi 2 harus menunggu selagi slot dipegang")
	case <-time.After(200 * time.Millisecond):
	}
	rel1()
	var rel2 func()
	select {
	case rel2 = <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("akuisisi 2 harus sukses setelah release")
	}
	if rel2 == nil {
		t.Fatal("akuisisi 2 error tak terduga")
	}
	defer rel2()

	rel, err := acquireProviderSlot(ctx, "ciora")
	if err != nil {
		t.Fatalf("provider tanpa batas harus lolos: %v", err)
	}
	rel()

	oldTimeout := providerQueueTimeout
	providerQueueTimeout = 150 * time.Millisecond
	defer func() { providerQueueTimeout = oldTimeout }()

	if _, err := acquireProviderSlot(ctx, "openagentic"); err == nil {
		t.Fatal("antrean habis waktu harus error")
	} else {
		if !strings.Contains(err.Error(), ProviderBusyMarker) {
			t.Fatalf("harus bawa marker sibuk: %v", err)
		}
		if isTransientError(err) {
			t.Fatalf("error antrean tidak boleh transient: %v", err)
		}
	}

	// Pembatalan context saat antre juga gagal ramah dan non-transient.
	c, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := acquireProviderSlot(c, "openagentic"); err == nil {
		t.Fatal("batal saat antre harus error")
	} else if isTransientError(err) {
		t.Fatalf("error batal tidak boleh transient: %v", err)
	}
}

// TestRateLimitWait: header X-RateLimit-Reset (unix) prioritas; fallback
// pola "reset after Ns" di body; tanpa petunjuk = 0.
func TestRateLimitWait(t *testing.T) {
	future := time.Now().Add(9 * time.Second).Unix()
	header := http.Header{}
	header.Set("X-RateLimit-Reset", strconv.FormatInt(future, 10))
	if w := rateLimitWait(header, ""); w < 8*time.Second || w > 12*time.Second {
		t.Fatalf("header harus ~9-10 dtk, dapat %v", w)
	}
	if w := rateLimitWait(http.Header{}, `{"error":{"message":"x (reset after 8s)","type":"capacity_saturated"}}`); w != 9*time.Second {
		t.Fatalf("body regex harus 9 dtk, dapat %v", w)
	}
	if w := rateLimitWait(http.Header{}, `{"error":"biasa"}`); w != 0 {
		t.Fatalf("tanpa petunjuk harus 0, dapat %v", w)
	}
	if w := rateLimitWait(http.Header{}, ""); w != 0 {
		t.Fatalf("body kosong harus 0, dapat %v", w)
	}
}

// TestPostChatTolerantParse: JSON valid berekor sampah ("done", framing
// "data:") tetap dipakai nilai pertamanya; 400 non-JSON + tools =
// toolsRejected dengan body tercatat (tak error).
func TestPostChatTolerantParse(t *testing.T) {
	valid := `{"choices":[{"index":0,"message":{"role":"assistant","content":"halo"},"finish_reason":"stop"}]}`
	for name, body := range map[string]string{
		"bersih":   valid,
		"ekor":     valid + "done",
		"sse":      "data: " + valid,
		"sse-done": valid + "\ndata: [DONE]",
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(body))
		}))
		g := NewGenericOpenAIAdapter("tes", srv.URL, "k", "m")
		parsed, rej, err := g.postChat(context.Background(), GenerateParams{}, nil)
		srv.Close()
		if err != nil || rej {
			t.Fatalf("%s: harus sukses tanpa fallback, err=%v rej=%v", name, err, rej)
		}
		if len(parsed.Choices) != 1 || parsed.Choices[0].Message.Content != "halo" {
			t.Fatalf("%s: isi salah: %+v", name, parsed.Choices)
		}
	}

	srv400 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("kesalahan tools tak dikenal"))
	}))
	defer srv400.Close()
	g := NewGenericOpenAIAdapter("tes", srv400.URL, "k", "m")
	_, rej, err := g.postChat(context.Background(), GenerateParams{}, []openAITool{{Type: "function"}})
	if err != nil || !rej {
		t.Fatalf("400 non-JSON + tools harus toolsRejected, err=%v rej=%v", err, rej)
	}
}
