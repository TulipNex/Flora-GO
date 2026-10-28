package ai

import (
	"context"
	"fmt"
	"strings"
	"time"

	"wabot-go/internal/timex"
)

// MaxRetries adalah jumlah percobaan ulang saat provider gagal karena error
// yang sifatnya sementara (timeout, rate limit, server overload) — bukan
// error permanen seperti API key salah atau argumen tidak valid.
const MaxRetries = 3

// maxTimeoutRetries membatasi percobaan ULANG khusus timeout. Timeout berarti
// provider/permintaan benar-benar lambat; mengulangnya 3x hanya membuat user
// menunggu berkali-kali lebih lama dengan peluang berhasil yang sama.
const maxTimeoutRetries = 1

// retryBackoffBase adalah jeda dasar antarpercobaan (1s, 2s, 3s). Var (bukan
// const) supaya tes bisa memendekkannya.
var retryBackoffBase = time.Second

// turnBudget membatasi TOTAL waktu satu giliran (semua putaran tool + retry).
// Tanpa ini, 2 percobaan timeout 150 detik bisa membuat user menunggu 5 menit
// lebih. Var supaya tes bisa memendekkannya.
var turnBudget = 4 * time.Minute

// isTimeoutError menandai error yang berasal dari batas waktu (bukan 5xx /
// rate limit) supaya kebijakan retry-nya bisa lebih ketat.
func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded")
}

// isGatewayTimeoutError menandai Cloudflare 524 / "gateway timeout": origin
// tidak mengirim respons sampai batas waktu gateway. Mengulang request yang
// sama hampir pasti memakan waktu yang sama lamanya (2 menit) lalu gagal lagi,
// jadi lebih baik berhenti dan melaporkan penyebabnya.
func isGatewayTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "524") || strings.Contains(msg, "gateway timeout")
}

// isTransientError menebak apakah sebuah error dari provider layak dicoba
// ulang. Ini heuristik sederhana berbasis pesan error (provider kita
// belum mengembalikan kode error terstruktur ke level ini) — cukup untuk
// membedakan "coba lagi mungkin berhasil" vs "tidak akan pernah berhasil".
func isTransientError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	// Semua 5xx dari adapter dilaporkan sebagai "status HTTP <kode>" (lihat
	// classifyChatResponse) — kenali polanya secara umum supaya kode baru
	// seperti 520/521/522/523/524 tidak lolos diam-diam sebagai permanen.
	if strings.Contains(msg, "status http 5") || strings.Contains(msg, "status http 429") {
		return true
	}
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

	// Satu cache per giliran user, dibagikan ke semua attempt: tool yang
	// sudah dieksekusi di attempt gagal tidak dijalankan ulang di attempt
	// berikutnya (anti aksi WA ganda). Lihat TurnToolCache di loop.go.
	cache := make(TurnToolCache)

	// Anggaran total satu giliran. Context turunan ini juga diteruskan ke
	// tool (AgentContext disalin, bukan dimutasi) supaya seluruh giliran
	// berhenti rapi saat waktunya habis.
	ctx, cancel := context.WithTimeout(actx.Context, turnBudget)
	defer cancel()
	runCtx := *actx
	runCtx.Context = ctx
	timeoutRetries := 0

	for attempt := 0; attempt < MaxRetries; attempt++ {
		text, err := RunAgentLoop(&runCtx, provider, history, systemPrompt, cache)
		if err == nil {
			if attempt > 0 {
				LogSide("retry", []AIField{
					{"attempt", fmt.Sprintf("%d", attempt+1)},
					{"hasil", "berhasil"},
				}, "")
			}
			return text, nil
		}

		lastErr = err
		LogSide("retry", []AIField{
			{"attempt", fmt.Sprintf("%d", attempt+1)},
			{"transient", fmt.Sprintf("%v", isTransientError(err))},
		}, err.Error())

		// Anggaran giliran habis: berhenti, jangan mulai percobaan yang
		// pasti langsung dibatalkan context.
		if ctx.Err() != nil {
			break
		}

		if !isTransientError(err) {
			break // error permanen, tidak ada gunanya diulang
		}

		// 524 (Cloudflare/gateway timeout karena origin tak kunjung menjawab)
		// tidak diulang: percobaan kedua menghabiskan waktu yang sama panjang
		// lalu gagal lagi. Streaming sudah jadi jalur utama; kalau masih 524,
		// penyebabnya perlu dilihat manusia (payload kebesaran / gateway down).
		if isGatewayTimeoutError(err) {
			LogSide("retry", []AIField{
				{"attempt", fmt.Sprintf("%d", attempt+1)},
				{"hasil", "524 gateway timeout — tidak diulang"},
			}, "")
			break
		}

		// Timeout hanya boleh diulang sekali (lihat maxTimeoutRetries).
		if isTimeoutError(err) {
			timeoutRetries++
			if timeoutRetries > maxTimeoutRetries {
				LogSide("retry", []AIField{
					{"attempt", fmt.Sprintf("%d", attempt+1)},
					{"hasil", "timeout — tidak diulang"},
				}, "")
				break
			}
		}

		// backoff sederhana: 1s, 2s, 3s — cukup untuk skala pemakaian ini,
		// tidak perlu exponential backoff yang lebih rumit. Tidur HANYA
		// bila masih ada attempt tersisa (audit T3): tidur setelah attempt
		// terakhir hanya menunda laporan gagal 3 detik tanpa manfaat.
		if attempt < MaxRetries-1 {
			select {
			case <-time.After(time.Duration(attempt+1) * retryBackoffBase):
			case <-ctx.Done():
				lastErr = ctx.Err()
				attempt = MaxRetries // hentikan loop, anggaran giliran habis
			}
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
		"[AI ERROR]\nWaktu: %s (%s)\nDari: %s\nChat: %s\nError: %s",
		timex.Now().Format("2006-01-02 15:04:05"),
		timex.Name(),
		senderJID,
		chatJID,
		err.Error(),
	)
}
