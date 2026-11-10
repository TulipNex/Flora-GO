package msgbuilder

import (
	"encoding/json"

	"wabot-go/internal/messaging"

	"github.com/polymorfa/hypermeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

// NativeFlowBuilder merakit InteractiveMessage berbasis tombol aksi native.
type NativeFlowBuilder struct {
	title     string
	subtitle  string
	body      string
	footer    string
	headerImg *waE2E.ImageMessage
	buttons   []*waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton
}

// NewNativeFlow membuat instance baru untuk menyusun tombol Native Flow.
func NewNativeFlow() *NativeFlowBuilder {
	return &NativeFlowBuilder{
		buttons: make([]*waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton, 0),
	}
}

// SetTitle mengatur judul header.
func (b *NativeFlowBuilder) SetTitle(title string) *NativeFlowBuilder {
	b.title = title
	return b
}

// SetSubtitle mengatur subjudul header.
func (b *NativeFlowBuilder) SetSubtitle(sub string) *NativeFlowBuilder {
	b.subtitle = sub
	return b
}

// SetBody mengatur teks isi pesan.
func (b *NativeFlowBuilder) SetBody(body string) *NativeFlowBuilder {
	b.body = body
	return b
}

// SetFooter mengatur teks footer kecil di bawah tombol.
func (b *NativeFlowBuilder) SetFooter(footer string) *NativeFlowBuilder {
	b.footer = footer
	return b
}

// SetHeaderImage mengatur header gambar yang sudah diupload.
func (b *NativeFlowBuilder) SetHeaderImage(img *waE2E.ImageMessage) *NativeFlowBuilder {
	b.headerImg = img
	return b
}

// AddQuickReply menambahkan tombol respon cepat.
func (b *NativeFlowBuilder) AddQuickReply(id, label string) *NativeFlowBuilder {
	params, _ := json.Marshal(map[string]string{
		"display_text": label,
		"id":           id,
	})
	b.buttons = append(b.buttons, &waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
		Name:             proto.String(messaging.NFQuickReply),
		ButtonParamsJSON: proto.String(string(params)),
	})
	return b
}

// AddURLButton menambahkan tombol buka tautan browser.
func (b *NativeFlowBuilder) AddURLButton(label, url string) *NativeFlowBuilder {
	params, _ := json.Marshal(map[string]string{
		"display_text": label,
		"url":          url,
	})
	b.buttons = append(b.buttons, &waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
		Name:             proto.String(messaging.NFCTAURL),
		ButtonParamsJSON: proto.String(string(params)),
	})
	return b
}

// AddCopyButton menambahkan tombol sekali tap salin teks/kode ke clipboard.
func (b *NativeFlowBuilder) AddCopyButton(label, copyCode string) *NativeFlowBuilder {
	params, _ := json.Marshal(map[string]string{
		"display_text": label,
		"copy_code":    copyCode,
	})
	b.buttons = append(b.buttons, &waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
		Name:             proto.String(messaging.NFCTACopy),
		ButtonParamsJSON: proto.String(string(params)),
	})
	return b
}

// AddCallButton menambahkan tombol sekali tap panggil nomor telepon.
func (b *NativeFlowBuilder) AddCallButton(label, phoneNumber string) *NativeFlowBuilder {
	params, _ := json.Marshal(map[string]string{
		"display_text": label,
		"phone_number": phoneNumber,
	})
	b.buttons = append(b.buttons, &waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
		Name:             proto.String(messaging.NFCTACall),
		ButtonParamsJSON: proto.String(string(params)),
	})
	return b
}

// Build merakit pesan menjadi *waE2E.Message siap kirim.
func (b *NativeFlowBuilder) Build() *waE2E.Message {
	im := &waE2E.InteractiveMessage{
		Body: &waE2E.InteractiveMessage_Body{Text: proto.String(b.body)},
		InteractiveMessage: &waE2E.InteractiveMessage_NativeFlowMessage_{
			NativeFlowMessage: &waE2E.InteractiveMessage_NativeFlowMessage{
				Buttons: b.buttons,
			},
		},
	}

	if b.title != "" || b.subtitle != "" || b.headerImg != nil {
		im.Header = &waE2E.InteractiveMessage_Header{
			HasMediaAttachment: proto.Bool(b.headerImg != nil),
		}
		if b.title != "" {
			im.Header.Title = proto.String(b.title)
		}
		if b.subtitle != "" {
			im.Header.Subtitle = proto.String(b.subtitle)
		}
		if b.headerImg != nil {
			im.Header.Media = &waE2E.InteractiveMessage_Header_ImageMessage{ImageMessage: b.headerImg}
		}
	}

	if b.footer != "" {
		im.Footer = &waE2E.InteractiveMessage_Footer{Text: proto.String(b.footer)}
	}

	return &waE2E.Message{InteractiveMessage: im}
}
