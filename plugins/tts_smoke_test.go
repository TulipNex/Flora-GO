package plugins

// Smoke test LIVE: butuh jaringan (tidak jalan di tes biasa).
//
//	TTS_SMOKE=1 go test ./plugins/ -run TestTTSSmoke -v
//
// Gunanya menangkap perubahan di sisi server yang tidak terlihat oleh tes
// vektor: Microsoft bisa mengganti skema Sec-MS-GEC (kejadian Okt 2026) atau
// memblokir IP panel, dan Google bisa mengubah endpoint. Tes vektor hanya
// membuktikan algoritma kita benar, bukan bahwa server masih menerimanya.

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

const ttsSmokeText = "Halo, ini uji suara Flora. Satu dua tiga."

func ttsSmokeEnabled(t *testing.T) {
	t.Helper()
	if strings.TrimSpace(os.Getenv("TTS_SMOKE")) == "" {
		t.Skip("set TTS_SMOKE=1 untuk menjalankan tes live")
	}
}

func TestTTSSmoke(t *testing.T) {
	ttsSmokeEnabled(t)

	t.Run("Edge", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		mp3, err := edgeTTSSynthesize(ctx, ttsSmokeText, ttsDefaultVoice)
		if err != nil {
			t.Fatalf("Edge gagal: %v", err)
		}
		if len(mp3) < 1000 {
			t.Fatalf("audio Edge terlalu kecil: %d byte", len(mp3))
		}
		t.Logf("Edge OK: %d byte", len(mp3))
	})

	t.Run("Google", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		mp3, err := googleTTSSynthesize(ctx, ttsSmokeText, ttsDefaultVoice)
		if err != nil {
			t.Fatalf("Google gagal: %v", err)
		}
		if len(mp3) < 1000 {
			t.Fatalf("audio Google terlalu kecil: %d byte", len(mp3))
		}
		t.Logf("Google OK: %d byte", len(mp3))
	})

	t.Run("Chain", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		mp3, err := ttsSynthesizeMP3(ctx, ttsSmokeText, ttsDefaultVoice)
		if err != nil {
			t.Fatalf("rantai TTS gagal: %v", err)
		}
		if len(mp3) < 1000 {
			t.Fatalf("audio terlalu kecil: %d byte", len(mp3))
		}
		t.Logf("Chain OK: %d byte", len(mp3))
	})
}
