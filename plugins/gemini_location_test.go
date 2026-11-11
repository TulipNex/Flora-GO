package plugins

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const geminiSampleMapsAnswer = `Toko roti terdekat yang ditemukan adalah *19x Bagel*, yang berlokasi di Düsseldorfer Straße 1-7, Frankfurt am Main, Germany.

Jaraknya sekitar 219 km dengan waktu tempuh berkendara kurang lebih 2 jam 27 menit.

Anda dapat melihat petunjuk arah lengkap melalui link berikut:
[Petunjuk Arah ke 19x Bagel]([https://www.google.com/maps/dir//19x+Bagel,+D%C3%BCsseldorfer+Stra%C3%9Fe+1-7,+Frankfurt+am+Main,+Germany/data=!4m9!4m8!1m0!1m5!1m1!19sChIJs7rr2GMJvUcRohuyxP0KPts!2m2!1d8.661928!2d50.1086247!3e0?entry](https://www.google.com/maps/dir//19x+Bagel,+D%C3%BCsseldorfer+Stra%C3%9Fe+1-7,+Frankfurt+am+Main,+Germany/data=!4m9!4m8!1m0!1m5!1m1!19sChIJs7rr2GMJvUcRohuyxP0KPts!2m2!1d8.661928!2d50.1086247!3e0?entry))`

// TestGeminiSplitMapsLinksSample mengunci kasus nyata: markdown bersarang dari
// Gemini harus diurai jadi teks + pin lokasi, bukan teks mentah penuh kurung.
func TestGeminiSplitMapsLinksSample(t *testing.T) {
	parts := geminiSplitMapsLinks(geminiSampleMapsAnswer)
	if len(parts) != 2 {
		t.Fatalf("potongan = %d, mau 2 (teks + pin): %+v", len(parts), parts)
	}
	if parts[0].Pin != nil {
		t.Fatalf("potongan pertama harus teks, dapat pin %+v", parts[0].Pin)
	}
	if !strings.Contains(parts[0].Text, "Anda dapat melihat petunjuk arah") {
		t.Fatalf("teks sebelum link hilang: %q", parts[0].Text)
	}
	if strings.Contains(parts[0].Text, "google.com/maps") {
		t.Fatal("link Maps tidak boleh tertinggal di teks")
	}

	pin := parts[1].Pin
	if pin == nil {
		t.Fatal("potongan kedua harus pin lokasi")
	}
	if !pin.HasCoord {
		t.Fatal("koordinat tidak terbaca")
	}
	// Google menaruh bujur di !1d dan lintang di !2d.
	if pin.Lat != 50.1086247 || pin.Lng != 8.661928 {
		t.Fatalf("koordinat = %v,%v, mau 50.1086247,8.661928", pin.Lat, pin.Lng)
	}
	if pin.Name != "19x Bagel" {
		t.Fatalf("nama = %q, mau \"19x Bagel\"", pin.Name)
	}
	if pin.Address != "Düsseldorfer Straße 1-7, Frankfurt am Main, Germany" {
		t.Fatalf("alamat = %q", pin.Address)
	}
}

// TestGeminiSplitMapsLinksOrdering memastikan urutan kirim mengikuti urutan
// asli: teks sebelum link, pin, lalu kalimat setelah link.
func TestGeminiSplitMapsLinksOrdering(t *testing.T) {
	text := "Peta: https://www.google.com/maps/place/Monas/@-6.1754,106.8272,17z dan itu saja."
	parts := geminiSplitMapsLinks(text)
	if len(parts) != 3 {
		t.Fatalf("potongan = %d, mau 3: %+v", len(parts), parts)
	}
	if parts[0].Text != "Peta:" {
		t.Fatalf("teks sebelum = %q", parts[0].Text)
	}
	if parts[1].Pin == nil || parts[1].Pin.Name != "Monas" {
		t.Fatalf("pin tengah = %+v", parts[1].Pin)
	}
	if parts[1].Pin.Lat != -6.1754 || parts[1].Pin.Lng != 106.8272 {
		t.Fatalf("koordinat pin = %v,%v", parts[1].Pin.Lat, parts[1].Pin.Lng)
	}
	if parts[2].Text != "dan itu saja." {
		t.Fatalf("teks sesudah = %q", parts[2].Text)
	}
}

// TestGeminiSplitMapsLinksKeepsOtherLinks: tautan non-Maps tidak boleh diubah.
func TestGeminiSplitMapsLinksKeepsOtherLinks(t *testing.T) {
	text := "Baca [dokumentasi](https://example.com/foo) dulu."
	parts := geminiSplitMapsLinks(text)
	if len(parts) != 1 || parts[0].Pin != nil {
		t.Fatalf("potongan = %+v, mau satu teks tanpa pin", parts)
	}
	if parts[0].Text != text {
		t.Fatalf("teks berubah: %q", parts[0].Text)
	}
}

// TestGeminiSplitMapsLinksDedupesSameLink: link yang sama dua kali hanya satu pin.
func TestGeminiSplitMapsLinksDedupesSameLink(t *testing.T) {
	link := "https://www.google.com/maps/place/Monas/@-6.1754,106.8272,17z"
	parts := geminiSplitMapsLinks("Lihat " + link + " lalu " + link + " selesai.")
	pins := 0
	for _, p := range parts {
		if p.Pin != nil {
			pins++
		}
	}
	if pins != 1 {
		t.Fatalf("pin = %d, mau 1", pins)
	}
}

// TestGeminiMapsPinQueryCoords membaca koordinat dari parameter q.
func TestGeminiMapsPinQueryCoords(t *testing.T) {
	pin := geminiParseMapsPin("https://maps.google.com/?q=-5.1477,119.4322", "")
	if !pin.HasCoord || pin.Lat != -5.1477 || pin.Lng != 119.4322 {
		t.Fatalf("koordinat = %+v", pin)
	}
	if pin.Address == "" {
		t.Fatal("alamat cadangan (koordinat) harus terisi")
	}
}

// TestGeminiMapsPinPlacePath membaca nama dari path /maps/place/.
func TestGeminiMapsPinPlacePath(t *testing.T) {
	pin := geminiParseMapsPin("https://www.google.com/maps/place/Toko+Roti+Enak/@-5.147,119.432,17z", "")
	if pin.Name != "Toko Roti Enak" {
		t.Fatalf("nama = %q", pin.Name)
	}
	if !pin.HasCoord || pin.Lat != -5.147 || pin.Lng != 119.432 {
		t.Fatalf("koordinat = %+v", pin)
	}
}

// TestGeminiMapsPinRejectsBadCoords memastikan nilai di luar rentang tidak
// pernah dikirim sebagai pin (0,0 = "null island").
func TestGeminiMapsPinRejectsBadCoords(t *testing.T) {
	for _, raw := range []string{
		"https://www.google.com/maps/place/X/@0,0,17z",
		"https://www.google.com/maps/place/X/@-91,200,17z",
	} {
		if pin := geminiParseMapsPin(raw, ""); pin.HasCoord {
			t.Fatalf("%s: koordinat tidak valid tapi diterima (%v,%v)", raw, pin.Lat, pin.Lng)
		}
	}
}

// TestGeminiMapsShortURL: hanya short link yang perlu diikuti.
func TestGeminiMapsShortURL(t *testing.T) {
	if !geminiMapsShortURL("https://maps.app.goo.gl/abc123") {
		t.Fatal("maps.app.goo.gl harus dianggap short link")
	}
	if geminiMapsShortURL("https://www.google.com/maps/place/X/@1,2,17z") {
		t.Fatal("URL lengkap tidak perlu diikuti")
	}
}

// TestGeminiResolveMapsURLFollowsRedirect membuktikan short link bisa diubah
// menjadi URL lengkap (berisi koordinat) tanpa jaringan sungguhan.
func TestGeminiResolveMapsURLFollowsRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/short" {
			http.Redirect(w, r, "/maps/place/Toko+Roti+Enak/@-5.147,119.432,17z", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	final, ok := geminiResolveMapsURL(context.Background(), srv.Client(), srv.URL+"/short")
	if !ok {
		t.Fatal("redirect tidak diikuti")
	}
	pin := geminiParseMapsPin(final, "")
	if !pin.HasCoord || pin.Lat != -5.147 || pin.Lng != 119.432 {
		t.Fatalf("hasil resolusi tidak memuat koordinat: %+v", pin)
	}
}

// TestGeminiPinText memastikan link tanpa koordinat tetap terkirim rapi.
func TestGeminiPinText(t *testing.T) {
	pin := geminiMapsPin{URL: "https://maps.app.goo.gl/abc", Label: "Petunjuk Arah ke 19x Bagel"}
	got := geminiPinText(pin)
	want := "Petunjuk Arah ke 19x Bagel: https://maps.app.goo.gl/abc"
	if got != want {
		t.Fatalf("geminiPinText() = %q, mau %q", got, want)
	}
}
