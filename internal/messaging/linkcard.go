// Package messaging berisi helper MERAKIT + MENGIRIM pesan WhatsApp yang
// lebih kaya dari teks polos (kartu link preview, dan calon format lain).
// Posisinya di antara store (infra tipis: DB, context, reply mentah) dan
// plugins (fitur): plugins -> messaging -> store. Boleh dipakai plugins
// maupun tool AI bridge. Sengaja BUKAN di store supaya store tidak tercemar
// logika fitur + dependensi HTTP/image.
package messaging

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"wabot-go/internal/logfmt"
	"wabot-go/internal/store"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types"
	"google.golang.org/protobuf/proto"
)

// maxThumbBytes membatasi byte gambar thumbnail yang diunduh. Thumbnail
// preview harus kecil; kegedean bikin klien gagal render.
const maxThumbBytes = 1 << 20 // 1MB

// maxInlineDim adalah sisi terpanjang thumbnail INLINE (yang ditempel
// langsung di pesan). Inline yang kebesaran (ratusan KB) bisa membuat
// klien gagal render — versi kecil ini hanya placeholder, versi full
// diambil klien lewat ThumbnailDirectPath.
const maxInlineDim = 320

// LinkCard adalah parameter kartu link preview. ThumbURL/FaviconURL kosong
// berarti tanpa thumbnail/favicon (kartu teks + judul + deskripsi saja).
// Body adalah isi pesan SETELAH baris URL; kosong berarti pakai Description.
type LinkCard struct {
	PageURL     string // ditulis di teks + MatchedText (cache preview per URL ini)
	Title       string // judul kartu
	Description string // deskripsi pendek kartu
	// ThumbURL adalah sumber gambar thumbnail. Boleh DAFTAR dipisah koma /
	// spasi: dicoba berurutan sampai satu berhasil (host gambar sering
	// intermiten). Kosong = tanpa thumbnail.
	ThumbURL string
	// ThumbBytes adalah gambar thumbnail LOKAL (mis. hasil .thumbnail dari
	// gambar yang dikirim owner). Kalau terisi, tidak ada HTTP sama sekali
	// dan ThumbURL diabaikan — jalur paling tahan gangguan host.
	ThumbBytes []byte
	// FaviconURL/FaviconBytes pola sama untuk ikon kartu.
	FaviconURL   string
	FaviconBytes []byte
	Body         string // isi pesan setelah URL ("" = pakai Description)
	// Mentions adalah JID yang di-tag di dalam Body. SYARAT PAIRING (sama
	// seperti ReplyTextWithMentions): Body WAJIB mengandung "@<User>" untuk
	// tiap JID dengan User yang sama persis (di grup mode LID = "@<LID>").
	// Kosong = kartu tanpa mention (perilaku lama).
	Mentions []types.JID
}

// SendLinkCard mengirim kartu link preview. Formatnya mengikuti contoh yang
// terbukti jalan (URL dulu baru isi, PreviewType NONE, upload bertipe
// MediaLinkThumbnail, messageSecret acak) — lihat catatan di tiap helper.
func SendLinkCard(ctx context.Context, mctx *store.MessageContext, card LinkCard) error {
	body := card.Body
	if body == "" {
		body = card.Description
	}

	ext := &waE2E.ExtendedTextMessage{
		Text:        proto.String(card.PageURL + "\n" + body),
		MatchedText: proto.String(card.PageURL),
		Title:       proto.String(card.Title),
		Description: proto.String(card.Description),
	}

	// Mention opsional: JID dinormalisasi via ToNonAD supaya device/agent
	// tidak ikut terbawa dan merusak pairing (aturan yang sama dengan
	// ReplyTextWithMentions di store).
	if len(card.Mentions) > 0 {
		bare := make([]string, 0, len(card.Mentions))
		for _, jid := range card.Mentions {
			bare = append(bare, jid.ToNonAD().String())
		}
		ext.ContextInfo = &waE2E.ContextInfo{MentionedJID: bare}
	}

	// Thumbnail opsional: byte lokal (kalau ada) menang — tanpa jaringan.
	// Selain itu daftar URL dicoba berurutan. Gagal di semua titik = kartu
	// tetap terkirim TANPA thumbnail (bukan gagal total), karena kartu
	// teksnya sendiri valid.
	if len(card.ThumbBytes) > 0 {
		attachThumbnailBytes(ctx, mctx, ext, card.ThumbBytes)
	} else if sources := SplitThumbSources(card.ThumbURL); len(sources) > 0 {
		attachThumbnail(ctx, mctx, ext, sources)
	}

	// Favicon opsional/pemanis dengan pola yang sama (gagal = dilewati).
	if len(card.FaviconBytes) > 0 {
		if favicon := faviconFromBytes(ctx, mctx, card.FaviconBytes); favicon != nil {
			ext.FaviconMmsMetadata = favicon
		}
	} else if sources := SplitThumbSources(card.FaviconURL); len(sources) > 0 {
		if favicon := fetchFavicon(ctx, mctx, sources); favicon != nil {
			ext.FaviconMmsMetadata = favicon
		}
	}

	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return fmt.Errorf("gagal buat secret: %w", err)
	}

	text := card.PageURL + "\n" + body
	if strings.TrimSpace(body) == "" {
		text = card.PageURL
	}
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, &waE2E.Message{
		MessageContextInfo: &waE2E.MessageContextInfo{
			MessageSecret: secret,
		},
		ExtendedTextMessage: ext,
	})
	if err != nil {
		return fmt.Errorf("gagal kirim kartu preview: %w", err)
	}
	store.LogOutgoing(mctx, "linkcard", text)
	store.TrackBotMessageID(resp.ID, text)
	return nil
}

// cachedThumb adalah hasil olahan satu sumber gambar yang disimpan supaya
// kartu yang sama (mis. .menu dibuka berkali-kali) tidak download +
// normalisasi + upload ulang tiap kali. Kunci cache: "url:<url>" untuk
// sumber URL, "blob:<sha256>" untuk byte lokal (gambar tersimpan owner).
type cachedThumb struct {
	small    []byte
	uploaded whatsmeow.UploadResponse
	width    uint32
	height   uint32
	ts       time.Time
}

var (
	thumbCacheMu sync.Mutex
	thumbCache   = map[string]*cachedThumb{}
)

// thumbCacheTTL mengikuti contoh yang jalan (cache media 6 jam).
const thumbCacheTTL = 6 * time.Hour

// Kunci cache memori untuk dua jenis sumber.
const (
	thumbCacheKeyURL  = "url:"
	thumbCacheKeyBlob = "blob:"
)

// lookupThumbCache mengambil entri cache yang masih segar (false = miss).
func lookupThumbCache(key string) (small []byte, uploaded *whatsmeow.UploadResponse, width, height uint32, ok bool) {
	thumbCacheMu.Lock()
	defer thumbCacheMu.Unlock()
	hit, found := thumbCache[key]
	if !found || time.Since(hit.ts) >= thumbCacheTTL {
		return nil, nil, 0, 0, false
	}
	cached := *hit
	return cached.small, &cached.uploaded, cached.width, cached.height, true
}

// storeThumbCache menyimpan hasil olahan + menyapu entri kedaluwarsa
// (audit T6): tanpa sapuan ini map tumbuh tanpa batas seumur proses
// (TTL selama ini hanya memengaruhi baca, bukan membersihkan).
func storeThumbCache(key string, small []byte, uploaded whatsmeow.UploadResponse, width, height uint32) {
	thumbCacheMu.Lock()
	defer thumbCacheMu.Unlock()
	thumbCache[key] = &cachedThumb{small: small, uploaded: uploaded, width: width, height: height, ts: time.Now()}
	now := time.Now()
	for k, e := range thumbCache {
		if now.Sub(e.ts) > thumbCacheTTL {
			delete(thumbCache, k)
		}
	}
}

// getCachedThumb mengembalikan thumbnail olahan untuk satu URL: dari cache
// kalau masih segar; kalau tidak, unduh (retry + cache disk sebagai cadangan
// saat host gambar sedang mati) lalu normalisasi + upload. Hasil upload aman
// dipakai ulang karena kontennya sama persis (content-addressed).
func getCachedThumb(ctx context.Context, mctx *store.MessageContext, thumbURL string) (small []byte, uploaded *whatsmeow.UploadResponse, width, height uint32, err error) {
	key := thumbCacheKeyURL + thumbURL
	if small, uploaded, width, height, ok := lookupThumbCache(key); ok {
		return small, uploaded, width, height, nil
	}

	raw, err := downloadThumb(ctx, thumbURL)
	if err != nil {
		cached, ok := readDiskThumb(thumbURL)
		if !ok {
			return nil, nil, 0, 0, fmt.Errorf("gagal ambil thumbnail: %w", err)
		}
		// Host gambar sedang tidak bisa diakses: pakai salinan terakhir yang
		// pernah sukses supaya thumbnail tidak hilang begitu saja. Umur file
		// sengaja tidak dibatasi — lebih baik gambar lama daripada kosong.
		logfmt.Warn("linkcard: %s gagal diunduh (%v), memakai cache lama\n", thumbURL, err)
		raw = cached
	} else {
		writeDiskThumb(thumbURL, raw)
	}
	return prepareCachedThumb(ctx, mctx, key, raw)
}

// getCachedThumbBytes pola sama untuk sumber byte lokal (tanpa jaringan).
func getCachedThumbBytes(ctx context.Context, mctx *store.MessageContext, data []byte) (small []byte, uploaded *whatsmeow.UploadResponse, width, height uint32, err error) {
	sum := sha256.Sum256(data)
	key := thumbCacheKeyBlob + hex.EncodeToString(sum[:12])
	if small, uploaded, width, height, ok := lookupThumbCache(key); ok {
		return small, uploaded, width, height, nil
	}
	return prepareCachedThumb(ctx, mctx, key, data)
}

// prepareCachedThumb menormalkan + mengunggah byte sumber, lalu menyimpan
// hasilnya di cache memori dengan kunci key.
func prepareCachedThumb(ctx context.Context, mctx *store.MessageContext, key string, raw []byte) (small []byte, uploaded *whatsmeow.UploadResponse, width, height uint32, err error) {
	full, smallBytes, w, h := normalizeThumb(raw)
	up, err := uploadThumb(ctx, mctx, full, whatsmeow.MediaLinkThumbnail)
	if err != nil {
		return nil, nil, 0, 0, fmt.Errorf("gagal upload thumbnail: %w", err)
	}
	storeThumbCache(key, smallBytes, *up, w, h)
	return smallBytes, up, w, h, nil
}

// SplitThumbSources memecah ThumbURL/FaviconURL menjadi daftar URL valid
// (dipisah koma/titik-koma/spasi). Entri non-http dibuang supaya nilai aneh
// di bot_settings (mis. sisa teks "reset") tidak menghasilkan permintaan HTTP
// yang tidak masuk akal.
func SplitThumbSources(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || unicode.IsSpace(r)
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		low := strings.ToLower(f)
		if !strings.HasPrefix(low, "http://") && !strings.HasPrefix(low, "https://") {
			continue
		}
		out = append(out, f)
	}
	return out
}

// attachThumbnail mencoba daftar URL berurutan sampai satu berhasil. Gagal
// di semua URL = ext dibiarkan tanpa thumbnail (kartu teks tetap terkirim),
// kegagalannya hanya ke console.
func attachThumbnail(ctx context.Context, mctx *store.MessageContext, ext *waE2E.ExtendedTextMessage, sources []string) {
	var lastErr error
	for _, src := range sources {
		small, uploaded, width, height, err := getCachedThumb(ctx, mctx, src)
		if err != nil {
			lastErr = err
			continue
		}
		setExtThumbnail(ext, small, uploaded, width, height)
		return
	}
	logfmt.Warn("linkcard: thumbnail dilewati (%v)\n", lastErr)
}

// attachThumbnailBytes memasang thumbnail dari byte lokal (tanpa jaringan
// sama sekali) — jalur utama setelah owner menyimpan gambar via .thumbnail.
func attachThumbnailBytes(ctx context.Context, mctx *store.MessageContext, ext *waE2E.ExtendedTextMessage, data []byte) {
	small, uploaded, width, height, err := getCachedThumbBytes(ctx, mctx, data)
	if err != nil {
		logfmt.Warn("linkcard: thumbnail lokal dilewati (%v)\n", err)
		return
	}
	setExtThumbnail(ext, small, uploaded, width, height)
}

// setExtThumbnail menulis semua field thumbnail kartu sekaligus (dipakai
// jalur URL maupun byte lokal).
func setExtThumbnail(ext *waE2E.ExtendedTextMessage, small []byte, uploaded *whatsmeow.UploadResponse, width, height uint32) {
	ext.JPEGThumbnail = small
	ext.ThumbnailDirectPath = proto.String(uploaded.DirectPath)
	ext.ThumbnailSHA256 = uploaded.FileSHA256
	ext.ThumbnailEncSHA256 = uploaded.FileEncSHA256
	ext.MediaKey = uploaded.MediaKey
	ext.MediaKeyTimestamp = proto.Int64(time.Now().Unix())
	ext.ThumbnailWidth = proto.Uint32(width)
	ext.ThumbnailHeight = proto.Uint32(height)
}

// maxFaviconDim adalah sisi maksimal ikon favicon di kartu (meniru contoh
// yang jalan: min(dimensi asli, 96)).
const maxFaviconDim = 96

// fetchFavicon merakit MMSThumbnailMetadata ikon favicon lewat cache yang
// sama dengan thumbnail, mencoba daftar URL berurutan. Nil balik berarti
// "tanpa favicon" (bukan error) — pemanggil tetap mengirim kartu utamanya.
func fetchFavicon(ctx context.Context, mctx *store.MessageContext, sources []string) *waE2E.MMSThumbnailMetadata {
	var lastErr error
	for _, src := range sources {
		_, uploaded, width, height, err := getCachedThumb(ctx, mctx, src)
		if err != nil {
			lastErr = err
			continue
		}
		return faviconMetadata(uploaded, width, height)
	}
	logfmt.Warn("linkcard: favicon dilewati (%v)\n", lastErr)
	return nil
}

// faviconFromBytes pola sama untuk favicon yang tersimpan lokal.
func faviconFromBytes(ctx context.Context, mctx *store.MessageContext, data []byte) *waE2E.MMSThumbnailMetadata {
	_, uploaded, width, height, err := getCachedThumbBytes(ctx, mctx, data)
	if err != nil {
		logfmt.Warn("linkcard: favicon lokal dilewati (%v)\n", err)
		return nil
	}
	return faviconMetadata(uploaded, width, height)
}

// faviconMetadata merakit metadata ikon dari hasil upload (dimensi dipangkas
// ke maxFaviconDim).
func faviconMetadata(uploaded *whatsmeow.UploadResponse, width, height uint32) *waE2E.MMSThumbnailMetadata {
	if width > maxFaviconDim {
		width = maxFaviconDim
	}
	if height > maxFaviconDim {
		height = maxFaviconDim
	}
	return &waE2E.MMSThumbnailMetadata{
		ThumbnailDirectPath: proto.String(uploaded.DirectPath),
		ThumbnailSHA256:     uploaded.FileSHA256,
		ThumbnailEncSHA256:  uploaded.FileEncSHA256,
		MediaKey:            uploaded.MediaKey,
		MediaKeyTimestamp:   proto.Int64(time.Now().Unix()),
		ThumbnailHeight:     proto.Uint32(height),
		ThumbnailWidth:      proto.Uint32(width),
	}
}

// thumbAlphaPNG adalah SAKLAR EKSPERIMEN (keputusan owner): true =
// sumber ber-alpha dikirim sebagai PNG (full) dengan harapan klien WA
// me-render transparansinya; false = perilaku lama (semua jadi JPEG).
// Catatan: field INLINE tetap JPEG (wajib formatnya) — yang diuji adalah
// gambar FULL yang diambil klien via direct path.
// Hasil observasi panel menentukan permanen/revert (1 baris ini saja).
const thumbAlphaPNG = true

// maxAlphaPNGBytes membatasi PNG full agar kartu ikon raksasa tidak
// membengkak (melebihi ini → downscale ≤1024px dulu, alpha tetap utuh).
const maxAlphaPNGBytes = 512 << 10

// normalizeThumb mengubah byte gambar sumber jadi thumbnail siap kirim.
// Mengembalikan versi FULL (untuk di-upload) dan versi SMALL (max 320px,
// untuk ditempel inline) + dimensi asli. Langkah ini WAJIB
// untuk sumber progressive JPEG (marker SOF2) — decoder thumbnail di klien
// WhatsApp tidak menanganinya sehingga preview tampil HITAM walau ukuran
// dan hash semuanya benar. jpeg.Encode stdlib selalu menghasilkan baseline
// yang aman di-render semua klien. Kalau decode gagal (format aneh),
// fallback ke byte asli + dimensi dari DecodeConfig supaya pengiriman
// tetap dicoba (best-effort, bukan gagal total).
func normalizeThumb(raw []byte) (full, small []byte, width, height uint32) {
	fallback := func() ([]byte, []byte, uint32, uint32) {
		var w, h uint32
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(raw)); err == nil && cfg.Width > 0 && cfg.Height > 0 {
			w, h = uint32(cfg.Width), uint32(cfg.Height)
		}
		return raw, raw, w, h
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return fallback()
	}
	bounds := img.Bounds()
	w, h := uint32(bounds.Dx()), uint32(bounds.Dy())

	// Jalur eksperimen alpha: sumber TIDAK opaque (ada channel alpha,
	// mis. PNG transparan) → FULL dikirim sebagai PNG supaya alpha
	// berkesempatan hidup sampai ke klien. Inline small tetap JPEG
	// (placeholder, wajib formatnya). Opaque → jalur JPEG lama persis.
	if thumbAlphaPNG && imageHasAlpha(img) {
		pngFull := raw
		if len(raw) > maxAlphaPNGBytes || w > 1024 || h > 1024 {
			scaled := img
			if w > 1024 || h > 1024 {
				scaled = downscaleBilinear(img, 1024)
			}
			var pngBuf bytes.Buffer
			if err := png.Encode(&pngBuf, scaled); err != nil {
				return fallback()
			}
			pngFull = pngBuf.Bytes()
		}
		smallImg := img
		if w > maxInlineDim || h > maxInlineDim {
			smallImg = downscaleBilinear(img, maxInlineDim)
		}
		var smallBuf bytes.Buffer
		if err := jpeg.Encode(&smallBuf, flattenWhite(smallImg), &jpeg.Options{Quality: 70}); err != nil {
			return pngFull, pngFull, w, h
		}
		return pngFull, smallBuf.Bytes(), w, h
	}

	var fullBuf bytes.Buffer
	if err := jpeg.Encode(&fullBuf, img, &jpeg.Options{Quality: 85}); err != nil {
		return fallback()
	}
	smallImg := img
	if w > maxInlineDim || h > maxInlineDim {
		smallImg = downscaleBilinear(img, maxInlineDim)
	}
	var smallBuf bytes.Buffer
	if err := jpeg.Encode(&smallBuf, smallImg, &jpeg.Options{Quality: 70}); err != nil {
		return fullBuf.Bytes(), fullBuf.Bytes(), w, h
	}
	return fullBuf.Bytes(), smallBuf.Bytes(), w, h
}

// imageHasAlpha true kalau gambar membawa channel alpha yang BERISI
// transparansi (bukan sekadar bertipe alpha). Dipakai untuk memilih jalur
// PNG vs JPEG. Opaque() hanya ada di tipe konkret (bukan interface
// image.Image), jadi dicek per tipe: decoder stdlib menghasilkan YCbCr
// (JPEG), RGBA/NRGBA (PNG), Paletted (GIF/PNG-8) atau Gray.
func imageHasAlpha(img image.Image) bool {
	switch m := img.(type) {
	case *image.NRGBA:
		return !m.Opaque()
	case *image.NRGBA64:
		return !m.Opaque()
	case *image.RGBA:
		return !m.Opaque()
	case *image.RGBA64:
		return !m.Opaque()
	case *image.Paletted:
		return !m.Opaque()
	case *image.Alpha, *image.Alpha16, *image.NYCbCrA:
		return true
	default:
		return false
	}
}

// flattenWhite menimpa gambar ber-alpha ke atas putih, menghasilkan
// gambar opaque untuk encode JPEG. HANYA dipakai untuk thumbnail INLINE
// (placeholder kecil yang formatnya wajib JPEG) — gambar FULL jalur alpha
// tetap PNG utuh. Putih di sini murni keterbatasan format placeholder,
// bukan matte hasil akhir.
func flattenWhite(img image.Image) image.Image {
	if !imageHasAlpha(img) {
		return img
	}
	b := img.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), img, b.Min, draw.Over)
	return dst
}

// downscaleBilinear mengecilkan gambar supaya sisi terpanjang = maxDim,
// memakai interpolasi bilinear (stdlib tidak punya scaler, dan menambah
// dependensi imaging hanya untuk thumbnail tidak sepadan). Kualitasnya
// cukup untuk thumbnail placeholder 320px.
func downscaleBilinear(img image.Image, maxDim uint32) image.Image {
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	scale := float64(maxDim) / float64(sw)
	if sh > sw {
		scale = float64(maxDim) / float64(sh)
	}
	dw := int(float64(sw) * scale)
	dh := int(float64(sh) * scale)
	if dw < 1 {
		dw = 1
	}
	if dh < 1 {
		dh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		srcY := (float64(y)+0.5)/scale - 0.5 + float64(b.Min.Y)
		y0 := int(srcY)
		if y0 < b.Min.Y {
			y0 = b.Min.Y
		}
		y1 := y0 + 1
		if y1 > b.Max.Y-1 {
			y1 = b.Max.Y - 1
		}
		fy := srcY - float64(y0)
		if fy < 0 {
			fy = 0
		}
		if fy > 1 {
			fy = 1
		}
		for x := 0; x < dw; x++ {
			srcX := (float64(x)+0.5)/scale - 0.5 + float64(b.Min.X)
			x0 := int(srcX)
			if x0 < b.Min.X {
				x0 = b.Min.X
			}
			x1 := x0 + 1
			if x1 > b.Max.X-1 {
				x1 = b.Max.X - 1
			}
			fx := srcX - float64(x0)
			if fx < 0 {
				fx = 0
			}
			if fx > 1 {
				fx = 1
			}
			r00, g00, b00, a00 := img.At(x0, y0).RGBA()
			r10, g10, b10, a10 := img.At(x1, y0).RGBA()
			r01, g01, b01, a01 := img.At(x0, y1).RGBA()
			r11, g11, b11, a11 := img.At(x1, y1).RGBA()
			r := uint8((float64(r00)*(1-fx)*(1-fy) + float64(r10)*fx*(1-fy) + float64(r01)*(1-fx)*fy + float64(r11)*fx*fy) / 257)
			g := uint8((float64(g00)*(1-fx)*(1-fy) + float64(g10)*fx*(1-fy) + float64(g01)*(1-fx)*fy + float64(g11)*fx*fy) / 257)
			bl := uint8((float64(b00)*(1-fx)*(1-fy) + float64(b10)*fx*(1-fy) + float64(b01)*(1-fx)*fy + float64(b11)*fx*fy) / 257)
			a := uint8((float64(a00)*(1-fx)*(1-fy) + float64(a10)*fx*(1-fy) + float64(a01)*(1-fx)*fy + float64(a11)*fx*fy) / 257)
			dst.SetRGBA(x, y, color.RGBA{R: r, G: g, B: bl, A: a})
		}
	}
	return dst
}

// uploadThumb mengupload byte thumbnail ke server WhatsApp. mediaType
// menentukan turunan kunci enkripsi — WAJIB cocok dengan pemakaian:
// MediaLinkThumbnail untuk kartu link, MediaImage untuk thumbnail pesan
// media biasa. Tipe salah = klien gagal dekripsi = tampil hitam.
func uploadThumb(ctx context.Context, mctx *store.MessageContext, thumb []byte, mediaType whatsmeow.MediaType) (*whatsmeow.UploadResponse, error) {
	uploaded, err := mctx.Client.Upload(ctx, thumb, mediaType)
	if err != nil {
		return nil, err
	}
	return &uploaded, nil
}

// Batas percobaan unduh thumbnail. Host gambar publik (catbox dsb.) kerap
// gagal sesaat; dua percobaan singkat jauh lebih murah daripada kartu tanpa
// gambar. Timeout per percobaan dipangkas supaya .menu tidak menggantung.
const (
	thumbAttempts       = 2
	thumbAttemptTimeout = 7 * time.Second
	thumbRetryDelay     = 400 * time.Millisecond
)

// thumbPermanentError menandai kegagalan yang tidak akan berubah bila
// diulang: URL bukan gambar langsung, file kebesaran, atau 4xx (mis. 404
// file sudah dihapus di host-nya).
type thumbPermanentError struct{ msg string }

func (e *thumbPermanentError) Error() string { return e.msg }

// downloadThumb mengunduh byte gambar untuk thumbnail dengan retry untuk
// kegagalan transient (timeout/jaringan/5xx/429). Kegagalan permanen
// langsung dikembalikan tanpa percobaan kedua.
func downloadThumb(ctx context.Context, rawURL string) ([]byte, error) {
	var lastErr error
	for attempt := 1; attempt <= thumbAttempts; attempt++ {
		data, err := downloadThumbOnce(ctx, rawURL)
		if err == nil {
			return data, nil
		}
		lastErr = err
		var permanent *thumbPermanentError
		if errors.As(err, &permanent) || ctx.Err() != nil {
			break
		}
		if attempt < thumbAttempts {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(thumbRetryDelay):
			}
		}
	}
	return nil, lastErr
}

// downloadThumbOnce satu percobaan unduh. Menolak konten non-gambar (mis.
// URL yang ternyata halaman HTML) dan file di atas maxThumbBytes — keduanya
// dilaporkan sebagai error permanen yang jelas.
func downloadThumbOnce(ctx context.Context, rawURL string) ([]byte, error) {
	dlCtx, cancel := context.WithTimeout(ctx, thumbAttemptTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(dlCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, &thumbPermanentError{msg: err.Error()}
	}
	req.Header.Set("User-Agent", "wabot-go/1.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg := fmt.Sprintf("server gambar menjawab HTTP %d", resp.StatusCode)
		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
			return nil, errors.New(msg) // transient: layak diulang
		}
		return nil, &thumbPermanentError{msg: msg}
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "image/") {
		return nil, &thumbPermanentError{msg: fmt.Sprintf("URL bukan file gambar langsung (Content-Type: %s)", ct)}
	}

	// +1 byte untuk deteksi kelebihan: kalau masih ada sisa setelah batas,
	// berarti file kebesaran.
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxThumbBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxThumbBytes {
		return nil, &thumbPermanentError{msg: "gambar terlalu besar untuk thumbnail (>1MB)"}
	}
	return data, nil
}

// thumbDiskDirName adalah folder cache byte thumbnail di bawah data/
// (gitignored, bukan wabot-tmp: isinya harus tahan restart dan tidak ikut
// disapu pembersih temp). Ukurannya kecil — hanya thumbnail/favicon per URL.
const thumbDiskDirName = "thumb-cache"

// diskThumbPath mengembalikan path cache untuk satu URL (nama = sha256 URL).
func diskThumbPath(thumbURL string) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cwd, "data", thumbDiskDirName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(thumbURL)))
	return filepath.Join(dir, hex.EncodeToString(sum[:])+".img"), nil
}

// writeDiskThumb menyimpan byte sumber yang berhasil diunduh (atomik: tulis
// ke file sementara di folder yang sama lalu rename). Best-effort — gagal
// tulis hanya berarti tidak ada cadangan, bukan kegagalan kirim kartu.
func writeDiskThumb(thumbURL string, data []byte) {
	path, err := diskThumbPath(thumbURL)
	if err != nil {
		logfmt.Warn("linkcard: cache thumbnail tak bisa disiapkan (%v)\n", err)
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "tmp-*")
	if err != nil {
		logfmt.Warn("linkcard: cache thumbnail tak bisa ditulis (%v)\n", err)
		return
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		logfmt.Warn("linkcard: cache thumbnail tak bisa ditulis (%v)\n", err)
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		logfmt.Warn("linkcard: cache thumbnail tak bisa ditulis (%v)\n", err)
		return
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		logfmt.Warn("linkcard: cache thumbnail tak bisa dipasang (%v)\n", err)
	}
}

// readDiskThumb membaca cadangan byte untuk URL (false = tidak ada/rusak).
func readDiskThumb(thumbURL string) ([]byte, bool) {
	path, err := diskThumbPath(thumbURL)
	if err != nil {
		return nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 || len(data) > maxThumbBytes {
		return nil, false
	}
	return data, true
}
