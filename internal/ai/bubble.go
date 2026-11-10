package ai

import (
	"regexp"
	"strings"
	"time"
)

// Kecepatan ketik "manusiawi" yang disimulasikan: karakter per detik.
// Dipilih angka wajar untuk WhatsApp (bukan super cepat, bukan super lambat).
const typingCharsPerSecond = 18

// MinBubbleDelay & MaxBubbleDelay adalah batas bawah/atas delay ketik per
// bubble, supaya bubble sangat pendek tidak instan (terasa robotic) dan
// bubble sangat panjang tidak bikin user menunggu kelamaan.
const (
	MinBubbleDelay = 500 * time.Millisecond
	MaxBubbleDelay = 4 * time.Second
)

// maxBubbleLen adalah target panjang maksimum satu bubble sebelum dipotong
// ke bubble berikutnya. Bukan batas keras — satu baris/list-item yang
// panjangnya sendiri sudah melebihi ini tetap dikirim utuh sebagai satu
// bubble (tidak pernah dipotong di TENGAH baris).
const maxBubbleLen = 250

// numberedListRegex & bulletListRegex mendeteksi baris yang merupakan
// item list (bernomor: "1. ", "2) " ; atau bullet: "- ", "* ", "• ").
// Dipakai supaya baris seperti ini SELALU jadi satu unit utuh dan tidak
// pernah digabung-potong sembarangan hanya berdasar tanda titik/koma.
var (
	numberedListRegex = regexp.MustCompile(`^\s*\d+[.\)]\s+`)
	bulletListRegex   = regexp.MustCompile(`^\s*[-*•]\s+`)
)

// markdownBoldRegex mengubah **teks** (Markdown standar, yang sering
// dihasilkan LLM) menjadi *teks* (format bold WhatsApp). WhatsApp hanya
// mengenali bintang TUNGGAL sebagai penanda bold — kalau dikirim mentah
// dengan bintang ganda, WA salah membaca satu bintang sebagai marker bold
// dan satu bintang lain tersisa sebagai karakter literal di layar.
var markdownBoldRegex = regexp.MustCompile(`\*\*(.+?)\*\*`)

// horizontalRuleRegex mendeteksi baris pemisah Markdown ("---", "***",
// "___", boleh diselingi spasi) yang tidak punya arti apapun di WhatsApp
// dan hanya muncul sebagai baris aneh berisi tanda hubung/bintang.
// RE2 (regex engine bawaan Go) tidak mendukung backreference, jadi tiga
// karakter pemisah yang mungkin (-, *, _) ditulis sebagai alternasi
// eksplisit, bukan "(x)...\1". Anchor penuh (^...$) supaya HANYA baris
// yang SELURUH isinya pemisah yang kena, bukan baris teks biasa yang
// kebetulan mengandung "-" atau bullet list yang diawali "*".
var horizontalRuleRegex = regexp.MustCompile(`^\s*(?:-\s*){3,}$|^\s*(?:\*\s*){3,}$|^\s*(?:_\s*){3,}$`)

// markdownHeadingRegex mendeteksi heading Markdown ("# ", "## ", "### ", dst)
// HANYA di awal baris dan HANYA kalau diikuti spasi setelah tanda pagar.
// Anchor ^ memastikan tidak menyentuh "#" di tengah/akhir kata seperti kode
// USSD "*888#" — karakter "#" di sana bukan di awal baris jadi tidak match.
var markdownHeadingRegex = regexp.MustCompile(`^\s*#{1,6}\s+`)

// markdownIndentedListRegex mendeteksi item list Markdown yang ter-indentasi
// (nested list dengan spasi/tab di awal baris). WhatsApp tidak mendukung
// indentasi list — spasi di depan bullet (*, -, •) membuat WhatsApp gagal
// merendernya sebagai bullet list dan menampilkan karakter asterisk mentah.
// Regex ini menangkap spasi/tab di depan marker list agar di-unindent ke kolom 0.
var markdownIndentedListRegex = regexp.MustCompile(`^[ \t]+([*•+-]\s+|\d+[.)]\s+|\[\d+\]\s+)`)

// parenAsteriskRegex mendeteksi tanda kurung yang memuat karakter asterisk (*).
// WhatsApp tidak mendukung format tebal/miring bersarang (nested formatting) —
// kemunculan asterisk di dalam tanda kurung pada baris tebal seperti
// "*Judul (*Istilah Asing*)*" merusak pasangan format bintang WhatsApp.
// Karakter asterisk di dalam kurung dibersihkan agar format tebal luar tetap utuh.
var parenAsteriskRegex = regexp.MustCompile(`\([^)\n]*\*[^)\n]*\)`)

// SanitizeForWhatsApp membersihkan format Markdown standar yang sering
// dikeluarkan LLM tapi tidak dikenali (atau merusak tampilan) WhatsApp:
//   - **teks** -> *teks* (bold WhatsApp memakai bintang tunggal)
//   - hapus asterisk di dalam tanda kurung ("(*teks*)" -> "(teks)") agar tidak merusak bold
//   - baris horizontal rule ("---", "***", dst) dibuang total
//   - prefix heading ("### ", "## ", "# ") di awal baris dibuang, ISI
//     barisnya tetap dipertahankan sebagai baris bold biasa
//   - list ter-indentasi ("  * Poin", "   - Poin") diratakan ke kiri (kolom 0)
//     supaya WhatsApp merendernya menjadi poin-poin (bullet), bukan asterisk mentah
//
// Prefix heading sengaja diubah jadi bold (bukan cuma dihapus polos)
// supaya baris judul seperti "### Contoh Penerapan:" tidak melebur jadi
// teks datar tanpa penekanan sama sekali.
func SanitizeForWhatsApp(text string) string {
	text = markdownBoldRegex.ReplaceAllString(text, "*$1*")
	text = parenAsteriskRegex.ReplaceAllStringFunc(text, func(m string) string {
		return strings.ReplaceAll(m, "*", "")
	})

	lines := strings.Split(text, "\n")
	cleaned := make([]string, 0, len(lines))
	for _, l := range lines {
		trimmedRight := strings.TrimRight(l, " \t")

		if horizontalRuleRegex.MatchString(trimmedRight) {
			// Baris pemisah dibuang total — bukan cuma dikosongkan jadi
			// baris kosong, supaya tidak menyisakan jeda paragraf yang
			// tidak diinginkan di posisi itu.
			continue
		}

		if loc := markdownHeadingRegex.FindStringIndex(trimmedRight); loc != nil {
			rest := strings.TrimSpace(trimmedRight[loc[1]:])
			if rest != "" && !strings.HasPrefix(rest, "*") {
				rest = "*" + rest + "*"
			}
			trimmedRight = rest
		}

		if markdownIndentedListRegex.MatchString(trimmedRight) {
			trimmedRight = markdownIndentedListRegex.ReplaceAllString(trimmedRight, "$1")
		}

		cleaned = append(cleaned, trimmedRight)
	}
	text = strings.Join(cleaned, "\n")

	// Rapikan baris kosong berlebih (3+ newline beruntun -> maksimal 2),
	// sisa umum dari output LLM atau dari baris horizontal rule yang baru
	// saja dibuang di atas.
	for strings.Contains(text, "\n\n\n") {
		text = strings.ReplaceAll(text, "\n\n\n", "\n\n")
	}

	return strings.TrimSpace(text)
}

// endsWithColon mengecek apakah sebuah baris narasi diakhiri titik dua —
// pola umum kalimat pengantar sebelum daftar/list ("Ciri khas utamanya:").
// Dipakai supaya baris seperti ini TIDAK dipisah ke bubble sendiri dari
// list yang mengikutinya persis di baris berikutnya, karena keduanya satu
// kesatuan makna.
func endsWithColon(line string) bool {
	return strings.HasSuffix(strings.TrimSpace(line), ":")
}

// SplitIntoBubbles memecah balasan AI yang panjang jadi beberapa "bubble"
// pesan terpisah, supaya terasa seperti orang mengetik beberapa pesan
// berturut-turut alih-alih satu paragraf besar.
//
// Sengaja split PER BARIS (bukan per kalimat via regex titik) — LLM sudah
// menaruh newline di batas paragraf dan tiap item list, jadi baris adalah
// unit pemisah yang jauh lebih aman: tidak pernah memotong "1." dari isi
// poinnya, tidak pernah kebingungan oleh titik dalam angka/singkatan.
// Baris-baris pendek yang berurutan (bukan list) digabung sampai mendekati
// maxBubbleLen; baris yang merupakan item list SELALU jadi bubble sendiri
// atau digabung utuh dengan list item lain, tidak pernah disatukan dengan
// paragraf naratif biasa di sekitarnya — KECUALI baris narasi itu adalah
// kalimat pengantar yang diakhiri titik dua tepat sebelum list dimulai,
// yang justru sengaja digabung karena satu kesatuan makna.
func SplitIntoBubbles(text string) []string {
	text = SanitizeForWhatsApp(text)
	if text == "" {
		return nil
	}

	rawLines := strings.Split(text, "\n")

	var bubbles []string
	var current strings.Builder
	currentIsList := false
	lastLineWasColonIntro := false

	flush := func() {
		if current.Len() > 0 {
			bubbles = append(bubbles, strings.TrimSpace(current.String()))
			current.Reset()
		}
	}

	for _, line := range rawLines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			// Baris kosong = pemisah paragraf yang disengaja model ->
			// selalu tutup bubble berjalan di sini.
			flush()
			currentIsList = false
			lastLineWasColonIntro = false
			continue
		}

		isListLine := numberedListRegex.MatchString(line) || bulletListRegex.MatchString(line)

		// Kalau jenis baris berubah (dari list ke narasi atau sebaliknya),
		// atau bubble berjalan sudah mendekati batas panjang, tutup dulu
		// bubble sebelum menambahkan baris ini — KECUALI baris sebelumnya
		// adalah kalimat pengantar ("...:") dan baris ini adalah awal list,
		// yang sengaja tetap digabung jadi satu bubble.
		wouldOverflow := current.Len() > 0 && current.Len()+len(trimmed)+1 > maxBubbleLen
		typeChanged := current.Len() > 0 && isListLine != currentIsList
		isColonIntroToList := typeChanged && isListLine && lastLineWasColonIntro

		if (wouldOverflow || typeChanged) && !isColonIntroToList {
			flush()
		}

		if current.Len() > 0 {
			current.WriteString("\n")
		}
		current.WriteString(trimmed)
		currentIsList = isListLine
		lastLineWasColonIntro = !isListLine && endsWithColon(trimmed)
	}
	flush()

	if len(bubbles) == 0 {
		return []string{text}
	}
	return bubbles
}

// TypingDelayFor menghitung berapa lama delay ketik yang disimulasikan
// sebelum satu bubble dikirim, berdasarkan PANJANG TEKS bubble itu sendiri
// (makin panjang, makin lama), dijepit ke [MinBubbleDelay, MaxBubbleDelay].
func TypingDelayFor(bubbleText string) time.Duration {
	chars := len(bubbleText)
	seconds := float64(chars) / float64(typingCharsPerSecond)
	delay := time.Duration(seconds * float64(time.Second))

	if delay < MinBubbleDelay {
		return MinBubbleDelay
	}
	if delay > MaxBubbleDelay {
		return MaxBubbleDelay
	}
	return delay
}
