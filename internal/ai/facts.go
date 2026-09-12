package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// factExtractionPrompt menginstruksikan model untuk membaca histori
// percakapan (plus fakta lama kalau ada) dan mengembalikan HANYA JSON
// object flat berisi fakta tentang USER (bukan tentang bot), tanpa
// pembungkus markdown/teks lain, supaya bisa langsung di-unmarshal.
//
// Fakta lama disertakan sebagai konteks supaya model bisa MENGGABUNGKAN
// (update field yang berubah, pertahankan field yang tidak dibahas lagi)
// alih-alih menimpa total tiap kali cuma dari histori terbaru saja.
const factExtractionPrompt = `Kamu adalah modul ekstraksi fakta. Tugasmu HANYA membaca potongan percakapan berikut dan mengeluarkan fakta-fakta tentang USER (lawan bicara), BUKAN tentang asisten/bot.

Aturan:
- Balas HANYA dengan JSON object flat (key-value string), tanpa markdown, tanpa penjelasan, tanpa teks lain apapun.
- Key bebas dalam bahasa Indonesia singkat (mis. "nama", "pekerjaan", "kota", "hobi", "preferensi").
- Gabungkan dengan fakta lama yang diberikan: pertahankan fakta lama yang masih relevan dan tidak dibantah, perbarui yang berubah, tambahkan yang baru ditemukan. Jangan hapus fakta lama hanya karena tidak disebut lagi di percakapan ini.
- Kalau memang tidak ada fakta baru maupun lama, balas dengan "{}" (object kosong).
- Jangan mengarang. Hanya catat yang benar-benar disebutkan/tersirat jelas di percakapan.

Fakta lama (JSON, mungkin kosong):
%s

Percakapan terbaru:
%s

Balas HANYA JSON:`

// BuildFactExtractionPrompt merangkai prompt final dari fakta lama
// (boleh string kosong) dan transkrip percakapan yang sudah diformat.
func BuildFactExtractionPrompt(oldFactsJSON, transcript string) string {
	if strings.TrimSpace(oldFactsJSON) == "" {
		oldFactsJSON = "{}"
	}
	return fmt.Sprintf(factExtractionPrompt, oldFactsJSON, transcript)
}

// FormatTranscript merangkai potongan histori (Content) jadi teks
// "role: isi" per baris, format sederhana yang cukup bagi model untuk
// memahami konteks tanpa perlu mengirim struktur Content asli.
func FormatTranscript(history []Content) string {
	var sb strings.Builder
	for _, c := range history {
		text := ""
		for _, p := range c.Parts {
			text += p.Text
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		sb.WriteString(c.Role)
		sb.WriteString(": ")
		sb.WriteString(text)
		sb.WriteString("\n")
	}
	return sb.String()
}

// ExtractFacts memanggil provider (Gemini) SEKALI dengan prompt ekstraksi
// murni (tanpa tool, tanpa system prompt persona bot) untuk menghasilkan
// JSON fakta gabungan. Dipanggil lewat goroutine terpisah oleh
// TriggerFactExtraction — fungsi ini sendiri sinkron & tidak tahu-menahu
// soal fire-and-forget.
//
// Hasil divalidasi sebagai JSON valid sebelum dikembalikan; kalau model
// mengembalikan sesuatu yang bukan JSON murni (mis. terbungkus ```json),
// dicoba dibersihkan dulu sekali sebelum menyerah.
func ExtractFacts(ctx context.Context, provider ProviderAdapter, oldFactsJSON, transcript string) (string, error) {
	prompt := BuildFactExtractionPrompt(oldFactsJSON, transcript)

	result, err := provider.Generate(ctx, GenerateParams{
		SystemPrompt: "", // prompt ekstraksi sudah lengkap berdiri sendiri, tidak perlu persona bot
		History: []Content{
			{Role: "user", Parts: []Part{{Text: prompt}}},
		},
		// Tools sengaja nil/kosong — ekstraksi fakta tidak pernah butuh
		// tool-calling, ini murni tugas baca-teks-keluarkan-JSON.
	})
	if err != nil {
		return "", fmt.Errorf("gagal panggil provider untuk ekstraksi fakta: %w", err)
	}

	// Karena Tools tidak dikirim, model seharusnya tidak pernah minta tool
	// call. Kalau tetap terjadi (model "nyasar"), anggap gagal daripada
	// mencoba menjalankan agent loop di dalam fact extractor — di luar
	// tanggung jawab fungsi ini.
	if len(result.ToolCalls) > 0 {
		return "", fmt.Errorf("model meminta tool call saat ekstraksi fakta (tidak didukung)")
	}

	raw := strings.TrimSpace(result.Text)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	if raw == "" {
		raw = "{}"
	}

	// Validasi: harus JSON object valid, kalau tidak dianggap gagal
	// (fail-soft ditangani di pemanggil, bukan di sini).
	var check map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &check); err != nil {
		return "", fmt.Errorf("hasil ekstraksi fakta bukan JSON valid: %w (raw: %s)", err, raw)
	}

	return raw, nil
}

// FormatFactsForPrompt mengubah JSON fakta tersimpan jadi baris teks
// singkat yang enak dibaca model saat disuntik ke system prompt utama.
// Fakta kosong/tidak ada mengembalikan string kosong (tidak menyuntik apa-apa).
func FormatFactsForPrompt(factsJSON string) string {
	factsJSON = strings.TrimSpace(factsJSON)
	if factsJSON == "" || factsJSON == "{}" {
		return ""
	}

	var facts map[string]string
	if err := json.Unmarshal([]byte(factsJSON), &facts); err != nil {
		return ""
	}
	if len(facts) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("Fakta yang sudah diketahui tentang user ini:\n")
	for k, v := range facts {
		sb.WriteString(fmt.Sprintf("- %s: %s\n", k, v))
	}
	return sb.String()
}
