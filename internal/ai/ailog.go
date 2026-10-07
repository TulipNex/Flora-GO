package ai

// File ini adalah logger observabilitas AI: setiap request ke provider dan
// setiap eksekusi tool dicatat UTUH ke console (keputusan owner: pantau
// penuh, jangan ada yang terpotong).
//
// Format blok (selaras blok MESSAGE): HANYA baris header `[jam - JENIS]`
// yang berwarna; detail lain masing-masing di baris `nama : nilai`;
// section isi (`content`, `args`, ...) mentah siap copy; ditutup garis
// pembatas. Contoh:
//   [16:06 - AI_TOOL]
//   name : reminder
//   at   : putaran=1 sesi=...
//   ...
//
// Saklar AI_LOG_FULL: "1"/kosong = penuh (default, sesuai permintaan);
// "0" = kembali ringkas ala compactOneLine (darurat banjir log).
// Gambar (ImageBytes) tidak pernah di-dump — cukup catat ukurannya.
// API key tidak pernah dicatat (hanya di header HTTP, bukan body yang di-log).

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"wabot-go/internal/logfmt"
	"wabot-go/internal/timex"
)

// aiLogFull dibaca sekali saat start dari env.
var aiLogFull = os.Getenv("AI_LOG_FULL") != "0"

// AILogFullMode mengembalikan true bila mode log penuh aktif.
func AILogFullMode() bool {
	return aiLogFull
}

// aiFold melipat teks panjang per 200 rune (tanpa membuang apa pun),
// newline asli dipertahankan.
func aiFold(s string) string {
	var sb strings.Builder
	for _, line := range strings.Split(s, "\n") {
		for len([]rune(line)) > 200 {
			r := []rune(line)
			sb.WriteString(string(r[:200]))
			sb.WriteString("\n")
			line = string(r[200:])
		}
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// AIField adalah satu baris detail `nama : nilai`.
type AIField struct {
	K string
	V string
}

// AISection adalah satu section isi (`label :` + teks mentah multi-baris).
type AISection struct {
	Label string
	Text  string
}

// aiBlockNew mencetak satu blok: header berwarna + tiap field di barisnya
// sendiri + tiap section mentah + pembatas. Header ikut merah bila ada
// field bernilai GAGAL/ERROR.
func aiBlockNew(kind string, fields []AIField, sections []AISection) {
	tag := "[" + kind + "]"
	header := fmt.Sprintf("[%s - %s]", timex.Now().Format("15:04"), kind)
	for _, f := range fields {
		if strings.Contains(strings.ToUpper(f.V), "GAGAL") || strings.Contains(strings.ToUpper(f.V), "ERROR") {
			tag += " ERROR"
			break
		}
	}
	fmt.Println(logfmt.Paint(tag, header))
	for _, f := range fields {
		fmt.Printf("%s : %s\n", f.K, f.V)
	}
	for _, s := range sections {
		fmt.Printf("%s :\n%s\n", s.Label, aiFold(s.Text))
	}
	fmt.Println(logfmt.Divider())
}

// aiJSON memarshal nilai jadi JSON satu baris (fallback %v bila gagal).
func aiJSON(v interface{}) string {
	if b, err := json.Marshal(v); err == nil {
		return string(b)
	}
	return fmt.Sprintf("%v", v)
}

// LogTurnStart mencatat pembuka giliran (dipanggil plugins/ai.go).
func LogTurnStart(from, at, provider, model, sesi string, histori int, userText string) {
	aiBlockNew("AI_TURN", []AIField{
		{"from", from},
		{"at", at},
		{"provider", provider},
		{"model", model},
		{"sesi", sesi},
		{"histori", fmt.Sprintf("%d", histori)},
	}, []AISection{
		{"content", userText},
	})
}

// LogTurnDone mencatat penutup giliran (dipanggil plugins/ai.go). Status
// GAGAL otomatis merah via Paint.
func LogTurnDone(status, body string) {
	aiBlockNew("AI_TURN", []AIField{
		{"status", status},
	}, []AISection{
		{"content", body},
	})
}

// LogSide mencatat panggilan LLM sampingan (fakta, ringkasan, naturalisasi,
// ringkas dokumen, retry) — dipakai lintas package. Gambar cukup ukurannya.
func LogSide(kind string, fields []AIField, body string) {
	if !AILogFullMode() {
		return
	}
	aiBlockNew("AI_SIDE", append([]AIField{{"kind", kind}}, fields...), []AISection{
		{"content", body},
	})
}
