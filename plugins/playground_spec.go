package plugins

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// playMarker adalah baris pertama wajib script playground. Selain jadi
// penanda visual, baris ini dipakai handler AI (IsPlaygroundScript) supaya isi
// script — yang boleh memuat kata "flora" — tidak memicu balasan AI.
const playMarker = "#playground"

// playTapPrefix adalah awalan id tombol quick_reply/single_select yang dirakit
// playground. Tap-nya dicegat CheckPlayTap (lihat playground.go).
const playTapPrefix = "play:"

const (
	playKindInteractive = "interactive"
	playKindAirich      = "airich"
	playKindEvent       = "event"
	playKindPhone       = "phone"
)

// Batas praktik WhatsApp (label tombol 20 rune, body interactive 1024, footer
// 60). Ditegakkan sebagai ERROR, bukan peringatan: nilai kelebihan dipotong
// klien tanpa pemberitahuan, jadi lebih baik script ditolak dengan jelas.
const (
	playMaxScriptRunes = 8000
	playMaxButtons     = 10
	playMaxCards       = 10
	playMaxRows        = 10
	playMaxTitle       = 60
	playMaxBody        = 1024
	playMaxFooter      = 60
	playMaxLabel       = 20
)

// playSpec adalah satu bubble yang disusun dari script #playground.
type playSpec struct {
	kind     string
	title    string
	subtitle string
	body     string
	footer   string
	image    string
	slot     string
	buttons  []playButton
	cards    []playCard
	a2ui     []playElem
	sections []playElem
}

// playButton adalah satu tombol native_flow. arg dipakai sesuai typ (id untuk
// reply, URL untuk url, kode untuk copy, nomor untuk call); rows khusus select.
type playButton struct {
	typ   string
	label string
	arg   string
	rows  []playRow
}

// playRow adalah satu baris single_select; id kosong diisi otomatis saat render.
type playRow struct {
	title string
	desc  string
	id    string
}

// playCard adalah satu kartu carousel beserta tombolnya.
type playCard struct {
	title   string
	body    string
	buttons []playButton
}

// playElem dipakai dua daftar yang bentuknya sama: a2ui (heading/text/card/
// image) dan section airich (heading/md/image/link/html).
type playElem struct {
	typ string
	a   string
	b   string
}

// playParse membaca script jadi playSpec. Semua kesalahan sintaks
// dikumpulkan (bukan berhenti di baris pertama) supaya owner bisa membetulkan
// beberapa baris sekaligus.
func playParse(text string) (playSpec, []string) {
	sp := playSpec{kind: playKindInteractive}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	start := playMarkerLine(lines)
	if start < 0 {
		return sp, []string{"Baris pertama script harus " + playMarker}
	}

	var errs []string
	for i := start + 1; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		ln := i + 1
		switch {
		case strings.HasPrefix(line, "+"):
			errs = append(errs, playParseAdd(&sp, line, ln)...)
		case strings.HasPrefix(line, "-"):
			errs = append(errs, playParseDel(&sp, line, ln)...)
		default:
			errs = append(errs, playParseField(&sp, line, ln)...)
		}
	}
	return sp, errs
}

// playMarkerLine mengembalikan indeks baris pertama non-kosong bila baris itu
// adalah #playground, atau -1. Baris kosong di atas marker ditoleransi karena
// WhatsApp kadang menyertakan newline pembuka.
func playMarkerLine(lines []string) int {
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		if t == "" {
			continue
		}
		if strings.EqualFold(t, playMarker) {
			return i
		}
		return -1
	}
	return -1
}

// playHasMarker melaporkan apakah teks dimulai dengan #playground. Dipakai
// parser dan penjaga AI.
func playHasMarker(text string) bool {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	return playMarkerLine(lines) >= 0
}

// playParseField menangani baris `kunci: nilai`.
func playParseField(sp *playSpec, line string, ln int) []string {
	key, val, ok := strings.Cut(line, ":")
	if !ok {
		return []string{playErrf(ln, "baris tidak dikenal: %q (pakai `kunci: nilai`, `+ elemen ...`, atau `- elemen N`)", line)}
	}
	key = strings.ToLower(strings.TrimSpace(key))
	val = strings.TrimSpace(val)
	switch key {
	case "kind":
		sp.kind = strings.ToLower(val)
	case "title":
		sp.title = val
	case "subtitle":
		sp.subtitle = val
	case "body":
		sp.body = val
	case "footer":
		sp.footer = val
	case "image":
		sp.image = val
	case "slot":
		sp.slot = strings.ToLower(val)
	default:
		return []string{playErrf(ln, "kunci `%s` tidak dikenal (sah: kind, title, subtitle, body, footer, image, slot)", key)}
	}
	return nil
}

// playParseAdd menangani baris `+ elemen ...`.
func playParseAdd(sp *playSpec, line string, ln int) []string {
	segs := playSplit(strings.TrimPrefix(line, "+"))
	head := strings.Fields(segs[0])
	if len(head) == 0 {
		return []string{playErrf(ln, "elemen kosong (sah: + button, + row, + card, + a2ui, + section)")}
	}
	args := segs[1:]

	switch head[0] {
	case "button":
		if len(head) < 2 {
			return []string{playErrf(ln, "format: + button <tipe> | label | argumen")}
		}
		btn, errs := playParseButton(head[1], args, ln)
		if errs != nil {
			return errs
		}
		if len(sp.cards) > 0 {
			last := &sp.cards[len(sp.cards)-1]
			last.buttons = append(last.buttons, btn)
		} else {
			sp.buttons = append(sp.buttons, btn)
		}
		return nil
	case "row":
		return playAppendRow(sp, args, ln)
	case "card":
		if len(args) == 0 || args[0] == "" {
			return []string{playErrf(ln, "format: + card | judul | isi")}
		}
		card := playCard{title: args[0]}
		if len(args) > 1 {
			card.body = args[1]
		}
		sp.cards = append(sp.cards, card)
		return nil
	case "a2ui":
		if len(head) < 2 {
			return []string{playErrf(ln, "format: + a2ui <heading|text|card|image> | isi")}
		}
		el, errs := playParseElem(head[1], args, ln, true)
		if errs != nil {
			return errs
		}
		sp.a2ui = append(sp.a2ui, el)
		return nil
	case "section":
		if len(head) < 2 {
			return []string{playErrf(ln, "format: + section <heading|md|image|link|html> | isi")}
		}
		el, errs := playParseElem(head[1], args, ln, false)
		if errs != nil {
			return errs
		}
		sp.sections = append(sp.sections, el)
		return nil
	default:
		return []string{playErrf(ln, "elemen `%s` tidak dikenal (sah: button, row, card, a2ui, section)", head[0])}
	}
}

// playParseButton membaca tipe + argumen satu tombol.
func playParseButton(typ string, args []string, ln int) (playButton, []string) {
	b := playButton{typ: strings.ToLower(typ)}
	need := func(n int, usage string) []string {
		if len(args) < n {
			return []string{playErrf(ln, "format: %s", usage)}
		}
		return nil
	}

	switch b.typ {
	case "reply":
		if e := need(1, "+ button reply | label | id"); e != nil {
			return b, e
		}
		b.label = args[0]
		if len(args) > 1 {
			b.arg = args[1]
		}
	case "url":
		if e := need(2, "+ button url | label | https://..."); e != nil {
			return b, e
		}
		b.label, b.arg = args[0], args[1]
	case "copy":
		if e := need(2, "+ button copy | label | kode"); e != nil {
			return b, e
		}
		b.label, b.arg = args[0], args[1]
	case "call":
		if e := need(2, "+ button call | label | 62812..."); e != nil {
			return b, e
		}
		b.label, b.arg = args[0], args[1]
	case "location":
		if e := need(1, "+ button location | label"); e != nil {
			return b, e
		}
		b.label = args[0]
	case "signup":
	case "select":
		if e := need(1, "+ button select | judul"); e != nil {
			return b, e
		}
		b.label = args[0]
	default:
		return b, []string{playErrf(ln, "tipe tombol `%s` tidak dikenal (reply|url|copy|call|location|signup|select)", typ)}
	}
	return b, nil
}

// playAppendRow menempelkan baris single_select ke tombol select terakhir.
func playAppendRow(sp *playSpec, args []string, ln int) []string {
	list := playTargetButtons(sp)
	if len(*list) == 0 || (*list)[len(*list)-1].typ != "select" {
		return []string{playErrf(ln, "`+ row` harus ditulis setelah `+ button select | judul`")}
	}
	if len(args) == 0 || args[0] == "" {
		return []string{playErrf(ln, "format: + row | judul | deskripsi | id")}
	}
	row := playRow{title: args[0]}
	if len(args) > 1 {
		row.desc = args[1]
	}
	if len(args) > 2 {
		row.id = args[2]
	}
	last := &(*list)[len(*list)-1]
	last.rows = append(last.rows, row)
	return nil
}

// playParseElem membaca elemen a2ui (isA2UI) atau section airich.
func playParseElem(typ string, args []string, ln int, isA2UI bool) (playElem, []string) {
	el := playElem{typ: strings.ToLower(typ)}
	need := func(n int, usage string) []string {
		if len(args) < n {
			return []string{playErrf(ln, "format: %s", usage)}
		}
		return nil
	}

	if isA2UI {
		switch el.typ {
		case "heading", "text", "image":
			if e := need(1, "+ a2ui "+el.typ+" | isi"); e != nil {
				return el, e
			}
			el.a = args[0]
		case "card":
			if e := need(2, "+ a2ui card | judul | deskripsi"); e != nil {
				return el, e
			}
			el.a, el.b = args[0], args[1]
		default:
			return el, []string{playErrf(ln, "tipe a2ui `%s` tidak dikenal (heading|text|card|image)", typ)}
		}
		return el, nil
	}

	switch el.typ {
	case "heading", "md", "image":
		if e := need(1, "+ section "+el.typ+" | isi"); e != nil {
			return el, e
		}
		el.a = args[0]
	case "link":
		if e := need(2, "+ section link | label | https://..."); e != nil {
			return el, e
		}
		el.a, el.b = args[0], args[1]
	case "html":
		if e := need(2, "+ section html | judul | popioo|soundtest|url:https://..."); e != nil {
			return el, e
		}
		el.a, el.b = args[0], args[1]
	default:
		return el, []string{playErrf(ln, "tipe section `%s` tidak dikenal (heading|md|image|link|html)", typ)}
	}
	return el, nil
}

// playParseDel menangani baris `- elemen N` (N berbasis 1).
func playParseDel(sp *playSpec, line string, ln int) []string {
	parts := strings.Fields(strings.TrimPrefix(line, "-"))
	if len(parts) != 2 {
		return []string{playErrf(ln, "format: - button N | - card N | - a2ui N | - section N")}
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil || n < 1 {
		return []string{playErrf(ln, "nomor elemen harus 1 atau lebih")}
	}
	drop := func(total int, remove func()) []string {
		if n > total {
			return []string{playErrf(ln, "%s ke-%d tidak ada (jumlah %d)", parts[0], n, total)}
		}
		remove()
		return nil
	}

	switch strings.ToLower(parts[0]) {
	case "button":
		list := playTargetButtons(sp)
		return drop(len(*list), func() {
			*list = append((*list)[:n-1], (*list)[n:]...)
		})
	case "card":
		return drop(len(sp.cards), func() {
			sp.cards = append(sp.cards[:n-1], sp.cards[n:]...)
		})
	case "a2ui":
		return drop(len(sp.a2ui), func() {
			sp.a2ui = append(sp.a2ui[:n-1], sp.a2ui[n:]...)
		})
	case "section":
		return drop(len(sp.sections), func() {
			sp.sections = append(sp.sections[:n-1], sp.sections[n:]...)
		})
	default:
		return []string{playErrf(ln, "elemen `%s` tidak bisa dihapus (button|card|a2ui|section)", parts[0])}
	}
}

// playTargetButtons mengembalikan daftar tombol aktif: tombol kartu terakhir
// bila sudah ada kartu, selain itu tombol bubble. Aturannya sama dengan
// `+ button`, supaya `+ button` dan `- button` tidak menunjuk daftar berbeda.
func playTargetButtons(sp *playSpec) *[]playButton {
	if len(sp.cards) > 0 {
		return &sp.cards[len(sp.cards)-1].buttons
	}
	return &sp.buttons
}

// playSplit memecah argumen per `|`. Pipe literal ditulis `\|` dan spasi di
// tepi tiap bagian dibuang, supaya label berspasi tidak perlu tanda kutip.
func playSplit(s string) []string {
	var out []string
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' && i+1 < len(s) && s[i+1] == '|' {
			b.WriteByte('|')
			i++
			continue
		}
		if c == '|' {
			out = append(out, strings.TrimSpace(b.String()))
			b.Reset()
			continue
		}
		b.WriteByte(c)
	}
	out = append(out, strings.TrimSpace(b.String()))
	return out
}

// playErrf membentuk satu kesalahan ber-nomor baris.
func playErrf(ln int, format string, args ...any) string {
	return fmt.Sprintf("Baris %d: %s", ln, fmt.Sprintf(format, args...))
}

// playValidate memeriksa spec sebelum dirender. Balikan kosong = siap kirim.
func playValidate(sp *playSpec) []string {
	if sp.kind == "" {
		sp.kind = playKindInteractive
	}
	switch sp.kind {
	case playKindInteractive:
		return playValidateInteractive(sp)
	case playKindAirich:
		return playValidateAirich(sp)
	case playKindEvent:
		return playValidateEvent(sp)
	case playKindPhone:
		return playValidatePhone(sp)
	default:
		return []string{fmt.Sprintf("kind `%s` tidak dikenal (interactive|airich|event|phone)", sp.kind)}
	}
}

func playValidateInteractive(sp *playSpec) []string {
	var errs []string
	if strings.TrimSpace(sp.body) == "" {
		errs = append(errs, "`body:` wajib diisi untuk kind interactive")
	}
	errs = append(errs, playCheckLength("title", sp.title, playMaxTitle)...)
	errs = append(errs, playCheckLength("subtitle", sp.subtitle, playMaxTitle)...)
	errs = append(errs, playCheckLength("body", sp.body, playMaxBody)...)
	errs = append(errs, playCheckLength("footer", sp.footer, playMaxFooter)...)
	errs = append(errs, playCheckURL(sp.image, "image")...)

	slot := sp.slot
	if slot == "" {
		slot = "buttons"
		if len(sp.cards) > 0 {
			slot = "carousel"
		}
	}
	switch slot {
	case "buttons":
		if len(sp.cards) > 0 {
			errs = append(errs, fmt.Sprintf("%d kartu tidak terpakai: pakai `slot: carousel`", len(sp.cards)))
		}
		if len(sp.buttons) == 0 && len(sp.a2ui) == 0 {
			errs = append(errs, "bubble interactive butuh minimal satu tombol, `+ a2ui`, atau kartu")
		}
		errs = append(errs, playValidateButtons(sp.buttons, "bubble")...)
	case "carousel":
		if len(sp.cards) == 0 {
			errs = append(errs, "`slot: carousel` butuh minimal satu `+ card`")
		}
		if len(sp.cards) > playMaxCards {
			errs = append(errs, fmt.Sprintf("%d kartu (maks %d)", len(sp.cards), playMaxCards))
		}
		if len(sp.buttons) > 0 {
			errs = append(errs, fmt.Sprintf("%d tombol di luar kartu: tulis `+ button` setelah `+ card`", len(sp.buttons)))
		}
		for i, c := range sp.cards {
			label := fmt.Sprintf("kartu %d", i+1)
			if strings.TrimSpace(c.title) == "" {
				errs = append(errs, label+": judul wajib")
			}
			errs = append(errs, playValidateButtons(c.buttons, label)...)
		}
	default:
		errs = append(errs, fmt.Sprintf("slot `%s` tidak dikenal (buttons|carousel)", sp.slot))
	}

	errs = append(errs, playValidateA2UI(sp.a2ui)...)
	return errs
}

func playValidateButtons(list []playButton, where string) []string {
	var errs []string
	if len(list) > playMaxButtons {
		errs = append(errs, fmt.Sprintf("%s: %d tombol (maks %d)", where, len(list), playMaxButtons))
	}
	for i, b := range list {
		label := fmt.Sprintf("%s tombol %d", where, i+1)
		if b.typ != "signup" && strings.TrimSpace(b.label) == "" {
			errs = append(errs, label+": label wajib")
		}
		if n := utf8.RuneCountInString(b.label); n > playMaxLabel {
			errs = append(errs, fmt.Sprintf("%s: label %d rune (maks %d)", label, n, playMaxLabel))
		}
		switch b.typ {
		case "url":
			errs = append(errs, playCheckURL(b.arg, label+" url")...)
		case "call":
			if !playDigitsOnly(b.arg) {
				errs = append(errs, label+": nomor telepon harus digit (contoh 628123456789)")
			}
		case "select":
			if len(b.rows) == 0 {
				errs = append(errs, label+": select butuh minimal satu `+ row`")
			}
			if len(b.rows) > playMaxRows {
				errs = append(errs, fmt.Sprintf("%s: %d baris (maks %d)", label, len(b.rows), playMaxRows))
			}
			for j, r := range b.rows {
				if strings.TrimSpace(r.title) == "" {
					errs = append(errs, fmt.Sprintf("%s row %d: judul wajib", label, j+1))
				}
			}
		}
	}
	return errs
}

func playValidateA2UI(list []playElem) []string {
	var errs []string
	for i, el := range list {
		label := fmt.Sprintf("a2ui %d", i+1)
		if strings.TrimSpace(el.a) == "" {
			errs = append(errs, label+": isi wajib")
		}
		if el.typ == "card" && strings.TrimSpace(el.b) == "" {
			errs = append(errs, label+": kartu butuh deskripsi")
		}
		if el.typ == "image" {
			errs = append(errs, playCheckURL(el.a, label)...)
		}
	}
	return errs
}

func playValidateAirich(sp *playSpec) []string {
	var errs []string
	if sp.subtitle != "" {
		errs = append(errs, "kind airich tidak memakai `subtitle:`")
	}
	if sp.footer != "" {
		errs = append(errs, "kind airich tidak memakai `footer:` (pakai `+ section link` atau `+ section md`)")
	}
	if sp.slot != "" {
		errs = append(errs, "kind airich tidak memakai `slot:`")
	}
	if len(sp.buttons) > 0 || len(sp.cards) > 0 || len(sp.a2ui) > 0 {
		errs = append(errs, "kind airich hanya memakai `+ section`")
	}
	if sp.title == "" && sp.body == "" && sp.image == "" && len(sp.sections) == 0 {
		errs = append(errs, "kind airich butuh minimal satu elemen (title, body, image, atau `+ section`)")
	}
	errs = append(errs, playCheckLength("title", sp.title, playMaxTitle)...)
	errs = append(errs, playCheckLength("body", sp.body, playMaxBody)...)
	errs = append(errs, playCheckURL(sp.image, "image")...)

	for i, el := range sp.sections {
		label := fmt.Sprintf("section %d", i+1)
		if strings.TrimSpace(el.a) == "" {
			errs = append(errs, label+": isi wajib")
		}
		switch el.typ {
		case "image":
			errs = append(errs, playCheckURL(el.a, label)...)
		case "link":
			errs = append(errs, playCheckURL(el.b, label+" url")...)
		case "html":
			if !playHTMLSourceOK(el.b) {
				errs = append(errs, label+": sumber html harus popioo, soundtest, atau `url:https://...`")
			}
			if strings.HasPrefix(el.b, "url:") {
				errs = append(errs, playCheckURL(strings.TrimPrefix(el.b, "url:"), label+" url")...)
			}
		}
	}
	return errs
}

func playValidateEvent(sp *playSpec) []string {
	var errs []string
	if strings.TrimSpace(sp.title) == "" {
		errs = append(errs, "kind event butuh `title:`")
	}
	if sp.subtitle != "" || sp.body != "" || sp.footer != "" || sp.slot != "" {
		errs = append(errs, "kind event hanya memakai `title:` dan `image:`")
	}
	if len(sp.buttons) > 0 || len(sp.cards) > 0 || len(sp.a2ui) > 0 || len(sp.sections) > 0 {
		errs = append(errs, "kind event hanya memakai `title:` dan `image:`")
	}
	errs = append(errs, playCheckLength("title", sp.title, playMaxTitle)...)
	errs = append(errs, playCheckURL(sp.image, "image")...)
	return errs
}

func playValidatePhone(sp *playSpec) []string {
	if sp.title != "" || sp.subtitle != "" || sp.body != "" || sp.footer != "" || sp.image != "" ||
		sp.slot != "" || len(sp.buttons) > 0 || len(sp.cards) > 0 ||
		len(sp.a2ui) > 0 || len(sp.sections) > 0 {
		return []string{"kind phone tidak memakai field lain, cukup `kind: phone`"}
	}
	return nil
}

func playCheckLength(label, val string, max int) []string {
	if n := utf8.RuneCountInString(val); n > max {
		return []string{fmt.Sprintf("%s: %d rune (maks %d)", label, n, max)}
	}
	return nil
}

func playCheckURL(raw, label string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		return []string{label + ": URL harus diawali http:// atau https://"}
	}
	return nil
}

func playDigitsOnly(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// playHTMLSourceOK memeriksa sumber `+ section html`. Template popioo/
// soundtest dipakai ulang dari plugin Dev supaya owner tidak perlu menempel
// HTML puluhan KB di WhatsApp.
func playHTMLSourceOK(src string) bool {
	switch src {
	case "popioo", "soundtest":
		return true
	}
	return strings.HasPrefix(src, "url:") && len(strings.TrimPrefix(src, "url:")) > 0
}
