package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"strings"

	"wabot-go/internal/store"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types/events"
	"google.golang.org/protobuf/proto"
)

func init() {
	registerPlugin(RegisterCarouselTest)
}

// RegisterCarouselTest mendaftarkan `.carouseltest` kategori Dev: uji apakah
// InteractiveMessage.carouselMessage bisa dikirim dari akun biasa.
//
// LATAR: getButtonTypeFromMessage hypermeow HANYA mengenali nativeFlowMessage,
// jadi carouselMessage tidak dapat node `biz` otomatis (lihat
// third_party/hypermeow/send.go). Eksperimen dua varian:
//   - Varian A (default): apa adanya — dugaan drop/kosong.
//   - Varian B: setelah patch send.go agar carouselMessage juga dihitung
//     "native_flow" (mendapat node `biz` name="mixed" yang sama).
//
// Bentuk: `.carouseltest [url|reply] [judul]` — url = card bertombol cta_url
// (seragam), reply = card bertombol quick_reply (seragam, id prefix
// "carousel_") untuk uji tap masuk.
//
// CATATAN bila Varian B terbukti jalan dan mau dipertahankan permanen:
// perbarui komentar blok `replace` di go.mod + AGENTS.md karena fork lokal
// kini membawa DUA patch (mixed + deteksi carousel).
func RegisterCarouselTest(r *Registry) {
	r.Register(Command{
		Name:        "carouseltest",
		Description: "Uji kartu carousel (url/reply)",
		Category:    CategoryDev,
		Handler:     handleCarouselTest,
	})
}

const carouselMaxTitle = 80

// parseCarouselArgs memisah mode + judul dari teks argumen mentah.
func parseCarouselArgs(raw string) (mode, title string) {
	title = strings.TrimSpace(raw)
	if f := strings.Fields(title); len(f) > 0 {
		switch strings.ToLower(f[0]) {
		case "url", "reply", "native":
			mode = strings.ToLower(f[0])
			title = strings.TrimSpace(strings.TrimPrefix(title, f[0]))
		}
	}
	if title == "" {
		title = "Uji Carousel"
	}
	if len([]rune(title)) > carouselMaxTitle {
		title = string([]rune(title)[:carouselMaxTitle])
	}
	return mode, title
}

func handleCarouselTest(ctx context.Context, mctx *store.MessageContext) error {
	// Command uji pengembang: hanya owner.
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	mode, title := parseCarouselArgs(mctx.RawArgs)
	if err := carouselSend(ctx, mctx, mode, title); err != nil {
		return store.ReplyText(ctx, mctx, "Gagal kirim carousel: "+err.Error())
	}
	return store.ReplyText(ctx, mctx, "Carousel terkirim. Laporkan: tampil 3 kartu? geser jalan? tombol berfungsi?")
}

// carouselSend mengirim kartu carousel tanpa balasan ringkasan — dipakai
// handler `.carouseltest` maupun agregator `.devtest`.
func carouselSend(ctx context.Context, mctx *store.MessageContext, mode, title string) error {
	if title == "" {
		title = "Uji Carousel"
	}
	if len([]rune(title)) > carouselMaxTitle {
		title = string([]rune(title)[:carouselMaxTitle])
	}

	cards := []*waE2E.InteractiveMessage{
		carouselTestCard(0, title+" 1", "Kartu pertama", mode),
		carouselTestCard(1, title+" 2", "Kartu kedua", mode),
		carouselTestCard(2, title+" 3", "Kartu ketiga", mode),
	}

	// Carousel: body level pesan saja (tanpa header/footer/button di luar
	// card — aturan Meta). Header/video ada di tiap card.
	msg := &waE2E.Message{
		InteractiveMessage: &waE2E.InteractiveMessage{
			Body: &waE2E.InteractiveMessage_Body{Text: proto.String("Uji carousel: geser kartu, lalu laporkan apakah tampil.")},
			InteractiveMessage: &waE2E.InteractiveMessage_CarouselMessage_{
				CarouselMessage: &waE2E.InteractiveMessage_CarouselMessage{
					Cards:          cards,
					MessageVersion: proto.Int32(1),
				},
			},
		},
	}

	// Node `biz` tidak dipasang manual (lihat catatan di RegisterCarouselTest):
	// Varian A = apa adanya; Varian B = patch send.go.
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, msg, btnTestBizExtra())
	if err != nil {
		return err
	}
	btnTestArm(mctx.Chat.String(), mctx.Sender.String())
	store.LogOutgoing(mctx, "carouseltest", mode+" "+title)
	store.TrackBotMessageID(resp.ID, "carouseltest "+mode+" "+title)
	return nil
}

// carouselTestCard merakit satu card carousel: header gambar (JPEG inline) +
// body + native_flow satu tombol (cta_url atau quick_reply, seragam antar
// card sesuai aturan Meta).
func carouselTestCard(idx int, title, body, mode string) *waE2E.InteractiveMessage {
	var btnName, params string
	if mode == "reply" {
		raw, _ := json.Marshal(map[string]string{
			"display_text": fmt.Sprintf("Balas %d", idx+1),
			"id":           fmt.Sprintf("carousel_btn_%d", idx+1),
		})
		btnName, params = "quick_reply", string(raw)
	} else {
		raw, _ := json.Marshal(map[string]string{
			"display_text": "Buka Tautan",
			"url":          fmt.Sprintf("https://example.com/card-%d", idx+1),
			"merchant_url": fmt.Sprintf("https://example.com/card-%d", idx+1),
		})
		btnName, params = "cta_url", string(raw)
	}

	return &waE2E.InteractiveMessage{
		Header: &waE2E.InteractiveMessage_Header{
			HasMediaAttachment: proto.Bool(true),
			Media:              &waE2E.InteractiveMessage_Header_JPEGThumbnail{JPEGThumbnail: carouselTestThumb(idx)},
			Title:              proto.String(title),
		},
		Body: &waE2E.InteractiveMessage_Body{Text: proto.String(body)},
		InteractiveMessage: &waE2E.InteractiveMessage_NativeFlowMessage_{
			NativeFlowMessage: &waE2E.InteractiveMessage_NativeFlowMessage{
				Buttons: []*waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{{
					Name:             proto.String(btnName),
					ButtonParamsJSON: proto.String(params),
				}},
			},
		},
	}
}

// carouselTestThumb membuat JPEG kecil (warna berbeda per card) sebagai
// header card. Inline supaya tak perlu upload media untuk uji wire.
func carouselTestThumb(idx int) []byte {
	palette := []color.RGBA{
		{R: 0x1E, G: 0x88, B: 0xE5, A: 0xFF},
		{R: 0x43, G: 0xA0, B: 0x47, A: 0xFF},
		{R: 0xE5, G: 0x39, B: 0x35, A: 0xFF},
	}
	c := palette[idx%len(palette)]
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		return nil
	}
	return buf.Bytes()
}

// CheckCarouselTestTap mencegat tap tombol quick_reply milik carousel test
// (prefix "carousel_"). Reuse extractor btntest agar jalur respons manapun
// (TemplateButtonReplyMessage / InteractiveResponseMessage) tertangkap.
func CheckCarouselTestTap(client *whatsmeow.Client, evt *events.Message, text string) bool {
	_ = text
	id, ok := btnTestPayloadID(evt)
	if !ok || !strings.HasPrefix(id, "carousel_") {
		return false
	}
	mctx := &store.MessageContext{Client: client, Chat: evt.Info.Chat, Sender: evt.Info.Sender, RawEvt: evt}
	_ = store.ReplyText(context.Background(), mctx, fmt.Sprintf("Tap carousel diterima! ID: %s", id))
	return true
}
