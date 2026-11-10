package plugins

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"wabot-go/internal/config"
	"wabot-go/internal/heavy"
	"wabot-go/internal/logfmt"
	"wabot-go/internal/messaging"
	"wabot-go/internal/store"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types"
	"github.com/polymorfa/hypermeow/types/events"
	"google.golang.org/protobuf/proto"
)

// sendDlVideo mengupload + mengirim file sebagai video dengan caption judul.
func sendDlVideo(ctx context.Context, mctx *store.MessageContext, file, caption string) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	return sendDlBytesVideo(ctx, mctx, data, caption)
}

// sendDlBytesVideo mengirim byte sebagai video (jalur scraper + yt-dlp).
// Thumbnail inline (1 frame via ffmpeg) + dimensi dipasang supaya daftar
// chat menampilkan pratinjau, bukan kartu mini kosong. Dimensi diambil dari
// thumbnail (aspeknya sama dengan video asli).
func sendDlBytesVideo(ctx context.Context, mctx *store.MessageContext, data []byte, caption string) error {
	return sendDlBytesVideoAlbum(ctx, mctx, data, caption, "", nil)
}

// sendDlBytesVideoAlbum adalah varian album dari sendDlBytesVideo: msgID
// memaksa ID pesan (untuk anak album, ID-nya di-generate duluan) dan assoc
// menempelkan MessageContextInfo association MEDIA_ALBUM. Keduanya kosong =
// perilaku single seperti sebelumnya.
func sendDlBytesVideoAlbum(ctx context.Context, mctx *store.MessageContext, data []byte, caption string, msgID types.MessageID, assoc *waE2E.MessageAssociation) error {
	// Normalisasi DULU (tolak kosong/HTML, transcode kontainer asing),
	// baru upload + thumb dari hasilnya — supaya tak terkirim bubble hantu.
	data, err := messaging.PrepareVideoBytes(ctx, data)
	if err != nil {
		return err
	}
	uploaded, err := mctx.Client.Upload(ctx, data, whatsmeow.MediaVideo)
	if err != nil {
		return err
	}
	thumb := messaging.InlineThumbForVideo(ctx, data)
	vm := &waE2E.VideoMessage{
		URL:               proto.String(uploaded.URL),
		DirectPath:        proto.String(uploaded.DirectPath),
		MediaKey:          uploaded.MediaKey,
		MediaKeyTimestamp: proto.Int64(time.Now().Unix()),
		Mimetype:          proto.String("video/mp4"),
		FileEncSHA256:     uploaded.FileEncSHA256,
		FileSHA256:        uploaded.FileSHA256,
		FileLength:        proto.Uint64(uploaded.FileLength),
		Caption:           proto.String(caption),
		JPEGThumbnail:     thumb,
	}
	if len(thumb) > 0 {
		if cfg, _, cerr := image.DecodeConfig(bytes.NewReader(thumb)); cerr == nil && cfg.Width > 0 {
			vm.Width = proto.Uint32(uint32(cfg.Width))
			vm.Height = proto.Uint32(uint32(cfg.Height))
		}
	}
	msg := &waE2E.Message{VideoMessage: vm}
	if assoc != nil {
		msg.MessageContextInfo = &waE2E.MessageContextInfo{MessageAssociation: assoc}
	}
	var resp whatsmeow.SendResponse
	if msgID != "" {
		resp, err = mctx.Client.SendMessage(ctx, mctx.Chat, msg, whatsmeow.SendRequestExtra{ID: msgID})
	} else {
		resp, err = mctx.Client.SendMessage(ctx, mctx.Chat, msg)
	}
	if err != nil {
		return err
	}
	store.LogOutgoing(mctx, "dl-video", caption)
	store.TrackBotMessageID(resp.ID, caption)
	return nil
}

// sendDlAudio mengupload + mengirim file sebagai audio (bukan voice note).
func sendDlAudio(ctx context.Context, mctx *store.MessageContext, file string) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	label := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	return sendDlBytesAudio(ctx, mctx, data, label)
}

// sendDlBytesAudio mengirim byte sebagai audio (jalur scraper + yt-dlp).
func sendDlBytesAudio(ctx context.Context, mctx *store.MessageContext, data []byte, label string) error {
	uploaded, err := mctx.Client.Upload(ctx, data, whatsmeow.MediaAudio)
	if err != nil {
		return err
	}
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, &waE2E.Message{
		AudioMessage: &waE2E.AudioMessage{
			URL:           proto.String(uploaded.URL),
			DirectPath:    proto.String(uploaded.DirectPath),
			MediaKey:      uploaded.MediaKey,
			Mimetype:      proto.String("audio/mpeg"),
			FileEncSHA256: uploaded.FileEncSHA256,
			FileSHA256:    uploaded.FileSHA256,
			FileLength:    proto.Uint64(uploaded.FileLength),
			PTT:           proto.Bool(false),
		},
	})
	if err != nil {
		return err
	}
	store.LogOutgoing(mctx, "dl-audio", label)
	store.TrackBotMessageID(resp.ID, label)
	return nil
}

// sendDlBytesImage mengupload + mengirim byte sebagai FOTO (bukan video).
// Menutup lubang lama: postingan foto IG/Pin gambar yang sebelumnya dikirim
// paksa sebagai video. Dimensi diisi kalau terbaca (decoder jpeg/png sudah
// terdaftar di package ini via sticker.go) — gagal baca = kirim tanpa
// dimensi, bukan gagal total.
func sendDlBytesImage(ctx context.Context, mctx *store.MessageContext, data []byte, caption string) error {
	return sendDlBytesImageAlbum(ctx, mctx, data, caption, "", nil)
}

// sendDlBytesImageAlbum adalah varian album dari sendDlBytesImage (lihat
// sendDlBytesVideoAlbum untuk arti msgID/assoc).
func sendDlBytesImageAlbum(ctx context.Context, mctx *store.MessageContext, data []byte, caption string, msgID types.MessageID, assoc *waE2E.MessageAssociation) error {
	im, err := messaging.UploadImageMessage(ctx, mctx.Client, data)
	if err != nil {
		return err
	}
	im.Caption = proto.String(caption)
	msg := &waE2E.Message{ImageMessage: im}
	if assoc != nil {
		msg.MessageContextInfo = &waE2E.MessageContextInfo{MessageAssociation: assoc}
	}
	var resp whatsmeow.SendResponse
	if msgID != "" {
		resp, err = mctx.Client.SendMessage(ctx, mctx.Chat, msg, whatsmeow.SendRequestExtra{ID: msgID})
	} else {
		resp, err = mctx.Client.SendMessage(ctx, mctx.Chat, msg)
	}
	if err != nil {
		return err
	}
	store.LogOutgoing(mctx, "dl-image", caption)
	store.TrackBotMessageID(resp.ID, caption)
	return nil
}

// detectDlImageMIME menebak mime dari magic bytes (tanpa percaya ekstensi).
func detectDlImageMIME(data []byte) string {
	switch {
	case len(data) > 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return "image/jpeg"
	case len(data) > 8 && string(data[1:4]) == "PNG":
		return "image/png"
	case len(data) > 12 && string(data[8:12]) == "WEBP":
		return "image/webp"
	case len(data) > 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a"):
		return "image/gif"
	default:
		return "image/jpeg"
	}
}

// isWebPBytes true kalau byte ber-magic RIFF....WEBP — tanpa percaya
// ekstensi URL (server sering menyajikan webp di balik URL .jpg).
func isWebPBytes(data []byte) bool {
	return len(data) > 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP"
}

// isAnimatedWebP menelusuri chunk container WebP dan true kalau ada chunk
// ANMF (frame animasi). Walk manual supaya tanpa dependensi decoder webp —
// bytes.Contains mentah rawan false-positive dari payload gambar.
// Payload ganjil di-pad 1 byte (aturan RIFF).
func isAnimatedWebP(data []byte) bool {
	if !isWebPBytes(data) {
		return false
	}
	off := 12 // lewati header RIFF
	for off+8 <= len(data) {
		fourCC := string(data[off : off+4])
		size := int(binary.LittleEndian.Uint32(data[off+4 : off+8]))
		if fourCC == "ANMF" {
			return true
		}
		if size < 0 || off+8+size > len(data) {
			break
		}
		off += 8 + size + (size & 1)
	}
	return false
}

// webpConvertTimeout membatasi sekali konversi ffmpeg webp (file carousel
// kecil, hitungan detik) supaya webp korup tidak menggantung pengiriman.
const webpConvertTimeout = 60 * time.Second

// runFfmpegConvert menjalankan ffmpeg (input byte → file output) lalu
// membaca hasilnya kembali. Pola temp+stderr sama dengan
// convertToStickerWebp di sticker.go. inArgs adalah opsi INPUT yang harus
// muncul SEBELUM "-i" (mis. "-c:v libwebp" untuk memaksa decoder tertentu);
// args adalah opsi output biasa. nil untuk inArgs kalau tidak perlu.
func runFfmpegConvert(ctx context.Context, data []byte, inExt, outName string, inArgs []string, args ...string) ([]byte, error) {
	tmpDir, err := os.MkdirTemp(store.MustBotTempDir(), "dl-webp-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)
	inPath := filepath.Join(tmpDir, "input"+inExt)
	outPath := filepath.Join(tmpDir, outName)
	if err := os.WriteFile(inPath, data, 0600); err != nil {
		return nil, err
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, webpConvertTimeout)
	defer cancel()
	full := append([]string{"-y"}, inArgs...)
	full = append(full, "-i", inPath)
	full = append(full, args...)
	full = append(full, outPath)
	release, acqErr := heavy.AcquireTranscode(ctx)
	if acqErr != nil {
		return nil, acqErr
	}
	defer release()
	cmd := exec.CommandContext(timeoutCtx, "ffmpeg", full...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %v (%s)", err, stderr.String())
	}
	return os.ReadFile(outPath)
}

// convertWebPToJPEG mengubah webp STATIS jadi JPEG (dimensi asli
// dipertahankan — bukan kanvas stiker 512x512, karena tujuannya anak
// album/gambar biasa).
//
// JPEG tidak punya channel alpha: piksel transparan (stiker) ter-decode
// jadi HITAM kalau dibiarkan (teramati: .toimg menghasilkan foto berlatar
// hitam). Filter menimpa frame putih seukuran input (drawbox t=fill dengan
// iw/ih eksplisit — tanpa dimensi hardcode) lalu overlay gambar asli di
// atasnya, sehingga latar jadi putih.
func convertWebPToJPEG(ctx context.Context, data []byte) ([]byte, error) {
	return runFfmpegConvert(ctx, data, ".webp", "out.jpg", nil,
		"-vf", "split[a][b];[a]drawbox=x=0:y=0:w=iw:h=ih:c=white:t=fill[bg];[bg][b]overlay,format=yuv420p",
		"-frames:v", "1", "-q:v", "3")
}

// convertWebPToMP4 mengubah webp ANIMASI jadi MP4 h264 supaya bisa menjadi
// anak VIDEO album (dan dipakai .togif).
//
// Decoder webp NATIVE ffmpeg (termasuk build Debian 12 di panel) melewati
// chunk animasi (log: "skipping unsupported chunk: ANIM/ANMF" → "image data
// not found") sehingga webp animasi GAGAL di-decode — wajib paksa decoder
// libwebp via opsi input -c:v SEBELUM -i. -t 10 membatasi durasi keluaran
// (stiker animasi pendek; tanpa ini animasi panjang jadi MP4 raksasa).
func convertWebPToMP4(ctx context.Context, data []byte) ([]byte, error) {
	return runFfmpegConvert(ctx, data, ".webp", "out.mp4", []string{"-c:v", "libwebp"},
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-movflags", "+faststart",
		"-t", "10")
}

// normalizePhotoBytes menyiapkan byte foto untuk dikirim: WebP statis →
// JPEG, WebP animasi → MP4 (isVideo true), non-WebP → apa adanya (JPEG/PNG
// tidak pernah di-re-encode). Konversi gagal = asSticker true (kirim mentah
// sebagai stiker — animasi/statis tetap hidup, lebih baik daripada bubble
// kosong yang tidak ke-render).
func normalizePhotoBytes(ctx context.Context, data []byte) (out []byte, isVideo, asSticker bool) {
	if !isWebPBytes(data) {
		return data, false, false
	}
	if isAnimatedWebP(data) {
		if mp4, err := convertWebPToMP4(ctx, data); err == nil && len(mp4) > 0 {
			return mp4, true, false
		}
		logfmt.Warn("downloader: webp animasi gagal jadi mp4, fallback stiker.\n")
		return data, false, true
	}
	if jpg, err := convertWebPToJPEG(ctx, data); err == nil && len(jpg) > 0 {
		return jpg, false, false
	}
	logfmt.Warn("downloader: webp gagal jadi jpeg, fallback stiker.\n")
	return data, false, true
}

// sendDlPhotoSmart mengirim byte foto dengan penanganan WebP: statis → JPEG
// (jalur gambar), animasi → MP4 (jalur video + thumb), gagal konversi →
// stiker mentah. Dipakai loop single downloader.
func sendDlPhotoSmart(ctx context.Context, mctx *store.MessageContext, data []byte, caption string) error {
	out, isVideo, asSticker := normalizePhotoBytes(ctx, data)
	if asSticker {
		label := caption
		if label == "" {
			label = "webp"
		}
		return sendWebPAsSticker(ctx, mctx, out, label)
	}
	if isVideo {
		return sendDlBytesVideo(ctx, mctx, out, caption)
	}
	return sendDlBytesImage(ctx, mctx, out, caption)
}

// sendWebPAsSticker mengirim byte webp mentah sebagai stiker (fallback bila
// konversi gagal). EXIF pack/author standar; gagal EXIF = kirim mentah
// (stiker tanpa metadata tetap tampil — lebih baik daripada dilewati).
func sendWebPAsSticker(ctx context.Context, mctx *store.MessageContext, data []byte, label string) error {
	webp := data
	author := config.ResolveStickerAuthor(mctx.SenderPushName())
	if ex, err := store.WriteStickerExif(data, config.ResolveStickerPackname(), author); err == nil {
		webp = ex
	} else {
		logfmt.Warn("downloader: EXIF stiker gagal (%v), kirim mentah.\n", err)
	}
	return sendStickerWebp(ctx, mctx, webp, "dl-sticker", label)
}

// urlRe mendeteksi URL http/https di teks bebas untuk autodownload.
var urlRe = regexp.MustCompile(`(?i)https?://[^\s]+`)

// firstSupportedURL mengambil SATU URL medsos pertama yang didukung dari
// teks (link pertama saja, sesuai keputusan — anti boros). Mengembalikan
// URL mentah + platformnya, atau kosong kalau tidak ada yang cocok.
func firstSupportedURL(text string) (string, *dlPlatform) {
	for _, raw := range urlRe.FindAllString(text, -1) {
		cleaned := strings.TrimRight(raw, ".,;:!?)'\"")
		host := dlURLHost(cleaned)
		if p := matchDlPlatform(host); p != nil {
			return cleaned, p
		}
	}
	return "", nil
}

// CheckAutodownload dipanggil handler untuk tiap pesan NON-command. Kalau
// autodownload aktif di chat ini dan teks mengandung link medsos yang
// didukung, link pertama didownload + dikirim — dan return true supaya
// pemanggil melewatkan trigger AI untuk pesan itu (keputusan owner: download
// saja, jangan dijawab AI juga). Seluruh kerja berat async di goroutine;
// fungsi ini kembali seketika. Kegagalan DIBALAS ke chat (keputusan owner:
// jangan bikin user menunggu tanpa kabar).
func CheckAutodownload(client *whatsmeow.Client, evt *events.Message, text string) bool {
	ctx := context.Background()
	chatJID := evt.Info.Chat.String()
	on, err := store.GroupsFor(client).IsAutodownloadOn(ctx, chatJID)
	if err != nil {
		logfmt.Warn("Gagal cek autodownload %s: %v\n", chatJID, err)
		return false
	}
	if !on {
		return false
	}
	rawURL, p := firstSupportedURL(text)
	if rawURL == "" || p == nil {
		return false
	}
	// YouTube dikecualikan dari autodownload (keputusan owner): berat
	// (yt-dlp + transcode, sampai 200MB) — hanya via command manual
	// .yt/.ytmp3. Return false supaya pesan diproses normal seperti pesan
	// biasa (termasuk trigger AI bila ada kata panggilan Flora).
	if p.command == "youtube" || p.command == "youtubemp3" {
		return false
	}

	mctx := &store.MessageContext{
		Client: client,
		Chat:   evt.Info.Chat,
		Sender: evt.Info.Sender,
		RawEvt: evt,
	}

	go func() {
		bg := context.Background()
		// Validasi penuh (domain + tameng SSRF) di dalam goroutine supaya
		// resolve DNS tidak menahan event loop.
		if verr := validateDlURL(bg, rawURL, p.hosts); verr != nil {
			_ = store.ReplyText(bg, mctx, fmt.Sprintf("Gagal autodownload: %v", verr))
			return
		}
		// Antrean global ikut berlaku (bagi pakai dengan command manual).
		select {
		case dlSem <- struct{}{}:
			defer func() { <-dlSem }()
		default:
			_ = store.ReplyText(bg, mctx, "Antrean download penuh (maks 2 bersamaan), link dilewati.")
			return
		}
		status, serr := sendDlStatus(bg, mctx, "Mengunduh, tunggu sebentar...")
		if serr != nil {
			logfmt.Warn("Autodownload: gagal kirim status ke %s: %v\n", chatJID, serr)
			return
		}
		if rerr := downloadAndSend(bg, mctx, rawURL, p, p.audio, status); rerr != nil {
			_ = store.ReplyText(bg, mctx, fmt.Sprintf("Gagal autodownload: %v", rerr))
		}
	}()
	return true
}

// CleanStaleDownloads menyapu folder temp yatim (wabot-dl-*) yang umurnya
// lebih dari dlStaleDays — pengaman kalau proses mati di tengah download
// sehingga defer RemoveAll tidak sempat jalan. Menyapu BotTempDir
// (<workdir>/wabot-tmp), BUKAN os.TempDir() (tmpfs 100MB di panel).
// Dipanggil sekali saat startup.
func CleanStaleDownloads() {
	tmpBase, err := store.BotTempDir()
	if err != nil {
		return
	}
	entries, err := os.ReadDir(tmpBase)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "wabot-dl-") {
			continue
		}
		p := filepath.Join(tmpBase, e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.IsDir() && time.Since(info.ModTime()) > dlStaleDays {
			_ = os.RemoveAll(p)
		}
	}
}
