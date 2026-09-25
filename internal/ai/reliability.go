package ai

import (
	"fmt"
	"strings"
	"time"
)

// MaxRetries adalah jumlah percobaan ulang saat provider gagal karena error
// yang sifatnya sementara (timeout, rate limit, server overload) — bukan
// error permanen seperti API key salah atau argumen tidak valid.
const MaxRetries = 3

// isTransientError menebak apakah sebuah error dari provider layak dicoba
// ulang. Ini heuristik sederhana berbasis pesan error (provider Gemini kita
// belum mengembalikan kode error terstruktur ke level ini) — cukup untuk
// membedakan "coba lagi mungkin berhasil" vs "tidak akan pernah berhasil".
func isTransientError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	transientMarkers := []string{
		"timeout",
		"deadline exceeded",
		"connection reset",
		"429", // rate limit
		"500", // internal server error
		"502", // bad gateway
		"503", // service unavailable
		"504", // gateway timeout
		"unavailable",
		"overloaded",
	}
	for _, marker := range transientMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// RunAgentLoopWithRetry membungkus RunAgentLoop dengan retry otomatis untuk
// error transient, dan notifikasi ke owner kalau tetap gagal setelah semua
// percobaan habis (atau error-nya permanen sejak awal, tidak layak diulang).
//
// notifyOwner dipisah sebagai parameter fungsi (bukan dipanggil langsung di
// sini) supaya package internal/ai tidak perlu tahu cara mengirim pesan
// WhatsApp (itu tanggung jawab plugins/ai.go yang punya akses whatsmeow
// client) — konsisten dengan prinsip package ini tetap bebas dependency
// whatsmeow.
func RunAgentLoopWithRetry(actx *AgentContext, provider ProviderAdapter, history []Content, systemPrompt string, notifyOwner func(err error)) (string, error) {
	var lastErr error

	for attempt := 0; attempt < MaxRetries; attempt++ {
		text, err := RunAgentLoop(actx, provider, history, systemPrompt)
		if err == nil {
			return text, nil
		}

		lastErr = err

		if !isTransientError(err) {
			break // error permanen, tidak ada gunanya diulang
		}

		// backoff sederhana: 1s, 2s, 3s — cukup untuk skala pemakaian ini,
		// tidak perlu exponential backoff yang lebih rumit.
		select {
		case <-time.After(time.Duration(attempt+1) * time.Second):
		case <-actx.Context.Done():
			lastErr = actx.Context.Err()
			attempt = MaxRetries // hentikan loop, context sudah dibatalkan
		}
	}

	if notifyOwner != nil {
		notifyOwner(lastErr)
	}
	return "", fmt.Errorf("gagal setelah beberapa percobaan: %w", lastErr)
}

// FormatOwnerErrorReport menyusun teks laporan error yang dikirim ke owner —
// dipisah jadi fungsi sendiri supaya format pesan konsisten dan gampang
// diubah tanpa menyentuh logic retry.
func FormatOwnerErrorReport(senderJID, chatJID string, err error) string {
	return fmt.Sprintf(
		"[AI ERROR]\nWaktu: %s\nDari: %s\nChat: %s\nError: %s",
		time.Now().Format("2006-01-02 15:04:05"),
		senderJID,
		chatJID,
		err.Error(),
	)
}
