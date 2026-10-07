package plugins

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"wabot-go/internal/logfmt"
	"wabot-go/internal/messaging"
	"wabot-go/internal/store"

	"github.com/coder/websocket"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func init() {
	registerPlugin(RegisterTTS)
}

const (
	// ttsWSBase adalah endpoint synthesize Read Aloud milik Microsoft Edge
	// (dipakai juga oleh library edge-tts 12k⭐): tanpa API key, tanpa
	// daftar. TrustedClientToken adalah token publik dari klien Edge.
	ttsWSBase = "wss://speech.platform.bing.com/consumer/speech/synthesize/readaloud/edge/v1?TrustedClientToken=6A5AA1D4EAFF4E9FB37E23D68491D6F4"

	// ttsGECVersion adalah versi token Sec-MS-GEC (format 1-<versi Chromium>,
	// mengikuti edge-tts).
	ttsGECVersion = "1-143.0.3650.75"

	// ttsOrigin meniru extension Chrome — nilai persis dari edge-tts
	// (tanpa ini handshake berisiko ditolak).
	ttsOrigin = "chrome-extension://jdiccldimpdaibmpdkjnbmckianbfold"
	ttsUA     = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36 Edg/143.0.0.0"

	// ttsTimeout membatasi seluruh sesi WebSocket (dial + sintesis).
	ttsTimeout = 60 * time.Second

	// ttsMaxChars membatasi teks per panggilan — TTS online tak cocok
	// untuk artikel panjang (lambat + boros), potong di sisi user.
	ttsMaxChars = 500
)

// ttsVoices adalah voice Indonesia yang tersedia. Default Gadis
// (persona Flora perempuan); Ardi untuk varian pria.
var ttsVoices = map[string]string{
	"gadis":     "id-ID-GadisNeural",
	"cewek":     "id-ID-GadisNeural",
	"wanita":    "id-ID-GadisNeural",
	"perempuan": "id-ID-GadisNeural",
	"ardi":      "id-ID-ArdiNeural",
	"cowok":     "id-ID-ArdiNeural",
	"pria":      "id-ID-ArdiNeural",
	"laki":      "id-ID-ArdiNeural",
}

const ttsDefaultVoice = "id-ID-GadisNeural"

func RegisterTTS(r *Registry) {
	r.Register(Command{
		Name:        "tts",
		Aliases:     []string{"vn"},
		Description: "Ubah teks jadi voice note (.tts <teks> [| ardi])",
		Category:    CategoryAlat,
		Handler:     handleTTS,
	})
}

// handleTTS memvalidasi input lalu mensintesis di goroutine terpisah (pola
// .brat/downloader): TTS + convert bisa belasan detik, event loop whatsmeow
// tidak boleh diblokir.
func handleTTS(ctx context.Context, mctx *store.MessageContext) error {
	text, voice := ttsParseInput(mctx)
	if strings.TrimSpace(text) == "" {
		return store.ReplyText(ctx, mctx, "Tulis teksnya: .tts halo apa kabar (atau reply pesan teks).")
	}
	if utf8.RuneCountInString(text) > ttsMaxChars {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Teks kepanjangan (maks %d karakter).", ttsMaxChars))
	}

	go func() {
		bg := context.Background()
		if err := ttsGenerate(bg, mctx, text, voice); err != nil {
			logfmt.Warn("tts: %v\n", err)
			_ = store.ReplyText(bg, mctx, "Gagal bikin voice note: "+err.Error())
		}
	}()
	return nil
}

// ttsParseInput mengambil teks (argumen / fallback reply via bratInputText
// yang sudah menangani quoted) dan voice opsional setelah pipe TERAKHIR:
// ".tts halo | ardi". Pipe yang tidak diikuti nama voice valid dianggap
// bagian dari teks (tidak merusak teks ber-pipe).
func ttsParseInput(mctx *store.MessageContext) (text, voice string) {
	raw := bratInputText(mctx)
	voice = ttsDefaultVoice
	if i := strings.LastIndex(raw, "|"); i >= 0 {
		if v, ok := ttsVoices[strings.ToLower(strings.TrimSpace(raw[i+1:]))]; ok {
			return strings.TrimSpace(raw[:i]), v
		}
	}
	return strings.TrimSpace(raw), voice
}

// ttsGenerate menjalankan pipeline: Edge TTS → MP3 → Opus → voice note.
// MP3 yang gagal jadi Opus (libopus hilang) jatuh ke audio biasa supaya
// tetap ada hasil (pola fallback .tomp3).
func ttsGenerate(ctx context.Context, mctx *store.MessageContext, text, voice string) error {
	ctx, cancel := context.WithTimeout(ctx, ttsTimeout)
	defer cancel()

	mp3Bytes, err := edgeTTSSynthesize(ctx, text, voice)
	if err != nil {
		return err
	}
	if len(mp3Bytes) == 0 {
		return fmt.Errorf("hasil sintesis kosong")
	}

	opusBytes, err := runFfmpegConvert(ctx, mp3Bytes, ".mp3", "out.opus", nil,
		"-c:a", "libopus", "-b:a", "48k")
	if err != nil || len(opusBytes) == 0 {
		logfmt.Warn("tts: convert opus gagal (%v), fallback audio biasa.\n", err)
		if serr := sendDlBytesAudio(ctx, mctx, mp3Bytes, "tts"); serr != nil {
			return fmt.Errorf("gagal kirim audio: %v", serr)
		}
		return nil
	}
	return sendVoiceNote(ctx, mctx, opusBytes, false)
}

// sendVoiceNote mengupload + mengirim byte Opus sebagai voice note
// (AudioMessage PTT=true, pola SendTextToSpeech repo referensi). Beda dari
// sendDlBytesAudio (PTT=false, audio/mpeg) sehingga helper baru di sini,
// bukan duplikasi. agent=true bila dari balasan AI (catat agent:true),
// false bila dari command .tts biasa.
func sendVoiceNote(ctx context.Context, mctx *store.MessageContext, data []byte, agent bool) error {
	if err := messaging.CheckMediaBytes(data); err != nil {
		return err
	}
	uploaded, err := mctx.Client.Upload(ctx, data, whatsmeow.MediaAudio)
	if err != nil {
		return err
	}
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, &waE2E.Message{
		AudioMessage: &waE2E.AudioMessage{
			URL:           proto.String(uploaded.URL),
			DirectPath:    proto.String(uploaded.DirectPath),
			MediaKey:      uploaded.MediaKey,
			Mimetype:      proto.String("audio/ogg; codecs=opus"),
			FileEncSHA256: uploaded.FileEncSHA256,
			FileSHA256:    uploaded.FileSHA256,
			FileLength:    proto.Uint64(uploaded.FileLength),
			PTT:           proto.Bool(true),
		},
	})
	if err != nil {
		return err
	}
	if agent {
		store.LogOutgoingAgent(mctx, "voicenote", "tts")
	} else {
		store.LogOutgoing(mctx, "voicenote", "tts")
	}
	store.TrackBotMessageID(resp.ID, "tts")
	return nil
}

// SynthesizeLong mensintesis teks TANPA batas panjang jadi satu MP3 utuh:
// teks dipecah per ≤4000 byte di batas kalimat/spasi (prioritas newline,
// lalu spasi, lalu batas rune valid; tidak membelah entity XML), tiap chunk
// disintesis terpisah (sesi WS baru per chunk, timeout 60s each), byte MP3
// digabung mentah. Frame MP3 independen sehingga gabungan valid dan diputar
// berurutan oleh semua decoder (termasuk WhatsApp). Dipakai mode suara
// Flora; command .tts manual tetap cap 500 char satu panggilan.
func SynthesizeLong(ctx context.Context, text, voice string) ([]byte, error) {
	text = ttsCleanText(strings.TrimSpace(text))
	if text == "" {
		return nil, fmt.Errorf("teks kosong")
	}
	chunks := splitTTSChunks(text, 4000)
	var out bytes.Buffer
	for i, c := range chunks {
		mp3, err := edgeTTSSynthesize(ctx, c, voice)
		if err != nil {
			return nil, fmt.Errorf("bagian %d/%d: %v", i+1, len(chunks), err)
		}
		if len(mp3) == 0 {
			return nil, fmt.Errorf("bagian %d/%d kosong", i+1, len(chunks))
		}
		out.Write(mp3)
	}
	return out.Bytes(), nil
}

// ttsParenRe mengenali (...) berpasangan (tanpa nested) untuk diubah jadi
// jeda apit. ":)" aman (tanpa buka) dan "(x" tanpa tutup dibiarkan.
var ttsParenRe = regexp.MustCompile(`\(([^()]*)\)`)

// ttsCleanText menyiapkan teks untuk DIBACAKAN (bukan ditampilkan):
// buang emoji, markdown WA (* _ ~ • # >), URL → "tautan", @angka/nomor
// panjang → "nomor tersebut", "/" → "," (jeda netral), "(...)" → ", ..., "
// (jeda apit). Struktur baris DIPERTAHANKAN (\n\n = ganti paragraf, \n =
// ganti baris) karena jeda disisipkan dari sana di SSML — beda dari versi
// lama yang menggabung semua jadi satu aliran datar. Teks chat asli TIDAK
// diubah — hanya versi suara. Dipusatkan di SynthesizeLong sehingga .tts
// manual dan voice-reply AI ikut aturan yang sama.
func ttsCleanText(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, ln := range lines {
		t := ttsCleanLine(ln)
		if strings.TrimSpace(t) == "" {
			blank++
			if blank <= 2 {
				out = append(out, "")
			}
			continue
		}
		blank = 0
		out = append(out, t)
	}
	return strings.Trim(strings.Join(out, "\n"), "\n")
}

// ttsCleanLine membersihkan satu baris (tanpa menyentuh newline).
// Urutan penting: URL dulu (mengandung "/" yang tak boleh disentuh),
// lalu kurung + garis miring, lalu emoji/markdown, terakhir mention/angka.
func ttsCleanLine(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		lw := strings.ToLower(w)
		if strings.HasPrefix(lw, "http://") || strings.HasPrefix(lw, "https://") || strings.HasPrefix(lw, "www.") {
			words[i] = "tautan"
		}
	}
	s = strings.Join(words, " ")
	s = ttsParenRe.ReplaceAllString(s, ", $1, ")
	s = strings.ReplaceAll(s, "/", ", ")
	var sb strings.Builder
	for _, r := range s {
		if isTTSEmoji(r) {
			sb.WriteRune(' ')
			continue
		}
		switch r {
		case '*', '_', '~', '•', '#', '>':
			sb.WriteRune(' ')
		default:
			sb.WriteRune(r)
		}
	}
	words = strings.Fields(sb.String())
	for i, w := range words {
		if strings.HasPrefix(w, "@") && len([]rune(w)) > 1 {
			words[i] = "nomor tersebut"
			continue
		}
		// "62812..." panjang (≥8 digit) → "nomor tersebut" supaya tidak
		// dieja digit per digit.
		digits := 0
		for _, r := range w {
			if r >= '0' && r <= '9' {
				digits++
			} else if r != '+' && r != '-' && r != ' ' && r != ',' {
				digits = -100
				break
			}
		}
		if digits >= 8 {
			words[i] = "nomor tersebut"
		}
	}
	joined := strings.Join(words, " ")
	// Rapikan sisa substitusi: ", ," → ",", " ," → ",".
	for strings.Contains(joined, ", ,") {
		joined = strings.ReplaceAll(joined, ", ,", ",")
	}
	for strings.Contains(joined, " ,") {
		joined = strings.ReplaceAll(joined, " ,", ",")
	}
	return strings.Trim(joined, " ,")
}

// isTTSEmoji true untuk rune emoji/simbol dekoratif yang sebaiknya tidak
// dibacakan: blok Emoticons, Dingbats, simbol lain (So), pictograph
// extended, variation selector, dan regional indicator (bendera).
func isTTSEmoji(r rune) bool {
	switch {
	case r >= 0x1F600 && r <= 0x1F64F, // emoticons
		r >= 0x1F300 && r <= 0x1F5FF, // simbol & piktograf
		r >= 0x1F680 && r <= 0x1F6FF, // transport & simbol
		r >= 0x1F700 && r <= 0x1F77F, // alkemis/geometris
		r >= 0x1F780 && r <= 0x1F7FF, // geometris extended
		r >= 0x1F800 && r <= 0x1F8FF, // panah suplemen
		r >= 0x1F900 && r <= 0x1F9FF, // suplemen simbol
		r >= 0x1FA00 && r <= 0x1FAFF, // simbol extended-A
		r >= 0x2600 && r <= 0x26FF,   // simbol lain (So)
		r >= 0x2700 && r <= 0x27BF,   // dingbats
		r >= 0x2B00 && r <= 0x2BFF,   // simbol panah/bintang
		r >= 0xFE00 && r <= 0xFE0F,   // variation selector
		r >= 0x1F1E6 && r <= 0x1F1FF: // regional indicator (bendera)
		return true
	}
	return false
}

// splitTTSChunks memecah teks jadi potongan ≤ maxBytes byte dengan urutan
// preferensi: newline → spasi → batas rune valid, lalu mundur bila titik
// potong membelah entity XML (&amp;). Selalu maju ≥1 byte (terminasi
// terjamin); potongan di-trim, yang kosong dibuang.
func splitTTSChunks(text string, maxBytes int) []string {
	var out []string
	b := []byte(strings.TrimSpace(text))
	for len(b) > maxBytes {
		cut := bytes.LastIndexByte(b[:maxBytes], '\n')
		if cut < 0 {
			cut = bytes.LastIndexByte(b[:maxBytes], ' ')
		}
		if cut < 0 {
			cut = maxBytes
			for cut > 0 && !utf8.Valid(b[:cut]) {
				cut--
			}
		}
		if i := bytes.LastIndexByte(b[:cut], '&'); i >= 0 && bytes.IndexByte(b[i:cut], ';') < 0 {
			cut = i
		}
		if cut <= 0 {
			cut = maxBytes
		}
		if part := bytes.TrimSpace(b[:cut]); len(part) > 0 {
			out = append(out, string(part))
		}
		b = bytes.TrimLeft(b[cut:], " \t\r\n")
	}
	if rest := bytes.TrimSpace(b); len(rest) > 0 {
		out = append(out, string(rest))
	}
	return out
}

// edgeTTSSynthesize memanggil Edge Read Aloud via WebSocket dan
// mengembalikan MP3. Protokol dipetakan 1:1 dari edge-tts (urutan dan
// format PENTING — server menolak variasi yang "hampir benar"):
//  1. Dial dengan query ConnectionId + Sec-MS-GEC + Sec-MS-GEC-Version,
//     header no-cache + Origin + Cookie muid.
//  2. Kirim speech.config (dengan X-Timestamp gaya-JS di baris pertama).
//  3. Kirim SSML (X-RequestId + X-Timestamp + Path).
//  4. Baca sampai turn.end: pesan TEKS dipisah di \r\n\r\n, pesan BINER
//     diawali length 2-byte big-endian; kumpulkan body Path:audio.
//
// Sec-MS-GEC adalah SHA256-uppercase dari (waktu-file-Windows dibulatkan
// ke 5 menit + trusted token) — tanpa ini handshake 403 (terverifikasi).
func edgeTTSSynthesize(ctx context.Context, text, voice string) ([]byte, error) {
	text = removeIncompatibleChars(text)
	reqID := ttsRequestID()
	connID := ttsRequestID()
	// Jeda eksplisit dari struktur baris (audit intonasi): SSML melebur
	// newline jadi spasi, jadi \n\n (ganti paragraf) → break 600ms dan \n
	// tunggal → break 300ms. Diterapkan SETELAH escape (tag break tidak
	// boleh ter-escape) dan SEBELUM chunking tak relevan — chunk sudah
	// dipecah di batas newline oleh splitTTSChunks, sisa newline di dalam
	// chunk tetap dapat jedanya di sini.
	spoken := ttsBreakSSML(xmlEscapeText(text))
	ssml := "<speak version='1.0' xmlns='http://www.w3.org/2001/10/synthesis' xml:lang='en-US'>" +
		"<voice name='" + voice + "'><prosody rate='+0%' volume='+0%' pitch='+0Hz'>" +
		spoken + "</prosody></voice></speak>"

	endpoint := ttsWSBase +
		"&ConnectionId=" + connID +
		"&Sec-MS-GEC=" + ttsGenerateGEC() +
		"&Sec-MS-GEC-Version=" + ttsGECVersion

	header := http.Header{}
	header.Set("Pragma", "no-cache")
	header.Set("Cache-Control", "no-cache")
	header.Set("Origin", ttsOrigin)
	header.Set("User-Agent", ttsUA)
	header.Set("Accept-Encoding", "gzip, deflate, br")
	header.Set("Accept-Language", "en-US,en;q=0.9")
	header.Set("Cookie", "muid="+ttsGenerateMUID()+";")
	conn, _, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		return nil, fmt.Errorf("layanan TTS tidak merespons: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	configMsg := "X-Timestamp:" + ttsJSDate() + "\r\n" +
		"Content-Type:application/json; charset=utf-8\r\n" +
		"Path:speech.config\r\n\r\n" +
		`{"context":{"synthesis":{"audio":{"metadataoptions":{"sentenceBoundaryEnabled":"false","wordBoundaryEnabled":"false"},"outputFormat":"audio-24khz-48kbitrate-mono-mp3"}}}}` + "\r\n"
	if err := conn.Write(ctx, websocket.MessageText, []byte(configMsg)); err != nil {
		return nil, fmt.Errorf("gagal kirim konfigurasi TTS: %v", err)
	}

	ssmlMsg := "X-RequestId:" + reqID + "\r\n" +
		"Content-Type:application/ssml+xml\r\n" +
		"X-Timestamp:" + ttsJSDate() + "Z\r\n" +
		"Path:ssml\r\n\r\n" + ssml
	if err := conn.Write(ctx, websocket.MessageText, []byte(ssmlMsg)); err != nil {
		return nil, fmt.Errorf("gagal kirim teks TTS: %v", err)
	}

	var audio bytes.Buffer
	for {
		msgType, data, err := conn.Read(ctx)
		if err != nil {
			return nil, fmt.Errorf("gagal baca hasil TTS: %v", err)
		}
		var headers, body string
		if msgType == websocket.MessageText {
			// Pesan teks: header dan body dipisah \r\n\r\n (TANPA
			// length prefix — beda dari pesan biner!).
			parts := strings.SplitN(string(data), "\r\n\r\n", 2)
			headers = parts[0]
			if len(parts) == 2 {
				body = parts[1]
			}
		} else {
			if len(data) < 2 {
				continue
			}
			hdrLen := int(binary.BigEndian.Uint16(data[:2]))
			if len(data) < 2+hdrLen {
				continue
			}
			headers = string(data[2 : 2+hdrLen])
			body = string(data[2+hdrLen:])
		}
		switch {
		case strings.Contains(headers, "Path:turn.end"):
			return audio.Bytes(), nil
		case strings.Contains(headers, "Path:audio"):
			audio.WriteString(body)
		}
		// turn.start / response / audio.metadata diabaikan.
	}
}

// ttsGenerateGEC membuat token Sec-MS-GEC: SHA256-uppercase dari waktu
// file Windows (epoch 1601, interval 100ns) yang dibulatkan ke 5 menit,
// digabung trusted token. Port persis drm.generate_sec_ms_gec edge-tts
// (urutan operasi float dipertahankan supaya hasilnya identik).
func ttsGenerateGEC() string {
	ticks := float64(time.Now().UTC().Unix()) + 11644473600
	ticks -= math.Mod(ticks, 300)
	ticks *= 1e9 / 100
	sum := sha256.Sum256([]byte(fmt.Sprintf("%.0f6A5AA1D4EAFF4E9FB37E23D68491D6F4", ticks)))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// ttsGenerateMUID membuat MUID acak 32-hex-uppercase untuk cookie.
func ttsGenerateMUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "0123456789ABCDEF0123456789ABCDEF"
	}
	return strings.ToUpper(hex.EncodeToString(b[:]))
}

// ttsJSDate membuat tanggal gaya-JavaScript dalam UTC (format persis
// edge-tts; dipakai di header X-Timestamp).
func ttsJSDate() string {
	return time.Now().UTC().Format("Mon Jan 02 2006 15:04:05 GMT+0000 (Coordinated Universal Time)")
}

// removeIncompatibleChars membuang karakter kontrol yang ditolak service
// (vertical tab dkk. dari hasil OCR/PDF) jadi spasi — port edge-tts.
func removeIncompatibleChars(s string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 0 && r <= 8) || r == 11 || r == 12 || (r >= 14 && r <= 31) {
			return ' '
		}
		return r
	}, s)
}

// ttsBreakSSML mengubah newline yang lolos (sudah ter-escape) jadi jeda
// SSML eksplisit. Urutan penting: \n\n dulu (600ms), lalu sisa \n (300ms).
func ttsBreakSSML(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	s = strings.ReplaceAll(s, "\n\n", `<break time="600ms"/>`)
	s = strings.ReplaceAll(s, "\n", `<break time="300ms"/>`)
	return s
}

// ttsRequestID membuat ID request 32-hex (format X-RequestId Edge).
func ttsRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "0123456789abcdef0123456789abcdef"
	}
	return hex.EncodeToString(b[:])
}

// xmlEscapeText meng-escape teks user untuk sisipan SSML (tanpa library,
// hanya 5 karakter spesial XML).
func xmlEscapeText(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return r.Replace(s)
}
