package plugins

// Fallback TTS lewat endpoint "Read Aloud" milik Google Translate.
//
// Ini BUKAN pengganti kualitas Edge (suaranya satu, tanpa pilihan karakter,
// dan endpoint tidak resmi), melainkan jaring pengaman supaya `.tts` /
// `.vn` / mode suara Flora tetap berbunyi saat Edge menolak token atau
// memblokir IP panel. Aktif otomatis; matikan dengan TTS_GOOGLE_FALLBACK=0.
//
// Batasan praktis endpoint: satu request hanya melayani ±200 karakter, jadi
// teks dipecah per kata (bukan per byte) dan potongan MP3 digabung mentah —
// frame MP3 independen, pola yang sama dipakai SynthesizeLong untuk Edge.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"wabot-go/internal/logfmt"
)

const (
	// googleTTSReadURL endpoint tw-ob: tanpa API key, bahasa dipilih via tl.
	googleTTSReadURL = "https://translate.google.com/translate_tts"

	// googleTTSMaxRunes batas karakter per request. Endpoint menerima ~200,
	// 180 memberi ruang untuk teks yang mengandung karakter multi-byte.
	googleTTSMaxRunes = 180

	// googleTTSMaxChunks membatasi satu panggilan (±9.000 karakter) supaya
	// teks raksasa tidak jadi puluhan request beruntun ke Google.
	googleTTSMaxChunks = 50

	// googleTTSTimeout batas satu request potongan.
	googleTTSTimeout = 20 * time.Second

	// googleTTSChunkGap jeda antar potongan supaya tidak dianggap flood.
	googleTTSChunkGap = 250 * time.Millisecond
)

var googleTTSClient = &http.Client{Timeout: 30 * time.Second}

// ttsGoogleFallbackEnabled membaca TTS_GOOGLE_FALLBACK (default aktif;
// "0"/"off"/"false"/"no" mematikan).
func ttsGoogleFallbackEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("TTS_GOOGLE_FALLBACK"))) {
	case "0", "off", "false", "no":
		return false
	default:
		return true
	}
}

// googleLangFromVoice mengambil kode bahasa dari nama voice Edge
// ("id-ID-GadisNeural" → "id"). Google memakai kode dua huruf yang sama.
func googleLangFromVoice(voice string) string {
	v := strings.ToLower(strings.TrimSpace(voice))
	if i := strings.IndexByte(v, '-'); i > 0 {
		v = v[:i]
	}
	if len(v) != 2 {
		return "id"
	}
	return v
}

// googleTTSChunks memecah teks jadi potongan ≤ maxRunes karakter pada batas
// kata. Kata yang lebih panjang dari batas dipotong paksa (dihitung per rune
// supaya tidak membelah karakter multi-byte).
func googleTTSChunks(text string, maxRunes int) []string {
	if maxRunes <= 0 {
		return nil
	}
	var out []string
	var cur []rune
	flush := func() {
		if s := strings.TrimSpace(string(cur)); s != "" {
			out = append(out, s)
		}
		cur = cur[:0]
	}
	for _, word := range strings.Fields(text) {
		w := []rune(word)
		for len(w) > maxRunes {
			flush()
			out = append(out, string(w[:maxRunes]))
			w = w[maxRunes:]
		}
		switch {
		case len(w) == 0:
			continue
		case len(cur) == 0:
			cur = append(cur, w...)
		case len(cur)+1+len(w) > maxRunes:
			flush()
			cur = append(cur, w...)
		default:
			cur = append(cur, ' ')
			cur = append(cur, w...)
		}
	}
	flush()
	return out
}

// googleTTSRequestURL merakit URL sintesis satu potongan.
func googleTTSRequestURL(chunk, lang string) string {
	q := url.Values{}
	q.Set("ie", "UTF-8")
	q.Set("client", "tw-ob")
	q.Set("tl", lang)
	q.Set("q", chunk)
	return googleTTSReadURL + "?" + q.Encode()
}

// googleTTSSynthesize mensintesis teks jadi MP3 lewat Google Translate.
func googleTTSSynthesize(ctx context.Context, text, voice string) ([]byte, error) {
	lang := googleLangFromVoice(voice)
	text = strings.TrimSpace(removeIncompatibleChars(text))
	if text == "" {
		return nil, fmt.Errorf("teks kosong")
	}
	chunks := googleTTSChunks(text, googleTTSMaxRunes)
	if len(chunks) == 0 {
		return nil, fmt.Errorf("teks kosong")
	}
	if len(chunks) > googleTTSMaxChunks {
		return nil, fmt.Errorf("teks terlalu panjang untuk TTS Google (%d potongan, maks %d)", len(chunks), googleTTSMaxChunks)
	}

	var out bytes.Buffer
	for i, chunk := range chunks {
		if i > 0 {
			if err := ttsSleep(ctx, googleTTSChunkGap); err != nil {
				return nil, err
			}
		}
		mp3, err := googleTTSSpeakChunk(ctx, chunk, lang)
		if err != nil {
			return nil, fmt.Errorf("potongan %d/%d: %v", i+1, len(chunks), err)
		}
		out.Write(mp3)
	}
	if out.Len() == 0 {
		return nil, fmt.Errorf("hasil sintesis Google kosong")
	}
	return out.Bytes(), nil
}

// googleTTSSpeakChunk mengambil satu potongan MP3. Status 429/5xx dan error
// jaringan diulang sekali; 4xx lain langsung gagal (tidak akan sembuh).
func googleTTSSpeakChunk(ctx context.Context, chunk, lang string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			if err := ttsSleep(ctx, googleTTSChunkGap*2); err != nil {
				return nil, err
			}
		}
		audio, retryable, err := googleTTSFetch(ctx, chunk, lang)
		if err == nil {
			return audio, nil
		}
		lastErr = err
		if !retryable {
			break
		}
		logfmt.Warn("tts: Google TTS percobaan %d gagal (%v), ulangi.\n", attempt+1, err)
	}
	return nil, lastErr
}

// googleTTSFetch melakukan satu request. retryable=false untuk kegagalan yang
// tidak akan berubah kalau diulang.
func googleTTSFetch(ctx context.Context, chunk, lang string) (audio []byte, retryable bool, err error) {
	reqCtx, cancel := context.WithTimeout(ctx, googleTTSTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, googleTTSRequestURL(chunk, lang), nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("User-Agent", ttsUA)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "id-ID,id;q=0.9,en;q=0.8")
	req.Header.Set("Referer", "https://translate.google.com/")

	resp, err := googleTTSClient.Do(req)
	if err != nil {
		return nil, true, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		retryable = resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		return nil, retryable, fmt.Errorf("status HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, true, err
	}
	if !googleAudioLooksMP3(body) {
		return nil, true, fmt.Errorf("balasan bukan audio MP3 (%d byte)", len(body))
	}
	return body, false, nil
}

// googleAudioLooksMP3 memastikan balasan benar-benar MP3: frame diawali tag
// "ID3" atau sync 0xFF 0xEx. Balasan HTML (halaman error/blokir) ditolak di
// sini supaya tidak dikirim ke WhatsApp sebagai voice note rusak.
func googleAudioLooksMP3(b []byte) bool {
	if len(b) < 256 {
		return false
	}
	if bytes.HasPrefix(b, []byte("ID3")) {
		return true
	}
	return b[0] == 0xFF && b[1]&0xE0 == 0xE0
}

// ttsSleep menunggu d dengan tetap menghormati pembatalan ctx.
func ttsSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
