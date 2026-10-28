package plugins

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/purpshell/meowcaller"
	"google.golang.org/protobuf/proto"

	"wabot-go/internal/logfmt"
	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterVideoCall)
}

// Batasan video call (diselaraskan dengan batas media lain di repo,
// mis. maxRVOMediaBytes 100MB di ownerrvo.go — download menahan byte di RAM).
const (
	// videoMaxSourceBytes membatasi video sumber (reply) untuk transcode.
	videoMaxSourceBytes = 100 << 20
	// videoFrameRate adalah fps target transcode sekaligus pacing pengiriman.
	videoFrameRate = 15
	// videoMaxSend membatasi durasi pompa frame (jaring pengaman: jangan
	// kirim selamanya kalau OnEnd luput).
	videoMaxSend = 10 * time.Minute
	// videoTranscodeTimeout membatasi sekali jalan ffmpeg.
	videoTranscodeTimeout = 150 * time.Second
	// videoMaxCallAge adalah umur maksimum satu entri di registry. Jaring
	// pengaman terakhir bila status akhir call tidak pernah terdeteksi
	// (OnEnd hanya satu slot dan tidak diputar ulang oleh meowcaller).
	videoMaxCallAge = 3 * time.Hour
)

// trackedVideoCall adalah satu call yang terdaftar untuk fitur video
// (outbound .videocall maupun incoming). Penghitung video + pompa dikelola
// di sini supaya bisa dihentikan bersih saat call berakhir.
type trackedVideoCall struct {
	call    *meowcaller.Call
	phone   string
	addedAt time.Time

	sinkMu sync.Mutex
	sink   *videoCounter

	// audioPath adalah file WAV trek audio video sumber (diekstrak saat
	// transcode, diputar sebagai pengganti greeting). Decoder + file
	// dibersihkan via closeAudio (dipanggil releaseVideoCall).
	audioPath string
	audioSrc  meowcaller.AudioSource
	audMu     sync.Mutex

	pumpMu     sync.Mutex
	pumpCancel context.CancelFunc
}

var (
	trackedVideoMu    sync.Mutex
	trackedVideoCalls = map[string]*trackedVideoCall{}
)

// trackVideoCall mendaftarkan call. Pemanggil TETAP pemilik callback OnEnd
// (meowcaller hanya menyimpan satu OnEnd — penimpaan menghapus perilaku
// lama), jadi cleanup dipanggil eksplisit dari closure OnEnd masing-masing
// jalur via releaseVideoCall. watchEnd dipasang sebagai jaring pengaman.
func trackVideoCall(call *meowcaller.Call, phone string) *trackedVideoCall {
	e := &trackedVideoCall{call: call, phone: phone, addedAt: time.Now()}
	trackedVideoMu.Lock()
	trackedVideoCalls[call.ID()] = e
	trackedVideoMu.Unlock()
	go e.watchEnd()
	return e
}

// watchEnd adalah jaring pengaman cleanup: OnEnd di meowcaller hanya satu
// slot dan TIDAK diputar ulang bila call sudah berakhir sebelum callback
// dipasang, sehingga entri registry + sink bisa tertinggal selamanya.
// Polling status call (bukan callback) supaya tidak berebut slot callback
// dengan jalur outbound yang memakai OnStateChange untuk fase ringing.
func (e *trackedVideoCall) watchEnd() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		trackedVideoMu.Lock()
		_, ok := trackedVideoCalls[e.call.ID()]
		trackedVideoMu.Unlock()
		if !ok {
			return
		}
		if e.call.State() == meowcaller.CallPhaseEnded || time.Since(e.addedAt) > videoMaxCallAge {
			releaseVideoCall(e.call.ID())
			return
		}
	}
}

// releaseVideoCall menghentikan pompa, melepas sink video, dan menghapus call
// dari registry. Aman dipanggil berkali-kali / untuk ID tak dikenal.
func releaseVideoCall(callID string) {
	trackedVideoMu.Lock()
	e, ok := trackedVideoCalls[callID]
	if ok {
		delete(trackedVideoCalls, callID)
	}
	trackedVideoMu.Unlock()
	if !ok {
		return
	}
	e.stopPump()
	// Lepas sink dulu supaya engine tidak menulis ke penghitung yang sudah
	// ditutup (meowcaller tidak menutup videoSink saat call berakhir).
	e.call.ReceiveVideo(nil)
	e.closeVideoSink()
	e.closeAudio()
}

// videoCounter adalah VideoSink yang hanya MENGHITUNG frame/byte video masuk
// untuk log — tidak menulis file. Dulu jalur ini memakai AnnexBRecorder yang
// menaruh rekaman .h264 di wabot-tmp dan tidak pernah dihapus, sehingga
// folder temp membengkak tiap video call. Bukti "ada video masuk" tetap
// didapat dari angka di log.
type videoCounter struct {
	mu     sync.Mutex
	frames int
	bytes  int64
	closed bool
}

// WriteVideo mencatat satu access unit H.264.
func (v *videoCounter) WriteVideo(accessUnit []byte) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return nil
	}
	v.frames++
	v.bytes += int64(len(accessUnit))
	return nil
}

// Close menghentikan penghitungan. Aman dipanggil berkali-kali.
func (v *videoCounter) Close() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.closed = true
	return nil
}

// stats mengembalikan jumlah frame + byte yang sudah diterima.
func (v *videoCounter) stats() (frames int, bytes int64) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.frames, v.bytes
}

// closeAudio menutup decoder audio pengganti greeting dan menghapus file
// WAV sementaranya (best-effort). Close ganda aman (kontrak AudioSource).
func (e *trackedVideoCall) closeAudio() {
	e.audMu.Lock()
	defer e.audMu.Unlock()
	if e.audioSrc != nil {
		_ = e.audioSrc.Close()
		e.audioSrc = nil
	}
	if e.audioPath != "" {
		_ = os.Remove(e.audioPath)
		e.audioPath = ""
	}
}

// setAudio mengganti trek audio aktif: decoder + file lama ditutup/dihapus
// dulu (Subscribe me-replace player tapi tidak menutup decoder lama).
func (e *trackedVideoCall) setAudio(src meowcaller.AudioSource, path string) {
	e.audMu.Lock()
	defer e.audMu.Unlock()
	if e.audioSrc != nil {
		_ = e.audioSrc.Close()
		e.audioSrc = nil
	}
	if e.audioPath != "" {
		_ = os.Remove(e.audioPath)
		e.audioPath = ""
	}
	e.audioSrc = src
	e.audioPath = path
}

// ensureVideoSink memasang penghitung video SEKALI (analog sisi terima audio).
// Tanpa sink, video inbound dibuang meowcaller.
func (e *trackedVideoCall) ensureVideoSink() {
	e.sinkMu.Lock()
	defer e.sinkMu.Unlock()
	if e.sink != nil {
		return
	}
	sink := &videoCounter{}
	e.sink = sink
	e.call.ReceiveVideo(sink)
}

// closeVideoSink menutup penghitung dan melaporkan berapa video yang masuk
// (bukti ada video dari peer — berguna karena path video upstream NOT
// VALIDATED). Tidak ada file yang ditulis, jadi tidak ada yang perlu dihapus.
func (e *trackedVideoCall) closeVideoSink() {
	e.sinkMu.Lock()
	defer e.sinkMu.Unlock()
	if e.sink == nil {
		return
	}
	sink := e.sink
	e.sink = nil
	if err := sink.Close(); err != nil {
		logfmt.Warn("Gagal menutup penghitung video %s: %v\n", e.phone, err)
		return
	}
	frames, bytes := sink.stats()
	if frames > 0 {
		logfmt.Call("Video %s: %d frame, %.1f KB diterima (tidak direkam ke file).\n",
			e.phone, frames, float64(bytes)/1024)
		return
	}
	logfmt.Call("Video %s: tidak ada frame yang masuk.\n", e.phone)
}

// startPump mengirim access-unit H.264 berulang dengan pacing frame-rate.
// SendVideo error = pipeline video belum live (belum accept/relay) —
// frame yang sama dicoba ulang, bukan dilewati. Setelah live tapi
// gate upgrade tertutup, meowcaller membuang diam-diam (nil) sampai peer
// ack — pompa tidak perlu tahu, cukup terus mengirim.
func (e *trackedVideoCall) startPump(aus [][]byte) {
	e.pumpMu.Lock()
	defer e.pumpMu.Unlock()
	if e.pumpCancel != nil {
		e.pumpCancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), videoMaxSend)
	e.pumpCancel = cancel
	frameDur := time.Second / videoFrameRate
	go func() {
		defer cancel()
		ticker := time.NewTicker(frameDur)
		defer ticker.Stop()
		i := 0
		for {
			if e.call.State() == meowcaller.CallPhaseEnded {
				return
			}
			if err := e.call.SendVideoWithDuration(aus[i], frameDur); err != nil {
				select {
				case <-ctx.Done():
					return
				case <-time.After(500 * time.Millisecond):
					continue
				}
			}
			i = (i + 1) % len(aus)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// stopPump menghentikan goroutine pengirim video (dipakai saat call berakhir).
func (e *trackedVideoCall) stopPump() {
	e.pumpMu.Lock()
	defer e.pumpMu.Unlock()
	if e.pumpCancel != nil {
		e.pumpCancel()
		e.pumpCancel = nil
	}
}

// attachVideoReception memasang sisi TERIMA video untuk satu call: penghitung
// bila call sudah video sejak awal (IsVideo), plus auto-accept upgrade
// mid-call peer (keputusan owner: langsung, tanpa konfirmasi).
func attachVideoReception(e *trackedVideoCall) {
	if e.call.IsVideo() {
		e.ensureVideoSink()
	}
	e.call.OnVideoState(func(st meowcaller.VideoState) {
		logfmt.Call("Video %s: aktif=%v upgrade=%v orientasi=%d\n",
			e.phone, st.Active, st.Upgrade, st.Orientation)
		if st.Upgrade {
			if err := e.call.AcceptVideo(); err != nil {
				logfmt.Warn("Gagal AcceptVideo dari %s: %v\n", e.phone, err)
				return
			}
			logfmt.Call("Upgrade video dari %s diterima otomatis.\n", e.phone)
			e.ensureVideoSink()
		} else if st.Active {
			e.ensureVideoSink()
		}
	})
}

// takeCallVideoSource mengambil bytes video dari pesan sendiri (kirim video
// dengan caption .videocall) atau quoted (reply video + .videocall <target>).
// Mengembalikan ok=false bila tidak ada video — pemanggil lanjut mode
// terima-saja. Pola unduh mengikuti shazamTakeAudio di shazam.go.
func takeCallVideoSource(ctx context.Context, mctx *store.MessageContext) (data []byte, ok bool, err error) {
	msg := store.UnwrapMessage(mctx.RawEvt.Message)
	quoted := store.UnwrapMessage(store.ExtractQuotedMessage(mctx.RawEvt))
	for _, m := range []*waE2E.Message{msg, quoted} {
		if m == nil {
			continue
		}
		if vid := m.GetVideoMessage(); vid != nil {
			data, err := mctx.Client.Download(ctx, vid)
			if err != nil {
				return nil, false, fmt.Errorf("gagal unduh video: %v", err)
			}
			if len(data) == 0 {
				return nil, false, fmt.Errorf("video kosong")
			}
			if len(data) > videoMaxSourceBytes {
				return nil, false, fmt.Errorf("video %.0f MB melebihi batas 100 MB", float64(len(data))/(1<<20))
			}
			return data, true, nil
		}
	}
	return nil, false, nil
}

// transcodeToAnnexB mengubah bytes video apapun menjadi H.264 Annex-B
// (640px, 15fps, baseline, GOP 30 — IDR tiap ~2 detik supaya recovery
// keyframe PLI/FIR peer cepat) lalu memecahnya per FRAME. Satu frame =
// satu SendVideo (lihat videoSender.protectAccessUnit: AUD dibuang,
// sisanya dipaketkan jadi satu unit RTP bertanda marker).
func transcodeToAnnexB(ctx context.Context, src []byte) ([][]byte, error) {
	tmpDir, err := os.MkdirTemp(store.MustBotTempDir(), "vcall-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)
	inPath := filepath.Join(tmpDir, "input.mp4")
	outPath := filepath.Join(tmpDir, "out.h264")
	if err := os.WriteFile(inPath, src, 0600); err != nil {
		return nil, err
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, videoTranscodeTimeout)
	defer cancel()
	cmd := exec.CommandContext(timeoutCtx, "ffmpeg",
		"-y", "-i", inPath,
		"-vf", "scale=640:-2",
		"-c:v", "libx264", "-profile:v", "baseline", "-level", "3.0",
		"-pix_fmt", "yuv420p", "-r", "15", "-g", "30",
		"-an", "-t", "180", "-f", "h264", outPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %v (%s)", err, stderr.String())
	}
	raw, err := os.ReadFile(outPath)
	if err != nil {
		return nil, err
	}
	aus := groupAnnexBFrames(raw)
	if len(aus) == 0 {
		return nil, fmt.Errorf("ffmpeg tidak menghasilkan frame H.264")
	}
	return aus, nil
}

// extractCallAudio mengekstrak trek audio video sumber menjadi WAV 16 kHz
// mono (format native meowcaller — tanpa biaya decode MP3). Mengembalikan
// path file di wabot-tmp (pemanggil menghapus via entry.audioPath).
// Video tanpa trek audio = ("", nil) — pemanggil fallback ke greeting.
func extractCallAudio(ctx context.Context, src []byte) (string, error) {
	tmpDir, err := os.MkdirTemp(store.MustBotTempDir(), "vcall-aud-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmpDir)
	inPath := filepath.Join(tmpDir, "input.mp4")
	outPath := filepath.Join(store.MustBotTempDir(),
		fmt.Sprintf("vcall-audio-%d.wav", time.Now().UnixNano()))
	if err := os.WriteFile(inPath, src, 0600); err != nil {
		return "", err
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(timeoutCtx, "ffmpeg",
		"-y", "-i", inPath,
		"-vn", "-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le",
		"-t", "180", outPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := stderr.String()
		// Video memang tidak punya trek audio — bukan error, fallback.
		if strings.Contains(msg, "matches no streams") ||
			strings.Contains(msg, "does not contain any stream") {
			return "", nil
		}
		return "", fmt.Errorf("ffmpeg audio: %v (%s)", err, msg)
	}
	return outPath, nil
}

// loopAudioSource membungkus WAVFile supaya diputar BERULANG mengikuti loop
// video: begitu sumber mencapai EOF, file dibuka ulang dan playback lanjut
// tanpa pernah memberi Player sinyal selesai. Tanpa ini, audio hanya berbunyi
// di putaran video pertama — Player masuk Idle lalu mengirim senyap
// padahal gambar terus looping.
type loopAudioSource struct {
	mu     sync.Mutex
	path   string
	src    meowcaller.AudioSource
	closed bool
}

// newLoopAudioSource membuka path (validasi awal) dan mengembalikan sumber
// audio yang memutar dirinya sendiri berulang.
func newLoopAudioSource(path string) (*loopAudioSource, error) {
	src, err := meowcaller.WAVFile(path)
	if err != nil {
		return nil, err
	}
	return &loopAudioSource{path: path, src: src}, nil
}

// ReadFrame mengembalikan frame berikutnya; saat sumber habis, file dibuka
// ulang lalu lanjut (loop) sampai Close dipanggil.
func (l *loopAudioSource) ReadFrame() ([]float32, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, io.EOF
	}
	frame, err := l.src.ReadFrame()
	if err == nil {
		return frame, nil
	}
	if err != io.EOF {
		return nil, err
	}
	// Akhir putaran — buka ulang dan mulai dari awal.
	_ = l.src.Close()
	next, oerr := meowcaller.WAVFile(l.path)
	if oerr != nil {
		return nil, oerr
	}
	l.src = next
	return l.src.ReadFrame()
}

// Close menghentikan loop; ReadFrame berikutnya mengembalikan io.EOF. Aman
// dipanggil berkali-kali.
func (l *loopAudioSource) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	if l.src != nil {
		err := l.src.Close()
		l.src = nil
		return err
	}
	return nil
}

// onAcceptVideo adalah varian onAccept untuk video call: bubble progres
// sama, tapi audio yang diputar adalah trek audio video sumber bila ada
// (greeting.mp3 hanya fallback).
func (p *callProgress) onAcceptVideo(call *meowcaller.Call, audioSrc meowcaller.AudioSource) {
	p.mu.Lock()
	if p.ended {
		p.mu.Unlock()
		return
	}
	p.acceptedAt = time.Now()
	phone := p.phone
	p.mu.Unlock()

	logfmt.Call("Panggilan ke %s diterima\n", phone)
	p.edit(context.Background(), "Terhubung dengan "+phone+"...")
	if audioSrc != nil {
		call.Play(audioSrc)
		return
	}
	if mp3, err := meowcaller.MP3File("greeting.mp3"); err != nil {
		logfmt.Error("Gagal membuka/decode greeting.mp3: %v\n", err)
	} else {
		call.Play(mp3)
	}
}

// splitAnnexBNALUs memecah stream Annex-B per NAL unit (start-code ikut
// masing-masing unit).
func splitAnnexBNALUs(raw []byte) [][]byte {
	var starts []int
	for i := 0; i+2 < len(raw); {
		if raw[i] == 0 && raw[i+1] == 0 && raw[i+2] == 1 {
			starts = append(starts, i)
			i += 3
		} else if i+3 < len(raw) && raw[i] == 0 && raw[i+1] == 0 && raw[i+2] == 0 && raw[i+3] == 1 {
			starts = append(starts, i)
			i += 4
		} else {
			i++
		}
	}
	var nalus [][]byte
	for k, s := range starts {
		end := len(raw)
		if k+1 < len(starts) {
			end = starts[k+1]
		}
		if end > s {
			nalus = append(nalus, raw[s:end])
		}
	}
	return nalus
}

// annexBNALUHeader mengambil header byte pertama setelah start-code
// (0 jika unit rusak).
func annexBNALUHeader(nalu []byte) byte {
	i := 0
	for i < len(nalu) && nalu[i] == 0 {
		i++
	}
	if i < len(nalu) && nalu[i] == 1 {
		i++
	} else {
		return 0
	}
	if i >= len(nalu) {
		return 0
	}
	return nalu[i]
}

// groupAnnexBFrames mengelompokkan NAL unit menjadi access-unit per frame:
// frame baru dimulai saat slice (tipe 1/5) muncul padahal unit berjalan
// sudah berisi slice. SPS/PPS/SEI (6/7/8) menempel ke frame berikutnya.
func groupAnnexBFrames(raw []byte) [][]byte {
	var frames [][]byte
	var cur []byte
	hasSlice := false
	for _, n := range splitAnnexBNALUs(raw) {
		typ := annexBNALUHeader(n) & 0x1F
		if (typ == 1 || typ == 5) && hasSlice {
			frames = append(frames, cur)
			cur = nil
			hasSlice = false
		}
		cur = append(cur, n...)
		if typ == 1 || typ == 5 {
			hasSlice = true
		}
	}
	if len(cur) > 0 {
		frames = append(frames, cur)
	}
	return frames
}

// RegisterVideoCall mendaftarkan command video call KHUSUS OWNER:
// .videocall (outbound video, sumber dari reply/kirim video ATAU aset
// #keyword). Aset dikelola lewat ".videocall save|del|list". 1:1 saja —
// group video upstream masih eksperimental.
func RegisterVideoCall(r *Registry) {
	r.Register(Command{
		Name:        "videocall",
		Aliases:     []string{"vcall"},
		Description: "Video call WhatsApp (owner). Reply video / aset #kw + .videocall @user / 628xxx (+<save|del|list> <kw>)",
		Category:    CategoryOwner,
		Handler:     handleVideoCall,
	})
}

// videoCallHelp adalah blok bantuan command .videocall.
func videoCallHelp() string {
	return "*Video Call*\n\n" +
		"`Call`\n" +
		"Format : .videocall <target> <asset>\n\n" +
		"```Target : @user • number • reply\n" +
		"Asset : #keyword • reply```\n\n" +
		"`Asset`\n" +
		"Format : .videocall <action> [keyword]\n\n" +
		"```Action : save • del • list\n" +
		"Keyword: random word```"
}

// handleVideoCall melakukan video call keluar. Target di-resolve persis
// seperti .voicecall (mention/reply/nomor); gambar dari video yang dikirim
// bersama command atau di-reply. Tanpa video = mode terima-saja (peer
// tetap bisa menyalakan kamera, direkam).
func handleVideoCall(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	if callClient == nil {
		return store.ReplyText(ctx, mctx, "Fitur call belum aktif.")
	}
	// Subcommand manajemen aset video (dicek sebelum resolve target).
	if len(mctx.Args) > 0 {
		switch strings.ToLower(strings.TrimSpace(mctx.Args[0])) {
		case "save":
			return handleVideoAssetSave(ctx, mctx)
		case "del":
			return handleCallAssetDel(ctx, mctx, callAssetVideo, "videocall")
		case "list":
			return handleCallAssetList(ctx, mctx, callAssetVideo, "videocall")
		}
	}

	raw := strings.TrimSpace(mctx.RawArgs)
	raw, keyword := splitAssetKeyword(raw)
	// Tanpa target apa pun -> bantuan, bukan pesan "format salah".
	if noCallTarget(mctx, raw) {
		return store.ReplyText(ctx, mctx, videoCallHelp())
	}
	target, err := resolveCallTarget(ctx, mctx, raw)
	if err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Video Call", err.Error()))
	}
	if IsBotPhoneTarget(mctx.Client, target) {
		return store.ReplyText(ctx, mctx, ErrMsg("Video Call", fmt.Sprintf("Nomor %s adalah nomor bot sendiri.", target)))
	}

	var aus [][]byte
	var audioSrc meowcaller.AudioSource
	var audioPath string
	haveVideo := false
	if keyword != "" {
		// Aset video tersimpan: frame + audio sudah di-cache → tanpa
		// transcode ulang saat call.
		var wavPath string
		aus, wavPath, err = loadVideoAsset(keyword)
		if err != nil {
			return store.ReplyText(ctx, mctx, err.Error())
		}
		haveVideo = true
		if wavPath != "" {
			if audioSrc, err = newLoopAudioSource(wavPath); err != nil {
				logfmt.Warn("Gagal buka audio aset #%s: %v (fallback greeting)\n", keyword, err)
				audioSrc = nil
			}
		}
		// audioPath sengaja kosong: WAV ini milik aset, JANGAN dihapus saat
		// call berakhir (closeAudio menghapus audioPath).
	} else {
		src, hv, verr := takeCallVideoSource(ctx, mctx)
		if verr != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal ambil video: %v", verr))
		}
		haveVideo = hv
		if haveVideo {
			if err := store.ReplyText(ctx, mctx, "Menyiapkan video (transcode)..."); err != nil {
				logfmt.Warn("Gagal kirim info transcode vcall: %v\n", err)
			}
			aus, err = transcodeToAnnexB(ctx, src)
			if err != nil {
				return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal siapkan video: %v", err))
			}
			// Trek audio video sumber dipakai sebagai pengganti greeting.
			// Gagal ekstrak = fallback greeting (warn saja, jangan gagalkan call).
			if audioPath, err = extractCallAudio(ctx, src); err != nil {
				logfmt.Warn("Gagal ekstrak audio vcall ke %s: %v (fallback greeting)\n", target, err)
				audioPath = ""
			} else if audioPath != "" {
				if audioSrc, err = newLoopAudioSource(audioPath); err != nil {
					logfmt.Warn("Gagal buka audio vcall %s: %v (fallback greeting)\n", audioPath, err)
					audioSrc = nil
					_ = os.Remove(audioPath)
					audioPath = ""
				}
			}
		}
	}

	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, textMessage("Memanggil video "+target+"..."))
	if err != nil {
		if audioSrc != nil {
			_ = audioSrc.Close()
		}
		if audioPath != "" {
			_ = os.Remove(audioPath)
		}
		return fmt.Errorf("gagal kirim bubble progres: %w", err)
	}
	p := &callProgress{client: mctx.Client, chat: mctx.Chat, msgID: resp.ID, phone: target}

	call, err := callClient.CallWithOptions(ctx, target, meowcaller.CallOptions{Video: true})
	if err != nil {
		if audioSrc != nil {
			_ = audioSrc.Close()
		}
		if audioPath != "" {
			_ = os.Remove(audioPath)
		}
		p.edit(ctx, "Gagal memanggil video "+target+": "+err.Error())
		return nil
	}
	entry := trackVideoCall(call, target)
	entry.setAudio(audioSrc, audioPath)
	// OnEnd dipasang PALING AWAL (sebelum callback lain): meowcaller tidak
	// memutar ulang OnEnd bila call keburu berakhir, jadi jendela antara
	// CallWithOptions kembali dan registrasi callback harus sesempit mungkin.
	call.OnEnd(func(reason string) {
		p.onEnd(reason)
		releaseVideoCall(call.ID())
	})
	attachVideoReception(entry)

	call.OnStateChange(func(ph meowcaller.CallPhase) {
		if ph == meowcaller.CallPhaseRinging {
			p.onRinging()
		}
	})
	call.OnPeerAccept(func() {
		p.onAcceptVideo(call, audioSrc)
		if len(aus) > 0 {
			entry.startPump(aus)
		}
	})
	return nil
}

// textMessage membangun pesan teks bubble (dipakai progres .videocall).
func textMessage(text string) *waE2E.Message {
	return &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String(text)},
	}
}

// CleanStaleCallTemp menyapu sisa artefak panggilan di wabot-tmp
// (vcall-*.h264, vcall-audio-*.wav, folder transcode vcall-*/vcall-aud-*).
//
// Versi lama bot merekam video masuk ke file .h264 dan tidak pernah
// menghapusnya, jadi file itu menumpuk. Sejak perekaman diganti penghitung
// (videoCounter) tidak ada lagi file baru; apa pun yang berawalan "vcall-"
// di folder temp adalah sampah. Dipanggil sekali saat startup — tidak ada
// panggilan aktif — sehingga aman menghapus semuanya tanpa cek umur.
func CleanStaleCallTemp() {
	tmpBase, err := store.BotTempDir()
	if err != nil {
		return
	}
	entries, err := os.ReadDir(tmpBase)
	if err != nil {
		return
	}
	var removed int
	var freed int64
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "vcall-") {
			continue
		}
		p := filepath.Join(tmpBase, e.Name())
		if info, ierr := e.Info(); ierr == nil && !info.IsDir() {
			freed += info.Size()
		}
		if rerr := os.RemoveAll(p); rerr != nil {
			logfmt.Warn("Gagal hapus sisa temp panggilan %s: %v\n", e.Name(), rerr)
			continue
		}
		removed++
	}
	if removed > 0 {
		logfmt.Clean("%d sisa file temp panggilan dibersihkan (%.1f MB).\n",
			removed, float64(freed)/(1<<20))
	}
}
