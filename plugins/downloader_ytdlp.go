package plugins

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"wabot-go/internal/store"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types"
	"google.golang.org/protobuf/proto"
)

// downloadViaYtdlp adalah jalur exec yt-dlp → temp → kirim, KHUSUS primer
// YouTube (keputusan owner): pilih format compliant sejak unduh (H.264+AAC
// ≤720p, fallback berlapis) sehingga hasilnya langsung bisa diputar tanpa
// transcode panel. Gagal-cepat timeout dlTimeout lalu pemanggil jatuh ke
// scraper — error yt-dlp tidak lagi fatal seperti dulu.
func downloadViaYtdlp(ctx context.Context, mctx *store.MessageContext, rawURL string, audioOnly bool, status types.MessageID) error {
	baseTmp, err := store.BotTempDir()
	if err != nil {
		return fmt.Errorf("gagal siapkan temp: %v", err)
	}
	dir, err := os.MkdirTemp(baseTmp, "wabot-dl-*")
	if err != nil {
		return fmt.Errorf("gagal siapkan temp: %v", err)
	}
	defer os.RemoveAll(dir) // umur file = umur job ini saja

	timeoutCtx, cancel := context.WithTimeout(ctx, dlTimeout)
	defer cancel()

	tmpl := filepath.Join(dir, "%(title).80s.%(ext)s")
	args := []string{"--no-playlist", "-o", tmpl,
		"--match-filter", fmt.Sprintf("duration < %d", maxDlSeconds)}
	if audioOnly {
		args = append(args, "-f", "bestaudio/best", "-x", "--audio-format", "mp3")
	} else {
		// Preferensi codec player-chat WhatsApp (terutama Android): H.264
		// + AAC. Tanpa ini yt-dlp bisa mengambil VP9/AV1 + Opus yang
		// ditolak player dengan "masalah dengan file video". Fallback
		// berlapis: avc1+m4a dulu, baru format umum (dlVideoFmt), lalu
		// best apa adanya — tetap terkirim walau sumbernya tidak punya
		// varian H.264 sama sekali.
		// --postprocessor-args Merger: moov di depan (faststart) supaya
		// bisa streaming saat download penerima belum selesai.
		args = append(args,
			"-f", "bv*[height<=720][vcodec^=avc1]+ba[acodec^=mp4a]/b[height<=720][vcodec^=avc1]/"+dlVideoFmt+"/b",
			"--merge-output-format", "mp4",
			"--postprocessor-args", "Merger+ffmpeg:-movflags +faststart",
		)
	}
	args = append(args, rawURL)

	// Exec TANPA shell (arg list) — URL jahat tidak bisa jadi injeksi.
	cmd := exec.CommandContext(timeoutCtx, "yt-dlp", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		if timeoutCtx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("download melebihi %s, dibatalkan.", dlTimeout)
		}
		if isMatchFilterReject(out) {
			return fmt.Errorf("video melebihi batas %d menit.", maxDlSeconds/60)
		}
		return fmt.Errorf("gagal download: %v", firstDlErrorLine(out))
	}

	file, err := pickDlFile(dir)
	if err != nil {
		return err
	}
	caption := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))

	// Routing ukuran: ≤100MB normal, 100-200MB jadi dokumen, >200MB tolak
	// (RAM panel tak muat). Stat di sini murah (file sudah di disk).
	if info, serr := os.Stat(file); serr == nil && info.Size() > maxDlMediaBytes {
		if info.Size() > maxDlDocBytes {
			return fmt.Errorf("file %.0f MB melebihi batas %d MB.", float64(info.Size())/(1<<20), maxDlDocBytes/(1<<20))
		}
		caption += "\n(dikirim sebagai dokumen, >100MB)"
		return sendDlDocument(ctx, mctx, file, caption)
	}

	if audioOnly {
		err = sendDlAudio(ctx, mctx, file)
	} else {
		err = sendDlVideo(ctx, mctx, file, caption)
	}
	// Pesan status "Mengunduh..." SENGAJA dibiarkan (keputusan owner) dan
	// tidak ada pesan penutup — media terakhir adalah penanda selesainya.
	if err != nil {
		return fmt.Errorf("gagal kirim hasil: %v", err)
	}
	return nil
}

// firstDlErrorLine mengambil baris paling informatif dari output yt-dlp
// yang gagal, urutan prioritas:
//  1. Baris exception TERAKHIR traceback Python ("ModuleNotFoundError: ...",
//     "ImportError: ...") — penyebab asli selalu di ekor, bukan kepala.
//  2. Baris "ERROR" yt-dlp (privat, login-wall, region-block, dsb).
//  3. 300 char awal.
//
// Pelajaran Sep 2026: traceback terpotong di kepala menyembunyikan akar
// (mis. yt-dlp vs Python 3.14) sehingga tak bisa didiagnosa.
func firstDlErrorLine(out []byte) string {
	lines := strings.Split(string(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" &&
			(strings.Contains(line, "Error") || strings.Contains(line, "Exception")) {
			return line
		}
	}
	for _, line := range lines {
		if strings.Contains(line, "ERROR") {
			return strings.TrimSpace(line)
		}
	}
	runes := []rune(strings.TrimSpace(string(out)))
	if len(runes) > 300 {
		return string(runes[:300]) + "..."
	}
	if len(runes) == 0 {
		return "error tidak diketahui"
	}
	return string(runes)
}

// isMatchFilterReject true kalau output yt-dlp menunjukkan penolakan
// --match-filter (durasi melebihi batas) — pesannya tidak selalu mengandung
// "ERROR", jadi dideteksi khusus supaya user dapat pesan ramah.
func isMatchFilterReject(out []byte) bool {
	return strings.Contains(strings.ToLower(string(out)), "match_filter")
}

// sendDlDocument mengupload + mengirim file sebagai dokumen (untuk hasil
// >100MB yang tak bisa jadi media playable). Mime dari ekstensi.
func sendDlDocument(ctx context.Context, mctx *store.MessageContext, file, caption string) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	mime := "application/octet-stream"
	switch strings.ToLower(filepath.Ext(file)) {
	case ".mp4", ".mov":
		mime = "video/mp4"
	case ".mp3":
		mime = "audio/mpeg"
	case ".m4a":
		mime = "audio/mp4"
	case ".webm":
		mime = "video/webm"
	}
	uploaded, err := mctx.Client.Upload(ctx, data, whatsmeow.MediaDocument)
	if err != nil {
		return err
	}
	filename := filepath.Base(file)
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, &waE2E.Message{
		DocumentMessage: &waE2E.DocumentMessage{
			URL:           proto.String(uploaded.URL),
			DirectPath:    proto.String(uploaded.DirectPath),
			MediaKey:      uploaded.MediaKey,
			Mimetype:      proto.String(mime),
			FileEncSHA256: uploaded.FileEncSHA256,
			FileSHA256:    uploaded.FileSHA256,
			FileLength:    proto.Uint64(uploaded.FileLength),
			FileName:      proto.String(filename),
			Caption:       proto.String(caption),
		},
	})
	if err != nil {
		return err
	}
	store.LogOutgoing(mctx, "dl-document", filename)
	store.TrackBotMessageID(resp.ID, filename)
	return nil
}

// pickDlFile memilih file hasil unduhan (terbesar, abaikan sisa .part).
func pickDlFile(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		return "", fmt.Errorf("tidak ada file yang terdownload (konten privat/dihapus?)")
	}
	best := ""
	var bestSize int64 = -1
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".part") || strings.HasSuffix(name, ".ytdl") || strings.HasSuffix(name, ".temp") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.IsDir() {
			continue
		}
		if info.Size() > bestSize {
			bestSize = info.Size()
			best = filepath.Join(dir, name)
		}
	}
	if best == "" {
		return "", fmt.Errorf("tidak ada file valid yang terdownload.")
	}
	return best, nil
}

// sendDlStatus mengirim pesan status + mengembalikan ID-nya untuk dihapus
// nanti setelah media terkirim.
func sendDlStatus(ctx context.Context, mctx *store.MessageContext, text string) (types.MessageID, error) {
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String(text)},
	})
	if err != nil {
		return "", err
	}
	store.LogOutgoing(mctx, "dl-status", text)
	return resp.ID, nil
}
