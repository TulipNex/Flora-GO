package plugins

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"image"
	"math/big"
	"strings"
	"sync"
	"time"

	"wabot-go/internal/logfmt"
	"wabot-go/internal/messaging"
	"wabot-go/internal/store"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func init() {
	registerPlugin(RegisterSwgc)
}

// Status grup (GroupStatusMessageV2): posting status yang tampil di tab
// Status DALAM grup — bukan status pribadi, bukan pesan grup biasa.
// Port resep Baileys: konten (teks/media + messageSecret acak 32B) dibungkus
// FutureProofMessage, dikirim ke JID grup (distribusi sender-key seperti
// pesan grup biasa). Terisolasi (file ini saja).
func RegisterSwgc(r *Registry) {
	r.Register(Command{
		Name:        "swgc",
		Aliases:     []string{"statusgc", "gstatus"},
		Description: "Kirim status grup (.swgc <teks>/reply media)",
		Category:    CategoryGrup,
		Handler:     handleSwgc,
	})
	r.Register(Command{
		Name:        "antistatus",
		Description: "Auto-hapus SWGC orang (.antistatus on/off)",
		Category:    CategoryGrup,
		Handler:     handleAntistatus,
	})
}

func handleAntistatus(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireModGate(ctx, mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	arg := strings.ToLower(strings.TrimSpace(strings.Join(mctx.Args, " ")))
	switch arg {
	case "on", "aktif", "enable":
		if err := store.Groups().SetAntistatus(ctx, mctx.Chat.String(), true); err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal menyalakan antistatus: %v", err))
		}
		return store.ReplyText(ctx, mctx, "Antistatus *aktif* — status grup dari orang lain otomatis dihapus (bot harus admin; status buatan bot sendiri tidak ikut dihapus).")
	case "off", "mati", "disable":
		if err := store.Groups().SetAntistatus(ctx, mctx.Chat.String(), false); err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal mematikan antistatus: %v", err))
		}
		return store.ReplyText(ctx, mctx, "Antistatus *mati*.")
	default:
		on, err := store.Groups().IsAntistatusOn(ctx, mctx.Chat.String())
		if err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal baca status antistatus: %v", err))
		}
		status := "mati"
		if on {
			status = "aktif"
		}
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Antistatus grup ini: *%s*\n\nPakai `.antistatus on` / `.antistatus off` untuk mengubah.", status))
	}
}

// antistatusFailCooldown membatasi balasan gagal-revoke: satu balasan per
// chat per jendela (status bisa datang bertubi-tubi; log console selalu jalan).
var (
	antistatusFailMu   sync.Mutex
	antistatusFailLast = map[string]time.Time{}
)

const antistatusFailCooldown = 10 * time.Minute

// CheckAntistatus mencegat SWGC orang di grup yang toggle-nya aktif lalu
// mencoba me-revoke-nya (butuh bot admin). true = ditangani (setop).
// Catatan jujur: server BISA menolak revoke pesan tipe status (hanya
// pembuatnya yang boleh hapus) — penolakan dicatat + dibalas SEKALI per
// jendela cooldown supaya jelas berhasil vs ditolak.
func CheckAntistatus(client *whatsmeow.Client, evt *events.Message) bool {
	if !evt.Info.IsGroup || evt.Info.IsFromMe {
		return false
	}
	msg := store.UnwrapMessage(evt.Message)
	if msg == nil || msg.GetGroupStatusMessageV2() == nil {
		return false
	}
	ctx := context.Background()
	on, err := store.Groups().IsAntistatusOn(ctx, evt.Info.Chat.String())
	if err != nil {
		logfmt.Warn("antistatus: baca toggle gagal (%v)\n", err)
		return false
	}
	if !on {
		return false
	}
	mctx := &store.MessageContext{Client: client, Chat: evt.Info.Chat, Sender: evt.Info.Sender, RawEvt: evt}
	isBotAdmin, err := mctx.IsBotAdmin(ctx)
	if err != nil {
		logfmt.Warn("antistatus: cek admin bot gagal (%v)\n", err)
		return false
	}
	if !isBotAdmin {
		logfmt.Warn("antistatus: bot bukan admin di %s, lewati.\n", evt.Info.Chat.String())
		return false
	}
	_, err = client.SendMessage(ctx, evt.Info.Chat, client.BuildRevoke(evt.Info.Chat, evt.Info.Sender, evt.Info.ID))
	if err != nil {
		logfmt.Warn("antistatus: revoke SWGC %s gagal (%v)\n", evt.Info.ID, err)
		antistatusFailMu.Lock()
		last := antistatusFailLast[evt.Info.Chat.String()]
		antistatusFailLast[evt.Info.Chat.String()] = time.Now()
		antistatusFailMu.Unlock()
		if time.Since(last) > antistatusFailCooldown {
			_ = store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal hapus status grup: %v", err))
		}
		return true
	}
	logfmt.Plain("antistatus: SWGC %s dari %s dihapus.\n", evt.Info.ID, evt.Info.Sender.String())
	return true
}

// swgcBgPalette meniru palet resep Baileys untuk status teks (ARGB).
var swgcBgPalette = []uint32{
	0xff792139, 0xff243640, 0xff37213e, 0xff213c32, 0xff546e7a,
}

func swgcRandomSecret() ([]byte, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	return secret, nil
}

func handleSwgc(ctx context.Context, mctx *store.MessageContext) error {
	if !mctx.IsGroup() {
		return store.ReplyText(ctx, mctx, "Perintah ini hanya di grup.")
	}
	text := strings.TrimSpace(mctx.RawArgs)
	img, vid, aud := swgcExtractMedia(mctx)
	if img == nil && vid == nil && aud == nil && text == "" {
		return store.ReplyText(ctx, mctx, "Kirim teks atau reply media.\nContoh: .swgc pengumuman rapat jam 19\natau reply foto/video/VN dengan caption .swgc ...")
	}
	status, err := sendDlStatus(ctx, mctx, "Mengirim status grup, tunggu sebentar...")
	if err != nil {
		return err
	}
	_ = status
	go func() {
		bg := context.Background()
		if rerr := swgcSend(bg, mctx, text, img, vid, aud); rerr != nil {
			_ = store.ReplyText(bg, mctx, fmt.Sprintf("Gagal kirim status grup: %v", rerr))
			return
		}
		_ = store.ReplyText(bg, mctx, "Status grup terkirim — cek tab Status grup ini.")
	}()
	return nil
}

// swgcMedia menampung SATU media (gambar/video/audio) untuk status grup.
type swgcMedia struct {
	img *waE2E.ImageMessage
	vid *waE2E.VideoMessage
	aud *waE2E.AudioMessage
}

// swgcExtractMedia mengambil gambar/video/audio dari pesan sendiri
// (kirim dengan caption) maupun quoted — pola extractStickerMedia.
func swgcExtractMedia(mctx *store.MessageContext) (img *waE2E.ImageMessage, vid *waE2E.VideoMessage, aud *waE2E.AudioMessage) {
	msg := store.UnwrapMessage(mctx.RawEvt.Message)
	quoted := store.UnwrapMessage(store.ExtractQuotedMessage(mctx.RawEvt))
	for _, m := range []*waE2E.Message{msg, quoted} {
		if m == nil {
			continue
		}
		if m.GetImageMessage() != nil {
			return m.GetImageMessage(), nil, nil
		}
		if m.GetVideoMessage() != nil {
			return nil, m.GetVideoMessage(), nil
		}
		if m.GetAudioMessage() != nil {
			return nil, nil, m.GetAudioMessage()
		}
	}
	return nil, nil, nil
}

// swgcSend membangun + mengirim GroupStatusMessageV2. secret dipakai di
// inner DAN outer messageContextInfo (ikut resep Baileys).
func swgcSend(ctx context.Context, mctx *store.MessageContext, text string, img *waE2E.ImageMessage, vid *waE2E.VideoMessage, aud *waE2E.AudioMessage) error {
	secret, err := swgcRandomSecret()
	if err != nil {
		return fmt.Errorf("gagal buat secret: %v", err)
	}
	inner := &waE2E.Message{}
	switch {
	case img != nil:
		built, err := swgcBuildImage(ctx, mctx, img, text)
		if err != nil {
			return err
		}
		inner.ImageMessage = built
	case vid != nil:
		built, err := swgcBuildVideo(ctx, mctx, vid, text)
		if err != nil {
			return err
		}
		inner.VideoMessage = built
	case aud != nil:
		built, err := swgcBuildAudio(ctx, mctx, aud)
		if err != nil {
			return err
		}
		inner.AudioMessage = built
	default:
		bg := swgcBgPalette[0]
		if n, err := rand.Int(rand.Reader, big.NewInt(int64(len(swgcBgPalette)))); err == nil {
			bg = swgcBgPalette[n.Int64()]
		}
		inner.ExtendedTextMessage = &waE2E.ExtendedTextMessage{
			Text:           proto.String(text),
			BackgroundArgb: proto.Uint32(bg),
		}
	}
	inner.MessageContextInfo = &waE2E.MessageContextInfo{MessageSecret: secret}
	outer := &waE2E.Message{
		GroupStatusMessageV2: &waE2E.FutureProofMessage{Message: inner},
		MessageContextInfo:   &waE2E.MessageContextInfo{MessageSecret: secret},
	}
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, outer)
	if err != nil {
		return err
	}
	store.LogOutgoing(mctx, "swgc", text)
	store.TrackBotMessageID(resp.ID, "swgc")
	return nil
}

// swgcBuildImage mengunduh → upload → rakit ImageMessage untuk status.
func swgcBuildImage(ctx context.Context, mctx *store.MessageContext, img *waE2E.ImageMessage, caption string) (*waE2E.ImageMessage, error) {
	data, err := mctx.Client.Download(ctx, img)
	if err != nil {
		return nil, fmt.Errorf("gagal unduh gambar: %v", err)
	}
	if len(data) == 0 || len(data) > maxDlMediaBytes {
		return nil, fmt.Errorf("gambar kosong/melebihi 100MB")
	}
	uploaded, err := mctx.Client.Upload(ctx, data, whatsmeow.MediaImage)
	if err != nil {
		return nil, fmt.Errorf("gagal upload gambar: %v", err)
	}
	out := &waE2E.ImageMessage{
		URL:               proto.String(uploaded.URL),
		DirectPath:        proto.String(uploaded.DirectPath),
		MediaKey:          uploaded.MediaKey,
		MediaKeyTimestamp: proto.Int64(time.Now().Unix()),
		Mimetype:          proto.String(detectDlImageMIME(data)),
		FileEncSHA256:     uploaded.FileEncSHA256,
		FileSHA256:        uploaded.FileSHA256,
		FileLength:        proto.Uint64(uploaded.FileLength),
		Caption:           proto.String(caption),
		JPEGThumbnail:     messaging.InlineThumbForImage(data),
	}
	if cfg, _, cerr := image.DecodeConfig(bytes.NewReader(data)); cerr == nil && cfg.Width > 0 {
		out.Width = proto.Uint32(uint32(cfg.Width))
		out.Height = proto.Uint32(uint32(cfg.Height))
	}
	return out, nil
}

// swgcBuildVideo menormalisasi (transcode kontainer asing) → upload →
// rakit VideoMessage untuk status.
func swgcBuildVideo(ctx context.Context, mctx *store.MessageContext, vid *waE2E.VideoMessage, caption string) (*waE2E.VideoMessage, error) {
	data, err := mctx.Client.Download(ctx, vid)
	if err != nil {
		return nil, fmt.Errorf("gagal unduh video: %v", err)
	}
	if len(data) == 0 || len(data) > maxDlDocBytes {
		return nil, fmt.Errorf("video kosong/melebihi 200MB")
	}
	data, err = messaging.PrepareVideoBytes(ctx, data)
	if err != nil {
		return nil, err
	}
	uploaded, err := mctx.Client.Upload(ctx, data, whatsmeow.MediaVideo)
	if err != nil {
		return nil, fmt.Errorf("gagal upload video: %v", err)
	}
	thumb := messaging.InlineThumbForVideo(ctx, data)
	out := &waE2E.VideoMessage{
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
			out.Width = proto.Uint32(uint32(cfg.Width))
			out.Height = proto.Uint32(uint32(cfg.Height))
		}
	}
	return out, nil
}

// swgcBuildAudio mengunduh → upload → rakit AudioMessage untuk status
// (PTT dipertahankan untuk voice note).
func swgcBuildAudio(ctx context.Context, mctx *store.MessageContext, aud *waE2E.AudioMessage) (*waE2E.AudioMessage, error) {
	data, err := mctx.Client.Download(ctx, aud)
	if err != nil {
		return nil, fmt.Errorf("gagal unduh audio: %v", err)
	}
	if len(data) == 0 || len(data) > maxDlDocBytes {
		return nil, fmt.Errorf("audio kosong/melebihi 200MB")
	}
	mime := aud.GetMimetype()
	if mime == "" {
		mime = "audio/mpeg"
	}
	uploaded, err := mctx.Client.Upload(ctx, data, whatsmeow.MediaAudio)
	if err != nil {
		return nil, fmt.Errorf("gagal upload audio: %v", err)
	}
	return &waE2E.AudioMessage{
		URL:           proto.String(uploaded.URL),
		DirectPath:    proto.String(uploaded.DirectPath),
		MediaKey:      uploaded.MediaKey,
		Mimetype:      proto.String(mime),
		FileEncSHA256: uploaded.FileEncSHA256,
		FileSHA256:    uploaded.FileSHA256,
		FileLength:    proto.Uint64(uploaded.FileLength),
		PTT:           proto.Bool(aud.GetPTT()),
	}, nil
}
