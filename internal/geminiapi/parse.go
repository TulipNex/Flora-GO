package geminiapi

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

// defaultStreamIdle adalah lama diam yang dianggap "jawaban selesai" setelah
// teks terakhir tiba. Server Gemini kadang tidak segera menutup koneksi
// StreamGenerate setelah jawaban lengkap, jadi menunggu EOF membuat setiap
// jawaban terasa lambat.
const defaultStreamIdle = 4 * time.Second

type streamLine struct {
	line string
	err  error
}

// parseStream membaca balasan StreamGenerate. Bentuknya deretan baris JSON;
// yang berisi jawaban adalah string JSON bersarang (payload "wrb.fr") yang
// perlu di-unmarshal lagi.
//
// Teks di dalam stream bersifat KUMULATIF (potongan pertama bisa hanya "M",
// potongan berikutnya "Merah, kuning, biru."), jadi yang dipakai adalah teks
// terpanjang yang pernah terlihat — bukan baris pertama.
//
// Kode error BardErrorInfo tidak langsung menggagalkan pembacaan: balasan bisa
// memuat teks lalu penanda error di baris berikutnya. Kode disimpan dan baru
// dipakai kalau seluruh stream tidak menghasilkan teks.
func parseStream(r io.Reader) (*Result, error) {
	return parseStreamIdle(r, defaultStreamIdle)
}

// parseStreamIdle membaca sampai salah satu dari tiga penanda selesai:
// penanda tutup stream (["e",4,…]), stream diam setelah ada jawaban, atau
// EOF. idle adalah lama diam yang dianggap selesai; idle <= 0 mematikannya.
func parseStreamIdle(r io.Reader, idle time.Duration) (*Result, error) {
	br := bufio.NewReaderSize(r, 64*1024)
	res := &Result{}
	errCode := 0

	// Pembacaan dipindah ke goroutine supaya bisa berhenti saat stream diam;
	// ReadString sendiri tidak punya batas waktu. Goroutine berhenti saat
	// done ditutup (gagal kirim) atau saat body ditutup pemanggil (baca gagal).
	lines := make(chan streamLine, 32)
	done := make(chan struct{})
	go func() {
		defer close(lines)
		for {
			line, err := br.ReadString('\n')
			select {
			case lines <- streamLine{line: line, err: err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	defer close(done)

	var timer *time.Timer
	var idleC <-chan time.Time
	arm := func() {
		if idle <= 0 {
			return
		}
		if timer == nil {
			timer = time.NewTimer(idle)
			idleC = timer.C
			return
		}
		timer.Stop()
		timer.Reset(idle)
	}
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

loop:
	for {
		select {
		case ev, ok := <-lines:
			if !ok {
				break loop
			}
			if trimmed := strings.TrimSpace(ev.line); trimmed != "" {
				handleLine(trimmed, res)
				if code, ok := findErrorCodeFromLine(trimmed); ok {
					errCode = code
				}
				answered := res.Text != ""
				endMarker := isStreamEnd(trimmed)
				if answered {
					// Penanda tutup stream: berhenti seketika.
					if endMarker {
						break loop
					}
					arm()
				} else if endMarker {
					// Penanda tutup tanpa jawaban (mis. balasan kosong):
					// beri kesempatan satu baris BardErrorInfo menyusul,
					// tapi jangan menunggu koneksi ditutup.
					arm()
				}
			}
			if ev.err != nil {
				if ev.err == io.EOF {
					break loop
				}
				return nil, fmt.Errorf("gagal baca balasan gemini: %w", ev.err)
			}
		case <-idleC:
			// Sudah ada jawaban, lalu server diam: jawaban selesai lebih
			// awal dari penutupan koneksi.
			break loop
		}
	}

	if res.Text == "" && errCode != 0 {
		switch errCode {
		case 1096, 1097:
			return nil, fmt.Errorf("%w: kode %d", ErrStaleConv, errCode)
		case 3, 1060:
			return nil, fmt.Errorf("%w: kode %d", ErrLimit, errCode)
		default:
			return nil, fmt.Errorf("%w: kode %d", ErrHTTP, errCode)
		}
	}
	return res, nil
}

// isStreamEnd true bila baris adalah penanda tutup stream Gemini (bentuknya
// ["e",4,null,null,131]). Pemeriksaan struktural dipakai supaya huruf "e" di
// dalam teks jawaban tidak salah dianggap penanda.
func isStreamEnd(line string) bool {
	if !strings.Contains(line, `"e"`) {
		return false
	}
	var v any
	if err := json.Unmarshal([]byte(line), &v); err != nil {
		return false
	}
	env, ok := v.([]any)
	if !ok {
		return false
	}
	for _, item := range env {
		pair, ok := item.([]any)
		if !ok || len(pair) == 0 {
			continue
		}
		if name, ok := pair[0].(string); ok && name == "e" {
			return true
		}
	}
	return false
}

// findErrorCodeFromLine mem-parse satu baris lalu mencari kode BardErrorInfo
// (kode 0 dianggap bukan error).
func findErrorCodeFromLine(line string) (int, bool) {
	if !strings.Contains(line, "BardErrorInfo") {
		return 0, false
	}
	var v any
	if err := json.Unmarshal([]byte(line), &v); err != nil {
		return 0, false
	}
	code, ok := findErrorCode(v)
	if !ok || code == 0 {
		return 0, false
	}
	return code, true
}

// handleLine menyerap satu baris stream. Teks kumulatif: yang disimpan adalah
// versi terpanjang, karena potongan awal stream sering terpotong ("M" untuk
// jawaban "Merah, kuning, biru.").
func handleLine(line string, res *Result) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, ")]}'") {
		return
	}
	var v any
	if err := json.Unmarshal([]byte(line), &v); err != nil {
		return // baris non-JSON (mis. penanda chunk) diabaikan
	}
	for _, p := range collectPayloads(v, nil, 0) {
		extractPayloadIDs(p, res)
		if text := extractPayloadText(p); len(text) > len(res.Text) {
			res.Text = text
		}
	}
}

// extractPayloadIDs mengambil "tiket percakapan" dari payload balasan: id
// percakapan (c_...) dan respons (r_...) di slot 1, serta id balasan di blok
// pertama slot 4.
//
// Satu balasan bisa memuat LEBIH DARI SATU payload, dan payload berikutnya
// membawa id percakapan LAIN — bukan lanjutan yang dipakai server. Uji live
// (Nov 2026) membuktikannya: id dari payload pertama diterima (jawaban benar),
// sedangkan id dari payload lain selalu ditolak `1097`. Karena itu tiket
// diambil dari payload PERTAMA yang membawa `c_...`, beserta pasangan `r_...`
// dari payload yang sama, lalu tidak pernah ditimpa. Id balasan (`rc_...`)
// hanya diisi bila belum ada, karena sebagian baris tidak membawanya.
func extractPayloadIDs(p []any, res *Result) {
	if len(p) > 1 && res.ConversationID == "" {
		if ids, ok := p[1].([]any); ok && len(ids) > 0 {
			if s, ok := ids[0].(string); ok && strings.HasPrefix(s, "c_") {
				res.ConversationID = s
				if len(ids) > 1 {
					if r, ok := ids[1].(string); ok && strings.HasPrefix(r, "r_") {
						res.ResponseID = r
					}
				}
			}
		}
	}
	if len(p) <= 4 || res.ReplyID != "" {
		return
	}
	blocks, ok := p[4].([]any)
	if !ok || len(blocks) == 0 {
		return
	}
	block, ok := blocks[0].([]any)
	if !ok || len(block) == 0 {
		return
	}
	if s, ok := block[0].(string); ok && s != "" {
		res.ReplyID = s
	}
}

// collectPayloads mencari string JSON bersarang (payload wrb.fr) di dalam
// struktur baris.
func collectPayloads(v any, out [][]any, depth int) [][]any {
	if depth > 6 {
		return out
	}
	switch t := v.(type) {
	case []any:
		for _, e := range t {
			out = collectPayloads(e, out, depth+1)
		}
	case string:
		s := strings.TrimSpace(t)
		if len(s) < 8 || !strings.HasPrefix(s, "[") {
			return out
		}
		var inner []any
		if err := json.Unmarshal([]byte(s), &inner); err != nil {
			return out
		}
		// Payload yang berguna punya minimal 5 slot (lihat extractPayloadText).
		if len(inner) >= 5 {
			out = append(out, inner)
		}
	}
	return out
}

// extractPayloadText mengambil teks jawaban dari satu payload balasan.
func extractPayloadText(p []any) string {
	if len(p) < 5 {
		return ""
	}
	blocks, ok := p[4].([]any)
	if !ok || len(blocks) == 0 {
		return ""
	}

	var text string
	for _, rawBlock := range blocks {
		block, ok := rawBlock.([]any)
		if !ok || len(block) < 2 {
			continue
		}
		switch v := block[1].(type) {
		case string:
			if v != "" {
				text = v
			}
		case []any:
			for _, item := range v {
				if s, ok := item.(string); ok && s != "" {
					text = s
				}
			}
		}
	}

	text = citeMarkRe.ReplaceAllString(text, "")
	text = imagePlaceholderRe.ReplaceAllString(text, "")
	text = imagenMarkRe.ReplaceAllString(text, "")
	text = strings.ReplaceAll(text, "<!-- end list -->", "")
	text = strings.ReplaceAll(text, "<ctrl94>thought", "")
	text = strings.ReplaceAll(text, "<ctrl95>", "")
	text = multiSpaceRe.ReplaceAllString(text, " ")
	return strings.TrimSpace(text)
}

// firstMatch mengembalikan grup pertama sebuah regex, atau string kosong.
func firstMatch(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

var (
	imagePlaceholderRe = regexp.MustCompile(`http://googleusercontent\.com/[^\s)\]\[]+`)
	imagenMarkRe       = regexp.MustCompile(`\[Imagen of (.*?)\]`)
	citeMarkRe         = regexp.MustCompile(`\[cite:\s*[0-9, ]+\]`)
	// errCodeRe hanya cadangan untuk kode yang muncul sebagai teks di dalam
	// string payload; bentuk aslinya adalah array (lihat findErrorCode).
	errCodeRe    = regexp.MustCompile(`BardErrorInfo[\s\S]{0,120}?\[\s*(\d+)`)
	multiSpaceRe = regexp.MustCompile(`[ \t]{2,}`)
)

// findErrorCode mencari kode BardErrorInfo di struktur balasan. Penanda ini
// muncul dua bentuk: array nyata ["...BardErrorInfo",[1097,null,"..."]] di
// dalam JSON, atau sebagai teks di dalam string payload. Nama tipenya memakai
// prefix panjang (type.googleapis.com/...BardErrorInfo), jadi pencocokan
// memakai akhiran, bukan kesamaan persis.
func findErrorCode(v any) (int, bool) {
	switch t := v.(type) {
	case []any:
		if len(t) >= 2 {
			if name, ok := t[0].(string); ok && strings.HasSuffix(name, "BardErrorInfo") {
				for _, e := range t[1:] {
					if code, ok := firstNumber(e); ok {
						return code, true
					}
				}
			}
		}
		for _, e := range t {
			if code, ok := findErrorCode(e); ok {
				return code, true
			}
		}
	case string:
		if !strings.Contains(t, "BardErrorInfo") {
			return 0, false
		}
		m := errCodeRe.FindStringSubmatch(t)
		if len(m) < 2 {
			return 0, false
		}
		var code int
		if _, err := fmt.Sscanf(m[1], "%d", &code); err != nil {
			return 0, false
		}
		return code, true
	}
	return 0, false
}

// firstNumber mengambil angka pertama dari struktur JSON (dipakai untuk kode
// error).
func firstNumber(v any) (int, bool) {
	switch t := v.(type) {
	case float64:
		return int(t), true
	case []any:
		for _, e := range t {
			if code, ok := firstNumber(e); ok {
				return code, true
			}
		}
	}
	return 0, false
}
