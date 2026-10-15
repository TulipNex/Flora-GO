package plugins

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"strings"
	"time"

	"wabot-go/internal/logfmt"
	"wabot-go/internal/messaging"
	"wabot-go/internal/store"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
)

func init() {
	registerPlugin(RegisterTourl)
}

const (
	// tourlCatboxAPI adalah endpoint upload catbox utama (file permanen,
	// butuh userhash). Beda dengan litterbox (sementara 72 jam, tanpa
	// key) yang dipakai .smeme — keduanya satu keluarga catbox.
	tourlCatboxAPI = "https://catbox.moe/user/api.php"

	// tourlUA meniru browser desktop (keluarga catbox sensitif terhadap
	// User-Agent klien HTTP mentah — pola sama dengan smemeUA).
	tourlUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"

	// tourlMaxBytes membatasi file yang diupload (alasan sama dengan
	// maxToMediaBytes: RAM panel terbatas, file raksasa tidak muat
	// di memori). Catbox sendiri menerima sampai 200MB.
	tourlMaxBytes = 100 << 20

	// tourlTimeout membatasi seluruh pipeline (unduh media WA + upload)
	// supaya request yang menggantung tidak menahan goroutine selamanya.
	tourlTimeout = 120 * time.Second

	// tourlMaxResponse membatasi balasan teks API upload — kalau server
	// balas blob, baca akan gagal jelas daripada menelan memori.
	tourlMaxResponse = 1 << 20 // 1MB
)

// tourlUploader adalah abstraksi provider upload. Provider baru (mis.
// litterbox, tmpfiles) tinggal mengimplementasikan interface ini dan
// mendaftarkan diri di tourlProviders — command .tourl tak perlu diubah.
type tourlUploader interface {
	// name adalah nama provider untuk dipilih via argumen (.tourl <nama>).
	name() string
	// upload mengunggah data dan mengembalikan URL publiknya.
	upload(ctx context.Context, data []byte, filename, mime string) (string, error)
}

// tourlProviders mendaftarkan semua provider upload yang tersedia.
// Kunci = nama argumen (.tourl <nama>); "catbox" adalah default.
var tourlProviders = map[string]tourlUploader{
	"catbox": catboxUploader{},
}

// RegisterTourl mendaftarkan command `.tourl` kategori Media: reply (atau
// kirim dengan caption) gambar/video/dokumen → balas URL publiknya.
func RegisterTourl(r *Registry) {
	r.Register(Command{
		Name:        "tourl",
		Description: "Upload media jadi link (reply gambar/video/dokumen)",
		Category:    CategoryMedia,
		Handler:     handleTourl,
	})
}

// handleTourl memvalidasi input lalu memproses di goroutine terpisah (pola
// sama dengan .smeme/.brat: pipeline jaringan bisa memakan belasan detik,
// jadi event loop hypermeow tidak boleh diblokir).
func handleTourl(ctx context.Context, mctx *store.MessageContext) error {
	providerName := strings.ToLower(strings.TrimSpace(mctx.RawArgs))
	if providerName == "" {
		providerName = "catbox"
	}
	uploader, ok := tourlProviders[providerName]
	if !ok {
		names := make([]string, 0, len(tourlProviders))
		for n := range tourlProviders {
			names = append(names, n)
		}
		return store.ReplyText(ctx, mctx,
			fmt.Sprintf("Provider \"%s\" tidak dikenal. Tersedia: %s.", providerName, strings.Join(names, ", ")))
	}

	media, mime, err := tourlExtractMedia(mctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, err.Error())
	}

	go func() {
		bg, cancel := context.WithTimeout(context.Background(), tourlTimeout)
		defer cancel()
		if err := tourlProcess(bg, mctx, uploader, media, mime); err != nil {
			logfmt.Warn("tourl: %v\n", err)
			_ = store.ReplyText(bg, mctx, "Gagal upload: "+err.Error())
		}
	}()
	return nil
}

// tourlProcess menjalankan pipeline: unduh media WA → validasi → upload →
// balas URL. Error berupa pesan ramah siap-balas.
func tourlProcess(ctx context.Context, mctx *store.MessageContext, uploader tourlUploader, media whatsmeow.DownloadableMessage, mime string) error {
	data, err := mctx.Client.Download(ctx, media)
	if err != nil {
		return fmt.Errorf("gagal unduh media: %v", err)
	}
	if err := messaging.CheckMediaBytes(data); err != nil {
		return err
	}
	if len(data) > tourlMaxBytes {
		return fmt.Errorf("file %.0f MB melebihi batas %d MB.",
			float64(len(data))/(1<<20), tourlMaxBytes/(1<<20))
	}

	filename := "upload" + tourlExt(mime, data)
	url, err := uploader.upload(ctx, data, filename, mime)
	if err != nil {
		return err
	}
	return store.ReplyText(ctx, mctx, url)
}

// tourlExtractMedia mengambil media unduhan (gambar/video/dokumen) baik dari
// pesan langsung (kirim media dengan caption .tourl) maupun dari pesan yang
// di-reply (quoted). Pesan di-unwrap dulu (audit A2) supaya tetap jalan di
// grup ber-Pesan-Sementara / pesan sekali-lihat.
func tourlExtractMedia(mctx *store.MessageContext) (whatsmeow.DownloadableMessage, string, error) {
	if media, mime := tourlPickMedia(store.UnwrapMessage(mctx.RawEvt.Message)); media != nil {
		return media, mime, nil
	}
	if media, mime := tourlPickMedia(store.UnwrapMessage(store.ExtractQuotedMessage(mctx.RawEvt))); media != nil {
		return media, mime, nil
	}
	return nil, "", fmt.Errorf("reply atau kirim gambar/video/dokumen dengan caption `.tourl`")
}

// tourlPickMedia memilih pesan media pertama yang didukung dari satu pesan
// yang sudah di-unwrap. Mengembalikan media + mimetype-nya.
func tourlPickMedia(msg *waE2E.Message) (whatsmeow.DownloadableMessage, string) {
	if msg == nil {
		return nil, ""
	}
	if img := msg.GetImageMessage(); img != nil {
		return img, img.GetMimetype()
	}
	if vid := msg.GetVideoMessage(); vid != nil {
		return vid, vid.GetMimetype()
	}
	if doc := msg.GetDocumentMessage(); doc != nil {
		return doc, doc.GetMimetype()
	}
	return nil, ""
}

// tourlExt menentukan ekstensi file dari mimetype; kalau mimetype kosong
// atau tak dikenal, tebak dari magic bytes (pola smemeExt) supaya API
// upload menerima file.
func tourlExt(mime string, data []byte) string {
	switch strings.ToLower(strings.TrimSpace(strings.SplitN(mime, ";", 2)[0])) {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	case "video/mp4":
		return ".mp4"
	case "application/pdf":
		return ".pdf"
	case "audio/mpeg", "audio/mp3":
		return ".mp3"
	case "audio/ogg":
		return ".ogg"
	}
	switch {
	case len(data) > 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return ".jpg"
	case len(data) > 8 && string(data[1:4]) == "PNG":
		return ".png"
	case len(data) > 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return ".webp"
	case len(data) > 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a"):
		return ".gif"
	case len(data) > 12 && string(data[4:8]) == "ftyp":
		return ".mp4"
	}
	return ".bin"
}

// escapeQuotes membersihkan karakter quote/backslash dari nama file agar
// aman dipakai di header Content-Disposition multipart.
func escapeQuotes(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, s)
}

// catboxUploader mengupload ke catbox.moe utama (file permanen). Butuh
// CATBOX_USERHASH dari environment (wabot-go tanpa dotenv loader — export
// dulu di shell/panel seperti pola test-env.ps1).
type catboxUploader struct{}

func (catboxUploader) name() string { return "catbox" }

func (catboxUploader) upload(ctx context.Context, data []byte, filename, mime string) (string, error) {
	userhash := strings.TrimSpace(os.Getenv("CATBOX_USERHASH"))
	if userhash == "" {
		return "", fmt.Errorf("CATBOX_USERHASH belum diset di environment")
	}
	if strings.TrimSpace(mime) == "" {
		mime = "application/octet-stream"
	}

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("reqtype", "fileupload"); err != nil {
		return "", fmt.Errorf("gagal siapkan upload: %v", err)
	}
	if err := w.WriteField("userhash", userhash); err != nil {
		return "", fmt.Errorf("gagal siapkan upload: %v", err)
	}
	fw, err := w.CreatePart(textproto.MIMEHeader{
		"Content-Disposition": {fmt.Sprintf(`form-data; name="fileToUpload"; filename="%s"`, escapeQuotes(filename))},
		"Content-Type":        {mime},
	})
	if err != nil {
		return "", fmt.Errorf("gagal siapkan upload: %v", err)
	}
	if _, err := io.Copy(fw, bytes.NewReader(data)); err != nil {
		return "", fmt.Errorf("gagal siapkan upload: %v", err)
	}
	if err := w.Close(); err != nil {
		return "", fmt.Errorf("gagal siapkan upload: %v", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tourlCatboxAPI, &buf)
	if err != nil {
		return "", fmt.Errorf("gagal siapkan upload: %v", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("User-Agent", tourlUA)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("server upload tidak merespons: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, tourlMaxResponse+1))
	if err != nil {
		return "", fmt.Errorf("gagal baca respons upload: %v", err)
	}
	if len(body) > tourlMaxResponse {
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
	// error dari server.
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
