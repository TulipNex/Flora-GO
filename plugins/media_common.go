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

	"wabot-go/internal/heavy"
	"wabot-go/internal/messaging"
	"wabot-go/internal/store"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

const maxToMediaBytes = 100 << 20

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

const (
	ttGIFFallbackFPS = 15
	ttGIFMaxFrames   = 150
)

var convertPath = sync.OnceValues(func() (string, error) {
	return exec.LookPath("convert")
})

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

	if out, err := runCmdOutput(ctx, webpConvertTimeout, convert, "-coalesce", inPath, frameTmpl); err != nil {
		return nil, fmt.Errorf("convert ekstrak frame: %v (%s)", err, out)
	}
	frames, _ := filepath.Glob(filepath.Join(tmpDir, "frame*.png"))
	if len(frames) == 0 {
		return nil, fmt.Errorf("tidak ada frame berhasil diekstrak")
	}
	if len(frames) > ttGIFMaxFrames {
		for _, f := range frames[ttGIFMaxFrames:] {
			_ = os.Remove(f)
		}
	}

	fps := ttGIFFallbackFPS
	if delays, derr := runCmdOutput(ctx, webpConvertTimeout, convert, "identify", "-format", "%T\n", inPath); derr == nil {
		if f := fpsFromDelays(delays); f > 0 {
			fps = f
		}
	}

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
	release, acqErr := heavy.AcquireTranscode(ctx)
	if acqErr != nil {
		cancel()
		return nil, acqErr
	}
	defer release()
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg rakit mp4: %v (%s)", err, stderr.String())
	}
	return os.ReadFile(outPath)
}

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
	fps := (100*n + sum/2) / sum
	if fps < 5 {
		fps = 5
	}
	if fps > 30 {
		fps = 30
	}
	return fps
}

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
	msg := &waE2E.Message{VideoMessage: vm}
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, msg)
	if err != nil {
		return err
	}
	store.LogOutgoing(mctx, "gif", "sticker->gif")
	store.TrackBotMedia(string(resp.ID), msg, "sticker->gif")
	return nil
}

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
