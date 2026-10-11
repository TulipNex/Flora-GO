// Package tableimage me-render tabel klasemen menjadi PNG (pure-Go, tanpa
// cgo): x/image + font OpenType Go yang di-vendor di fonts/. Dipakai
// `.klasemen` supaya tidak terbentur batas ~25 karakter per baris di bubble
// teks WhatsApp.
package tableimage

import (
	"bytes"
	"context"
	"embed"
	"image"
	"image/color"
	"image/png"
	"sync"

	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

func encodePNG(img image.Image) []byte {
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, img); err != nil {
		return nil
	}
	return buf.Bytes()
}

//go:embed fonts/Montserrat-SemiBold.ttf fonts/Montserrat-Bold.ttf
var fontFS embed.FS

//go:embed logos/*.png
var logoFS embed.FS

var leagueLogoCache = struct {
	sync.Mutex
	m map[string]image.Image
}{m: make(map[string]image.Image)}

// LeagueLogo mengembalikan logo liga (decode sekali, lalu cache).
// Kode = kode ESPN ("eng.1", ..., "uefa.champions"). nil bila tak ada.
func LeagueLogo(code string) image.Image {
	leagueLogoCache.Lock()
	defer leagueLogoCache.Unlock()
	if img, ok := leagueLogoCache.m[code]; ok {
		return img
	}
	raw, err := logoFS.ReadFile("logos/" + code + ".png")
	if err != nil {
		return nil
	}
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil
	}
	leagueLogoCache.m[code] = src
	return src
}

// Font default renderer (Montserrat); override via SetFonts untuk sampling.
const (
	fontRegularName = "fonts/Montserrat-SemiBold.ttf"
	fontBoldName    = "fonts/Montserrat-Bold.ttf"
)

// Override font kustom (dipakai sampling/varian). nil = kembali ke embedded.
var (
	fontOverrideMu sync.RWMutex
	fontRegularOvr []byte
	fontBoldOvr    []byte
)

// SetFonts menimpa font renderer (nil = embedded Go). Dipakai untuk
// mencoba font kustom tanpa mengubah file ter-vendor. Selalu mengosongkan
// cache face karena kunci cache tidak membedakan identitas font.
func SetFonts(regular, bold []byte) {
	fontOverrideMu.Lock()
	fontRegularOvr = regular
	fontBoldOvr = bold
	fontOverrideMu.Unlock()
	faceCache.Lock()
	faceCache.m = make(map[float64]font.Face)
	faceCache.Unlock()
	boldCache.Lock()
	boldCache.m = make(map[float64]font.Face)
	boldCache.Unlock()
}

var (
	faceCache = struct {
		sync.Mutex
		m map[float64]font.Face
	}{m: make(map[float64]font.Face)}
	boldCache = struct {
		sync.Mutex
		m map[float64]font.Face
	}{m: make(map[float64]font.Face)}
)

func face(size float64, bold bool) font.Face {
	var c *struct {
		sync.Mutex
		m map[float64]font.Face
	}
	name := fontRegularName
	if bold {
		c, name = &boldCache, fontBoldName
	} else {
		c = &faceCache
	}
	// Override membatalkan cache (face lama milik font lain).
	fontOverrideMu.RLock()
	var ovr []byte
	if bold {
		ovr = fontBoldOvr
	} else {
		ovr = fontRegularOvr
	}
	fontOverrideMu.RUnlock()
	cacheKey := size
	if ovr != nil {
		cacheKey = -size
		if bold {
			cacheKey -= 0.5
		}
	}
	c.Lock()
	defer c.Unlock()
	if f, ok := c.m[cacheKey]; ok {
		return f
	}
	raw := ovr
	if raw == nil {
		var err error
		raw, err = fontFS.ReadFile(name)
		if err != nil {
			panic(err)
		}
	}
	ft, err := opentype.Parse(raw)
	if err != nil {
		panic(err)
	}
	f, err := opentype.NewFace(ft, &opentype.FaceOptions{Size: size, DPI: 72})
	if err != nil {
		panic(err)
	}
	c.m[cacheKey] = f
	return f
}

// Zone adalah kategori zona klasemen satu baris.
type Zone int

const (
	ZoneNone Zone = iota
	ZoneUCL
	ZoneUEL
	ZonePO
	ZoneRele
	ZoneElim
)

// Color mengembalikan warna bilah zona (nil = tanpa bilah).
func (z Zone) Color() color.Color {
	switch z {
	case ZoneUCL:
		return ZoneUCLColor
	case ZoneUEL:
		return ZoneUELColor
	case ZonePO:
		return ZonePOColor
	case ZoneRele:
		return ZoneReleColor
	case ZoneElim:
		return ZoneElimColor
	default:
		return nil
	}
}

// Label mengembalikan teks legenda zona.
func (z Zone) Label() string {
	switch z {
	case ZoneUCL:
		return "UCL"
	case ZoneUEL:
		return "UEL"
	case ZonePO:
		return "Playoff"
	case ZoneRele:
		return "Degradasi"
	case ZoneElim:
		return "Eliminasi"
	default:
		return ""
	}
}

// StandingRow adalah satu baris tabel. ZoneNone = tanpa bilah warna.
type StandingRow struct {
	Pos  int
	Team string
	M    string
	W    string
	D    string
	L    string
	GF   string
	GA   string
	GD   string
	Pts  string
	Zone Zone
	Logo []byte // PNG tim (boleh kosong — digambar tanpa logo)
}

var (
	cInk      = color.RGBA{0x1f, 0x2a, 0x37, 0xff}
	cMuted    = color.RGBA{0x6b, 0x77, 0x8a, 0xff}
	cHeaderBg = color.RGBA{0x19, 0x64, 0x3d, 0xff} // 19643D hijau tua
	cHeadRow  = color.RGBA{0xf2, 0xd3, 0x8d, 0xff} // F2D38D krem
	cHeadInk  = color.RGBA{0x19, 0x64, 0x3d, 0xff} // teks kepala krem-bg
	cCream    = color.RGBA{0xf2, 0xd3, 0x8d, 0xff} // F2D38D teks judul
	cAccent   = color.RGBA{0xda, 0x45, 0x1f, 0xff} // DA451F oranye (Pts + badge)
	cZebra    = color.RGBA{0xf6, 0xf8, 0xfa, 0xff}
	cWhite    = color.RGBA{0xff, 0xff, 0xff, 0xff}
	cGrid     = color.RGBA{0xd9, 0xe0, 0xe8, 0xff}

	ZoneUCLColor  = color.RGBA{0x22, 0xa0, 0x6b, 0xff}
	ZoneUELColor  = color.RGBA{0x2f, 0x80, 0xed, 0xff}
	ZonePOColor   = color.RGBA{0xe2, 0xa6, 0x3d, 0xff}
	ZoneReleColor = color.RGBA{0xe5, 0x48, 0x4d, 0xff}
	ZoneElimColor = color.RGBA{0xe5, 0x48, 0x4d, 0xff}
)

// zoneOrder adalah urutan tampil legenda footer.
var zoneOrder = []Zone{ZoneUCL, ZoneUEL, ZonePO, ZoneRele, ZoneElim}

// renderScale mengalikan seluruh layout + font: 1 = 900px (hemat),
// 2 = 1800px (tajam setelah transcode server WA yang batas 1600px).
const renderScale = 2

const (
	imgW     = 1100 * renderScale
	pad      = 28 * renderScale
	titleH   = 150 * renderScale
	headH    = 58 * renderScale
	rowH     = 66 * renderScale
	logoSize = 46 * renderScale
	colPos   = 66 * renderScale
	colLogo  = 58 * renderScale
	colM     = 58 * renderScale
	colW     = 58 * renderScale
	colD     = 58 * renderScale
	colL     = 58 * renderScale
	colGF    = 82 * renderScale
	colGA    = 82 * renderScale
	colGD    = 82 * renderScale
	colPts   = 92 * renderScale
	colZone  = 12 * renderScale
	gap      = 10 * renderScale
	swatch   = 22 * renderScale

	leagueLogoBox = 110 * renderScale
	footLineH     = 46 * renderScale
	footPadV      = 22 * renderScale

	// Layout tetap blok brand footer (dari playground user, px skala 2x,
	// origin kiri-atas footer, dijangkar atas).
	ftFloraX  = 1048
	ftFloraY  = 110
	ftFloraH  = 168
	ftDivX    = 1695
	ftDivY    = 115
	ftDivLen  = 169
	ftDivW    = 6
	ftPowX    = 1740
	ftPowY    = 97
	ftPowFont = 48.0
	ftEspnX   = 1736
	ftEspnY   = 173
	ftEspnH   = 111
	// Garis bawah konten brand (divider/espn di 284, flora 278).
	ftBrandBottom = 284
)

func fillRect(dst *image.RGBA, x0, y0, x1, y1 int, c color.Color) {
	r := image.Rect(x0, y0, x1, y1).Intersect(dst.Bounds())
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			dst.Set(x, y, c)
		}
	}
}

func hline(dst *image.RGBA, x0, x1, y int, c color.Color) {
	for x := x0; x < x1; x++ {
		dst.Set(x, y, c)
	}
}

// drawText menggambar teks dengan anchor kiri pada (x, centerY).
// Baseline = centerY + (ascent-descent)/2 sehingga tengah visual teks
// (dari puncak ascender ke dasar descender) tepat di centerY.
func drawText(dst *image.RGBA, f font.Face, s string, x, centerY int, c color.Color) {
	m := f.Metrics()
	y := centerY + (m.Ascent.Round()-m.Descent.Round())/2
	d := &font.Drawer{Dst: dst, Src: image.NewUniform(c), Face: f,
		Dot: fixed.Point26_6{X: fixed.I(x), Y: fixed.I(y)}}
	d.DrawString(s)
}

func textWidth(f font.Face, s string) int {
	return font.MeasureString(f, s).Round()
}

// fitTeam memotong nama tim + "…" bila melebihi lebar kolom.
func fitTeam(f font.Face, name string, maxW int) string {
	if textWidth(f, name) <= maxW {
		return name
	}
	r := []rune(name)
	for len(r) > 0 {
		r = r[:len(r)-1]
		if textWidth(f, string(r)+"…") <= maxW {
			return string(r) + "…"
		}
	}
	return ""
}

// drawTextCenter menggambar teks rata tengah dalam [x0,x1) pada centerY.
func drawTextCenter(dst *image.RGBA, f font.Face, s string, x0, x1, centerY int, c color.Color) {
	w := textWidth(f, s)
	x := x0 + (x1-x0-w)/2
	if x < x0 {
		x = x0
	}
	drawText(dst, f, s, x, centerY, c)
}

func decodeLogo(raw []byte, size int) image.Image {
	if len(raw) == 0 {
		return nil
	}
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil
	}
	b := src.Bounds()
	if b.Dx() == 0 || b.Dy() == 0 {
		return nil
	}
	// Skala proporsional ke kotak size×size.
	scale := float64(size) / float64(max(b.Dx(), b.Dy()))
	dw, dh := int(float64(b.Dx())*scale+0.5), int(float64(b.Dy())*scale+0.5)
	if dw < 1 {
		dw = 1
	}
	if dh < 1 {
		dh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	return dst
}

// RenderStandings me-render tabel klasemen penuh menjadi PNG (skala
// renderScale, encoder BestCompression). Legenda zona digambar di footer
// gambar (kotak warna + label — font Go tanpa glyph emoji); caption teks
// pemanggil cukup membawa judul + sumber.
func RenderStandings(_ context.Context, leagueCode, title, subtitle string, rows []StandingRow) []byte {
	colTeam := imgW - 2*pad - (colZone + colPos + colLogo + colM + colW + colD + colL + colGF + colGA + colGD + colPts) - 10*gap
	items := legendItems(rows)
	footH := footPadV*2 + len(items)*footLineH
	// Blok brand kanan layout TETAP (playground user): konten paling
	// bawah y=289 → footer menampungnya + margin.
	if btm := ftBrandBottom + footPadV; btm > footH {
		footH = btm
	}
	h := titleH + headH + len(rows)*rowH + footH
	dst := image.NewRGBA(image.Rect(0, 0, imgW, h))
	fillRect(dst, 0, 0, imgW, h, cWhite)

	// Bilah judul: logo liga kiri + nama/musim di sampingnya.
	fillRect(dst, 0, 0, imgW, titleH, cHeaderBg)
	tx := pad
	if logo := LeagueLogo(leagueCode); logo != nil {
		scaled := fitBox(logo, leagueLogoBox, leagueLogoBox)
		lb := scaled.Bounds()
		lx := pad + (leagueLogoBox-lb.Dx())/2
		ly := (titleH - lb.Dy()) / 2
		draw.Draw(dst, image.Rect(lx, ly, lx+lb.Dx(), ly+lb.Dy()), scaled, lb.Min, draw.Over)
		tx = pad + leagueLogoBox + gap*2
	}
	drawText(dst, face(40*renderScale, true), title, tx, titleH/2-14*renderScale, cCream)
	drawText(dst, face(25*renderScale, false), subtitle, tx, titleH/2+30*renderScale, creamDim())

	// Baris kepala kolom.
	y := titleH
	fillRect(dst, 0, y, imgW, y+headH, cHeadRow)
	hline(dst, 0, imgW, y+headH-1, cGrid)
	fHead := face(24*renderScale, true)
	x := pad + colZone + gap
	drawTextCenter(dst, fHead, "No", x, x+colPos, y+headH/2, cHeadInk)
	x += colPos + gap + colLogo + gap
	drawText(dst, fHead, "Klub", x, y+headH/2, cHeadInk)
	x += colTeam + gap
	heads := []struct {
		label string
		w     int
	}{{"M", colM}, {"W", colW}, {"D", colD}, {"L", colL}, {"GF", colGF}, {"GA", colGA}, {"GD", colGD}, {"Pts", colPts}}
	for _, hh := range heads {
		drawTextCenter(dst, fHead, hh.label, x, x+hh.w, y+headH/2, cHeadInk)
		x += hh.w + gap
	}

	// Baris tim.
	fTeam := face(29*renderScale, false)
	fStat := face(27*renderScale, false)
	fPts := face(30*renderScale, true)
	y += headH
	for i, r := range rows {
		if i%2 == 1 {
			fillRect(dst, 0, y, imgW, y+rowH, cZebra)
		}
		hline(dst, 0, imgW, y+rowH-1, cGrid)
		if zc := r.Zone.Color(); zc != nil {
			fillRect(dst, pad, y+8*renderScale, pad+colZone, y+rowH-8*renderScale, zc)
		}
		cy := y + rowH/2
		x := pad + colZone + gap
		drawTextCenter(dst, fStat, itoa(r.Pos), x, x+colPos, cy, cMuted)
		x += colPos + gap
		if logo := decodeLogo(r.Logo, logoSize); logo != nil {
			lb := logo.Bounds()
			lx := x + (colLogo-lb.Dx())/2
			ly := y + (rowH-lb.Dy())/2
			draw.Draw(dst, image.Rect(lx, ly, lx+lb.Dx(), ly+lb.Dy()), logo, lb.Min, draw.Over)
		}
		x += colLogo + gap
		drawText(dst, fTeam, fitTeam(fTeam, r.Team, colTeam), x, cy, cInk)
		x += colTeam + gap
		stats := []struct {
			v string
			w int
		}{{r.M, colM}, {r.W, colW}, {r.D, colD}, {r.L, colL}, {r.GF, colGF}, {r.GA, colGA}, {r.GD, colGD}}
		for _, st := range stats {
			drawTextCenter(dst, fStat, st.v, x, x+st.w, cy, cInk)
			x += st.w + gap
		}
		drawTextCenter(dst, fPts, r.Pts, x, x+colPts, cy, cAccent)
		y += rowH
	}
	drawLegend(dst, rows, y, footH)
	return encodePNG(dst)
}

// legendItem adalah satu entri legenda footer (kotak warna + label).
type legendItem struct {
	label string
	c     color.Color
}

// legendItems mengembalikan zona yang benar-benar muncul di tabel
// (EPL: UCL/UEL/Degradasi; UCL: UCL/Playoff/Eliminasi).
func legendItems(rows []StandingRow) []legendItem {
	var items []legendItem
	for _, z := range zoneOrder {
		for _, r := range rows {
			if r.Zone == z {
				items = append(items, legendItem{label: z.Label(), c: z.Color()})
				break
			}
		}
	}
	if len(items) == 0 {
		for _, z := range []Zone{ZoneUCL, ZoneUEL, ZonePO, ZoneRele} {
			items = append(items, legendItem{label: z.Label(), c: z.Color()})
		}
	}
	return items
}

// creamDim adalah krem 70% untuk teks sekunder di header hijau.
func creamDim() color.Color {
	return color.RGBA{0xf2, 0xd3, 0x8d, 0xb3}
}

// fitBox menskala gambar proporsional agar muat dalam kotak maxW×maxH.
func fitBox(src image.Image, maxW, maxH int) image.Image {
	b := src.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return nil
	}
	scale := float64(maxW) / float64(b.Dx())
	if s := float64(maxH) / float64(b.Dy()); s < scale {
		scale = s
	}
	if scale >= 1 {
		return src
	}
	dw, dh := int(float64(b.Dx())*scale+0.5), int(float64(b.Dy())*scale+0.5)
	if dw < 1 {
		dw = 1
	}
	if dh < 1 {
		dh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	return dst
}

// drawLegend menggambar footer: legenda vertikal kiri + badge ESPN
// kanan-bawah. Font tanpa glyph emoji → penanda berupa kotak warna.
func drawLegend(dst *image.RGBA, rows []StandingRow, y, footH int) {
	hline(dst, 0, imgW, y, cGrid)
	items := legendItems(rows)
	f := face(24*renderScale, false)
	x := pad
	top := y + (footH-len(items)*footLineH)/2
	for _, it := range items {
		cy := top + footLineH/2
		fillRect(dst, x, cy-swatch/2, x+swatch, cy+swatch/2, it.c)
		drawText(dst, f, it.label, x+swatch+12*renderScale, cy, cMuted)
		top += footLineH
	}
	// Blok brand kanan: LAYOUT TETAP dari playground user (koordinat
	// footer-lokal, px skala 2x; dijangkar ke ATAS footer). Kedua file
	// logo berlatar hitam → dihapus jadi transparan (flood-fill tepi).
	// Logo hilang = elemen itu dilewati (bukan error).
	flora := fitHeight(stripNearBlack(LeagueLogo("flora")), ftFloraH)
	if flora != nil {
		fb := flora.Bounds()
		draw.Draw(dst, image.Rect(ftFloraX, y+ftFloraY, ftFloraX+fb.Dx(), y+ftFloraY+fb.Dy()),
			flora, fb.Min, draw.Over)
	}
	for i := 0; i < ftDivW; i++ {
		for yy := y + ftDivY; yy < y+ftDivY+ftDivLen; yy++ {
			dst.Set(ftDivX+i, yy, cGrid)
		}
	}
	lf := face(ftPowFont, false)
	lm := lf.Metrics()
	drawText(dst, lf, "Powered by :", ftPowX,
		y+ftPowY+(lm.Ascent.Round()+lm.Descent.Round())/2, cMuted)
	if espn := fitHeight(stripNearBlack(LeagueLogo("espn")), ftEspnH); espn != nil {
		eb := espn.Bounds()
		draw.Draw(dst, image.Rect(ftEspnX, y+ftEspnY, ftEspnX+eb.Dx(), y+ftEspnY+eb.Dy()),
			espn, eb.Min, draw.Over)
	}
}

// stripNearBlack menghapus latar (nyaris) hitam menjadi transparan via
// flood-fill dari keempat tepi. Dipakai untuk logo Flora baru.
func stripNearBlack(src image.Image) image.Image {
	if src == nil {
		return nil
	}
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Src)
	isBg := func(x, y int) bool {
		r, g, bl, _ := dst.At(x, y).RGBA()
		return r < 0x1818 && g < 0x1818 && bl < 0x1818
	}
	type pt struct{ x, y int }
	var stack []pt
	push := func(x, y int) {
		if x < 0 || y < 0 || x >= b.Dx() || y >= b.Dy() {
			return
		}
		if !isBg(x, y) {
			return
		}
		if _, _, _, a := dst.At(x, y).RGBA(); a == 0 {
			return
		}
		dst.SetRGBA(x, y, color.RGBA{})
		stack = append(stack, pt{x, y})
	}
	for x := 0; x < b.Dx(); x++ {
		push(x, 0)
		push(x, b.Dy()-1)
	}
	for y := 0; y < b.Dy(); y++ {
		push(0, y)
		push(b.Dx()-1, y)
	}
	for len(stack) > 0 {
		p := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		push(p.x+1, p.y)
		push(p.x-1, p.y)
		push(p.x, p.y+1)
		push(p.x, p.y-1)
	}
	return dst
}

// fitHeight menskala gambar ke tinggi h (proporsional). nil bila src nil.
func fitHeight(src image.Image, h int) image.Image {
	if src == nil {
		return nil
	}
	b := src.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 || b.Dy() == h {
		return src
	}
	scale := float64(h) / float64(b.Dy())
	dw := int(float64(b.Dx())*scale + 0.5)
	if dw < 1 {
		dw = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, h))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	return dst
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
