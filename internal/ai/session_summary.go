package ai

// File ini mengimplementasikan INGATAN HEMAT: sesi yang idle (tak ada pesan
// masuk) diringkas di latar belakang menjadi ≤1000 karakter, disimpan FIFO
// maksimal 4 per kunci sesi, lalu riwayat live dikosongkan. Pesan berikutnya
// disuntik ringkasan sebagai konteks pengganti riwayat utuh.
//
// Definisi "selesai ngobrol" = diam selama IdleTimeout (10 menit, opsi a
// yang disetujui owner): trigger reply/stiker ikut mati, selaras dengan
// penyapuan ini. Aturan anti-bug bawaan:
//   - Gagal ringkas/simpan = hapus NOTHING (riwayat utuh dipertahankan).
//   - Sebelum simpan, di bawah lock yang sama pastikan entry masih ada DAN
//     panjang riwayat tak berubah (anti balapan .agent reset / pesan baru).
//   - Pesan yang masuk membatalkan ringkasan berjalan (riwayat dipakai utuh).
//   - `.agent reset` menghapus ringkasan juga (lupa total).

import (
	"context"
	"fmt"
	"strings"
	"time"

	"wabot-go/internal/logfmt"
	"wabot-go/internal/store"
)

// summarySweepInterval adalah frekuensi penyapu memeriksa sesi idle.
const summarySweepInterval = time.Minute

// summaryMaxTranscriptRunes membatasi transkrip yang dikirim ke peringkas
// (diambil ekor = bagian terbaru bila kepanjangan).
const summaryMaxTranscriptRunes = 8000

// summaryBackstopTurns adalah batas giliran histori yang dikirim ke
// provider (40 giliran ≈ 80 Content user+model). Diterapkan HANYA saat
// merakit kiriman — penyimpanan (memori + SQLite) tetap utuh.
const summaryBackstopTurns = 40

// StartSummarySweeper menjalankan goroutine penyapu sesi idle. Panggil sekali
// di main.go setelah AI terinisialisasi. Berhenti saat ctx dibatalkan.
func StartSummarySweeper(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(summarySweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sweepIdleSessions()
			}
		}
	}()
}

// sweepIdleSessions mencari sesi idle yang riwayatnya belum diringkas lalu
// memicu peringkasan background per sesi (satu goroutine per sesi).
func sweepIdleSessions() {
	GlobalSession.mu.Lock()
	type job struct {
		key     string
		history []Content
		length  int
	}
	var jobs []job
	for key, entry := range GlobalSession.sessions {
		if len(entry.history) == 0 || entry.summarizing {
			continue
		}
		if time.Since(entry.lastActiveAt) <= IdleTimeout {
			continue
		}
		entry.summarizing = true
		jobs = append(jobs, job{key: key, history: append([]Content{}, entry.history...), length: len(entry.history)})
	}
	GlobalSession.mu.Unlock()

	for _, j := range jobs {
		go summarizeSession(j.key, j.history, j.length)
	}
}

// summarizeSession meringkas satu sesi di background lalu (bila sukses dan
// tak ada balapan) menyimpan ringkasan + mengosongkan riwayat live.
func summarizeSession(key string, history []Content, length int) {
	done := func() {
		GlobalSession.mu.Lock()
		if entry, ok := GlobalSession.sessions[key]; ok {
			entry.summarizing = false
		}
		GlobalSession.mu.Unlock()
	}

	ctx := context.Background()
	provider, err := GetActiveProvider(ctx)
	if err != nil {
		logfmt.Warn("ringkas sesi %s dilewati: provider tak tersedia: %v\n", key, err)
		done()
		return
	}
	transcript := FormatTranscript(history)
	if r := []rune(transcript); len(r) > summaryMaxTranscriptRunes {
		transcript = string(r[len(r)-summaryMaxTranscriptRunes:])
	}
	if strings.TrimSpace(transcript) == "" {
		done()
		return
	}
	summary, err := SummarizeTranscript(ctx, provider, transcript)
	if err != nil {
		logfmt.Warn("ringkas sesi %s gagal (riwayat dipertahankan): %v\n", key, err)
		done()
		return
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		done()
		return
	}

	GlobalSession.mu.Lock()
	defer GlobalSession.mu.Unlock()
	entry, ok := GlobalSession.sessions[key]
	if !ok || len(entry.history) != length {
		// Balapan: entry di-reset atau ada pesan baru masuk — buang hasil,
		// riwayat utuh dipakai (pesan menang).
		logfmt.Info("ringkasan sesi %s dibuang (balapan reset/pesan baru).\n", key)
		if e, ok2 := GlobalSession.sessions[key]; ok2 {
			e.summarizing = false
		}
		return
	}
	if err := store.AI().SaveSessionSummary(ctx, key, summary); err != nil {
		logfmt.Warn("simpan ringkasan sesi %s gagal (riwayat dipertahankan): %v\n", key, err)
		entry.summarizing = false
		return
	}
	entry.history = nil
	entry.summarizing = false
	if err := store.AI().ClearHistory(ctx, key); err != nil {
		logfmt.Warn("bersih riwayat sesi %s gagal: %v\n", key, err)
	}
	logfmt.Info("sesi %s diringkas (%d→ringkasan).\n", key, length)
}

// SummarizeTranscript meminta model merumuskan ulang transkrip menjadi
// ringkasan ≤1000 karakter: topik, fakta penting, hal belum selesai.
// Tanpa tool, tanpa persona — murni tugas baca-teks.
func SummarizeTranscript(ctx context.Context, provider ProviderAdapter, transcript string) (string, error) {
	result, err := provider.Generate(ctx, GenerateParams{
		SystemPrompt: "Kamu peringkas percakapan. Rumuskan transkrip berikut menjadi ringkasan " +
			"MAKSIMAL 1000 karakter dalam bahasa Indonesia santai: topik utama, fakta penting " +
			"(nama, angka, keputusan), dan hal yang belum selesai. Jangan tambah info baru, " +
			"jangan bertanya, keluarkan hanya ringkasannya.",
		History: []Content{
			{Role: "user", Parts: []Part{{Text: transcript}}},
		},
	})
	if err != nil {
		return "", err
	}
	if len(result.ToolCalls) > 0 {
		return "", fmt.Errorf("model meminta tool saat meringkas")
	}
	LogSide("summary", []AIField{
		{"transkrip", fmt.Sprintf("%d kar", len([]rune(transcript)))},
		{"hasil", fmt.Sprintf("%d kar", len([]rune(result.Text)))},
	}, result.Text)
	return strings.TrimSpace(result.Text), nil
}

// FormatSummariesForPrompt menyusun blok konteks ringkasan sesi untuk
// disuntik ke system prompt (tertua dulu, bernomor).
func FormatSummariesForPrompt(summaries []string) string {
	if len(summaries) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("Ringkasan sesi-sesi sebelumnya dengan user ini (tertua dulu, konteks saja):\n")
	for i, s := range summaries {
		fmt.Fprintf(&sb, "[Sesi-%d] %s\n", i+1, strings.TrimSpace(s))
	}
	return strings.TrimSpace(sb.String())
}

// CapHistoryForSend memangkas histori ke N giliran terakhir (2 Content per
// giliran) HANYA untuk dikirim ke provider. Penyimpanan tidak disentuh.
func CapHistoryForSend(history []Content, turns int) []Content {
	max := turns * 2
	if len(history) <= max {
		return history
	}
	return history[len(history)-max:]
}
