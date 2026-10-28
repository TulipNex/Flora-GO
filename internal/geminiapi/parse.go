package geminiapi

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

// buildChatRequest menyusun nilai f.req. Bentuknya adalah array 97 slot milik
// antarmuka web Gemini; hanya slot yang dipakai yang diisi, sisanya null.
// Nomor slot berasal dari perilaku halaman (bukan API resmi), jadi kalau
// protokol berubah, di sinilah tempat menyesuaikannya.
func buildChatRequest(in Request, lang string) string {
	req := make([]any, 97)

	files := make([]any, 0, len(in.Files))
	for _, f := range in.Files {
		files = append(files, []any{[]any{f.ID, 1}, f.Name})
	}

	convID, respID, choiceID, turn := "", "", "", 0
	if in.Conv != nil {
		convID = in.Conv.ID
		respID = in.Conv.ResponseID
		choiceID = in.Conv.ChoiceID
		turn = in.Conv.Turn
	}

	req[0] = []any{in.Prompt, 0, nil, files, nil, nil, 0}
	req[1] = []any{lang}
	// Slot 2: [conversation_id, response_id, choice_id, null x6, ""].
	req[2] = []any{convID, respID, choiceID, nil, nil, nil, nil, nil, nil, ""}
	req[6] = []any{1}
	req[7] = 1
	req[10] = 1
	req[11] = 0
	req[17] = []any{[]any{turn}}
	req[18] = 0
	req[27] = 1
	req[30] = []any{4}
	req[41] = []any{1}
	req[53] = 0
	req[59] = newUUID()
	req[61] = []any{}
	req[68] = 2
	req[79] = modelMode(in.Model)
	req[80] = 1 // mode berpikir normal
	if in.Thinking {
		req[80] = 2 // mode berpikir diperluas
	}
	req[91] = 0
	if in.Conv == nil {
		req[96] = 1 // giliran pertama
	} else {
		req[96] = 0
	}

	inner, err := json.Marshal(req)
	if err != nil {
		// Array di atas hanya berisi tipe JSON dasar; kegagalan di sini
		// berarti bug pemrograman, bukan masukan pengguna.
		return "[]"
	}
	outer, err := json.Marshal([]any{nil, string(inner)})
	if err != nil {
		return "[]"
	}
	return string(outer)
}

// modelMode memetakan nama model ke nomor mode di slot 79.
func modelMode(model string) int {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "pro", "gemini-pro", "gemini-3.1-pro", "gemini-3.8-pro":
		return 3
	case "lite", "flash-lite", "gemini-3.5-flash-lite":
		return 6
	default:
		return 1
	}
}

// ModelLabel adalah nama tampilan model untuk caption.
func ModelLabel(model string) string {
	switch modelMode(model) {
	case 3:
		return "Gemini Pro"
	case 6:
		return "Gemini Flash Lite"
	default:
		return "Gemini Flash"
	}
}

func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fallback deterministik tidak diperlukan: UUID hanya penanda
		// permintaan, bukan rahasia.
		return "00000000-0000-4000-8000-000000000000"
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	dst := make([]byte, 36)
	hex.Encode(dst[0:8], b[0:4])
	dst[8] = '-'
	hex.Encode(dst[9:13], b[4:6])
	dst[13] = '-'
	hex.Encode(dst[14:18], b[6:8])
	dst[18] = '-'
	hex.Encode(dst[19:23], b[8:10])
	dst[23] = '-'
	hex.Encode(dst[24:36], b[10:16])
	return string(dst)
}

var imagePlaceholderRe = regexp.MustCompile(`http://googleusercontent\.com/[^\s)\]\[]+`)
var imagenMarkRe = regexp.MustCompile(`\[Imagen of (.*?)\]`)
var citeMarkRe = regexp.MustCompile(`\[cite:\s*[0-9, ]+\]`)
var errCodeRe = regexp.MustCompile(`BardErrorInfo[\s\S]{0,80}?\[\s*(\d+)\s*\]`)
var multiSpaceRe = regexp.MustCompile(`[ \t]{2,}`)

// parseStream membaca balasan StreamGenerate. Bentuknya deretan baris JSON;
// sebagian besar baris tidak berguna, yang berisi jawaban adalah string
// bersarang yang perlu di-unmarshal lagi (payload "wrb.fr").
//
// Kode error BardErrorInfo tidak langsung menggagalkan pembacaan: balasan
// bisa memuat teks lalu penanda error di baris berikutnya. Kode disimpan dan
// baru dipakai kalau seluruh stream tidak menghasilkan teks/gambar.
func parseStream(r io.Reader) (*Result, error) {
	return parseStreamIdle(r, defaultStreamIdle)
}

// defaultStreamIdle adalah lama diam yang dianggap "jawaban selesai" setelah
// teks terakhir tiba. Server Gemini kadang tidak segera menutup koneksi
// StreamGenerate setelah jawaban lengkap (terukur ±20 dtk di panel), jadi
// menunggu EOF membuat setiap jawaban terasa lambat 20 detik.
const defaultStreamIdle = 4 * time.Second

type streamLine struct {
	line string
	err  error
}

// parseStreamIdle membaca sampai salah satu dari tiga penanda selesai:
// penanda tutup stream (["e",4,…]), stream diam setelah ada jawaban, atau
// EOF. idle adalah lama diam yang dianggap selesai; idle <= 0 mematikannya.
func parseStreamIdle(r io.Reader, idle time.Duration) (*Result, error) {
	br := bufio.NewReaderSize(r, 64*1024)
	res := &Result{}
	seen := make(map[string]bool)
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
				handleLine(trimmed, res, seen)
				if code, ok := findErrorCodeFromLine(trimmed); ok {
					errCode = code
				}
				answered := res.Text != "" || len(res.Images) > 0
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

	if res.Text == "" && len(res.Images) == 0 && errCode != 0 {
		switch errCode {
		case 1096:
			return nil, fmt.Errorf("%w: percakapan tidak berlaku (%d)", ErrStaleConv, errCode)
		case 3, 1060:
			return nil, fmt.Errorf("%w: kode %d", ErrLimit, errCode)
		default:
			return nil, fmt.Errorf("%w: kode %d", ErrHTTP, errCode)
		}
	}
	return res, nil
}

// isStreamEnd true bila baris adalah penanda tutup stream Gemini// (bentuknya ["e",4,null,null,131]). Pemeriksaan struktural dipakai supaya
// huruf "e" di dalam teks jawaban tidak salah dianggap penanda.
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

func handleLine(line string, res *Result, seen map[string]bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, ")]}'") {
		return
	}
	var v any
	if err := json.Unmarshal([]byte(line), &v); err != nil {
		return // baris non-JSON (mis. penanda chunk) diabaikan
	}

	payloads := collectPayloads(v, nil, 0)
	for _, p := range payloads {
		text, conv, images := extractPayload(p)
		if conv != nil {
			res.Conv = conv
		}
		if text != "" {
			res.Text = text
		}
		for _, img := range images {
			if seen[img] {
				continue
			}
			seen[img] = true
			res.Images = append(res.Images, img)
		}
		for _, cand := range collectMediaCandidates(p, 0) {
			if !containsString(res.Candidates, cand) {
				res.Candidates = append(res.Candidates, cand)
			}
		}
	}
}

// containsString melaporkan apakah s sudah ada di daftar (dipakai untuk
// dedupe kandidat URL; daftarnya kecil).
func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// collectMediaCandidates mengumpulkan SEMUA URL media di satu payload, bukan
// hanya slot pertama: stream Gemini kadang memuat lebih dari satu bentuk
// (preview dan URL penuh), dan `.gemini diag image` perlu membandingkannya.
func collectMediaCandidates(v any, depth int) []string {
	if depth > 12 {
		return nil
	}
	switch t := v.(type) {
	case []any:
		var out []string
		for _, e := range t {
			out = append(out, collectMediaCandidates(e, depth+1)...)
		}
		return out
	case map[string]any:
		var out []string
		for _, e := range t {
			out = append(out, collectMediaCandidates(e, depth+1)...)
		}
		return out
	case string:
		s := strings.TrimSpace(t)
		if !strings.HasPrefix(s, "http") {
			return nil
		}
		if !strings.Contains(s, "googleusercontent.com") && !strings.Contains(s, "usercontent.google.com") {
			return nil
		}
		if u := sanitizeMediaURL(s); u != "" {
			return []string{u}
		}
	}
	return nil
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
		// Payload yang berguna punya minimal 5 slot (lihat extractPayload).
		if len(inner) >= 5 {
			out = append(out, inner)
		}
	}
	return out
}

// extractPayload mengambil teks kumulatif terakhir, id percakapan, dan URL
// gambar dari satu payload balasan.
func extractPayload(p []any) (string, *Conversation, []string) {
	if len(p) < 5 {
		return "", nil, nil
	}
	blocks, ok := p[4].([]any)
	if !ok || len(blocks) == 0 {
		return "", nil, nil
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

	var conv *Conversation
	if ids, ok := p[1].([]any); ok && len(ids) >= 2 {
		convID, _ := ids[0].(string)
		respID, _ := ids[1].(string)
		var choiceID string
		if first, ok := blocks[0].([]any); ok && len(first) > 0 {
			choiceID, _ = first[0].(string)
		}
		if convID != "" && respID != "" {
			// Nomor giliran (slot 17) dihitung pemanggil saat menyimpan.
			conv = &Conversation{ID: convID, ResponseID: respID, ChoiceID: choiceID}
		}
	}

	images := extractImages(blocks)

	text = citeMarkRe.ReplaceAllString(text, "")
	text = imagePlaceholderRe.ReplaceAllString(text, "")
	text = imagenMarkRe.ReplaceAllString(text, "")
	text = strings.ReplaceAll(text, "<!-- end list -->", "")
	text = strings.ReplaceAll(text, "<ctrl94>thought", "")
	text = strings.ReplaceAll(text, "<ctrl95>", "")
	text = multiSpaceRe.ReplaceAllString(text, " ")
	text = strings.TrimSpace(text)

	return text, conv, images
}

// extractImages mengambil URL gambar hasil generate dari slot 12/7.
func extractImages(blocks []any) []string {
	if len(blocks) == 0 {
		return nil
	}
	first, ok := blocks[0].([]any)
	if !ok || len(first) < 13 {
		return nil
	}
	holder, ok := first[12].([]any)
	if !ok || len(holder) < 8 {
		return nil
	}
	group, ok := holder[7].([]any)
	if !ok || len(group) == 0 {
		return nil
	}
	entries, ok := group[0].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, raw := range entries {
		entry, ok := raw.([]any)
		if !ok || len(entry) == 0 {
			continue
		}
		head, ok := entry[0].([]any)
		if !ok || len(head) < 4 {
			continue
		}
		meta, ok := head[3].([]any)
		if !ok || len(meta) < 4 {
			continue
		}
		switch v := meta[3].(type) {
		case string:
			if strings.HasPrefix(v, "http") {
				out = append(out, withSize(sanitizeMediaURL(v)))
			}
		case []any:
			for _, item := range v {
				if s, ok := item.(string); ok && strings.HasPrefix(s, "http") {
					out = append(out, withSize(sanitizeMediaURL(s)))
					break
				}
			}
		}
	}
	return out
}

// sanitizeMediaURL membersihkan URL yang diekstrak dari payload JSON. Nilainya
// bisa membawa entitas HTML atau karakter penutup dari markup; satu karakter
// tambahan saja membuat tanda tangan Google ditolak (HTTP 403).
func sanitizeMediaURL(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.ReplaceAll(s, "&amp;", "&")
	if i := strings.IndexAny(s, " \t\r\n\"'<>)]}\\`"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimRight(s, ".,;:")
}

func withSize(u string) string {
	if strings.Contains(u, "=") || strings.Contains(u, "?") {
		return u
	}
	return u + "=s2048"
}

// findErrorCode mencari kode BardErrorInfo di struktur balasan. Penanda ini
// muncul dua bentuk: array nyata ["BardErrorInfo",[1096]] di dalam JSON, atau
// sebagai teks di dalam string payload. Keduanya ditangani.
func findErrorCode(v any) (int, bool) {
	switch t := v.(type) {
	case []any:
		if len(t) >= 2 {
			if name, ok := t[0].(string); ok && name == "BardErrorInfo" {
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
