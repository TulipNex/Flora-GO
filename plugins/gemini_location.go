package plugins

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"wabot-go/internal/store"
)

// gemini_location.go — mengubah link Google Maps di jawaban Gemini menjadi
// "pin lokasi" (LocationMessage) yang dikirim sebagai bubble terpisah.
//
// Kenapa: Gemini (mode tamu) sering menutup jawaban dengan markdown seperti
//
//	[Petunjuk Arah ke X]([https://www.google.com/maps/dir//X/…](https://…))
//
// Bentuk bersarang itu tidak dikenali WhatsApp, jadi yang tampil justru teks
// mentah penuh kurung. Link-nya sendiri sudah memuat semua data yang dibutuhkan
// (nama tempat, alamat, koordinat), jadi bot mengurainya sendiri dan mengirim
// pin — bisa diketuk langsung tanpa membuka browser.
//
// Aturan: teks sebelum link, pin lokasi, lalu teks setelah link — urutan kirim
// sama dengan urutan di jawaban asli. Link yang koordinatnya tidak bisa diambil
// (short link gagal diikuti) tetap dikirim sebagai TEKS supaya informasinya
// tidak hilang, hanya dirapikan jadi satu baris "label: url".

const (
	// geminiMaxPins membatasi jumlah pin per jawaban; link selebihnya dikirim
	// sebagai teks. Tanpa batas, jawaban yang menyebut banyak tempat bisa
	// membanjiri chat dengan bubble.
	geminiMaxPins = 3
	// geminiMapsResolveTimeout membatasi satu percobaan mengikuti short link.
	geminiMapsResolveTimeout = 6 * time.Second
)

// geminiMapsPin adalah satu link Maps yang sudah diurai. HasCoord false berarti
// koordinat tidak ditemukan; pemanggil mengirimnya sebagai teks biasa.
type geminiMapsPin struct {
	URL      string
	Label    string
	Name     string
	Address  string
	Lat      float64
	Lng      float64
	HasCoord bool
}

// geminiAnswerPart adalah satu potongan jawaban yang dikirim berurutan. Tepat
// satu dari Text/Pin yang terisi.
type geminiAnswerPart struct {
	Text string
	Pin  *geminiMapsPin
}

// geminiLinkSpan menandai rentang teks yang harus diganti bubble lokasi.
type geminiLinkSpan struct {
	Start, End int
	Label      string
	URL        string
}

var (
	// geminiMapsURLRe menangkap URL polos (tanpa spasi/kurung sudut). Tanda
	// kutip sengaja tidak termasuk supaya atribut HTML tidak ikut tertangkap.
	geminiMapsURLRe = regexp.MustCompile("https?://[^\\s<>\"'`]+")
	// Urutan pola koordinat mengikuti yang paling spesifik lebih dulu.
	geminiCoord3d4dRe = regexp.MustCompile(`!3d(-?\d{1,3}(?:\.\d+)?)!4d(-?\d{1,3}(?:\.\d+)?)`)
	geminiCoordAtRe   = regexp.MustCompile(`/@(-?\d{1,3}(?:\.\d+)?),(-?\d{1,3}(?:\.\d+)?)`)
	// Di blok data Google, `!1d` adalah BUJUR dan `!2d` adalah LINTANG
	// (dibuktikan pada URL nyata: !1d8.661928 = Frankfurt, !2d50.1086247).
	geminiCoord1d2dRe = regexp.MustCompile(`!1d(-?\d{1,3}(?:\.\d+)?)!2d(-?\d{1,3}(?:\.\d+)?)`)
)

// geminiSplitMapsLinks memecah jawaban menjadi potongan teks dan pin lokasi.
// Potongan kosong dibuang, dan link yang sama tidak dikirim dua kali.
func geminiSplitMapsLinks(text string) []geminiAnswerPart {
	spans := geminiScanMapsLinks(text)
	if len(spans) == 0 {
		if t := strings.TrimSpace(text); t != "" {
			return []geminiAnswerPart{{Text: t}}
		}
		return nil
	}

	parts := make([]geminiAnswerPart, 0, len(spans)*2+1)
	seen := make(map[string]bool, len(spans))
	pins := 0
	cursor := 0

	for _, sp := range spans {
		if sp.Start > cursor {
			if t := strings.TrimSpace(text[cursor:sp.Start]); t != "" {
				parts = append(parts, geminiAnswerPart{Text: t})
			}
		}
		cursor = sp.End

		pin := geminiParseMapsPin(sp.URL, sp.Label)
		key := strings.ToLower(strings.TrimSpace(pin.URL))
		if seen[key] {
			continue
		}
		seen[key] = true

		if pins >= geminiMaxPins {
			// Link berlebih dikirim sebagai teks, bukan dibuang.
			parts = append(parts, geminiAnswerPart{Text: geminiPinText(pin)})
			continue
		}
		parts = append(parts, geminiAnswerPart{Pin: &pin})
		pins++
	}

	if cursor < len(text) {
		if t := strings.TrimSpace(text[cursor:]); t != "" {
			parts = append(parts, geminiAnswerPart{Text: t})
		}
	}
	return parts
}

// geminiScanMapsLinks memindai teks dan mengembalikan semua rentang link Maps:
// markdown `[label](target)` (termasuk bentuk bersarang/rusak) dan URL polos.
func geminiScanMapsLinks(text string) []geminiLinkSpan {
	var spans []geminiLinkSpan
	for i := 0; i < len(text); {
		if text[i] == '[' {
			if end, label, target, ok := geminiMatchMarkdownLink(text, i); ok {
				if raw := geminiFirstMapsURL(target); raw != "" {
					spans = append(spans, geminiLinkSpan{Start: i, End: end, Label: label, URL: raw})
				}
				i = end
				continue
			}
			i++
			continue
		}
		if raw, end, ok := geminiBareMapsURL(text, i); ok {
			spans = append(spans, geminiLinkSpan{Start: i, End: end, URL: raw})
			i = end
			continue
		}
		i++
	}
	return spans
}

// geminiMatchMarkdownLink mencari bentuk `[label](target)` mulai dari posisi
// `[`. Target boleh memuat kurung bertingkat (kasus Gemini: `([url](url))`),
// jadi penutupnya dicari dengan menghitung kedalaman kurung.
func geminiMatchMarkdownLink(text string, start int) (end int, label, target string, ok bool) {
	idx := strings.Index(text[start:], "](")
	if idx < 0 {
		return 0, "", "", false
	}
	label = strings.TrimSpace(text[start+1 : start+idx])
	open := start + idx + 1 // posisi '('
	close := geminiMatchParen(text, open)
	if close < 0 {
		return 0, "", "", false
	}
	return close + 1, label, text[open+1 : close], true
}

// geminiMatchParen mengembalikan indeks ')' pasangan dari '(' pada posisi open.
func geminiMatchParen(text string, open int) int {
	depth := 0
	for i := open; i < len(text); i++ {
		switch text[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// geminiFirstMapsURL mengambil URL Maps pertama (terpanjang di antara kandidat,
// karena bentuk bersarang bisa memuat URL yang sama dua kali) dari isi target
// markdown.
func geminiFirstMapsURL(target string) string {
	best := ""
	for _, candidate := range geminiMapsURLRe.FindAllString(target, -1) {
		candidate = geminiTrimURLTail(candidate)
		if !geminiIsMapsURL(candidate) {
			continue
		}
		if len(candidate) > len(best) {
			best = candidate
		}
	}
	return best
}

// geminiBareMapsURL mengenali URL Maps yang ditulis tanpa markdown, mulai dari
// posisi tepat setelah spasi/awal teks (supaya tidak memotong kata lain).
func geminiBareMapsURL(text string, i int) (raw string, end int, ok bool) {
	if !strings.HasPrefix(text[i:], "http://") && !strings.HasPrefix(text[i:], "https://") {
		return "", 0, false
	}
	if i > 0 && !strings.ContainsRune(" \t\n\r([", rune(text[i-1])) {
		return "", 0, false
	}
	loc := geminiMapsURLRe.FindStringIndex(text[i:])
	if loc == nil {
		return "", 0, false
	}
	candidate := geminiTrimURLTail(text[i : i+loc[1]])
	if !geminiIsMapsURL(candidate) {
		return "", 0, false
	}
	return candidate, i + len(candidate), true
}

// geminiTrimURLTail membuang tanda baca penutup yang ikut tertangkap regex
// (kurung penutup markdown, titik akhir kalimat, dan sejenisnya).
func geminiTrimURLTail(raw string) string {
	raw = strings.TrimRight(raw, ").,;:!?")
	if i := strings.IndexAny(raw, "]"); i >= 0 {
		raw = raw[:i]
	}
	return raw
}

// geminiIsMapsURL memastikan URL benar-benar milik Google Maps, supaya tautan
// lain di jawaban tidak ikut diubah menjadi pin.
func geminiIsMapsURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	path := strings.ToLower(u.Path)
	switch {
	case host == "maps.app.goo.gl", host == "maps.google.com":
		return true
	case host == "goo.gl":
		return strings.HasPrefix(path, "/maps")
	case host == "g.co":
		return strings.HasPrefix(path, "/kgs") || strings.HasPrefix(path, "/maps")
	case host == "google.com", strings.HasPrefix(host, "google."),
		strings.HasPrefix(host, "www.google."), strings.HasPrefix(host, "maps.google."),
		strings.Contains(host, ".google."):
		return strings.HasPrefix(path, "/maps")
	}
	return false
}

// geminiParseMapsPin mengurai URL Maps menjadi nama, alamat, dan koordinat.
func geminiParseMapsPin(raw, label string) geminiMapsPin {
	pin := geminiMapsPin{URL: raw, Label: geminiCleanMapsLabel(label)}
	pin.Lat, pin.Lng, pin.HasCoord = geminiMapsCoords(raw)
	pin.Name, pin.Address = geminiMapsPlaceText(raw)
	if pin.Name == "" {
		pin.Name = pin.Label
	}
	if pin.Name == "" {
		pin.Name = "Lokasi"
	}
	if pin.Address == "" && pin.HasCoord {
		pin.Address = fmt.Sprintf("%.6f, %.6f", pin.Lat, pin.Lng)
	}
	return pin
}

// geminiCleanMapsLabel merapikan label markdown agar layak jadi nama cadangan.
func geminiCleanMapsLabel(label string) string {
	label = strings.TrimSpace(label)
	label = strings.Trim(label, "*_` ")
	label = strings.TrimSpace(label)
	return label
}

// geminiMapsCoords mengambil koordinat dari URL Maps. Urutan pola mengikuti
// tingkat kepercayaan: blok data (!3d/!4d), posisi peta (/@lat,lng), blok data
// lama (!1d/!2d), lalu parameter query.
func geminiMapsCoords(raw string) (lat, lng float64, ok bool) {
	if m := geminiCoord3d4dRe.FindStringSubmatch(raw); m != nil {
		if lat, lng, ok := geminiParseCoordPair(m[1], m[2]); ok {
			return lat, lng, true
		}
	}
	if m := geminiCoordAtRe.FindStringSubmatch(raw); m != nil {
		if lat, lng, ok := geminiParseCoordPair(m[1], m[2]); ok {
			return lat, lng, true
		}
	}
	if m := geminiCoord1d2dRe.FindStringSubmatch(raw); m != nil {
		if lat, lng, ok := geminiParseCoordPair(m[2], m[1]); ok {
			return lat, lng, true
		}
	}
	if u, err := url.Parse(raw); err == nil {
		q := u.Query()
		for _, key := range []string{"ll", "q", "query", "daddr", "destination", "center", "sll"} {
			value := strings.TrimSpace(q.Get(key))
			left, right, sep := strings.Cut(value, ",")
			if !sep {
				continue
			}
			if lat, lng, ok := geminiParseCoordPair(left, right); ok {
				return lat, lng, true
			}
		}
	}
	return 0, 0, false
}

// geminiParseCoordPair memvalidasi sepasang angka agar "koordinat" palsu (0,0
// atau nilai di luar rentang) tidak pernah dikirim sebagai pin.
func geminiParseCoordPair(a, b string) (lat, lng float64, ok bool) {
	lat, err1 := strconv.ParseFloat(strings.TrimSpace(a), 64)
	lng, err2 := strconv.ParseFloat(strings.TrimSpace(b), 64)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	if lat < -90 || lat > 90 || lng < -180 || lng > 180 {
		return 0, 0, false
	}
	if lat == 0 && lng == 0 {
		return 0, 0, false
	}
	return lat, lng, true
}

// geminiMapsPlaceText mengambil nama & alamat dari path (`/maps/place/<teks>/`
// atau `/maps/dir//<tujuan>/`), lalu dari parameter query sebagai cadangan.
func geminiMapsPlaceText(raw string) (name, address string) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", ""
	}
	segments := strings.Split(u.Path, "/")
	for i, segment := range segments {
		switch strings.ToLower(segment) {
		case "place", "search", "dir":
			j := i + 1
			for j < len(segments) && strings.TrimSpace(segments[j]) == "" {
				j++
			}
			if j < len(segments) {
				if name, address := geminiSplitPlaceText(geminiDecodeMapsText(segments[j])); name != "" {
					return name, address
				}
			}
		}
	}
	q := u.Query()
	for _, key := range []string{"q", "query", "destination"} {
		value := strings.TrimSpace(q.Get(key))
		if value == "" || geminiIsCoordinateText(value) {
			continue
		}
		if name, address := geminiSplitPlaceText(value); name != "" {
			return name, address
		}
	}
	return "", ""
}

// geminiIsCoordinateText true bila isinya "lat,lng" (bukan nama tempat).
func geminiIsCoordinateText(value string) bool {
	left, right, sep := strings.Cut(value, ",")
	if !sep {
		return false
	}
	_, _, ok := geminiParseCoordPair(left, right)
	return ok
}

// geminiSplitPlaceText memecah "Nama, Alamat lengkap" menjadi dua bagian.
func geminiSplitPlaceText(value string) (name, address string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", ""
	}
	parts := strings.SplitN(value, ",", 2)
	name = strings.TrimSpace(parts[0])
	if len(parts) == 1 {
		return name, ""
	}
	return name, strings.TrimSpace(parts[1])
}

// geminiDecodeMapsText mengubah segmen path Maps ("19x+Bagel%2C+Frankfurt")
// menjadi teks biasa. QueryUnescape menangani '+' sekaligus '%xx'; kalau ada
// escape rusak, fallback hanya mengganti '+'.
func geminiDecodeMapsText(value string) string {
	if decoded, err := url.QueryUnescape(value); err == nil {
		return strings.TrimSpace(decoded)
	}
	return strings.TrimSpace(strings.ReplaceAll(value, "+", " "))
}

// geminiPinText menyusun teks pengganti untuk pin yang koordinatnya tidak
// diketahui: satu baris "label: url" yang rapi, bukan markdown bersarang.
func geminiPinText(pin geminiMapsPin) string {
	label := strings.TrimSpace(pin.Label)
	if label == "" {
		label = strings.TrimSpace(pin.Name)
	}
	link := strings.TrimSpace(pin.URL)
	if link == "" {
		return label
	}
	if label == "" || strings.Contains(label, link) {
		return link
	}
	return label + ": " + link
}

// geminiMapsShortURL menandai link pendek yang koordinatnya hanya ada di
// halaman tujuan, jadi perlu diikuti sekali.
func geminiMapsShortURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "maps.app.goo.gl" || host == "goo.gl" || host == "g.co"
}

// geminiResolveMapsURL mengikuti short link dan mengembalikan URL akhirnya.
// client boleh nil (dipakai produksi); tes mengirim client sendiri supaya bisa
// memakai httptest tanpa menyentuh jaringan.
func geminiResolveMapsURL(ctx context.Context, client *http.Client, raw string) (string, bool) {
	if client == nil {
		client = &http.Client{Timeout: geminiMapsResolveTimeout}
	}
	if req, err := http.NewRequestWithContext(ctx, http.MethodHead, raw, nil); err == nil {
		if resp, err := client.Do(req); err == nil {
			final := resp.Request.URL.String()
			resp.Body.Close()
			if final != "" && final != raw {
				return final, true
			}
		}
	}
	// Sebagian shortener menolak HEAD: coba sekali dengan GET sekecil mungkin.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("Range", "bytes=0-0")
	resp, err := client.Do(req)
	if err != nil {
		return "", false
	}
	final := resp.Request.URL.String()
	resp.Body.Close()
	if final == "" {
		return "", false
	}
	return final, true
}

// geminiResolvePin mengikuti short link lalu mengurai ulang hasilnya. Gagal =
// pin dikembalikan apa adanya (pemanggil mengirimnya sebagai teks).
func geminiResolvePin(ctx context.Context, pin geminiMapsPin) geminiMapsPin {
	rctx, cancel := context.WithTimeout(ctx, geminiMapsResolveTimeout)
	defer cancel()
	final, ok := geminiResolveMapsURL(rctx, nil, pin.URL)
	if !ok {
		return pin
	}
	if resolved := geminiParseMapsPin(final, pin.Label); resolved.HasCoord {
		return resolved
	}
	return pin
}

// geminiSendAnswer mengirim jawaban sebagai rangkaian bubble: teks, pin lokasi
// untuk setiap link Maps, lalu teks lanjutan — urutan sama dengan jawaban asli.
// Bubble pertama me-reply perintah, sisanya pesan lepas dengan jeda, mengikuti
// pola multi-bubble yang sudah dipakai jalur AI.
func geminiSendAnswer(ctx context.Context, mctx *store.MessageContext, text string) error {
	text = geminiCleanAnswer(text)
	parts := geminiSplitMapsLinks(text)
	if len(parts) == 0 {
		return fmt.Errorf("jawaban kosong")
	}

	sent := false
	seenCoord := make(map[string]bool, len(parts))
	for _, part := range parts {
		if part.Pin != nil {
			pin := *part.Pin
			if !pin.HasCoord && geminiMapsShortURL(pin.URL) {
				pin = geminiResolvePin(ctx, pin)
			}
			if !pin.HasCoord {
				for _, chunk := range geminiSplitReply(geminiPinText(pin), geminiBubbleRunes) {
					if err := geminiSendTextBubble(ctx, mctx, chunk, &sent); err != nil {
						return err
					}
				}
				continue
			}
			key := fmt.Sprintf("%.5f,%.5f", pin.Lat, pin.Lng)
			if seenCoord[key] {
				continue
			}
			seenCoord[key] = true
			if err := geminiWaitBubble(ctx, sent); err != nil {
				return err
			}
			var err error
			if sent {
				err = store.SendLocation(ctx, mctx, pin.Name, pin.Address, pin.Lat, pin.Lng)
			} else {
				err = store.ReplyLocation(ctx, mctx, pin.Name, pin.Address, pin.Lat, pin.Lng)
			}
			if err != nil {
				return err
			}
			sent = true
			continue
		}
		for _, chunk := range geminiSplitReply(part.Text, geminiBubbleRunes) {
			if err := geminiSendTextBubble(ctx, mctx, chunk, &sent); err != nil {
				return err
			}
		}
	}
	if !sent {
		return fmt.Errorf("jawaban kosong")
	}
	return nil
}

// geminiSendTextBubble mengirim satu bubble teks; bubble pertama me-reply.
func geminiSendTextBubble(ctx context.Context, mctx *store.MessageContext, text string, sent *bool) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if err := geminiWaitBubble(ctx, *sent); err != nil {
		return err
	}
	var err error
	if *sent {
		err = store.SendText(ctx, mctx, text)
	} else {
		err = store.ReplyText(ctx, mctx, text)
	}
	if err != nil {
		return err
	}
	*sent = true
	return nil
}

// geminiWaitBubble memberi jeda antar bubble supaya terasa natural (dan tidak
// menabrak batas laju WhatsApp). Bubble pertama tidak dijeda.
func geminiWaitBubble(ctx context.Context, sent bool) error {
	if !sent {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(geminiBubbleDelay):
		return nil
	}
}
