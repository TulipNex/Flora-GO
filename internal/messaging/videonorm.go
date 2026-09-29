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
)

// normTimeout membatasi sekali probe/transcode ffmpeg (file panel kecil;
// audio 16MB ~ puluhan detik, transcode 720p beberapa menit).
const normTimeout = 4 * time.Minute

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

// transcodeToMP4 mengubah video apa pun jadi MP4 H.264 + AAC + faststart,
// tinggi maks 720p (standar unduhan lama yang terbukti diputar semua klien).
func transcodeToMP4(ctx context.Context, inPath, outPath string) error {
	timeoutCtx, cancel := context.WithTimeout(ctx, normTimeout)
	defer cancel()
	cmd := exec.CommandContext(timeoutCtx, "ffmpeg",
		"-y", "-i", inPath,
		"-c:v", "libx264", "-pix_fmt", "yuv420p",
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

// PrepareVideoBytes memastikan byte video layak dikirim sebagai VideoMessage:
// tolak kosong/HTML (fail-LOUD, error), passthrough bila sudah compliant
// (nol biaya untuk video normal), transcode selain itu. Gagal probe/
// transcode = byte asli + log (fail-soft, perilaku lama) — kecuali validasi
// isi yang tetap error supaya tak terkirim bubble hantu.
func PrepareVideoBytes(ctx context.Context, data []byte) ([]byte, error) {
	if err := CheckMediaBytes(data); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "wabot-vnorm-*")
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
		fmt.Printf("⚠️ videonorm: probe gagal (%v), kirim apa adanya.\n", err)
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
	out := filepath.Join(dir, "out.mp4")
	if err := transcodeToMP4(ctx, in, out); err != nil {
		fmt.Printf("⚠️ videonorm: transcode gagal (%v), kirim apa adanya.\n", err)
		return data, nil
	}
	norm, err := os.ReadFile(out)
	if err != nil || len(norm) == 0 {
		return data, nil
	}
	// Validasi output: stream video wajib tetap ada (input bervideo →
	// output tak bervideo = kirim yang asli, jangan MP4 hitam).
	if _, ov, _, err := probeVideo(ctx, out); err != nil || ov == "" {
		fmt.Printf("⚠️ videonorm: output kehilangan video, kirim apa adanya.\n")
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
