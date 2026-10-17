package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/purpshell/meowcaller"
	whatsmeow "github.com/polymorfa/hypermeow"

	"wabot-go/internal/logfmt"
	"wabot-go/internal/store"
)

// Penyimpanan ASET panggilan (suara untuk voice call, video untuk video call)
// yang bisa dipanggil ulang via keyword (#nama) — tidak lagi terpaku
// greeting.mp3 atau reply video tiap kali.
//
// Layout persistent (di dalam data/, tahan restart & tidak ikut deploy):
//
//	data/call-assets/audio/<keyword>/  meta.json · source.bin · audio.wav
//	data/call-assets/video/<keyword>/  meta.json · source.bin · frames.h264 · audio.wav
//
// Kata kunci di-sanitasi ke [a-z0-9_-] (maks 32). .voicecall save selalu
// menyimpan aset SUARA, .videocall save selalu aset VIDEO — jadi #keyword di
// masing-masing command selalu merujuk jenis yang benar.
const (
	callAssetAudio = "audio"
	callAssetVideo = "video"
)

// callAssetKeywordRe membatasi keyword ke karakter aman filesystem (huruf
// kecil/angka/underscore/dash, wajib diawali alfanumerik).
var callAssetKeywordRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// callAssetMeta adalah metadata ringkas satu aset.
type callAssetMeta struct {
	Type    string `json:"type"`
	AddedAt int64  `json:"addedAt"`
	Size    int64  `json:"size"`
}

// callAssetRoot mengembalikan folder induk aset untuk satu jenis (audio/video).
func callAssetRoot(kind string) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(cwd, "data", "call-assets", kind), nil
}

// callAssetDir mengembalikan folder satu aset. Tidak membuat folder.
func callAssetDir(kind, keyword string) (string, error) {
	root, err := callAssetRoot(kind)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, keyword), nil
}

// normalizeAssetKeyword membersihkan/memvalidasi keyword (prefix '#' opsional).
func normalizeAssetKeyword(s string) (string, error) {
	k := strings.ToLower(strings.TrimSpace(s))
	k = strings.TrimPrefix(k, "#")
	if !callAssetKeywordRe.MatchString(k) {
		return "", fmt.Errorf("keyword %q tidak valid (huruf kecil/angka/_- , maks 32, diawali alfanumerik)", s)
	}
	return k, nil
}

// splitAssetKeyword memisahkan token "#keyword" dari teks argumen mentah dan
// mengembalikan sisanya apa adanya. Dipakai SEBELUM resolve target / split
// "|jam" supaya penanda aset tidak dianggap bagian nomor telepon.
func splitAssetKeyword(raw string) (rest string, keyword string) {
	fields := strings.Fields(raw)
	kept := make([]string, 0, len(fields))
	for _, f := range fields {
		if keyword == "" && len(f) > 1 && strings.HasPrefix(f, "#") {
			keyword = strings.ToLower(strings.TrimPrefix(f, "#"))
			continue
		}
		kept = append(kept, f)
	}
	return strings.Join(kept, " "), keyword
}

// replaceAssetDir mengosongkan lalu membuat ulang folder aset (overwrite aman).
func replaceAssetDir(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return os.MkdirAll(dir, 0755)
}

// writeCallAssetMeta menulis meta.json (best-effort di pemanggil).
func writeCallAssetMeta(dir, kind string, size int64) error {
	b, err := json.Marshal(callAssetMeta{Type: kind, AddedAt: time.Now().Unix(), Size: size})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "meta.json"), b, 0600)
}

// takeRepliedMediaBytes mengunduh media dari pesan yang di-reply (audio
// selalu; video/document bila includeVideo). data=nil berarti tidak ada media
// yang relevan (bukan error).
func takeRepliedMediaBytes(ctx context.Context, mctx *store.MessageContext, includeVideo bool) ([]byte, string, error) {
	quoted := store.UnwrapMessage(store.ExtractQuotedMessage(mctx.RawEvt))
	if quoted == nil {
		return nil, "", nil
	}
	var dl whatsmeow.DownloadableMessage
	var mime string
	switch {
	case quoted.GetAudioMessage() != nil:
		a := quoted.GetAudioMessage()
		dl, mime = a, a.GetMimetype()
	case includeVideo && quoted.GetVideoMessage() != nil:
		v := quoted.GetVideoMessage()
		dl, mime = v, v.GetMimetype()
	case quoted.GetDocumentMessage() != nil:
		d := quoted.GetDocumentMessage()
		dl, mime = d, d.GetMimetype()
	default:
		return nil, "", nil
	}
	data, err := mctx.Client.Download(ctx, dl)
	if err != nil {
		return nil, "", err
	}
	return data, mime, nil
}

// openVoiceAsset membuka WAV aset suara (siap diputar meowcaller).
func openVoiceAsset(keyword string) (meowcaller.AudioSource, error) {
	dir, err := callAssetDir(callAssetAudio, keyword)
	if err != nil {
		return nil, err
	}
	wav := filepath.Join(dir, "audio.wav")
	if _, err := os.Stat(wav); err != nil {
		return nil, fmt.Errorf("aset suara #%s tidak ada", keyword)
	}
	return meowcaller.WAVFile(wav)
}

// loadVideoAsset memuat aset video: frame Annex-B (di-split ulang per frame)
// plus path WAV audionya ("" bila aset tanpa trek audio).
func loadVideoAsset(keyword string) (frames [][]byte, wavPath string, err error) {
	dir, err := callAssetDir(callAssetVideo, keyword)
	if err != nil {
		return nil, "", err
	}
	raw, err := os.ReadFile(filepath.Join(dir, "frames.h264"))
	if err != nil {
		return nil, "", fmt.Errorf("aset video #%s tidak ada", keyword)
	}
	frames = groupAnnexBFrames(raw)
	if len(frames) == 0 {
		return nil, "", fmt.Errorf("aset video #%s rusak (tanpa frame H.264)", keyword)
	}
	wavPath = filepath.Join(dir, "audio.wav")
	if _, statErr := os.Stat(wavPath); statErr != nil {
		wavPath = ""
	}
	return frames, wavPath, nil
}

// handleVoiceAssetSave menangani ".voicecall save <keyword>": reply voice
// note/audio/video → ekstrak WAV 16 kHz mono (format native meowcaller).
func handleVoiceAssetSave(ctx context.Context, mctx *store.MessageContext) error {
	if len(mctx.Args) < 2 {
		return store.ReplyText(ctx, mctx, ErrMsg("Voice Call", "Reply voice note/audio/video lalu .voicecall save <keyword>."))
	}
	keyword, err := normalizeAssetKeyword(mctx.Args[1])
	if err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Voice Call", err.Error()))
	}
	data, _, err := takeRepliedMediaBytes(ctx, mctx, true)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal unduh media: %v", err))
	}
	if data == nil {
		return store.ReplyText(ctx, mctx, "Reply voice note, audio, atau video yang ingin disimpan sebagai suara call.")
	}
	if len(data) > videoMaxSourceBytes {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Media %.0f MB melebihi batas 100 MB.", float64(len(data))/(1<<20)))
	}

	wavTmp, err := extractCallAudio(ctx, data)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal ekstrak audio: %v", err))
	}
	if wavTmp == "" {
		return store.ReplyText(ctx, mctx, "Media itu tidak punya trek audio.")
	}
	defer os.Remove(wavTmp)

	wav, err := os.ReadFile(wavTmp)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal baca audio hasil: %v", err))
	}

	dir, err := callAssetDir(callAssetAudio, keyword)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal siapkan folder aset: %v", err))
	}
	if err := replaceAssetDir(dir); err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal membuat folder aset: %v", err))
	}
	if err := os.WriteFile(filepath.Join(dir, "audio.wav"), wav, 0600); err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal simpan audio: %v", err))
	}
	// source disimpan best-effort (untuk referensi/re-transcode nanti).
	_ = os.WriteFile(filepath.Join(dir, "source.bin"), data, 0600)
	_ = writeCallAssetMeta(dir, callAssetAudio, int64(len(data)))

	logfmt.Info("Aset suara #%s tersimpan (%.1f KB).\n", keyword, float64(len(wav))/1024)
	return store.ReplyText(ctx, mctx, fmt.Sprintf(
		"Suara _#%s_ tersimpan (%.1f KB).\n\nPenggunaan:\n> .voicecall <target> #%s",
		keyword, float64(len(wav))/1024, keyword))
}

// handleVideoAssetSave menangani ".videocall save <keyword>": reply video →
// transcode SEKALI ke Annex-B (640px/15fps) + ekstrak audio, cache di folder
// aset supaya call berikutnya instan.
func handleVideoAssetSave(ctx context.Context, mctx *store.MessageContext) error {
	if len(mctx.Args) < 2 {
		return store.ReplyText(ctx, mctx, ErrMsg("Video Call", "Reply video lalu .videocall save <keyword>."))
	}
	keyword, err := normalizeAssetKeyword(mctx.Args[1])
	if err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Video Call", err.Error()))
	}
	src, ok, err := takeCallVideoSource(ctx, mctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal ambil video: %v", err))
	}
	if !ok {
		return store.ReplyText(ctx, mctx, "Reply video yang ingin disimpan sebagai aset video call.")
	}

	if err := store.ReplyText(ctx, mctx, "Menyiapkan aset video transcode..."); err != nil {
		logfmt.Warn("Gagal kirim info transcode aset video: %v\n", err)
	}
	aus, err := transcodeToAnnexB(ctx, src)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal transcode video: %v", err))
	}
	raw := bytes.Join(aus, nil)

	// Audio opsional — video tanpa trek audio tetap aset valid (call pakai
	// greeting sebagai audio).
	var wav []byte
	if wavTmp, aerr := extractCallAudio(ctx, src); aerr != nil {
		logfmt.Warn("Gagal ekstrak audio aset video #%s: %v (tanpa audio)\n", keyword, aerr)
	} else if wavTmp != "" {
		if b, rerr := os.ReadFile(wavTmp); rerr == nil {
			wav = b
		}
		_ = os.Remove(wavTmp)
	}

	dir, err := callAssetDir(callAssetVideo, keyword)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal siapkan folder aset: %v", err))
	}
	if err := replaceAssetDir(dir); err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal membuat folder aset: %v", err))
	}
	if err := os.WriteFile(filepath.Join(dir, "frames.h264"), raw, 0600); err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal simpan frame video: %v", err))
	}
	_ = os.WriteFile(filepath.Join(dir, "source.bin"), src, 0600)
	if len(wav) > 0 {
		_ = os.WriteFile(filepath.Join(dir, "audio.wav"), wav, 0600)
	}
	_ = writeCallAssetMeta(dir, callAssetVideo, int64(len(src)))

	audioNote := ""
	if len(wav) == 0 {
		audioNote = " (tanpa audio — greeting)"
	}
	logfmt.Info("Aset video #%s tersimpan: %d frame, %.1f KB.%s\n", keyword, len(aus), float64(len(raw))/1024, audioNote)
	return store.ReplyText(ctx, mctx, fmt.Sprintf(
		"Video transcode _#%s_ tersimpan (%.1f KB).\n\nPenggunaan:\n> .videocall <target> #%s",
		keyword, float64(len(raw))/1024, keyword))
}

// callCmdTitle memetakan nama command call ke judul tampilan standar.
func callCmdTitle(cmd string) string {
	if cmd == "videocall" {
		return "Video Call"
	}
	return "Voice Call"
}

// handleCallAssetDel menangani ".voicecall/.videocall del <keyword>".
func handleCallAssetDel(ctx context.Context, mctx *store.MessageContext, kind, cmd string) error {
	if len(mctx.Args) < 2 {
		return store.ReplyText(ctx, mctx, ErrMsg(callCmdTitle(cmd), fmt.Sprintf("Format: .%s del <keyword>.", cmd)))
	}
	keyword, err := normalizeAssetKeyword(mctx.Args[1])
	if err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg(callCmdTitle(cmd), err.Error()))
	}
	dir, err := callAssetDir(kind, keyword)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal akses aset: %v", err))
	}
	if _, err := os.Stat(dir); err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Aset `#%s` tidak ditemukan.", keyword))
	}
	if err := os.RemoveAll(dir); err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal hapus aset: %v", err))
	}
	return store.ReplyText(ctx, mctx, fmt.Sprintf("Aset _#%s_ dihapus.", keyword))
}

// handleCallAssetList menangani ".voicecall/.videocall list": daftar keyword
// aset beserta ukuran file utamanya.
func handleCallAssetList(ctx context.Context, mctx *store.MessageContext, kind, cmd string) error {
	root, err := callAssetRoot(kind)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal akses aset: %v", err))
	}
	entries, err := os.ReadDir(root)
	if err != nil && !os.IsNotExist(err) {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal baca aset: %v", err))
	}
	main := "audio.wav"
	if kind == callAssetVideo {
		main = "frames.h264"
	}
	var lines []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		size := int64(0)
		if st, serr := os.Stat(filepath.Join(root, e.Name(), main)); serr == nil {
			size = st.Size()
		}
		lines = append(lines, fmt.Sprintf("#%s (%.1f KB)", e.Name(), float64(size)/1024))
	}
	if len(lines) == 0 {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Belum ada aset %s. Reply media lalu `.%s save <keyword>`.", kind, cmd))
	}
	sort.Strings(lines)
	return store.ReplyText(ctx, mctx, fmt.Sprintf("Aset %s (%d):\n%s", kind, len(lines), strings.Join(lines, "\n")))
}
