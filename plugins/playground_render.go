package plugins

import (
	"context"
	"fmt"
	"strings"
	"time"

	"wabot-go/internal/external"
	"wabot-go/internal/messaging"
	"wabot-go/internal/messaging/msgbuilder"

	"github.com/google/uuid"
	whatsmeow "github.com/polymorfa/hypermeow"
	waBinary "github.com/polymorfa/hypermeow/binary"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

const (
	playMaxImageBytes = 4 << 20  // gambar header/thumbnail
	playMaxHTMLBytes  = 64 << 10 // `+ section html | url:...`
	playFetchTimeout  = 20 * time.Second
)

// playRender menyiapkan satu bubble dari spec. Hanya gambar (header/thumbnail)
// dan HTML `url:` yang menyentuh jaringan; sisanya perakitan proto murni.
func playRender(ctx context.Context, client *whatsmeow.Client, sp playSpec) (*waE2E.Message, whatsmeow.SendRequestExtra, error) {
	var headerImg *waE2E.ImageMessage
	var thumb []byte

	if sp.image != "" {
		data, err := playFetchData(ctx, sp.image, playMaxImageBytes, "gambar")
		if err != nil {
			return nil, whatsmeow.SendRequestExtra{}, err
		}
		switch sp.kind {
		case playKindInteractive:
			jpeg := messaging.HeaderJPEG(data, 1600, 85)
			if len(jpeg) == 0 {
				return nil, whatsmeow.SendRequestExtra{}, fmt.Errorf("gambar header tidak bisa dibaca (butuh JPEG/PNG/GIF/WebP)")
			}
			headerImg, err = messaging.UploadImageMessage(ctx, client, jpeg)
			if err != nil {
				return nil, whatsmeow.SendRequestExtra{}, fmt.Errorf("gagal unggah gambar header: %w", err)
			}
		case playKindEvent:
			thumb = messaging.HeaderJPEG(data, 640, 85)
			if len(thumb) == 0 {
				return nil, whatsmeow.SendRequestExtra{}, fmt.Errorf("gambar event tidak bisa dibaca (butuh JPEG/PNG/GIF/WebP)")
			}
		}
	}

	msg, err := playBuild(ctx, sp, headerImg, thumb)
	if err != nil {
		return nil, whatsmeow.SendRequestExtra{}, err
	}
	extra := whatsmeow.SendRequestExtra{ID: whatsmeow.GenerateMessageID()}
	if sp.kind == playKindEvent {
		// Node yang sama dengan .eventtest: tanpa context_source, klien tidak
		// mengenali bubble sebagai undangan acara.
		extra.AdditionalNodes = &[]waBinary.Node{{
			Tag:   "meta",
			Attrs: waBinary.Attrs{"context_source": "event_invitation"},
		}}
	}
	return msg, extra, nil
}

// playBuild merakit proto tanpa jaringan; gambar yang sudah disiapkan
// (headerImg/thumb) diteruskan pemanggil supaya fungsi ini bisa diuji murni.
func playBuild(ctx context.Context, sp playSpec, headerImg *waE2E.ImageMessage, thumb []byte) (*waE2E.Message, error) {
	switch sp.kind {
	case playKindInteractive:
		return playBuildInteractive(sp, headerImg), nil
	case playKindAirich:
		return playBuildAirich(ctx, sp)
	case playKindEvent:
		return &waE2E.Message{EventInviteMessage: &waE2E.EventInviteMessage{
			EventID:       proto.String(uuid.New().String()),
			EventTitle:    proto.String(sp.title),
			JPEGThumbnail: thumb,
			StartTime:     proto.Int64(time.Now().Unix()),
		}}, nil
	case playKindPhone:
		return &waE2E.Message{RequestPhoneNumberMessage: &waE2E.RequestPhoneNumberMessage{}}, nil
	default:
		return nil, fmt.Errorf("kind `%s` tidak dikenal", sp.kind)
	}
}

// playSlot mengembalikan isi bubble interactive: carousel bila ada kartu.
func playSlot(sp playSpec) string {
	if sp.slot != "" {
		return sp.slot
	}
	if len(sp.cards) > 0 {
		return "carousel"
	}
	return "buttons"
}

func playBuildInteractive(sp playSpec, headerImg *waE2E.ImageMessage) *waE2E.Message {
	im := &waE2E.InteractiveMessage{
		Body: &waE2E.InteractiveMessage_Body{Text: proto.String(sp.body)},
	}
	if sp.title != "" || sp.subtitle != "" || headerImg != nil {
		h := &waE2E.InteractiveMessage_Header{}
		if sp.title != "" {
			h.Title = proto.String(sp.title)
		}
		if sp.subtitle != "" {
			h.Subtitle = proto.String(sp.subtitle)
		}
		if headerImg != nil {
			h.HasMediaAttachment = proto.Bool(true)
			h.Media = &waE2E.InteractiveMessage_Header_ImageMessage{ImageMessage: headerImg}
		}
		im.Header = h
	}
	if sp.footer != "" {
		im.Footer = &waE2E.InteractiveMessage_Footer{Text: proto.String(sp.footer)}
	}

	if playSlot(sp) == "carousel" {
		cards := make([]*waE2E.InteractiveMessage, 0, len(sp.cards))
		for _, c := range sp.cards {
			card := msgbuilder.NewCard().SetTitle(c.title).SetBody(c.body)
			for i, b := range c.buttons {
				card.AddButton(playNFButton(b, i+1))
			}
			cards = append(cards, card.BuildCard())
		}
		im.InteractiveMessage = &waE2E.InteractiveMessage_CarouselMessage_{
			CarouselMessage: &waE2E.InteractiveMessage_CarouselMessage{Cards: cards},
		}
	} else {
		btns := make([]messaging.NFButton, 0, len(sp.buttons))
		for i, b := range sp.buttons {
			btns = append(btns, playNFButton(b, i+1))
		}
		im.InteractiveMessage = &waE2E.InteractiveMessage_NativeFlowMessage_{
			NativeFlowMessage: &waE2E.InteractiveMessage_NativeFlowMessage{
				Buttons: messaging.NativeFlowButtons(btns...),
			},
		}
	}

	if len(sp.a2ui) > 0 {
		a2 := msgbuilder.NewA2UI()
		for i, el := range sp.a2ui {
			playA2UIAdd(a2, el, i+1)
		}
		im.BloksWidget = &waE2E.InteractiveMessage_BloksWidget{
			Uuid: proto.String(whatsmeow.GenerateMessageID()),
			Data: proto.String(a2.SurfaceData()),
			Type: proto.String("im_a2ui"),
		}
		im.ContextInfo = &waE2E.ContextInfo{Expiration: proto.Uint32(7776000)}
	}

	return &waE2E.Message{InteractiveMessage: im}
}

// playNFButton memetakan satu tombol DSL ke konstruktor params yang sudah
// terbukti jalan (.btntest). id quick_reply/row kosong diberi awalan play:
// supaya tap-nya bisa dicegat CheckPlayTap.
func playNFButton(b playButton, n int) messaging.NFButton {
	switch b.typ {
	case "reply":
		id := b.arg
		if id == "" {
			id = fmt.Sprintf("%s%d", playTapPrefix, n)
		}
		return messaging.QuickReply(id, b.label)
	case "url":
		return messaging.CTAURL(b.label, b.arg)
	case "copy":
		return messaging.CTACopy(b.label, b.arg)
	case "call":
		return messaging.CTACall(b.label, b.arg)
	case "location":
		return messaging.SendLocation(b.label)
	case "signup":
		return messaging.InAppSignup()
	case "select":
		rows := make([]messaging.NFRow, 0, len(b.rows))
		for i, r := range b.rows {
			id := r.id
			if id == "" {
				id = fmt.Sprintf("%srow%d", playTapPrefix, i+1)
			}
			rows = append(rows, messaging.NFRow{ID: id, Title: r.title, Description: r.desc})
		}
		return messaging.SingleSelect(b.label, messaging.NFSection{Rows: rows})
	}
	return messaging.NFButton{}
}

// playA2UIAdd menambahkan satu elemen A2UI. Id komponen harus unik per
// surface, jadi nomor urut elemen dipakai.
func playA2UIAdd(a2 *msgbuilder.A2UIBuilder, el playElem, n int) {
	id := fmt.Sprintf("play_%d", n)
	switch el.typ {
	case "heading":
		a2.AddHeading(id, el.a, "h1")
	case "text":
		a2.AddText(id, el.a, "body")
	case "card":
		a2.AddCardBox(id, el.a, el.b)
	case "image":
		a2.AddImage(id, el.a)
	}
}

func playBuildAirich(ctx context.Context, sp playSpec) (*waE2E.Message, error) {
	b := msgbuilder.NewAIRich()
	if sp.title != "" {
		b.AddHeading(sp.title)
	}
	if sp.body != "" {
		b.AddMarkdown(sp.body)
	}
	if sp.image != "" {
		b.AddImage(sp.image, sp.image)
	}

	type footerLink = struct{ Label, URL string }
	links := make([]footerLink, 0)
	for _, el := range sp.sections {
		switch el.typ {
		case "heading":
			b.AddHeading(el.a)
		case "md":
			b.AddMarkdown(el.a)
		case "image":
			b.AddImage(el.a, el.a)
		case "link":
			links = append(links, footerLink{Label: el.a, URL: el.b})
		case "html":
			html, pageURL, err := playHTML(ctx, el.b)
			if err != nil {
				return nil, err
			}
			b.AddEmbeddedHTML(el.a, html, pageURL)
		}
	}
	if len(links) > 0 {
		b.AddFooterLinks(links...)
	}
	return b.Build(), nil
}

// playHTML mengambil sumber layar HTML: template bawaan atau URL owner.
func playHTML(ctx context.Context, source string) (string, string, error) {
	switch source {
	case "popioo":
		return popiooHTML, "https://example.com", nil
	case "soundtest":
		return soundTestHTML, "https://example.com", nil
	}
	if !strings.HasPrefix(source, "url:") {
		return "", "", fmt.Errorf("sumber html `%s` tidak dikenal (popioo|soundtest|url:https://...)", source)
	}
	u := strings.TrimSpace(strings.TrimPrefix(source, "url:"))
	data, err := playFetchData(ctx, u, playMaxHTMLBytes, "HTML")
	if err != nil {
		return "", "", err
	}
	return string(data), u, nil
}

// playFetchData mengunduh URL dengan batas ukuran (spool, bukan io.ReadAll)
// supaya URL salah yang menunjuk berkas besar tidak menahan memori.
func playFetchData(ctx context.Context, rawURL string, maxBytes int64, label string) ([]byte, error) {
	dctx, cancel := context.WithTimeout(ctx, playFetchTimeout)
	defer cancel()
	blob, err := external.DownloadBlobLimit(dctx, rawURL, nil, maxBytes)
	if err != nil {
		return nil, fmt.Errorf("gagal unduh %s: %w", label, err)
	}
	defer blob.Close()
	data, err := blob.Bytes()
	if err != nil {
		return nil, fmt.Errorf("gagal baca %s: %w", label, err)
	}
	return data, nil
}
