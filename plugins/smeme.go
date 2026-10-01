package plugins

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"wabot-go/internal/config"
	"wabot-go/internal/messaging"
	"wabot-go/internal/scraper"
	"wabot-go/internal/store"

	"go.mau.fi/whatsmeow/proto/waE2E"
)

func init() {
	registerPlugin(RegisterSmeme)
}

const (
	// smemeMemegenBase adalah endpoint render memegen.link. API-nya
	// stateless: semua info meme (template custom + teks + background)
	// terkandung di URL, responsnya LANGSUNG gambar PNG tanpa JSON.
	smemeMemegenBase = "https://api.memegen.link/images/custom"

	// smemeFont adalah font teks meme (query font=). Impact = font meme
	// klasik. ID valid lain (tanpa perlu ubah kode lain, cukup ganti
	// konstanta ini): titilliumweb/thick (default memegen),
	// titilliumweb-thin/thin, kalam/comic, notosans, notosanshebrew/he,
	// hgminchob/jp, segoe/tiny.
	smemeFont = "impact"

	// smemeLitterURL adalah SATU-SATUNYA endpoint upload (litterbox,
	// keluarga catbox: gratis, tanpa key, simpan 72 jam — lebih dari
	// cukup karena langsung dipakai detik itu juga). Dipakai karena
	// memegen butuh background berupa URL publik, sedangkan media
	// WhatsApp tidak punya URL publik.
	//
	// HANYA litterbox, bukan catbox utama — files.catbox.moe menjawab
	// HEAD dengan Content-Length: 0 sehingga memegen menolaknya sebagai
	// background (HTTP 415 + render fallback abu-abu). Litterbox
	// mengembalikan length yang benar dan terverifikasi diterima memegen.
	smemeLitterURL = "https://litterbox.catbox.moe/resources/internals/api.php"

	// smemeUA meniru browser desktop (keluarga catbox sensitif terhadap
	// User-Agent klien HTTP mentah).
	smemeUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"

	// smemeMaxText membatasi tiap sisi teks (atas/bawah) supaya URL
	// render tetap wajar — teks meme memang dirancang pendek.
	smemeMaxText = 100

	// smemePipelineTimeout membatasi seluruh pipeline jaringan (unduh
	// media WA + upload litterbox + render memegen + unduh hasil) supaya
	// request yang menggantung tidak menahan goroutine selamanya.
	smemePipelineTimeout = 90 * time.Second

	// smemeMaxResponse membatasi balasan teks litterbox — kalau server
	// balas blob, baca akan gagal jelas daripada menelan memori.
	smemeMaxResponse = 1 << 20 // 1MB
)

// RegisterSmeme mendaftarkan command `.smeme` (alias `.stikermeme`, `.sm`)
// kategori Media: reply gambar + teks `atas | bawah` → stiker meme.
func RegisterSmeme(r *Registry) {
	r.Register(Command{
		Name:        "smeme",
		Aliases:     []string{"stikermeme", "sm"},
		Description: "Bikin stiker meme dari gambar (reply gambar + `.smeme atas | bawah`)",
		Category:    CategoryMedia,
		Handler:     handleSmeme,
	})
}

// handleSmeme memvalidasi input (teks + media background) lalu memproses
// di goroutine terpisah (pola sama dengan .brat/downloader): pipeline
// jaringan bisa memakan belasan detik, jadi event loop whatsmeow tidak
// boleh diblokir.
func handleSmeme(ctx context.Context, mctx *store.MessageContext) error {
	top, bottom, err := smemeInputText(mctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, err.Error())
	}

	// Reuse jalur ContextInfo bersama milik .sticker (sudah unwrap
	// Ephemeral/ViewOnce) — hanya gambar yang diterima, video ditolak
	// tegas karena custom background memegen image-only (GIF/MP4 sebagai
	// background terverifikasi dibalas 415).
	img, vid, merr := extractStickerMedia(mctx)
	if merr != nil {
		return store.ReplyText(ctx, mctx,
			"Reply gambar dulu, contoh: reply foto lalu ketik `.smeme halo | apa kabar`.")
	}
	if vid != nil {
		return store.ReplyText(ctx, mctx, "Video tidak didukung — reply gambar saja.")
	}

	go func() {
		bg := context.Background()
		if err := smemeGenerate(bg, mctx, img, top, bottom); err != nil {
			fmt.Printf("⚠️ smeme: %v\n", err)
			_ = store.ReplyText(bg, mctx, "Gagal bikin meme: "+err.Error())
		}
	}()
	return nil
}

// smemeInputText memecah RawArgs menjadi teks atas dan bawah dengan
// pemisah `|`. Tanpa `|` seluruh teks dianggap teks atas. Minimal satu
// sisi terisi; tiap sisi dibatasi smemeMaxText karakter.
func smemeInputText(mctx *store.MessageContext) (top, bottom string, err error) {
	parts := strings.SplitN(strings.TrimSpace(mctx.RawArgs), "|", 2)
	top = strings.TrimSpace(parts[0])
	if len(parts) == 2 {
		bottom = strings.TrimSpace(parts[1])
	}
	if top == "" && bottom == "" {
		return "", "", fmt.Errorf("Tulis teksnya, contoh: `.smeme halo | apa kabar` (salah satu sisi boleh kosong).")
	}
	if utf8.RuneCountInString(top) > smemeMaxText || utf8.RuneCountInString(bottom) > smemeMaxText {
		return "", "", fmt.Errorf("Tiap sisi teks maks %d karakter.", smemeMaxText)
	}
	return top, bottom, nil
}

// smemeGenerate menjalankan seluruh pipeline: unduh media WA → upload ke
// litterbox (jadi URL publik) → render via memegen → unduh hasil →
// convert ke stiker → pasang EXIF → kirim. Error berupa pesan ramah
// siap-balas.
func smemeGenerate(ctx context.Context, mctx *store.MessageContext, img *waE2E.ImageMessage, top, bottom string) error {
	ctx, cancel := context.WithTimeout(ctx, smemePipelineTimeout)
	defer cancel()

	media, err := mctx.Client.Download(ctx, img)
	if err != nil {
		return fmt.Errorf("gagal unduh gambar: %v", err)
	}
	if err := messaging.CheckMediaBytes(media); err != nil {
		return err
	}

	bgURL, err := uploadToLitterbox(ctx, media)
	if err != nil {
		return err
	}

	meme, err := fetchMemegenMedia(ctx, bgURL, top, bottom)
	if err != nil {
		return err
	}
	if err := messaging.CheckMediaBytes(meme); err != nil {
		return err
	}

	// Pipeline convert stiker yang SAMA dengan .brat/.sticker (ffmpeg pad
	// 512x512 transparan + EXIF pack/author) — reuse, bukan duplikasi.
	webp, err := convertToStickerWebp(meme, false)
	if err != nil {
		return fmt.Errorf("gagal convert ke stiker: %v", err)
	}
	webp, err = store.WriteStickerExif(webp, config.StickerPackname, config.StickerAuthor)
	if err != nil {
		return fmt.Errorf("gagal set metadata stiker: %v", err)
	}

	label := strings.TrimSpace(top + " | " + bottom)
	if runes := []rune(label); len(runes) > 60 {
		label = string(runes[:60])
	}
	return sendStickerWebp(ctx, mctx, webp, "smeme", label)
}

// uploadToLitterbox mengunggah byte gambar ke litterbox dan mengembalikan
// URL publiknya (simpan 72 jam). Tidak ada fallback host lain — keputusan
// owner: satu jalur upload, gagal ya gagal dengan pesan yang jelas.
// Error menyertakan pesan body server supaya penolakan bisa didiagnosa.
func uploadToLitterbox(ctx context.Context, data []byte) (string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("reqtype", "fileupload"); err != nil {
		return "", fmt.Errorf("gagal siapkan upload: %v", err)
	}
	// Litterbox butuh field time (masa simpan) — 72 jam (maksimal).
	if err := w.WriteField("time", "72h"); err != nil {
		return "", fmt.Errorf("gagal siapkan upload: %v", err)
	}
	fw, err := w.CreateFormFile("fileToUpload", "smeme"+smemeExt(data))
	if err != nil {
		return "", fmt.Errorf("gagal siapkan upload: %v", err)
	}
	if _, err := io.Copy(fw, bytes.NewReader(data)); err != nil {
		return "", fmt.Errorf("gagal siapkan upload: %v", err)
	}
	if err := w.Close(); err != nil {
		return "", fmt.Errorf("gagal siapkan upload: %v", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, smemeLitterURL, &buf)
	if err != nil {
		return "", fmt.Errorf("gagal siapkan upload: %v", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("User-Agent", smemeUA)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("server upload tidak merespons: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, smemeMaxResponse+1))
	if err != nil {
		return "", fmt.Errorf("gagal baca respons upload: %v", err)
	}
	if len(body) > smemeMaxResponse {
		return "", fmt.Errorf("respons upload terlalu besar")
	}
	trimmed := strings.TrimSpace(string(body))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if trimmed == "" {
			trimmed = "(tanpa detail)"
		}
		if len(trimmed) > 200 {
			trimmed = trimmed[:200]
		}
		return "", fmt.Errorf("upload HTTP %d: %s", resp.StatusCode, trimmed)
	}

	// Sukses SELALU berupa URL https polos — teks lain berarti pesan
	// error dari server (mis. tipe file diblokir).
	if !strings.HasPrefix(trimmed, "https://") {
		if len(trimmed) > 120 {
			trimmed = trimmed[:120]
		}
		if trimmed == "" {
			trimmed = "(kosong)"
		}
		return "", fmt.Errorf("upload ditolak: %s", trimmed)
	}
	return trimmed, nil
}

// smemeExt menebak ekstensi dari magic bytes (pola sama dengan
// detectDlImageMIME milik downloader) supaya litterbox menerima file.
func smemeExt(data []byte) string {
	switch {
	case len(data) > 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return ".jpg"
	case len(data) > 8 && string(data[1:4]) == "PNG":
		return ".png"
	case len(data) > 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return ".webp"
	case len(data) > 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a"):
		return ".gif"
	default:
		return ".jpg"
	}
}

// memegenEscape mengkodekan satu segmen teks path memegen.link: spasi
// jadi underscore, karakter URL-reserved jadi pola tilde. strings.Replacer
// bekerja single-pass kiri-ke-kanan sehingga karakter "~" yang baru
// disisipkan tidak diproses ulang. Aturan persis dari docs memegen.link;
// teks kosong jadi "_" (placeholder segmen kosong versi memegen).
func memegenEscape(s string) string {
	if strings.TrimSpace(s) == "" {
		return "_"
	}
	r := strings.NewReplacer(
		"_", "__",
		"-", "--",
		" ", "_",
		"\n", "~n",
		"?", "~q",
		"&", "~a",
		"%", "~p",
		"#", "~h",
		"/", "~s",
		`\`, "~b",
		"<", "~l",
		">", "~g",
		`"`, "''",
	)
	return r.Replace(s)
}

// fetchMemegenMedia merender meme via memegen custom background lalu
// mengunduh PNG hasilnya lewat scraper.DownloadBytes (tameng SSRF + cap
// 100MB). URL render dirakit penuh oleh kita (host + template fixed,
// hanya segmen teks dari user yang sudah di-escape) sehingga tidak ada
// host buatan user yang lolos ke downloader.
func fetchMemegenMedia(ctx context.Context, bgURL, top, bottom string) ([]byte, error) {
	renderURL := smemeMemegenBase + "/" +
		memegenEscape(top) + "/" + memegenEscape(bottom) + ".png" +
		"?background=" + url.QueryEscape(bgURL) +
		"&font=" + smemeFont

	data, err := scraper.DownloadBytes(ctx, renderURL)
	if err != nil {
		// Memegen menjawab 415 kalau background ditolak — body-nya
		// render fallback abu-abu yang tidak boleh terkirim ke user,
		// jadi petakan ke pesan ramah supaya user coba gambar lain.
		if strings.Contains(err.Error(), "415") {
			return nil, fmt.Errorf("generator menolak background (coba gambar lain)")
		}
		return nil, fmt.Errorf("gagal render meme: %v", err)
	}
	return data, nil
}
