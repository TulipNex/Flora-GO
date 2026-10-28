package geminiapi

import (
	"io"
	"testing"
	"time"
)

// TestParseStreamStopsAtEndMarker memastikan pembacaan berhenti di penanda
// tutup stream walau koneksi TIDAK ditutup server. Inilah penyebab jawaban
// terasa lambat ±20 dtk di panel: dulu parseStream menunggu EOF.
func TestParseStreamStopsAtEndMarker(t *testing.T) {
	line := buildStreamLine(t, streamInner([]string{"Halo"}, false))
	pr, pw := io.Pipe()
	go func() {
		// sengaja tidak Close: meniru server yang menahan koneksi
		_, _ = pw.Write([]byte(line + "\n" + `[["e",4,null,null,131]]` + "\n"))
	}()

	type outcome struct {
		res *Result
		err error
	}
	ch := make(chan outcome, 1)
	go func() {
		res, err := parseStreamIdle(pr, 2*time.Second)
		ch <- outcome{res, err}
	}()

	select {
	case got := <-ch:
		if got.err != nil {
			t.Fatalf("parse: %v", got.err)
		}
		if got.res.Text != "Halo" {
			t.Fatalf("teks = %q, mau %q", got.res.Text, "Halo")
		}
	case <-time.After(3 * time.Second):
		_ = pr.Close()
		t.Fatal("parseStream masih menunggu EOF setelah penanda tutup")
	}
	_ = pr.Close()
}

// TestParseStreamIdleStop memastikan stream yang diam setelah jawaban
// dianggap selesai (tanpa penanda tutup, tanpa EOF).
func TestParseStreamIdleStop(t *testing.T) {
	line := buildStreamLine(t, streamInner([]string{"Halo dunia"}, false))
	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write([]byte(line + "\n"))
	}()

	start := time.Now()
	res, err := parseStreamIdle(pr, 150*time.Millisecond)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res.Text != "Halo dunia" {
		t.Fatalf("teks = %q", res.Text)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("berhenti terlalu lambat: %v", elapsed)
	}
	_ = pr.Close()
}

// TestParseStreamWaitsForFirstText memastikan jeda panjang SEBELUM jawaban
// pertama tidak dipotong (server bisa "berpikir" puluhan detik).
func TestParseStreamWaitsForFirstText(t *testing.T) {
	line := buildStreamLine(t, streamInner([]string{"Terlambat"}, false))
	pr, pw := io.Pipe()
	go func() {
		time.Sleep(400 * time.Millisecond) // > idle 150ms
		_, _ = pw.Write([]byte(line + "\n"))
		_ = pw.Close()
	}()

	res, err := parseStreamIdle(pr, 150*time.Millisecond)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res.Text != "Terlambat" {
		t.Fatalf("teks = %q, mau %q", res.Text, "Terlambat")
	}
}

// TestParseStreamEndMarkerWithoutAnswer memastikan penanda tutup tanpa teks
// tidak membuat bot menunggu koneksi ditutup (balasan kosong harus gagal
// cepat, bukan menggantung sampai timeout).
func TestParseStreamEndMarkerWithoutAnswer(t *testing.T) {
	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write([]byte(`[["e",4,null,null,131]]` + "\n"))
	}()

	start := time.Now()
	res, err := parseStreamIdle(pr, 150*time.Millisecond)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res.Text != "" || len(res.Images) != 0 {
		t.Fatalf("harus kosong, dapat %q / %d gambar", res.Text, len(res.Images))
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("berhenti terlalu lambat: %v", elapsed)
	}
	_ = pr.Close()
}

// TestIsStreamEnd memastikan penanda tutup dikenali dan teks biasa tidak
// salah dianggap penanda.
func TestIsStreamEnd(t *testing.T) {
	if !isStreamEnd(`[["e",4,null,null,131]]`) {
		t.Fatal("penanda tutup tidak dikenali")
	}
	line := buildStreamLine(t, streamInner([]string{"huruf e di teks"}, false))
	if isStreamEnd(line) {
		t.Fatal("baris jawaban salah dianggap penanda tutup")
	}
	if isStreamEnd(`["e"]`) {
		t.Fatal("array bersarang yang tidak berbentuk penanda tidak boleh cocok")
	}
}

// TestCannedRefusalDetection memastikan kalimat penolakan generik dikenali
// (dipakai untuk menyimpan payload diagnostik), tanpa menandai jawaban biasa.
func TestCannedRefusalDetection(t *testing.T) {
	refusals := []string{
		"Saya tidak bisa membantu Anda karena saya hanya model bahasa dan tidak mampu memahami hal tersebut.",
		"Sebagai model bahasa, saya tidak dirancang untuk membantu soal itu.",
	}
	for _, s := range refusals {
		if !isCannedRefusal(s) {
			t.Fatalf("tidak dikenali: %q", s)
		}
	}
	if isCannedRefusal("Sejarah internet dimulai pada 1969.") {
		t.Fatal("jawaban biasa salah dianggap penolakan")
	}
}
