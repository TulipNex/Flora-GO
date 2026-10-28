package messaging

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"wabot-go/internal/heavy"
	"wabot-go/internal/logfmt"
	"wabot-go/internal/store"
)

// normTimeout membatasi transcode BERAT ffmpeg (file panel kecil;
// transcode 720p beberapa menit). Langkah ringan (remux/audio) punya
// timeout sendiri yang jauh lebih pendek di bawah.
const normTimeout = 4 * time.Minute

// remuxTimeout untuk salin wadah (-c copy): hanya pindah wadah + faststart,
// tanpa sentuh gambar/suara. Detik-an untuk file normal.
const remuxTimeout = 60 * time.Second

// audioFixTimeout untuk ubah suara saja (-c:v copy): gambar utuh, hanya
// suara jadi AAC. Jauh lebih cepat dari transcode total.
const audioFixTimeout = 2 * time.Minute

// CheckMediaBytes menolak byte kosong atau HTML (halaman error proxy/CDN
// yang lolos status HTTP 200) SEBELUM diupload — mencegah bubble tak
// terlihat yang server-ACK tapi tak bisa di-render klien (kasus video
// YouTube Sep 2026: byte TS/HTML terkirim, ffmpeg gagal, chat kosong).
func CheckMediaBytes(data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("media kosong (0 byte)")
	}
	if t := bytes.TrimSpace(data); len(t) > 0 && t[0] == '<' {
		return fmt.Errorf("bukan media (HTML)")
	}
	return nil
}

// ffprobeOut adalah subset output ffprobe yang dibaca (JSON).
type ffprobeOut struct {
	Format struct {
		Name string `json:"format_name"`
	} `json:"format"`
	Streams []struct {
		CodecName string `json:"codec_name"`
		CodecType string `json:"codec_type"`
	} `json:"streams"`
}

// probeVideo memakai ffprobe: nama format + codec stream video/audio
// pertama. Error apa pun = tidak diketahui (pemanggil fail-soft).
func probeVideo(ctx context.Context, path string) (format, vcodec, acodec string, err error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(timeoutCtx, "ffprobe",
		"-v", "error",
		"-show_entries", "format=format_name",
		"-show_entries", "stream=codec_name,codec_type",
		"-of", "json",
		path)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", "", "", err
	}
	var probed ffprobeOut
	if err := json.Unmarshal(out.Bytes(), &probed); err != nil {
		return "", "", "", err
	}
	format = strings.ToLower(probed.Format.Name)
	for _, s := range probed.Streams {
		switch s.CodecType {
		case "video":
			if vcodec == "" {
				vcodec = strings.ToLower(s.CodecName)
			}
		case "audio":
			if acodec == "" {
				acodec = strings.ToLower(s.CodecName)
			}
		}
	}
	return format, vcodec, acodec, nil
}

// waCompliant true untuk MP4 (mov) + H.264 + (tanpa audio / AAC) —
// kombinasi yang pasti diputar player chat WhatsApp.
func waCompliant(format, vcodec, acodec string) bool {
	if !strings.Contains(format, "mp4") && !strings.Contains(format, "mov") {
		return false
	}
	if vcodec != "h264" && vcodec != "avc1" && vcodec != "avc" {
		return false
	}
	if acodec != "" && acodec != "none" && acodec != "aac" {
		return false
	}
	return true
}

// remuxCopy menyalin wadah saja (-c copy) + faststart, tanpa menyentuh
// gambar/suara. Ringan (detik-an) dan kualitas 100% sama. Cukup untuk
// kasus umum: sudah H264/AAC tapi wadah mov/webm/ts atau moov di belakang.
func remuxCopy(ctx context.Context, inPath, outPath string) error {
	return runFfmpeg(ctx, remuxTimeout, "remux",
		"-y", "-i", inPath,
		"-c", "copy", "-movflags", "+faststart",
		outPath)
}

// transcodeAudioOnly mengubah SUARA saja jadi AAC, gambar disalin utuh
// (-c:v copy). Untuk kasus gambar sudah H264 tapi suara aneh/kosong.
func transcodeAudioOnly(ctx context.Context, inPath, outPath string) error {
	return runFfmpeg(ctx, audioFixTimeout, "audiofix",
		"-y", "-i", inPath,
		"-c:v", "copy", "-c:a", "aac", "-movflags", "+faststart",
		outPath)
}

// runFfmpeg menjalankan satu perintah ffmpeg dengan timeout + log ringkas.
func runFfmpeg(ctx context.Context, timeout time.Duration, tag string, args ...string) error {
	release, err := heavy.AcquireTranscode(ctx)
	if err != nil {
		return fmt.Errorf("ffmpeg %s: %v", tag, err)
	}
	defer release()
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(timeoutCtx, "ffmpeg", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg %s: %v (%s)", tag, err, lastLine(stderr.String()))
	}
	return nil
}

// transcodeToMP4 mengubah video apa pun jadi MP4 H.264 + AAC + faststart,
// tinggi maks 720p (standar unduhan lama yang terbukti diputar semua klien).
// Dipakai sebagai LANGKAH TERAKHIR setelah remux/audiofix gagal.
// Preset veryfast + CRF 23: 2-3x lebih cepat dari default dengan kualitas
// yang tetap bagus untuk chat.
func transcodeToMP4(ctx context.Context, inPath, outPath string) error {
	release, err := heavy.AcquireTranscode(ctx)
	if err != nil {
		return err
	}
	defer release()
	timeoutCtx, cancel := context.WithTimeout(ctx, normTimeout)
	defer cancel()
	cmd := exec.CommandContext(timeoutCtx, "ffmpeg",
		"-y", "-i", inPath,
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "23",
		"-pix_fmt", "yuv420p",
		"-vf", `scale=-2:min(ih\,720)`,
		"-c:a", "aac", "-movflags", "+faststart",
		outPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg: %v (%s)", err, lastLine(stderr.String()))
	}
	return nil
}

// tryStep menjalankan satu langkah perbaikan lalu memvalidasi hasilnya:
// output harus ada + stream video tetap ada + (bila strict) compliant.
// Mengembalikan (byte, true) bila langkah berhasil dan layak kirim.
func tryStep(ctx context.Context, dir, in, tag string, strict bool, fn func(ctx context.Context, inPath, outPath string) error) ([]byte, bool) {
	out := filepath.Join(dir, tag+".mp4")
	if err := fn(ctx, in, out); err != nil {
		logfmt.Warn("videonorm: %s gagal (%v).\n", tag, err)
		return nil, false
	}
	norm, err := os.ReadFile(out)
	if err != nil || len(norm) == 0 {
		return nil, false
	}
	format, vcodec, acodec, err := probeVideo(ctx, out)
	if err != nil || vcodec == "" {
		logfmt.Warn("videonorm: %s output kehilangan video, lewati.\n", tag)
		return nil, false
	}
	if strict && !waCompliant(format, vcodec, acodec) {
		return nil, false
	}
	fmt.Printf("videonorm: %s -> %s/%s/%s (%d byte)\n", tag, format, vcodec, acodec, len(norm))
	return norm, true
}

// PrepareVideoBytes memastikan byte video layak dikirim sebagai VideoMessage.
// Urutan hemat CPU (ringan dulu, berat terakhir):
//  1. tolak kosong/HTML (fail-LOUD, error)
//  2. passthrough bila sudah compliant (nol biaya)
//  3. remux -c copy (detik-an, kualitas utuh)
//  4. audiofix -c:v copy (gambar utuh, suara jadi AAC)
//  5. transcode total veryfast (langkah terakhir)
// Gagal semua = byte asli + log (fail-soft, perilaku lama) — kecuali validasi
// isi yang tetap error supaya tak terkirim bubble hantu.
func PrepareVideoBytes(ctx context.Context, data []byte) ([]byte, error) {
	if err := CheckMediaBytes(data); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(store.MustBotTempDir(), "wabot-vnorm-*")
	if err != nil {
		return data, nil
	}
	defer os.RemoveAll(dir)
	in := filepath.Join(dir, "in.bin")
	if err := os.WriteFile(in, data, 0600); err != nil {
		return data, nil
	}
	format, vcodec, acodec, err := probeVideo(ctx, in)
	if err != nil {
		logfmt.Warn("videonorm: probe gagal (%v), kirim apa adanya.\n", err)
		return data, nil
	}
	// TANPA stream video = bukan video (kasus Sep 2026: TS 37MB berisi
	// audio saja → transcode jadi MP4 hitam + thumb gagal). Tolak LOUD
	// supaya tak terkirim bubble hitam: lebih baik pesan gagal jujur /
	// fallback rantai berikutnya jalan.
	if vcodec == "" {
		return nil, fmt.Errorf("tidak ada stream video (%s/%s)", format, acodec)
	}
	if waCompliant(format, vcodec, acodec) {
		return data, nil
	}
	// Langkah ringan dulu sebelum transcode berat. Tiap langkah divalidasi:
	// output harus compliant, kalau tidak lanjut ke langkah berikutnya.
	if norm, ok := tryStep(ctx, dir, in, "remux", true, remuxCopy); ok {
		return norm, nil
	}
	if norm, ok := tryStep(ctx, dir, in, "audiofix", true, transcodeAudioOnly); ok {
		return norm, nil
	}
	out := filepath.Join(dir, "out.mp4")
	if err := transcodeToMP4(ctx, in, out); err != nil {
		logfmt.Warn("videonorm: transcode gagal (%v), kirim apa adanya.\n", err)
		return data, nil
	}
	norm, err := os.ReadFile(out)
	if err != nil || len(norm) == 0 {
		return data, nil
	}
	// Validasi output: stream video wajib tetap ada (input bervideo →
	// output tak bervideo = kirim yang asli, jangan MP4 hitam).
	if _, ov, _, err := probeVideo(ctx, out); err != nil || ov == "" {
		logfmt.Warn("videonorm: output kehilangan video, kirim apa adanya.\n")
		return data, nil
	}
	fmt.Printf("videonorm: %s/%s/%s -> mp4/h264+aac (%d -> %d byte)\n", format, vcodec, acodec, len(data), len(norm))
	return norm, nil
}

// lastLine mengambil baris non-kosong TERAKHIR (error ffmpeg yang asli
// selalu di ujung output; baris pertama hanya banner versi yang
// menyesatkan — pelajaran Sep 2026).
func lastLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return "-"
}
