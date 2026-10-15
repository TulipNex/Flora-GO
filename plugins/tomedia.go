package plugins

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"wabot-go/internal/messaging"
	"wabot-go/internal/store"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func init() {
	registerPlugin(RegisterToMedia)
}

// maxToMediaBytes membatasi input .tomp3 (alasan sama dengan downloader:
// RAM panel 1,46GB tidak muat file raksasa di memori + temp ffmpeg).
const maxToMediaBytes = 100 << 20

// RegisterToMedia mendaftarkan 3 command konversi media kategori Alat:
// .toimg (stiker→foto), .togif (stiker animasi→GIF), .tomp3 (video→audio).
// Semua reuse helper package ini (download/convert/kirim milik .sticker &
// downloader) — satu-satunya kode kirim baru adalah GIF playback di bawah.
func RegisterToMedia(r *Registry) {
	r.Register(Command{
		Name:        "toimg",
		Aliases:     []string{"toimage"},
		Description: "Ubah stiker jadi foto (reply stiker)",
		Category:    CategoryMedia,
		Handler:     handleToImg,
	})
	r.Register(Command{
		Name:        "togif",
		Aliases:     []string{"tovid", "tovideo"},
		Description: "Ubah stiker bergerak jadi GIF (reply stiker animasi)",
		Category:    CategoryMedia,
		Handler:     handleToGif,
	})
	r.Register(Command{
		Name:        "tomp3",
		Aliases:     []string{"toaudio"},
		Description: "Ekstrak audio video jadi MP3 (reply/kirim video)",
		Category:    CategoryMedia,
		Handler:     handleToMp3,
	})
}

// toQuotedSticker mengambil StickerMessage dari pesan yang di-reply.
// Stiker tidak bisa membawa caption, jadi reply adalah satu-satunya sumber
// (pola extractWatermarkMedia di stickerwatermark.go).
func toQuotedSticker(mctx *store.MessageContext) (*waE2E.StickerMessage, error) {
	quoted := store.UnwrapMessage(store.ExtractQuotedMessage(mctx.RawEvt))
	if quoted == nil {
		return nil, fmt.Errorf("reply ke stiker dulu, lalu ketik perintahnya")
	}
	if sticker := quoted.GetStickerMessage(); sticker != nil {
		return sticker, nil
	}
	return nil, fmt.Errorf("yang di-reply harus berupa stiker")
}

// handleToImg mengubah stiker jadi foto PNG: alpha + dimensi asli utuh
// tanpa latar apa pun (PNG mendukung transparansi, jadi tidak perlu
// trik flatten seperti JPEG). Dieksekusi sinkron (detikan, pola .sticker).
func handleToImg(ctx context.Context, mctx *store.MessageContext) error {
	sticker, err := toQuotedSticker(mctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, err.Error())
	}
	mediaBytes, err := mctx.Client.Download(ctx, sticker)
	if err != nil {
		return store.ReplyText(ctx, mctx, "Gagal download stiker: "+err.Error())
	}
	if !isWebPBytes(mediaBytes) {
		return store.ReplyText(ctx, mctx, "Stiker ini bukan format webp, tidak bisa dijadikan foto.")
	}
	// Stiker animasi → frame pertama (native decoder ffmpeg cukup untuk
	// webp STATIS/frame-tunggal; yang bermasalah hanya decode animasi).
	pngBytes, err := runFfmpegConvert(ctx, mediaBytes, ".webp", "out.png", nil,
		"-frames:v", "1")
	if err != nil {
		return store.ReplyText(ctx, mctx, "Gagal convert ke foto: "+err.Error())
	}
	if err := sendDlBytesImage(ctx, mctx, pngBytes, ""); err != nil {
		return store.ReplyText(ctx, mctx, "Gagal kirim foto: "+err.Error())
	}
	return nil
}

// handleToGif mengubah stiker ANIMASI jadi GIF (dikirim sebagai video
// dengan flag GifPlayback: autoplay looping tanpa chrome player).
// Stiker statis ditolak tegas — itu ranah .toimg.
func handleToGif(ctx context.Context, mctx *store.MessageContext) error {
	sticker, err := toQuotedSticker(mctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, err.Error())
	}
	mediaBytes, err := mctx.Client.Download(ctx, sticker)
	if err != nil {
		return store.ReplyText(ctx, mctx, "Gagal download stiker: "+err.Error())
	}
	if !isWebPBytes(mediaBytes) {
		return store.ReplyText(ctx, mctx, "Stiker ini bukan format webp.")
	}
	if !isAnimatedWebP(mediaBytes) {
		return store.ReplyText(ctx, mctx, "Stiker ini tidak bergerak. Untuk stiker diam, pakai .toimg.")
	}
	// Decode webp animasi via ImageMagick (ffmpeg panel tidak punya decoder
	// animasi — lihat convertAnimatedWebPToMP4), lalu kirim sebagai GIF.
	mp4Bytes, err := convertAnimatedWebPToMP4(ctx, mediaBytes)
	if err != nil {
		return store.ReplyText(ctx, mctx, "Gagal convert ke GIF: "+err.Error())
	}
	if err := sendGifPlayback(ctx, mctx, mp4Bytes); err != nil {
		return store.ReplyText(ctx, mctx, "Gagal kirim GIF: "+err.Error())
	}
	return nil
}

// Batas pipeline GIF: fps fallback kalau deteksi delay gagal, dan jumlah
// frame maksimum (≈10 detik @15fps) supaya animasi panjang tidak jadi MP4
// raksasa.
const (
	ttGIFFallbackFPS = 15
	ttGIFMaxFrames   = 150
)

// convertPath dicari sekali (ImageMagick 6 = binary "convert"). Panel
// tanpa convert membuat .togif gagal jelas, bukan hang/timeout misterius.
var convertPath = sync.OnceValues(func() (string, error) {
	return exec.LookPath("convert")
})

// convertAnimatedWebPToMP4 mengubah webp ANIMASI jadi MP4 h264 via
// ImageMagick (ekstrak frame) + ffmpeg (rakit). Dipakai .togif karena
// ffmpeg panel TIDAK PUNYA decoder animasi webp: decoder native melewati
// chunk ANIM/ANMF ("image data not found"), dan decoder libwebp tidak
// dikompilasi ("Unknown decoder 'libwebp'") — terverifikasi dari log panel.
// ImageMagick men-decode animasi lewat libwebp langsung sehingga bebas
// dari keterbatasan ffmpeg tersebut. Encode libx264 panel terbukti ada.
func convertAnimatedWebPToMP4(ctx context.Context, data []byte) ([]byte, error) {
	convert, err := convertPath()
	if err != nil {
		return nil, fmt.Errorf("ImageMagick (convert) tidak tersedia di server")
	}
	baseTmp, err := store.BotTempDir()
	if err != nil {
		return nil, err
	}
	tmpDir, err := os.MkdirTemp(baseTmp, "togif-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)

	inPath := filepath.Join(tmpDir, "input.webp")
	outPath := filepath.Join(tmpDir, "output.mp4")
	frameTmpl := filepath.Join(tmpDir, "frame%03d.png")
	if err := os.WriteFile(inPath, data, 0600); err != nil {
		return nil, err
	}

	// 1. Ekstrak frame. -coalesce WAJIB: frame webp animasi bisa berupa
	// delta parsial, tanpa coalesce hasil rakitannya rusak/berbayang.
	if out, err := runCmdOutput(ctx, webpConvertTimeout, convert, "-coalesce", inPath, frameTmpl); err != nil {
		return nil, fmt.Errorf("convert ekstrak frame: %v (%s)", err, out)
	}
	frames, _ := filepath.Glob(filepath.Join(tmpDir, "frame*.png"))
	if len(frames) == 0 {
		return nil, fmt.Errorf("tidak ada frame berhasil diekstrak")
	}
	// Glob mengembalikan urutan abjad = urutan frame (%03d zero-padded),
	// jadi potong ekornya aman untuk membatasi durasi.
	if len(frames) > ttGIFMaxFrames {
		for _, f := range frames[ttGIFMaxFrames:] {
			_ = os.Remove(f)
		}
	}

	// 2. Deteksi fps dari delay tiap frame (identify %T = centisecond).
	// Gagal baca = fallback 15fps (stiker animasi umumnya 10-20fps).
	fps := ttGIFFallbackFPS
	if delays, derr := runCmdOutput(ctx, webpConvertTimeout, convert, "identify", "-format", "%T\n", inPath); derr == nil {
		if f := fpsFromDelays(delays); f > 0 {
			fps = f
		}
	}

	// 3. Rakit MP4 dari urutan gambar (pola temp+stderr sama dengan
	// runFfmpegConvert, tapi inputnya file sequence sehingga tidak reuse
	// helper itu yang didesain untuk input byte tunggal).
	timeoutCtx, cancel := context.WithTimeout(ctx, webpConvertTimeout)
	defer cancel()
	cmd := exec.CommandContext(timeoutCtx, "ffmpeg",
		"-y", "-framerate", strconv.Itoa(fps),
		"-i", frameTmpl,
		"-c:v", "libx264", "-pix_fmt", "yuv420p",
		"-movflags", "+faststart",
		"-t", "10",
		outPath,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg rakit mp4: %v (%s)", err, stderr.String())
	}
	return os.ReadFile(outPath)
}

// fpsFromDelays menghitung fps dari daftar delay frame (satu integer
// centisecond per baris, format "%T" identify). 0 = tak terbaca.
func fpsFromDelays(out string) int {
	var sum, n int
	for _, line := range strings.Split(out, "\n") {
		d, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil || d <= 0 {
			continue
		}
		sum += d
		n++
	}
	if n == 0 || sum == 0 {
		return 0
	}
	fps := (100*n + sum/2) / sum // round(100 / rata-rata delay)
	if fps < 5 {
		fps = 5
	}
	if fps > 30 {
		fps = 30
	}
	return fps
}

// runCmdOutput menjalankan satu perintah dengan timeout, mengembalikan
// stdout-nya. Error menyertakan stderr supaya pesan tool yang gagal
// (convert/identify) ikut terlihat di log — pola yang sama dipakai
// runFfmpegConvert untuk ffmpeg.
func runCmdOutput(ctx context.Context, timeout time.Duration, name string, args ...string) (string, error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(timeoutCtx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%v (%s)", err, stderr.String())
	}
	return stdout.String(), nil
}

// sendGifPlayback mengupload + mengirim byte MP4 sebagai GIF WhatsApp
// (VideoMessage dengan GifPlayback=true). Thumb inline + dimensi dipasang
// supaya daftar chat menampilkan pratinjau (pola sendDlBytesVideoAlbum di
// downloader.go — yang tidak punya varian GIF, makanya helper baru di sini
// bukan duplikasi).
func sendGifPlayback(ctx context.Context, mctx *store.MessageContext, data []byte) error {
	if err := messaging.CheckMediaBytes(data); err != nil {
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
		JPEGThumbnail:     thumb,
		GifPlayback:       proto.Bool(true),
	}
	if len(thumb) > 0 {
		if cfg, _, cerr := image.DecodeConfig(bytes.NewReader(thumb)); cerr == nil && cfg.Width > 0 {
			vm.Width = proto.Uint32(uint32(cfg.Width))
			vm.Height = proto.Uint32(uint32(cfg.Height))
		}
	}
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, &waE2E.Message{VideoMessage: vm})
	if err != nil {
		return err
	}
	store.LogOutgoing(mctx, "gif", "sticker->gif")
	store.TrackBotMessageID(resp.ID, "sticker->gif")
	return nil
}

// handleToMp3 mengekstrak trek audio video jadi MP3. Sumber: reply video
// atau kirim video dengan caption .tomp3 (reuse extractStickerMedia yang
// sudah menangani kedua jalur + unwrap).
func handleToMp3(ctx context.Context, mctx *store.MessageContext) error {
	_, vid, err := extractStickerMedia(mctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, "Reply video atau kirim video dengan caption .tomp3.")
	}
	if vid == nil {
		return store.ReplyText(ctx, mctx, "Reply video atau kirim video dengan caption .tomp3.")
	}
	mediaBytes, err := mctx.Client.Download(ctx, vid)
	if err != nil {
		return store.ReplyText(ctx, mctx, "Gagal download video: "+err.Error())
	}
	if len(mediaBytes) > maxToMediaBytes {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Video %.0f MB melebihi batas %d MB.",
			float64(len(mediaBytes))/(1<<20), maxToMediaBytes/(1<<20)))
	}
	mp3Bytes, err := extractMP3(ctx, mediaBytes)
	if err != nil {
		return store.ReplyText(ctx, mctx, "Gagal ekstrak audio: "+err.Error())
	}
	if err := sendDlBytesAudio(ctx, mctx, mp3Bytes, "tomp3"); err != nil {
		return store.ReplyText(ctx, mctx, "Gagal kirim audio: "+err.Error())
	}
	return nil
}

// extractMP3 mengambil trek audio video jadi MP3 (libmp3lame). Tanpa
// fallback format lain: kalau encoder MP3 tidak ada di ffmpeg server,
// gagal dengan pesan jelas (bukan file setengah-jadi dengan MIME salah).
// Build ffmpeg standar (termasuk yang dipakai .sticker) selalu membawa
// libmp3lame, jadi jalur ini praktis tidak pernah gagal karena itu.
func extractMP3(ctx context.Context, data []byte) ([]byte, error) {
	out, err := runFfmpegConvert(ctx, data, ".mp4", "out.mp3", nil,
		"-vn", "-c:a", "libmp3lame", "-q:a", "4")
	if err != nil {
		lowered := strings.ToLower(err.Error())
		if strings.Contains(lowered, "unknown encoder") || strings.Contains(lowered, "libmp3lame") {
			return nil, fmt.Errorf("ffmpeg server tidak mendukung MP3 (hubungi owner)")
		}
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("hasil audio kosong (video mungkin tidak bersuara)")
	}
	return out, nil
}
