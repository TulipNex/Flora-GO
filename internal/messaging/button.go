// File ini memusatkan perakitan + pengiriman bubble tombol WhatsApp
// (InteractiveMessage + NativeFlowMessage). Sebelumnya tiap plugin menulis
// proto-nya sendiri (pdfmerge.go, pdflocal.go, dev_btntest.go) sehingga peta
// nama tombol -> params JSON tercecer. Di sini peta itu jadi SATU sumber
// kebenaran.
//
// Node `biz` TIDAK dipasang manual: hypermeow menempelkannya otomatis untuk
// tiap InteractiveMessage+NativeFlowMessage (lihat getMessageContent /
// buildNativeFlowBizNode di third_party/hypermeow/send.go). Node ganda =
// error 479; nama turunan = error 473/405 (fork lokal selalu mengirim
// name="mixed").
package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"wabot-go/internal/store"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

// NFButton adalah satu tombol native_flow: nama + params yang di-marshal ke
// buttonParamsJSON. Params boleh nil (jadi "{}"), string/[]byte (dipakai apa
// adanya bila sudah di-marshal sendiri), atau struct/map apa pun.
type NFButton struct {
	Name   string
	Params any
}

// NFText adalah teks bubble interactive. Body wajib secara praktik (tanpa
// body bubble tak tampil); Title/Subtitle mengisi header, Footer opsional.
// HeaderImage (sudah di-upload via UploadImageMessage) menampilkan header
// bergambar penuh. Thumb hanya bitmap pratinjau kecil — klien MENGABAIKAN
// JPEGThumbnail sebagai header, jadi pakai HeaderImage untuk kartu bergambar.
type NFText struct {
	Title       string
	Subtitle    string
	Body        string
	Footer      string
	HeaderImage *waE2E.ImageMessage
	Thumb       []byte
}

// Nama tombol native_flow yang dikenal server. Konstanta ini disediakan
// supaya plugin tak menulis string liar. Ringkasan status live (akun bisnis,
// Okt 2026 — lihat plugins/dev_btntest.go):
//
//	Jalan        : quick_reply, single_select, cta_url, cta_copy, cta_call,
//	               send_location, inapp_signup, booking_confirmation.
//	Butuh Meta   : flow/galaxy_message (perlu Flow asli di server Meta).
//	Server-only  : review_and_pay, payment_info, wa_payment_transaction_details,
//	               mpm, automated_greeting_message_view_catalog,
//	               call_permission_request, cta_catalog, address_message
//	               (payload pembayaran/katalog terstruktur, divalidasi server;
//	               tak reproducible dari bot biasa). JANGAN uji ulang tanpa
//	               alasan baru.
const (
	NFQuickReply           = "quick_reply"
	NFSingleSelect         = "single_select"
	NFCTAURL               = "cta_url"
	NFCTACopy              = "cta_copy"
	NFCTACall              = "cta_call"
	NFSendLocation         = "send_location"
	NFInAppSignup          = "inapp_signup"
	NFBookingConfirmation  = "booking_confirmation"
	NFFlow                 = "flow"
	NFGalaxyMessage        = "galaxy_message"
	NFAddressMessage       = "address_message"
	NFCTACatalog           = "cta_catalog"
	NFCTAReminder          = "cta_reminder"
	NFCTACancelReminder    = "cta_cancel_reminder"
	NFReviewAndPay         = "review_and_pay"
	NFPaymentInfo          = "payment_info"
	NFMPM                  = "mpm"
	NFPaymentTxDetails     = "wa_payment_transaction_details"
	NFAutomatedGreetingCat = "automated_greeting_message_view_catalog"
	NFCallPermissionReq    = "call_permission_request"
)

// ---- Konstruktor params tombol (peta name -> JSON) ----

// QuickReply: tombol yang mengirim balik `id`-nya saat ditekan.
func QuickReply(id, display string) NFButton {
	return NFButton{NFQuickReply, map[string]string{"display_text": display, "id": id}}
}

// CTAURL: tombol membuka tautan. URL juga diisi ke merchant_url (paritas
// pola komunitas Baileys).
func CTAURL(display, url string) NFButton {
	return NFButton{NFCTAURL, map[string]string{"display_text": display, "url": url, "merchant_url": url}}
}

// CTACopy: tombol menyalin copy_code ke clipboard.
func CTACopy(display, code string) NFButton {
	return NFButton{NFCTACopy, map[string]string{"display_text": display, "copy_code": code}}
}

// CTACall: tombol memanggil nomor (format internasional, digit saja).
func CTACall(display, phone string) NFButton {
	return NFButton{NFCTACall, map[string]string{"display_text": display, "phone_number": phone}}
}

// SendLocation: tombol minta lokasi pengguna. display boleh kosong.
func SendLocation(display string) NFButton {
	if display == "" {
		return NFButton{NFSendLocation, nil}
	}
	return NFButton{NFSendLocation, map[string]string{"display_text": display}}
}

// InAppSignup: tombol pendaftaran (tanpa params).
func InAppSignup() NFButton { return NFButton{NFInAppSignup, nil} }

// NFRow adalah satu baris di dalam single_select.
type NFRow struct {
	ID          string // id yang dikembalikan saat dipilih (wajib, unik)
	Title       string // label baris (wajib)
	Description string // baris kedua (opsional)
	Header      string // header kecil (opsional)
}

// NFSection mengelompokkan baris.
type NFSection struct {
	Title string
	Rows  []NFRow
}

// SingleSelect: tombol yang membuka sheet daftar pilihan (persis satu baris
// terpilih). Aturan praktik: maksimum 10 baris total (lihat dev_btntest).
func SingleSelect(title string, sections ...NFSection) NFButton {
	secs := make([]map[string]any, 0, len(sections))
	for _, s := range sections {
		rows := make([]map[string]any, 0, len(s.Rows))
		for _, r := range s.Rows {
			row := map[string]any{"id": r.ID, "title": r.Title}
			if r.Description != "" {
				row["description"] = r.Description
			}
			if r.Header != "" {
				row["header"] = r.Header
			}
			rows = append(rows, row)
		}
		sec := map[string]any{"rows": rows}
		if s.Title != "" {
			sec["title"] = s.Title
		}
		secs = append(secs, sec)
	}
	return NFButton{NFSingleSelect, map[string]any{"title": title, "sections": secs}}
}

// NFBookingDisplay adalah blok display_content booking_confirmation.
type NFBookingDisplay struct {
	Language                      string
	MeetingType                   string
	BottomSheetHeader             string
	AddToCalendarCTA              string
	ViewOnMapsCTA                 string
	ManageBookingCTA              string
	ManageBookingNotSupportedText string
	ReadMore                      string
}

// NFBooking adalah params kartu booking_confirmation. Field kosong di-drop
// supaya tombol kondisionalnya ikut hilang (perilaku yang diuji .booktest).
type NFBooking struct {
	StartDatetime        time.Time
	EndDatetime          time.Time
	Location             string
	BookingURL           string
	PhoneNumber          string
	BookingManagementURL string
	Description          string
	Email                string
	DisplayText          string
	Display              NFBookingDisplay
}

// BookingConfirmation: kartu janji (kalender/peta/kelola booking).
func BookingConfirmation(b NFBooking) NFButton {
	p := map[string]any{}
	if !b.StartDatetime.IsZero() {
		p["start_datetime"] = b.StartDatetime.Format(time.RFC3339)
	}
	if !b.EndDatetime.IsZero() {
		p["end_datetime"] = b.EndDatetime.Format(time.RFC3339)
	}
	putIf := func(k, v string) {
		if v != "" {
			p[k] = v
		}
	}
	putIf("location", b.Location)
	putIf("booking_url", b.BookingURL)
	putIf("phone_number", b.PhoneNumber)
	putIf("booking_management_url", b.BookingManagementURL)
	putIf("description", b.Description)
	putIf("email", b.Email)
	putIf("display_text", b.DisplayText)

	d := map[string]string{}
	dput := func(k, v string) {
		if v != "" {
			d[k] = v
		}
	}
	dput("display_language", b.Display.Language)
	dput("display_meeting_type", b.Display.MeetingType)
	dput("display_bottom_sheet_header", b.Display.BottomSheetHeader)
	dput("display_add_to_calendar_cta_text", b.Display.AddToCalendarCTA)
	dput("display_view_on_maps_cta_text", b.Display.ViewOnMapsCTA)
	dput("display_manage_booking_cta_text", b.Display.ManageBookingCTA)
	dput("display_manage_booking_not_supported_text", b.Display.ManageBookingNotSupportedText)
	dput("display_read_more", b.Display.ReadMore)
	if len(d) > 0 {
		p["display_content"] = d
	}
	return NFButton{NFBookingConfirmation, p}
}

// NFFlowParams adalah params tombol flow/galaxy_message (WhatsApp Flows).
// Nama tombol ditentukan lewat Name (NFFlow atau NFGalaxyMessage).
type NFFlowParams struct {
	Name      string // default NFFlow
	FlowID    string
	FlowToken string
	CTA       string
	Action    string // "navigate" (punya Screen) atau "data_exchange"
	Screen    string
	Data      map[string]any
	Mode      string // default "published"
	Version   string // default "3"
}

// Flow: tombol membuka Flow. Butuh Flow asli di server Meta — dummy hanya
// menampilkan kartu kosong + notifikasi kesalahan.
func Flow(f NFFlowParams) NFButton {
	name := f.Name
	if name == "" {
		name = NFFlow
	}
	mode := f.Mode
	if mode == "" {
		mode = "published"
	}
	version := f.Version
	if version == "" {
		version = "3"
	}
	p := map[string]any{
		"mode":                 mode,
		"flow_message_version": version,
		"flow_token":           f.FlowToken,
		"flow_id":              f.FlowID,
		"flow_cta":             f.CTA,
		"flow_action":          f.Action,
	}
	if f.Action == "navigate" {
		payload := map[string]any{"screen": f.Screen}
		if f.Data != nil {
			payload["data"] = f.Data
		}
		p["flow_action_payload"] = payload
	}
	return NFButton{name, p}
}

// AddressMessage: tombol form alamat. Live: geo-block (hanya India/Singapura).
func AddressMessage(display string) NFButton {
	return NFButton{NFAddressMessage, map[string]string{"display_text": display}}
}

// ---- Perakitan + pengiriman ----

// NativeFlow merakit satu bubble InteractiveMessage + NativeFlowMessage dari
// daftar tombol (node `biz` biar hypermeow yang menempel).
func NativeFlow(t NFText, buttons ...NFButton) *waE2E.Message {
	pb := make([]*waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton, 0, len(buttons))
	for _, b := range buttons {
		pb = append(pb, &waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
			Name:             proto.String(b.Name),
			ButtonParamsJSON: proto.String(marshalParams(b.Params)),
		})
	}

	im := &waE2E.InteractiveMessage{
		Body: &waE2E.InteractiveMessage_Body{Text: proto.String(t.Body)},
		InteractiveMessage: &waE2E.InteractiveMessage_NativeFlowMessage_{
			NativeFlowMessage: &waE2E.InteractiveMessage_NativeFlowMessage{Buttons: pb},
		},
	}
	if t.Title != "" || t.Subtitle != "" || t.HeaderImage != nil || len(t.Thumb) > 0 {
		im.Header = &waE2E.InteractiveMessage_Header{}
		if t.Title != "" {
			im.Header.Title = proto.String(t.Title)
		}
		if t.Subtitle != "" {
			im.Header.Subtitle = proto.String(t.Subtitle)
		}
		switch {
		case t.HeaderImage != nil:
			im.Header.HasMediaAttachment = proto.Bool(true)
			im.Header.Media = &waE2E.InteractiveMessage_Header_ImageMessage{ImageMessage: t.HeaderImage}
		case len(t.Thumb) > 0:
			im.Header.HasMediaAttachment = proto.Bool(true)
			im.Header.Media = &waE2E.InteractiveMessage_Header_JPEGThumbnail{JPEGThumbnail: t.Thumb}
		}
	}
	if t.Footer != "" {
		im.Footer = &waE2E.InteractiveMessage_Footer{Text: proto.String(t.Footer)}
	}
	return &waE2E.Message{InteractiveMessage: im}
}

// SendNativeFlow merakit + mengirim bubble tombol, lalu mencatat log & ID
// pesan bot. msgType/text dipakai untuk logging (lihat store.LogOutgoing) dan
// text juga jadi label pelacak pesan bot.
func SendNativeFlow(ctx context.Context, mctx *store.MessageContext, t NFText, msgType, text string, buttons ...NFButton) error {
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, NativeFlow(t, buttons...),
		whatsmeow.SendRequestExtra{ID: whatsmeow.GenerateMessageID()})
	if err != nil {
		return fmt.Errorf("gagal kirim bubble tombol: %w", err)
	}
	store.LogOutgoing(mctx, msgType, text)
	store.TrackBotMessageID(resp.ID, text)
	return nil
}

// marshalParams mengubah Params jadi string JSON. nil -> "{}". string/[]byte
// dipakai apa adanya (untuk params yang sudah di-marshal sendiri).
func marshalParams(p any) string {
	switch v := p.(type) {
	case nil:
		return "{}"
	case string:
		if v == "" {
			return "{}"
		}
		return v
	case []byte:
		if len(v) == 0 {
			return "{}"
		}
		return string(v)
	default:
		raw, err := json.Marshal(p)
		if err != nil {
			return "{}"
		}
		return string(raw)
	}
}
