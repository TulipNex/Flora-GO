package plugins

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Potongan Google TTS wajib ≤ batas, tidak kosong, dan tidak membelah rune
// (kalau terbelah, karakter multi-byte jadi "�" yang ikut dibacakan).
func TestGoogleTTSChunks(t *testing.T) {
	text := strings.Repeat("Flora membantu owner mengurus grup dan dokumen. ", 20)
	chunks := googleTTSChunks(text, googleTTSMaxRunes)
	if len(chunks) == 0 {
		t.Fatal("tidak ada potongan")
	}
	for i, c := range chunks {
		if c == "" {
			t.Fatalf("potongan %d kosong", i)
		}
		if !utf8.ValidString(c) {
			t.Fatalf("potongan %d bukan UTF-8 valid: %q", i, c)
		}
		if n := utf8.RuneCountInString(c); n > googleTTSMaxRunes {
			t.Fatalf("potongan %d kepanjangan: %d rune (maks %d)", i, n, googleTTSMaxRunes)
		}
	}
	// Tidak ada teks yang hilang: jumlah kata harus sama.
	if got, want := len(strings.Fields(strings.Join(chunks, " "))), len(strings.Fields(text)); got != want {
		t.Fatalf("jumlah kata berubah: %d, ingin %d", got, want)
	}
}

// Kata tunggal yang lebih panjang dari batas dipotong paksa, tetap valid
// UTF-8 dan tidak melebihi batas.
func TestGoogleTTSChunksLongWord(t *testing.T) {
	word := strings.Repeat("あ", 400)
	chunks := googleTTSChunks(word, googleTTSMaxRunes)
	if len(chunks) != 3 {
		t.Fatalf("potongan = %d, ingin 3", len(chunks))
	}
	for i, c := range chunks {
		if !utf8.ValidString(c) {
			t.Fatalf("potongan %d bukan UTF-8 valid", i)
		}
		if n := utf8.RuneCountInString(c); n > googleTTSMaxRunes {
			t.Fatalf("potongan %d = %d rune (maks %d)", i, n, googleTTSMaxRunes)
		}
	}
	if got := strings.Join(chunks, ""); got != word {
		t.Fatal("isi kata berubah setelah dipotong")
	}
}

func TestGoogleTTSChunksEmpty(t *testing.T) {
	if got := googleTTSChunks("   \n\t ", googleTTSMaxRunes); len(got) != 0 {
		t.Fatalf("teks kosong menghasilkan %d potongan", len(got))
	}
}

func TestGoogleLangFromVoice(t *testing.T) {
	cases := map[string]string{
		"id-ID-GadisNeural": "id",
		"id-ID-ArdiNeural":  "id",
		"en-US-EmmaNeural":  "en",
		"ID":                "id",
		"":                  "id",
		"aneh":              "id",
	}
	for voice, want := range cases {
		if got := googleLangFromVoice(voice); got != want {
			t.Errorf("googleLangFromVoice(%q) = %q, ingin %q", voice, got, want)
		}
	}
}

func TestGoogleTTSRequestURL(t *testing.T) {
	got := googleTTSRequestURL("halo apa kabar", "id")
	for _, want := range []string{"tl=id", "client=tw-ob", "ie=UTF-8", "q=halo+apa+kabar"} {
		if !strings.Contains(got, want) {
			t.Fatalf("URL %q tidak memuat %q", got, want)
		}
	}
	if !strings.HasPrefix(got, googleTTSReadURL+"?") {
		t.Fatalf("URL tidak memakai endpoint Google: %q", got)
	}
}

// Balasan HTML (halaman blokir/error) tidak boleh dianggap audio.
func TestGoogleAudioLooksMP3(t *testing.T) {
	id3 := append([]byte("ID3"), make([]byte, 400)...)
	frame := append([]byte{0xFF, 0xFB}, make([]byte, 400)...)
	html := append([]byte("<html><body>sorry</body></html>"), make([]byte, 400)...)
	if !googleAudioLooksMP3(id3) || !googleAudioLooksMP3(frame) {
		t.Error("MP3 valid ditolak")
	}
	if googleAudioLooksMP3(html) {
		t.Error("HTML diterima sebagai MP3")
	}
	if googleAudioLooksMP3(frame[:100]) {
		t.Error("potongan terlalu pendek diterima sebagai MP3")
	}
}

// Fallback harus bisa dimatikan lewat env (dipakai saat owner tidak mau
// request ke Google sama sekali).
func TestTTSGoogleFallbackEnabled(t *testing.T) {
	t.Setenv("TTS_GOOGLE_FALLBACK", "")
	if !ttsGoogleFallbackEnabled() {
		t.Fatal("default harus aktif")
	}
	for _, off := range []string{"0", "off", "false", "no"} {
		t.Setenv("TTS_GOOGLE_FALLBACK", off)
		if ttsGoogleFallbackEnabled() {
			t.Fatalf("%q seharusnya mematikan fallback", off)
		}
	}
}
