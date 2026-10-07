package plugins

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"wabot-go/internal/logfmt"
	"wabot-go/internal/messaging"
	"wabot-go/internal/scraper"
	"wabot-go/internal/store"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func init() {
	registerPlugin(RegisterShareLoc)
}

// RegisterShareLoc mendaftarkan command `.shareloc` kategori Umum, boleh
// dipakai semua user: kirim titik lokasi statis ke chat ini.
//
// LocationMessage adalah pesan level-teks (tanpa upload media — tidak ada
// field URL/DirectPath/MediaKey, hanya koordinat + label + thumbnail inline
// opsional yang tidak kita isi). Format:
//
//	.shareloc <lat,long> [nama | alamat] [--img <url>]
//	.shareloc -6.2,106.8 Monas | Jakarta Pusat
//	.shareloc -6.2 106.8 Monas --img https://example.com/gedung.jpg
//
// Preview peta (field JPEGThumbnail) default-nya memakai thumbnail menu
// bot (DB kustom menang atas MENU_THUMB_URL — aturan yang sama dengan
// kartu .menu); flag --img menimpanya dengan gambar bebas. Unduh gambar
// lewat scraper.DownloadBytes (tameng SSRF + cap 100MB) lalu dikecilkan
// via messaging.InlineThumbForImage (JPEG baseline 128px yang terbukti
// ke-render klien). Gagal di titik mana pun = kirim tanpa preview
// (perilaku lama), bukan gagal total.
func RegisterShareLoc(r *Registry) {
	r.Register(Command{
		Name:        "shareloc",
		Description: "Kirim titik lokasi (.shareloc <lat,long> [nama] [--img url])",
		Category:    CategoryUmum,
		Handler:     handleShareLoc,
	})
}

// sharelocRe mengambil pasangan koordinat di AWAL argumen: desimal
// bertanda, dipisah koma dan/atau spasi. Sisanya adalah label.
var sharelocRe = regexp.MustCompile(`^(-?\d+(?:\.\d+)?)\s*[, ]\s*(-?\d+(?:\.\d+)?)\s*(.*)$`)

// sharelocImgRe mendeteksi flag "--img URL" / "--img=URL" di mana saja.
var sharelocImgRe = regexp.MustCompile(`(?i)(?:^|\s)--img[= ](\S+)\s*`)

// sharelocThumbTimeout membatasi unduh gambar preview. Handler ini jalan
// sinkron di event loop (bukan goroutine) — tanpa timeout, fetch yang
// menggantung ikut menggantung seluruh bot.
const sharelocThumbTimeout = 25 * time.Second

func handleShareLoc(ctx context.Context, mctx *store.MessageContext) error {
	raw := strings.TrimSpace(mctx.RawArgs)
	if raw == "" && len(mctx.Args) > 0 {
		raw = strings.Join(mctx.Args, " ")
	}
	// Flag --img diambil dulu (bentuk "--img URL" maupun "--img=URL")
	// supaya tidak dikira bagian nama lokasi.
	imgURL := ""
	if m := sharelocImgRe.FindStringSubmatch(raw); m != nil {
		imgURL = strings.TrimSpace(m[1])
		raw = strings.TrimSpace(sharelocImgRe.ReplaceAllString(raw, " "))
	}
	if imgURL != "" {
		if err := isPublicGetURL(ctx, imgURL); err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("URL gambar tidak valid: %v\nContoh: .shareloc -6.2,106.8 Monas --img https://example.com/gedung.jpg", err))
		}
	}
	m := sharelocRe.FindStringSubmatch(raw)
	if m == nil {
		return store.ReplyText(ctx, mctx, "Format: .shareloc <lat,long> [nama | alamat] [--img <url>]\nContoh: .shareloc -6.2,106.8 Monas | Jakarta Pusat")
	}
	lat, err1 := strconv.ParseFloat(m[1], 64)
	long, err2 := strconv.ParseFloat(m[2], 64)
	if err1 != nil || err2 != nil || lat < -90 || lat > 90 || long < -180 || long > 180 {
		return store.ReplyText(ctx, mctx, "Koordinat tidak valid. Lintang -90..90, bujur -180..180.\nContoh: .shareloc -6.2,106.8 Monas")
	}

	// Label opsional: "nama | alamat". Kosong = titik koordinat polos
	// (WhatsApp tetap me-render pin + tombol buka peta).
	name, address := "", ""
	if label := strings.TrimSpace(m[3]); label != "" {
		parts := strings.SplitN(label, "|", 2)
		name = strings.TrimSpace(parts[0])
		if len(parts) == 2 {
			address = strings.TrimSpace(parts[1])
		}
	}

	loc := &waE2E.LocationMessage{
		DegreesLatitude:  proto.Float64(lat),
		DegreesLongitude: proto.Float64(long),
	}
	if name != "" {
		loc.Name = proto.String(name)
	}
	if address != "" {
		loc.Address = proto.String(address)
	}
	// Preview peta: --img menang atas thumbnail menu; kosong = tanpa
	// preview (perilaku lama). Timeout sendiri karena handler sinkron.
	if thumb := sharelocPreview(ctx, imgURL); thumb != nil {
		loc.JPEGThumbnail = thumb
	}
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, &waE2E.Message{LocationMessage: loc})
	if err != nil {
		logfmt.Warn("shareloc: gagal kirim (%v)\n", err)
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal kirim lokasi: %v", err))
	}
	label := firstNonEmptyLoc(name, fmt.Sprintf("%.5f,%.5f", lat, long))
	store.LogOutgoing(mctx, "location", label)
	store.TrackBotMessageID(string(resp.ID), label)
	return nil
}

// sharelocPreview mengunduh + mengecilkan gambar preview untuk
// JPEGThumbnail lokasi. Urutan sumber: --img, thumbnail menu, kosong.
// Mengembalikan nil bila tak ada sumber / unduh gagal / bukan gambar
// valid — pemanggil mengirim tanpa preview (fail-soft). Live-location
// bergerak TIDAK didukung (butuh rangkaian update ber-sequence yang tak
// terdokumentasi) — preview di sini hanya gambar diam.
func sharelocPreview(ctx context.Context, imgURL string) []byte {
	thumbURL := strings.TrimSpace(imgURL)
	if thumbURL == "" {
		thumbURL = resolveMenuThumbURL(ctx)
	}
	if thumbURL == "" {
		return nil
	}
	tctx, cancel := context.WithTimeout(ctx, sharelocThumbTimeout)
	defer cancel()
	data, err := scraper.DownloadBytes(tctx, thumbURL)
	if err != nil {
		logfmt.Warn("shareloc: unduh preview gagal (%v), kirim tanpa preview.\n", err)
		return nil
	}
	thumb := messaging.InlineMapThumb(data)
	if thumb == nil {
		logfmt.Warn("shareloc: preview bukan gambar valid, kirim tanpa preview.\n")
		return nil
	}
	// Sukses itu informatif di sini (satu-satunya cara membedakan "thumb
	// terkirim" vs "fallback peta karena thumb nil" dari console).
	logfmt.Plain("shareloc: preview %d bytes dari %s\n", len(thumb), thumbURL)
	return thumb
}

// firstNonEmptyLoc mengembalikan string non-kosong pertama (helper lokal
// supaya tidak bergantung ke package scraper untuk hal sepele ini).
func firstNonEmptyLoc(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
