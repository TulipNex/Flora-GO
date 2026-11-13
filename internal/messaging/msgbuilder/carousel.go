package msgbuilder

import (
	"context"
	"encoding/json"

	"wabot-go/internal/messaging"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types"
	"google.golang.org/protobuf/proto"
)

// CarouselCardBuilder merakit satu kartu individual di dalam carousel.
type CarouselCardBuilder struct {
	title     string
	body      string
	headerImg *waE2E.ImageMessage
	buttons   []*waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton
}

// NewCard membuat instance baru untuk satu kartu carousel.
func NewCard() *CarouselCardBuilder {
	return &CarouselCardBuilder{
		buttons: make([]*waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton, 0),
	}
}

// SetTitle mengatur judul pada header kartu.
func (c *CarouselCardBuilder) SetTitle(title string) *CarouselCardBuilder {
	c.title = title
	return c
}

// SetBody mengatur teks isi kartu.
func (c *CarouselCardBuilder) SetBody(body string) *CarouselCardBuilder {
	c.body = body
	return c
}

// SetHeaderImage mengatur header gambar kartu yang sudah diupload.
func (c *CarouselCardBuilder) SetHeaderImage(img *waE2E.ImageMessage) *CarouselCardBuilder {
	c.headerImg = img
	return c
}

// AddQuickReply menambahkan tombol quick reply pada kartu.
func (c *CarouselCardBuilder) AddQuickReply(id, label string) *CarouselCardBuilder {
	params, _ := json.Marshal(map[string]string{
		"display_text": label,
		"id":           id,
	})
	c.buttons = append(c.buttons, &waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
		Name:             proto.String(messaging.NFQuickReply),
		ButtonParamsJSON: proto.String(string(params)),
	})
	return c
}

// AddURLButton menambahkan tombol buka URL pada kartu.
func (c *CarouselCardBuilder) AddURLButton(label, url string) *CarouselCardBuilder {
	params, _ := json.Marshal(map[string]string{
		"display_text": label,
		"url":          url,
	})
	c.buttons = append(c.buttons, &waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
		Name:             proto.String(messaging.NFCTAURL),
		ButtonParamsJSON: proto.String(string(params)),
	})
	return c
}

// AddButton menambahkan satu tombol native_flow bebas ke kartu. Dipakai
// playground untuk tipe selain quick_reply/url (copy, call, location,
// signup, single_select).
func (c *CarouselCardBuilder) AddButton(b messaging.NFButton) *CarouselCardBuilder {
	c.buttons = append(c.buttons, messaging.NativeFlowButtons(b)...)
	return c
}

// BuildCard merakit kartu menjadi *waE2E.InteractiveMessage.
func (c *CarouselCardBuilder) BuildCard() *waE2E.InteractiveMessage {
	im := &waE2E.InteractiveMessage{
		Header: &waE2E.InteractiveMessage_Header{
			HasMediaAttachment: proto.Bool(c.headerImg != nil),
		},
		Body: &waE2E.InteractiveMessage_Body{
			Text: proto.String(c.body),
		},
		InteractiveMessage: &waE2E.InteractiveMessage_NativeFlowMessage_{
			NativeFlowMessage: &waE2E.InteractiveMessage_NativeFlowMessage{
				Buttons: c.buttons,
			},
		},
	}

	if c.title != "" {
		im.Header.Title = proto.String(c.title)
	}

	if c.headerImg != nil {
		im.Header.Media = &waE2E.InteractiveMessage_Header_ImageMessage{ImageMessage: c.headerImg}
	}

	return im
}

// CarouselBuilder merakit wadah induk pesan carousel WhatsApp.
type CarouselBuilder struct {
	bodyText string
	cards    []*CarouselCardBuilder
}

// NewCarousel membuat instance baru untuk pesan carousel.
func NewCarousel() *CarouselBuilder {
	return &CarouselBuilder{
		cards: make([]*CarouselCardBuilder, 0),
	}
}

// SetBody mengatur deskripsi teks di atas carousel kartu.
func (b *CarouselBuilder) SetBody(text string) *CarouselBuilder {
	b.bodyText = text
	return b
}

// AddCard menambahkan satu kartu ke dalam carousel.
func (b *CarouselBuilder) AddCard(card *CarouselCardBuilder) *CarouselBuilder {
	if card != nil {
		b.cards = append(b.cards, card)
	}
	return b
}

// Build merakit seluruh carousel menjadi *waE2E.Message siap kirim.
func (b *CarouselBuilder) Build() *waE2E.Message {
	cardMsgs := make([]*waE2E.InteractiveMessage, 0, len(b.cards))
	for _, c := range b.cards {
		cardMsgs = append(cardMsgs, c.BuildCard())
	}

	return &waE2E.Message{
		InteractiveMessage: &waE2E.InteractiveMessage{
			Body: &waE2E.InteractiveMessage_Body{
				Text: proto.String(b.bodyText),
			},
			InteractiveMessage: &waE2E.InteractiveMessage_CarouselMessage_{
				CarouselMessage: &waE2E.InteractiveMessage_CarouselMessage{
					Cards: cardMsgs,
				},
			},
		},
	}
}

// Send mengirim carousel ke chat tujuan melalui whatsmeow.
func (b *CarouselBuilder) Send(ctx context.Context, client *whatsmeow.Client, chat types.JID) (whatsmeow.SendResponse, error) {
	return client.SendMessage(ctx, chat, b.Build(), whatsmeow.SendRequestExtra{ID: whatsmeow.GenerateMessageID()})
}
