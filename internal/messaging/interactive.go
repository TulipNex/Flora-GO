package messaging

import (
	"context"
	"encoding/json"
	"fmt"

	"wabot-go/internal/store"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// Builder pesan interaktif WhatsApp (tombol native flow, tombol legacy,
// carousel). Referensi struktur: YuukiBot (Riflxz) messagebuilder.go +
// message_builder.go (NIXCODE port) — lisensinya mewajibkan credit, maka
// dicatat di sini; implementasi ditulis ulang clean-room dari definisi
// proto, bukan salinan kode.
//
// KEPUTUSAN PENGIRIMAN (vonis AIRich: wrapper ViewOnceMessage menghilangkan
// pesan): SEMUA builder di sini mengirim MENTAH dulu. Bila uji lapangan
// menunjukkan tombol tak tampil/klik, varian wrapper+biz (RichBizNodes)
// menyusul — bukan asumsi.

// ---------------------------------------------------------------------------
// NativeFlowBuilder — tombol native flow modern
// ---------------------------------------------------------------------------

// NativeFlowBuilder merakit InteractiveMessage + tombol native flow:
// quick_reply, cta_call, cta_url, cta_copy, send_location, cta_reminder,
// address_message, single_select (+section/row).
type NativeFlowBuilder struct {
	title    string
	subtitle string
	body     string
	footer   string
	ctx      *waE2E.ContextInfo
	params   string
	buttons  []*waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton
	imgUp    *whatsmeow.UploadResponse
	imgMIME  string
}

// NewNativeFlowBuilder membuat builder tombol native flow.
func NewNativeFlowBuilder() *NativeFlowBuilder {
	return &NativeFlowBuilder{}
}

func (b *NativeFlowBuilder) SetTitle(t string) *NativeFlowBuilder    { b.title = t; return b }
func (b *NativeFlowBuilder) SetSubtitle(s string) *NativeFlowBuilder { b.subtitle = s; return b }
func (b *NativeFlowBuilder) SetBody(s string) *NativeFlowBuilder     { b.body = s; return b }
func (b *NativeFlowBuilder) SetFooter(s string) *NativeFlowBuilder   { b.footer = s; return b }
func (b *NativeFlowBuilder) SetContextInfo(ci *waE2E.ContextInfo) *NativeFlowBuilder {
	b.ctx = ci
	return b
}

// SetParams mengisi messageParamsJson (mis. bottom_sheet).
func (b *NativeFlowBuilder) SetParams(p string) *NativeFlowBuilder { b.params = p; return b }

// SetImageHeader memakai media hasil upload (UploadResponse penuh — URL saja
// TIDAK cukup, takkan render). mime kosong = image/jpeg.
func (b *NativeFlowBuilder) SetImageHeader(up *whatsmeow.UploadResponse, mime string) *NativeFlowBuilder {
	b.imgUp = up
	b.imgMIME = mime
	return b
}

func (b *NativeFlowBuilder) addButton(name, paramsJSON string) *NativeFlowBuilder {
	b.buttons = append(b.buttons, &waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
		Name:             proto.String(name),
		ButtonParamsJSON: proto.String(paramsJSON),
	})
	return b
}

// AddReply — tombol quick reply.
func (b *NativeFlowBuilder) AddReply(displayText, id string) *NativeFlowBuilder {
	return b.addButton("quick_reply", fmt.Sprintf(`{"display_text":%q,"id":%q}`, displayText, id))
}

// AddCall — tombol panggil.
func (b *NativeFlowBuilder) AddCall(displayText, id string) *NativeFlowBuilder {
	return b.addButton("cta_call", fmt.Sprintf(`{"display_text":%q,"id":%q}`, displayText, id))
}

// AddURL — tombol buka URL.
func (b *NativeFlowBuilder) AddURL(displayText, url string, webviewInteraction bool) *NativeFlowBuilder {
	return b.addButton("cta_url", fmt.Sprintf(`{"display_text":%q,"url":%q,"webview_interaction":%v}`,
		displayText, url, webviewInteraction))
}

// AddCopy — tombol salin kode.
func (b *NativeFlowBuilder) AddCopy(displayText, copyCode string) *NativeFlowBuilder {
	return b.addButton("cta_copy", fmt.Sprintf(`{"display_text":%q,"copy_code":%q}`, displayText, copyCode))
}

// AddLocation — tombol kirim lokasi.
func (b *NativeFlowBuilder) AddLocation() *NativeFlowBuilder {
	return b.addButton("send_location", `{}`)
}

// AddReminder — tombol buat pengingat.
func (b *NativeFlowBuilder) AddReminder(displayText, id string) *NativeFlowBuilder {
	return b.addButton("cta_reminder", fmt.Sprintf(`{"display_text":%q,"id":%q}`, displayText, id))
}

// AddAddress — tombol kirim alamat.
func (b *NativeFlowBuilder) AddAddress(displayText, id string) *NativeFlowBuilder {
	return b.addButton("address_message", fmt.Sprintf(`{"display_text":%q,"id":%q}`, displayText, id))
}

// selectSection adalah satu section dropdown single_select.
type selectSection struct {
	Title string
	Rows  []selectRow
}

// selectRow adalah satu baris dropdown.
type selectRow struct {
	Header string
	Title  string
	Desc   string
	ID     string
}

// AddSelection memulai dropdown single_select; section diisi via
// AddSelectSection/AddSelectRow BERURUTAN (meniru MakeSection/MakeRow
// referensi tanpa state tersembunyi antar-tombol).
func (b *NativeFlowBuilder) AddSelection(title string, sections []selectSection) *NativeFlowBuilder {
	type rowParam struct {
		Header      string `json:"header"`
		Title       string `json:"title"`
		Description string `json:"description"`
		ID          string `json:"id"`
	}
	type sectionParam struct {
		Title          string     `json:"title"`
		HighlightLabel string     `json:"highlight_label,omitempty"`
		Rows           []rowParam `json:"rows"`
	}
	var sps []sectionParam
	for _, sec := range sections {
		var rows []rowParam
		for _, r := range sec.Rows {
			rows = append(rows, rowParam{Header: r.Header, Title: r.Title, Description: r.Desc, ID: r.ID})
		}
		sps = append(sps, sectionParam{Title: sec.Title, Rows: rows})
	}
	paramsJSON, _ := json.Marshal(map[string]any{"title": title, "sections": sps})
	return b.addButton("single_select", string(paramsJSON))
}

// SelectSection/SelectRow adalah alias publik tipe section/row (untuk
// pemanggil di luar package).
type SelectSection = selectSection
type SelectRow = selectRow

// buildCard merakit InteractiveMessage (tanpa wrapper). Dipakai Send dan
// CarouselBuilder.
func (b *NativeFlowBuilder) buildCard() *waE2E.InteractiveMessage {
	header := &waE2E.InteractiveMessage_Header{
		Title:              proto.String(b.title),
		Subtitle:           proto.String(b.subtitle),
		HasMediaAttachment: proto.Bool(b.imgUp != nil),
	}
	if b.imgUp != nil {
		mime := b.imgMIME
		if mime == "" {
			mime = "image/jpeg"
		}
		header.Media = &waE2E.InteractiveMessage_Header_ImageMessage{
			ImageMessage: &waE2E.ImageMessage{
				URL:           proto.String(b.imgUp.URL),
				DirectPath:    proto.String(b.imgUp.DirectPath),
				MediaKey:      b.imgUp.MediaKey,
				FileEncSHA256: b.imgUp.FileEncSHA256,
				FileSHA256:    b.imgUp.FileSHA256,
				FileLength:    proto.Uint64(b.imgUp.FileLength),
				Mimetype:      proto.String(mime),
			},
		}
	}
	return &waE2E.InteractiveMessage{
		Header:      header,
		Body:        &waE2E.InteractiveMessage_Body{Text: proto.String(b.body)},
		Footer:      &waE2E.InteractiveMessage_Footer{Text: proto.String(b.footer)},
		ContextInfo: b.ctx,
		InteractiveMessage: &waE2E.InteractiveMessage_NativeFlowMessage_{
			NativeFlowMessage: &waE2E.InteractiveMessage_NativeFlowMessage{
				MessageParamsJSON: proto.String(b.params),
				Buttons:           b.buttons,
			},
		},
	}
}

// Build membungkus card jadi pesan siap kirim (mentah — lihat catatan atas).
func (b *NativeFlowBuilder) Build() *waE2E.Message {
	return &waE2E.Message{InteractiveMessage: b.buildCard()}
}

// Send mengirim pesan tombol (mentah). Minimal satu tombol.
func (b *NativeFlowBuilder) Send(ctx context.Context, mctx *store.MessageContext) (types.MessageID, error) {
	if len(b.buttons) == 0 {
		return "", fmt.Errorf("butuh minimal satu tombol")
	}
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, b.Build())
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

// ---------------------------------------------------------------------------
// LegacyButtonsBuilder — buttonsMessage lawas
// ---------------------------------------------------------------------------

// LegacyButtonsBuilder merakit ButtonsMessage klasik (RESPONSE buttons +
// header media/teks). Deprecated di WhatsApp modern — dipertahankan untuk
// kompatibilitas, ditandai di tiap pemakaian.
type LegacyButtonsBuilder struct {
	body     string
	footer   string
	ctx      *waE2E.ContextInfo
	buttons  []*waE2E.ButtonsMessage_Button
	thumbURL string
	imgUp    *whatsmeow.UploadResponse
	imgMIME  string
}

// NewLegacyButtonsBuilder membuat builder tombol legacy.
func NewLegacyButtonsBuilder() *LegacyButtonsBuilder {
	return &LegacyButtonsBuilder{}
}

func (b *LegacyButtonsBuilder) SetBody(s string) *LegacyButtonsBuilder   { b.body = s; return b }
func (b *LegacyButtonsBuilder) SetFooter(s string) *LegacyButtonsBuilder { b.footer = s; return b }
func (b *LegacyButtonsBuilder) SetContextInfo(ci *waE2E.ContextInfo) *LegacyButtonsBuilder {
	b.ctx = ci
	return b
}

// AddButton menambah tombol balasan (id kosong = acak).
func (b *LegacyButtonsBuilder) AddButton(displayText, buttonID string) *LegacyButtonsBuilder {
	if buttonID == "" {
		buttonID = fmt.Sprintf("btn-%d", len(b.buttons)+1)
	}
	b.buttons = append(b.buttons, &waE2E.ButtonsMessage_Button{
		ButtonID: proto.String(buttonID),
		ButtonText: &waE2E.ButtonsMessage_Button_ButtonText{
			DisplayText: proto.String(displayText),
		},
		Type: waE2E.ButtonsMessage_Button_RESPONSE.Enum(),
	})
	return b
}

// SetImageHeader memakai media hasil upload penuh.
func (b *LegacyButtonsBuilder) SetImageHeader(up *whatsmeow.UploadResponse, mime string) *LegacyButtonsBuilder {
	b.imgUp = up
	b.imgMIME = mime
	return b
}

// Build merakit ButtonsMessage (header media > teks).
func (b *LegacyButtonsBuilder) Build() *waE2E.Message {
	bm := &waE2E.ButtonsMessage{
		ContentText: proto.String(b.body),
		FooterText:  proto.String(b.footer),
		ContextInfo: b.ctx,
		Buttons:     b.buttons,
	}
	if b.imgUp != nil {
		mime := b.imgMIME
		if mime == "" {
			mime = "image/jpeg"
		}
		bm.Header = &waE2E.ButtonsMessage_ImageMessage{
			ImageMessage: &waE2E.ImageMessage{
				URL:           proto.String(b.imgUp.URL),
				DirectPath:    proto.String(b.imgUp.DirectPath),
				MediaKey:      b.imgUp.MediaKey,
				FileEncSHA256: b.imgUp.FileEncSHA256,
				FileSHA256:    b.imgUp.FileSHA256,
				FileLength:    proto.Uint64(b.imgUp.FileLength),
				Mimetype:      proto.String(mime),
			},
		}
		bm.HeaderType = waE2E.ButtonsMessage_IMAGE.Enum()
	} else {
		bm.HeaderType = waE2E.ButtonsMessage_EMPTY.Enum()
	}
	return &waE2E.Message{ButtonsMessage: bm}
}

// Send mengirim tombol legacy (mentah). Minimal satu tombol.
func (b *LegacyButtonsBuilder) Send(ctx context.Context, mctx *store.MessageContext) (types.MessageID, error) {
	if len(b.buttons) == 0 {
		return "", fmt.Errorf("butuh minimal satu tombol")
	}
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, b.Build())
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

// ---------------------------------------------------------------------------
// CarouselBuilder — kartu swipe horizontal
// ---------------------------------------------------------------------------

// carouselMaxCards adalah batas kartu per carousel (aturan WhatsApp).
const carouselMaxCards = 10

// CarouselCard adalah satu kartu: teks + tombol + media WAJIB.
type CarouselCard struct {
	title    string
	subtitle string
	body     string
	footer   string
	ctx      *waE2E.ContextInfo
	buttons  []*waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton
	imgUp    *whatsmeow.UploadResponse
	imgMIME  string
}

// NewCarouselCard membuat kartu kosong (media wajib diisi via SetImage).
func NewCarouselCard() *CarouselCard {
	return &CarouselCard{}
}

func (c *CarouselCard) SetTitle(t string) *CarouselCard    { c.title = t; return c }
func (c *CarouselCard) SetSubtitle(s string) *CarouselCard { c.subtitle = s; return c }
func (c *CarouselCard) SetBody(s string) *CarouselCard     { c.body = s; return c }
func (c *CarouselCard) SetFooter(s string) *CarouselCard   { c.footer = s; return c }

// SetImage wajib dipanggil (WhatsApp menolak kartu tanpa media).
func (c *CarouselCard) SetImage(up *whatsmeow.UploadResponse, mime string) *CarouselCard {
	c.imgUp = up
	c.imgMIME = mime
	return c
}

// AddReply menambah tombol quick reply ke kartu.
func (c *CarouselCard) AddReply(displayText, id string) *CarouselCard {
	c.buttons = append(c.buttons, &waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
		Name:             proto.String("quick_reply"),
		ButtonParamsJSON: proto.String(fmt.Sprintf(`{"display_text":%q,"id":%q}`, displayText, id)),
	})
	return c
}

// buildCard merakit kartu (error bila tanpa media).
func (c *CarouselCard) buildCard() (*waE2E.InteractiveMessage, error) {
	if c.imgUp == nil {
		return nil, fmt.Errorf("kartu carousel wajib punya gambar")
	}
	mime := c.imgMIME
	if mime == "" {
		mime = "image/jpeg"
	}
	return &waE2E.InteractiveMessage{
		Header: &waE2E.InteractiveMessage_Header{
			Title:              proto.String(c.title),
			Subtitle:           proto.String(c.subtitle),
			HasMediaAttachment: proto.Bool(true),
			Media: &waE2E.InteractiveMessage_Header_ImageMessage{
				ImageMessage: &waE2E.ImageMessage{
					URL:           proto.String(c.imgUp.URL),
					DirectPath:    proto.String(c.imgUp.DirectPath),
					MediaKey:      c.imgUp.MediaKey,
					FileEncSHA256: c.imgUp.FileEncSHA256,
					FileSHA256:    c.imgUp.FileSHA256,
					FileLength:    proto.Uint64(c.imgUp.FileLength),
					Mimetype:      proto.String(mime),
				},
			},
		},
		Body:        &waE2E.InteractiveMessage_Body{Text: proto.String(c.body)},
		Footer:      &waE2E.InteractiveMessage_Footer{Text: proto.String(c.footer)},
		ContextInfo: c.ctx,
		InteractiveMessage: &waE2E.InteractiveMessage_NativeFlowMessage_{
			NativeFlowMessage: &waE2E.InteractiveMessage_NativeFlowMessage{
				Buttons: c.buttons,
			},
		},
	}, nil
}

// CarouselBuilder merakit pesan carousel (swipe horizontal).
type CarouselBuilder struct {
	cards []*waE2E.InteractiveMessage
}

// NewCarouselBuilder membuat builder carousel kosong.
func NewCarouselBuilder() *CarouselBuilder {
	return &CarouselBuilder{}
}

// AddCard menambah kartu (maks 10, media wajib).
func (cb *CarouselBuilder) AddCard(card *CarouselCard) error {
	if len(cb.cards) >= carouselMaxCards {
		return fmt.Errorf("carousel maksimal %d kartu", carouselMaxCards)
	}
	im, err := card.buildCard()
	if err != nil {
		return err
	}
	cb.cards = append(cb.cards, im)
	return nil
}

// Len mengembalikan jumlah kartu.
func (cb *CarouselBuilder) Len() int { return len(cb.cards) }

// Build merakit pesan carousel (mentah — lihat catatan atas).
func (cb *CarouselBuilder) Build() *waE2E.Message {
	return &waE2E.Message{
		InteractiveMessage: &waE2E.InteractiveMessage{
			InteractiveMessage: &waE2E.InteractiveMessage_CarouselMessage_{
				CarouselMessage: &waE2E.InteractiveMessage_CarouselMessage{
					Cards:            cb.cards,
					MessageVersion:   proto.Int32(1),
					CarouselCardType: waE2E.InteractiveMessage_CarouselMessage_HSCROLL_CARDS.Enum(),
				},
			},
		},
	}
}

// Send mengirim carousel (mentah). Minimal satu kartu.
func (cb *CarouselBuilder) Send(ctx context.Context, mctx *store.MessageContext) (types.MessageID, error) {
	if len(cb.cards) == 0 {
		return "", fmt.Errorf("carousel butuh minimal satu kartu")
	}
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, cb.Build())
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}
