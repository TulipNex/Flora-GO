package geminiapi

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// Smoke test live ke Gemini mode tamu. Tidak jalan pada `go test ./...` biasa
// karena memakai kuota (dan jaringan) sungguhan. Jalankan manual:
//
//	GEMINI_SMOKE=1 go test ./internal/geminiapi/ -run TestSmoke -v   (satu tanya)
//	GEMINI_SMOKE=2 go test ./internal/geminiapi/ -run TestSmoke -v   (dua giliran + injeksi riwayat)
//	GEMINI_SMOKE=3 go test ./internal/geminiapi/ -run TestSmoke -v   (diag: cookie → meta → chat)
//	GEMINI_SMOKE=4 go test ./internal/geminiapi/ -run TestSmoke -v   (dua giliran lewat tiket percakapan)
//	GEMINI_SMOKE=5 go test ./internal/geminiapi/ -run TestSmoke -v   (tiket palsu ditolak tanpa bakar kuota)
//	GEMINI_SMOKE=6 go test ./internal/geminiapi/ -run TestSmoke -v   (tiga giliran: tiket hasil giliran lanjutan)
func TestSmoke(t *testing.T) {
	level := strings.TrimSpace(os.Getenv("GEMINI_SMOKE"))
	if level == "" || level == "0" {
		t.Skip("set GEMINI_SMOKE=1..6 untuk uji live")
	}
	client := New()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	switch level {
	case "1":
		res, err := client.Ask(ctx, "Sebutkan tiga warna dalam satu baris.")
		if err != nil {
			t.Fatalf("ask: %v", err)
		}
		t.Logf("jawaban: %s", res.Text)
		if strings.TrimSpace(res.Text) == "" {
			t.Fatal("jawaban kosong")
		}

	case "2":
		// Jalur cadangan: bila server menolak tiket, riwayat dibawa di dalam
		// prompt — persis seperti plugins/gemini.go.
		first, err := client.Ask(ctx, "Nama saya Raka. Jawab singkat.")
		if err != nil {
			t.Fatalf("ask pertama: %v", err)
		}
		t.Logf("giliran 1: %s", first.Text)

		prompt := "Percakapan sebelumnya:\n" +
			"User: Nama saya Raka. Jawab singkat.\n" +
			"Gemini: " + first.Text + "\n\n" +
			"User: Siapa nama saya? Jawab satu kata."
		second, err := client.Ask(ctx, prompt)
		if err != nil {
			t.Fatalf("ask kedua: %v", err)
		}
		t.Logf("giliran 2: %s", second.Text)
		if !strings.Contains(strings.ToLower(second.Text), "raka") {
			t.Fatalf("riwayat tidak terbaca: %q", second.Text)
		}

	case "3":
		d := client.Diag(ctx)
		t.Logf("cookie: ok=%v %s", d.Cookie.OK, d.Cookie.Detail)
		t.Logf("meta: ok=%v %s", d.Meta.OK, d.Meta.Detail)
		t.Logf("chat: ok=%v %s (%s)", d.Chat.OK, d.Chat.Detail, d.Latency.Round(time.Millisecond))
		if d.Answer != "" {
			t.Logf("jawaban: %s", d.Answer)
		}
		if d.Dump != "" {
			t.Logf("dump: %s", d.Dump)
		}
		if !d.Cookie.OK || !d.Meta.OK || !d.Chat.OK {
			t.Fatal("diag gagal")
		}

	case "4":
		// Dua giliran lewat tiket percakapan: giliran kedua TIDAK membawa
		// riwayat, jadi jawaban benar berarti server benar-benar mengingat.
		first, err := client.Ask(ctx, "Nama saya Raka. Jawab singkat.")
		if err != nil {
			t.Fatalf("ask pertama: %v", err)
		}
		ticket := first.Resume()
		t.Logf("giliran 1: %s", first.Text)
		t.Logf("tiket: %+v", ticket)
		if ticket.Empty() {
			t.Skip("server tidak mengembalikan tiket percakapan (pakai GEMINI_SMOKE=2)")
		}

		second, err := client.AskWith(ctx, "Siapa nama saya? Jawab satu kata.", ticket)
		if err != nil {
			t.Fatalf("ask kedua (tiket): %v", err)
		}
		t.Logf("giliran 2: %s", second.Text)
		if !strings.Contains(strings.ToLower(second.Text), "raka") {
			t.Fatalf("tiket tidak membuat server mengingat percakapan: %q", second.Text)
		}

	case "5":
		// Tiket palsu harus ditolak sebagai ErrStaleConv tanpa percobaan
		// kedua, supaya pemanggil bisa langsung memakai riwayat prompt.
		fake := Resume{
			ConversationID: "c_0000000000000000",
			ResponseID:     "r_0000000000000000",
			ReplyID:        "rc_0000000000000000",
		}
		_, err := client.AskWith(ctx, "Halo.", fake)
		t.Logf("error tiket palsu: %v", err)
		if err == nil {
			t.Skip("server menerima tiket palsu; uji ini tidak berlaku")
		}
		if !errors.Is(err, ErrStaleConv) {
			t.Fatalf("error = %v, mau ErrStaleConv", err)
		}

	case "6":
		// Tiga giliran lewat tiket: giliran ketiga memakai tiket hasil
		// giliran KEDUA (yang lahir dari percakapan lanjutan). Ini yang paling
		// rawan, karena id percakapan pada balasan lanjutan bisa berbeda.
		first, err := client.Ask(ctx, "Nama saya Raka. Jawab singkat.")
		if err != nil {
			t.Fatalf("ask pertama: %v", err)
		}
		ticket := first.Resume()
		t.Logf("giliran 1: %s (tiket %+v)", first.Text, ticket)
		if ticket.Empty() {
			t.Skip("server tidak mengembalikan tiket percakapan (pakai GEMINI_SMOKE=2)")
		}

		second, err := client.AskWith(ctx, "Siapa nama saya? Jawab satu kata.", ticket)
		if err != nil {
			t.Fatalf("ask kedua (tiket): %v", err)
		}
		ticket = second.Resume()
		t.Logf("giliran 2: %s (tiket %+v)", second.Text, ticket)
		if !strings.Contains(strings.ToLower(second.Text), "raka") {
			if smokeRefusal(second.Text) {
				t.Skipf("giliran 2 dijawab penolakan model (bukan tiket ditolak): %q", second.Text)
			}
			t.Fatalf("giliran 2 tidak ingat nama: %q", second.Text)
		}

		third, err := client.AskWith(ctx, "Sebutkan nama saya lagi. Jawab satu kata.", ticket)
		if err != nil {
			t.Fatalf("ask ketiga (tiket lanjutan): %v", err)
		}
		t.Logf("giliran 3: %s (tiket %+v)", third.Text, third.Resume())
		if !strings.Contains(strings.ToLower(third.Text), "raka") {
			// Model tamu kadang membalas penolakan generik walau tiketnya
			// diterima (tanpa error, id percakapan tetap sama). Itu bukan
			// kegagalan tiket, jadi jangan gagalkan uji live karenanya.
			if smokeRefusal(third.Text) {
				t.Skipf("giliran 3 dijawab penolakan model (bukan tiket ditolak): %q", third.Text)
			}
			t.Fatalf("tiket hasil giliran lanjutan tidak berlaku: %q", third.Text)
		}

	default:
		t.Fatalf("GEMINI_SMOKE=%s tidak dikenal (pakai 1..6)", level)
	}
}

// smokeRefusal mendeteksi balasan penolakan generik dari model tamu. Uji live
// hanya menguji tiket, bukan kepatuhan model, jadi balasan seperti ini dianggap
// tidak konklusif.
func smokeRefusal(text string) bool {
	t := strings.ToLower(text)
	for _, marker := range []string{
		"tidak bisa membantu", "tidak dapat membantu", "tidak mampu",
		"cannot help", "can't help", "unable to",
	} {
		if strings.Contains(t, marker) {
			return true
		}
	}
	return false
}
