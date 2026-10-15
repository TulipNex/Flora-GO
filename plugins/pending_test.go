package plugins

import (
	"testing"
	"time"
)

// TestPendingStore memastikan mekanik pending pick (Set/Peek/Consume + TTL)
// bekerja: nilai kembali dengan waktu simpan, Peek tidak menghapus, Consume
// sekali pakai, dan entri basi dianggap tidak ada.
func TestPendingStore(t *testing.T) {
	s := newPendingStore[string](50 * time.Millisecond)

	if _, _, ok := s.Peek("chat@s.whatsapp.net", "user@s.whatsapp.net"); ok {
		t.Fatal("Peek pada store kosong harus ok=false")
	}

	s.Set("chat@s.whatsapp.net", "user@s.whatsapp.net", "hasil-1")

	v, at, ok := s.Peek("chat@s.whatsapp.net", "user@s.whatsapp.net")
	if !ok || v != "hasil-1" {
		t.Fatalf("Peek = %q,%v; mau hasil-1,true", v, ok)
	}
	if time.Since(at) > time.Second {
		t.Fatalf("waktu simpan tidak wajar: %v", at)
	}

	// Peek tidak menghapus.
	if _, _, ok := s.Peek("chat@s.whatsapp.net", "user@s.whatsapp.net"); !ok {
		t.Fatal("Peek kedua harus tetap ada")
	}

	// Consume sekali pakai.
	if v, ok := s.Consume("chat@s.whatsapp.net", "user@s.whatsapp.net"); !ok || v != "hasil-1" {
		t.Fatalf("Consume = %q,%v; mau hasil-1,true", v, ok)
	}
	if _, ok := s.Consume("chat@s.whatsapp.net", "user@s.whatsapp.net"); ok {
		t.Fatal("Consume kedua harus ok=false (sekali pakai)")
	}

	// Basi = tidak ada.
	s.Set("c@s.whatsapp.net", "u@s.whatsapp.net", "basi")
	time.Sleep(60 * time.Millisecond)
	if _, _, ok := s.Peek("c@s.whatsapp.net", "u@s.whatsapp.net"); ok {
		t.Fatal("entri basi harus ok=false")
	}
	if _, ok := s.Consume("c@s.whatsapp.net", "u@s.whatsapp.net"); ok {
		t.Fatal("Consume entri basi harus ok=false")
	}
}

// TestPendingStoreKeyNormalized memastikan kunci memakai bentuk JID bare
// (ToNonAD) sehingga perbedaan format pengirim tidak menghilangkan pending.
func TestPendingStoreKeyNormalized(t *testing.T) {
	s := newPendingStore[int](time.Minute)
	s.Set("628111:5@s.whatsapp.net", "628222@s.whatsapp.net", 7)

	if v, _, ok := s.Peek("628111@s.whatsapp.net", "628222:9@s.whatsapp.net"); !ok || v != 7 {
		t.Fatalf("kunci ternormalisasi gagal: v=%d ok=%v", v, ok)
	}
}
