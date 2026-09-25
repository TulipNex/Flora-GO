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
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"wabot-go/internal/store"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
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
	ThumbURL    string // sumber gambar thumbnail ("" = tanpa thumbnail)
	FaviconURL  string // sumber ikon kartu ("" = tanpa favicon)
	Body        string // isi pesan setelah URL ("" = pakai Description)
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

	// Thumbnail opsional: kalau ThumbURL diisi, unduh + normalisasi +
	// upload, lalu pasang field-nya. Gagal = kartu tetap terkirim TANPA
	// thumbnail (bukan gagal total), karena kartu teksnya sendiri valid.
	if strings.TrimSpace(card.ThumbURL) != "" {
		attachThumbnail(ctx, mctx, ext, card.ThumbURL)
	}

	// Favicon opsional/pemanis dengan pola yang sama (gagal = dilewati).
	if strings.TrimSpace(card.FaviconURL) != "" {
		if favicon := fetchFavicon(ctx, mctx, card.FaviconURL); favicon != nil {
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

// cachedThumb adalah hasil olahan satu URL gambar yang disimpan supaya
// kartu yang sama (mis. .menu dibuka berkali-kali) tidak download +
// normalisasi + upload ulang tiap kali.
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

// getCachedThumb mengembalikan thumbnail olahan untuk URL: dari cache kalau
// masih segar, kalau tidak unduh + normalisasi + upload dulu lalu simpan.
// Hasil upload (DirectPath/hash/mediakey) aman dipakai ulang karena
// kontennya sama persis (content-addressed).
func getCachedThumb(ctx context.Context, mctx *store.MessageContext, thumbURL string) (small []byte, uploaded *whatsmeow.UploadResponse, width, height uint32, err error) {
	thumbCacheMu.Lock()
	if hit, ok := thumbCache[thumbURL]; ok && time.Since(hit.ts) < thumbCacheTTL {
		cached := *hit
		thumbCacheMu.Unlock()
		return cached.small, &cached.uploaded, cached.width, cached.height, nil
	}
	thumbCacheMu.Unlock()

	raw, err := downloadThumb(ctx, thumbURL)
	if err != nil {
		return nil, nil, 0, 0, fmt.Errorf("gagal ambil thumbnail: %w", err)
	}
	full, small, width, height := normalizeThumb(raw)
	up, err := uploadThumb(ctx, mctx, full)
	if err != nil {
		return nil, nil, 0, 0, fmt.Errorf("gagal upload thumbnail: %w", err)
	}

	thumbCacheMu.Lock()
	thumbCache[thumbURL] = &cachedThumb{small: small, uploaded: *up, width: width, height: height, ts: time.Now()}
	thumbCacheMu.Unlock()
	return small, up, width, height, nil
}

// attachThumbnail mengisi field thumbnail di ext lewat cache. Gagal di titik
// mana pun = ext dibiarkan tanpa thumbnail (kartu teks tetap terkirim),
// kegagalannya hanya ke console.
func attachThumbnail(ctx context.Context, mctx *store.MessageContext, ext *waE2E.ExtendedTextMessage, thumbURL string) {
	small, uploaded, width, height, err := getCachedThumb(ctx, mctx, thumbURL)
	if err != nil {
		fmt.Printf("⚠️ linkcard: thumbnail dilewati (%v)\n", err)
		return
	}
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
// sama dengan thumbnail. Nil balik berarti "tanpa favicon" (bukan error) —
// pemanggil tetap mengirim kartu utamanya.
func fetchFavicon(ctx context.Context, mctx *store.MessageContext, faviconURL string) *waE2E.MMSThumbnailMetadata {
	_, uploaded, width, height, err := getCachedThumb(ctx, mctx, faviconURL)
	if err != nil {
		fmt.Printf("⚠️ linkcard: favicon dilewati (%v)\n", err)
		return nil
	}
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

// normalizeThumb mengubah byte gambar sumber jadi baseline JPEG standar.
// Mengembalikan versi FULL (dimensi asli, untuk di-upload) dan versi SMALL
// (max 320px, untuk ditempel inline) + dimensi asli. Langkah ini WAJIB
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

// uploadThumb mengupload byte thumbnail ke server WhatsApp dengan tipe
// media MediaLinkThumbnail ("thumbnail-link") — WAJIB tipe ini, bukan
// MediaImage. Kunci enkripsi diturunkan dari tipe medianya, upload pakai
// tipe yang salah membuat klien gagal dekripsi dan preview tampil hitam.
func uploadThumb(ctx context.Context, mctx *store.MessageContext, thumb []byte) (*whatsmeow.UploadResponse, error) {
	uploaded, err := mctx.Client.Upload(ctx, thumb, whatsmeow.MediaLinkThumbnail)
	if err != nil {
		return nil, err
	}
	return &uploaded, nil
}

// downloadThumb mengunduh byte gambar untuk thumbnail. Menolak konten
// non-gambar (mis. URL yang ternyata halaman HTML) dan file di atas
// maxThumbBytes — keduanya dilaporkan sebagai error yang jelas.
func downloadThumb(ctx context.Context, rawURL string) ([]byte, error) {
	dlCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(dlCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "wabot-go/1.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server gambar menjawab HTTP %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "image/") {
		return nil, fmt.Errorf("URL bukan file gambar langsung (Content-Type: %s)", ct)
	}

	// +1 byte untuk deteksi kelebihan: kalau masih ada sisa setelah batas,
	// berarti file kebesaran.
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxThumbBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxThumbBytes {
		return nil, fmt.Errorf("gambar terlalu besar untuk thumbnail (>1MB)")
	}
	return data, nil
}
