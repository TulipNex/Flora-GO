package ai

import (
	"context"
	"errors"
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

// AttemptFailure adalah satu percobaan provider yang gagal di dalam satu
// giliran. Disimpan lengkap supaya laporan ke owner memuat SEMUA kegagalan —
// dulu hanya error percobaan terakhir yang dikirim, sehingga owner tidak tahu
// percobaan sebelumnya 500/timeout (dan mengira model yang baru dipilih sehat).
type AttemptFailure struct {
	Attempt   int
	Transient bool
	Elapsed   time.Duration
	Err       error
}

// TurnError adalah kegagalan satu giliran AI beserta jejak semua percobaan.
// Error()/Unwrap() meneruskan error akhir supaya pemanggil yang hanya peduli
// pesan atau errors.Is tetap bekerja seperti sebelumnya.
type TurnError struct {
	Attempts []AttemptFailure
	Err      error
}

func (e *TurnError) Error() string {
	if e == nil || e.Err == nil {
		return "gagal tanpa keterangan"
	}
	return e.Err.Error()
}

func (e *TurnError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// RunAgentLoopWithRetry membungkus RunAgentLoop dengan retry otomatis untuk
// error transient. Kegagalan TIDAK dikirim dari sini: pemanggil (plugins/ai.go)
// yang melaporkan ke owner lewat FormatOwnerErrorReport, supaya satu giliran
// hanya menghasilkan SATU laporan (termasuk error konfigurasi yang terjadi
// sebelum retry loop jalan).
func RunAgentLoopWithRetry(actx *AgentContext, provider ProviderAdapter, history []Content, systemPrompt string) (string, error) {
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
	attempts := make([]AttemptFailure, 0, MaxRetries)

	for attempt := 0; attempt < MaxRetries; attempt++ {
		attemptStart := time.Now()
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
		transient := isTransientError(err)
		attempts = append(attempts, AttemptFailure{
			Attempt:   attempt + 1,
			Transient: transient,
			Elapsed:   time.Since(attemptStart),
			Err:       err,
		})
		LogSide("retry", []AIField{
			{"attempt", fmt.Sprintf("%d", attempt+1)},
			{"transient", fmt.Sprintf("%v", transient)},
		}, err.Error())

		// Anggaran giliran habis: berhenti, jangan mulai percobaan yang
		// pasti langsung dibatalkan context.
		if ctx.Err() != nil {
			break
		}

		if !transient {
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

	return "", &TurnError{
		Attempts: attempts,
		Err:      fmt.Errorf("gagal setelah beberapa percobaan: %w", lastErr),
	}
}

// Batas panjang laporan owner: error adapter bisa memuat cuplikan body 500
// karakter, dan satu giliran bisa punya 3 percobaan — tanpa batas, satu
// laporan bisa jadi novel yang sulit dibaca di WhatsApp.
const (
	maxOwnerAttemptChars = 300
	maxOwnerReportChars  = 4000
)

// FormatOwnerErrorReport menyusun teks laporan error yang dikirim ke owner —
// dipisah jadi fungsi sendiri supaya format pesan konsisten dan gampang
// diubah tanpa menyentuh logic retry.
//
// Sejak Nov 2026 laporan WAJIB memuat provider + model (owner sering berganti
// model untuk mencari yang sehat; tanpa dua baris itu, error tidak bisa
// dipetakan ke model yang sedang diuji) dan SELURUH percobaan, bukan hanya
// error terakhir. Error konfigurasi (API key kosong, model kosong) tidak punya
// percobaan sama sekali — itu dilaporkan sebagai "0 percobaan (gagal sebelum
// request dikirim)".
func FormatOwnerErrorReport(senderJID, chatJID, provider, model string, elapsed time.Duration, err error) string {
	attempts := []AttemptFailure(nil)
	final := err
	var turnErr *TurnError
	if errors.As(err, &turnErr) {
		attempts = turnErr.Attempts
		final = turnErr.Err
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "[AI ERROR]\nWaktu: %s (%s)\nProvider: %s\nModel: %s\nDari: %s\nChat: %s\nDurasi giliran: %s\n",
		timex.Now().Format("2006-01-02 15:04:05"),
		timex.Name(),
		orDash(provider),
		orDash(model),
		senderJID,
		chatJID,
		elapsed.Round(time.Millisecond),
	)
	if len(attempts) == 0 {
		sb.WriteString("Percobaan: 0 (gagal sebelum request dikirim)\n")
	} else {
		fmt.Fprintf(&sb, "Percobaan: %d\n", len(attempts))
		for _, a := range attempts {
			kind := "permanen"
			if a.Transient {
				kind = "transient"
			}
			fmt.Fprintf(&sb, "%d. %s %s: %s\n", a.Attempt, kind, a.Elapsed.Round(time.Millisecond), truncateOwnerErr(a.Err))
		}
	}
	fmt.Fprintf(&sb, "Hasil akhir: %s", truncateOwnerErr(final))
	return truncateOwnerReport(strings.TrimRight(sb.String(), "\n"))
}

// orDash mengganti nilai kosong dengan "-" supaya baris laporan tidak terlihat
// seperti data yang hilang.
func orDash(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "-"
	}
	return s
}

// truncateOwnerErr merapikan satu error jadi satu baris pendek. Baris baru
// diganti spasi supaya struktur laporan (nomor percobaan) tidak rusak oleh
// cuplikan body respons.
func truncateOwnerErr(err error) string {
	if err == nil {
		return "-"
	}
	msg := strings.TrimSpace(err.Error())
	if msg == "" {
		return "-"
	}
	msg = strings.ReplaceAll(msg, "\r", " ")
	msg = strings.ReplaceAll(msg, "\n", " ")
	msg = strings.Join(strings.Fields(msg), " ")
	if r := []rune(msg); len(r) > maxOwnerAttemptChars {
		return string(r[:maxOwnerAttemptChars]) + "…"
	}
	return msg
}

// truncateOwnerReport membatasi panjang total laporan (rune-aware supaya tidak
// memotong karakter multi-byte di tengah).
func truncateOwnerReport(s string) string {
	if r := []rune(s); len(r) > maxOwnerReportChars {
		return string(r[:maxOwnerReportChars]) + "…"
	}
	return s
}
