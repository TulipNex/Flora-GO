package messaging

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/polymorfa/hypermeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

// mustJSON memarshal nilai jadi string JSON kanonik (kunci terurut) untuk
// dibandingkan dengan Params tombol.
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal harapan: %v", err)
	}
	return string(raw)
}

func TestMarshalParams(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"nil", nil, "{}"},
		{"string kosong", "", "{}"},
		{"string dipakai apa adanya", `{"id":"x"}`, `{"id":"x"}`},
		{"byte kosong", []byte{}, "{}"},
		{"byte apa adanya", []byte(`{"a":1}`), `{"a":1}`},
		{"map", map[string]string{"display_text": "Halo", "id": "abc"}, `{"display_text":"Halo","id":"abc"}`},
	}
	for _, c := range cases {
		if got := marshalParams(c.in); got != c.want {
			t.Errorf("%s: marshalParams(%v) = %q; mau %q", c.name, c.in, got, c.want)
		}
	}
}

func TestButtonConstructors(t *testing.T) {
	cases := []struct {
		name string
		btn  NFButton
		want string
	}{
		{"quick_reply", QuickReply("id1", "Balas"), `{"display_text":"Balas","id":"id1"}`},
		{"cta_url", CTAURL("Buka", "https://e.com"), `{"display_text":"Buka","merchant_url":"https://e.com","url":"https://e.com"}`},
		{"cta_copy", CTACopy("Salin", "CODE"), `{"copy_code":"CODE","display_text":"Salin"}`},
		{"cta_call", CTACall("Telepon", "6280000000000"), `{"display_text":"Telepon","phone_number":"6280000000000"}`},
		{"send_location kosong", SendLocation(""), "{}"},
		{"send_location teks", SendLocation("Lokasi"), `{"display_text":"Lokasi"}`},
		{"inapp_signup", InAppSignup(), "{}"},
		{"address_message", AddressMessage("Alamat"), `{"display_text":"Alamat"}`},
	}
	for _, c := range cases {
		if got := marshalParams(c.btn.Params); got != c.want {
			t.Errorf("%s: params = %q; mau %q", c.name, got, c.want)
		}
	}
}

func TestButtonNames(t *testing.T) {
	cases := []struct {
		btn  NFButton
		want string
	}{
		{QuickReply("i", "d"), NFQuickReply},
		{CTAURL("d", "u"), NFCTAURL},
		{CTACopy("d", "c"), NFCTACopy},
		{CTACall("d", "p"), NFCTACall},
		{SendLocation(""), NFSendLocation},
		{InAppSignup(), NFInAppSignup},
		{AddressMessage("d"), NFAddressMessage},
		{SingleSelect("t"), NFSingleSelect},
	}
	for _, c := range cases {
		if c.btn.Name != c.want {
			t.Errorf("nama tombol = %q; mau %q", c.btn.Name, c.want)
		}
	}
}

func TestSingleSelect(t *testing.T) {
	btn := SingleSelect("Menu",
		NFSection{Title: "Makanan", Rows: []NFRow{
			{ID: "nasi", Title: "Nasi", Description: "Goreng"},
			{ID: "mie", Title: "Mie", Header: "pedas"},
		}},
		NFSection{Rows: []NFRow{{ID: "air", Title: "Air"}}},
	)
	want := mustJSON(t, map[string]any{
		"title": "Menu",
		"sections": []map[string]any{
			{"title": "Makanan", "rows": []map[string]any{
				{"id": "nasi", "title": "Nasi", "description": "Goreng"},
				{"id": "mie", "title": "Mie", "header": "pedas"},
			}},
			{"rows": []map[string]any{
				{"id": "air", "title": "Air"},
			}},
		},
	})
	if got := marshalParams(btn.Params); got != want {
		t.Errorf("single_select params = %q; mau %q", got, want)
	}
}

func TestBookingConfirmation(t *testing.T) {
	start := time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC)
	end := start.Add(30 * time.Minute)
	full := BookingConfirmation(NFBooking{
		StartDatetime: start,
		EndDatetime:   end,
		Location:      "Indonesia",
		BookingURL:    "https://e.com/b",
		PhoneNumber:   "6280000000000",
		Email:         "a@e.com",
		DisplayText:   "Lihat",
		Display:       NFBookingDisplay{Language: "id", ReadMore: "Selengkapnya"},
	})
	want := mustJSON(t, map[string]any{
		"start_datetime": "2026-01-02T15:04:05Z",
		"end_datetime":   "2026-01-02T15:34:05Z",
		"location":       "Indonesia",
		"booking_url":    "https://e.com/b",
		"phone_number":   "6280000000000",
		"email":          "a@e.com",
		"display_text":   "Lihat",
		"display_content": map[string]string{
			"display_language":  "id",
			"display_read_more": "Selengkapnya",
		},
	})
	if got := marshalParams(full.Params); got != want {
		t.Errorf("booking penuh = %q; mau %q", got, want)
	}

	// Minimal: hanya waktu → tak ada display_content/field kosong.
	min := BookingConfirmation(NFBooking{StartDatetime: start, EndDatetime: end})
	wantMin := mustJSON(t, map[string]any{
		"start_datetime": "2026-01-02T15:04:05Z",
		"end_datetime":   "2026-01-02T15:34:05Z",
	})
	if got := marshalParams(min.Params); got != wantMin {
		t.Errorf("booking minimal = %q; mau %q", got, wantMin)
	}
}

func TestFlow(t *testing.T) {
	nav := Flow(NFFlowParams{
		FlowID:    "flow-1",
		FlowToken: "tok",
		CTA:       "Buka",
		Action:    "navigate",
		Screen:    "WELCOME",
		Data:      map[string]any{"n": 1},
	})
	want := mustJSON(t, map[string]any{
		"mode":                 "published",
		"flow_message_version": "3",
		"flow_token":           "tok",
		"flow_id":              "flow-1",
		"flow_cta":             "Buka",
		"flow_action":          "navigate",
		"flow_action_payload":  map[string]any{"screen": "WELCOME", "data": map[string]any{"n": 1}},
	})
	if nav.Name != NFFlow {
		t.Errorf("flow default name = %q; mau %q", nav.Name, NFFlow)
	}
	if got := marshalParams(nav.Params); got != want {
		t.Errorf("flow navigate = %q; mau %q", got, want)
	}

	// data_exchange: tanpa payload.
	de := Flow(NFFlowParams{Name: NFGalaxyMessage, FlowID: "f", FlowToken: "t", CTA: "c", Action: "data_exchange"})
	if de.Name != NFGalaxyMessage {
		t.Errorf("flow name = %q; mau %q", de.Name, NFGalaxyMessage)
	}
	if got := marshalParams(de.Params); got == "" || strings.Contains(got, "flow_action_payload") {
		t.Errorf("data_exchange tak boleh punya payload: %q", got)
	}
}

func TestNativeFlowStructure(t *testing.T) {
	msg := NativeFlow(
		NFText{Title: "Judul", Subtitle: "Sub", Body: "Isi", Footer: "Kaki"},
		QuickReply("a", "A"),
		CTAURL("B", "https://e.com"),
	)
	im := msg.GetInteractiveMessage()
	if im == nil {
		t.Fatal("InteractiveMessage nil")
	}
	if got := im.GetHeader().GetTitle(); got != "Judul" {
		t.Errorf("title = %q", got)
	}
	if got := im.GetHeader().GetSubtitle(); got != "Sub" {
		t.Errorf("subtitle = %q", got)
	}
	if got := im.GetBody().GetText(); got != "Isi" {
		t.Errorf("body = %q", got)
	}
	if got := im.GetFooter().GetText(); got != "Kaki" {
		t.Errorf("footer = %q", got)
	}
	nf := im.GetNativeFlowMessage()
	if nf == nil {
		t.Fatal("NativeFlowMessage nil")
	}
	btns := nf.GetButtons()
	if len(btns) != 2 {
		t.Fatalf("jumlah tombol = %d; mau 2", len(btns))
	}
	if got := btns[0].GetName(); got != NFQuickReply {
		t.Errorf("tombol[0] name = %q", got)
	}
	if got := btns[0].GetButtonParamsJSON(); got != `{"display_text":"A","id":"a"}` {
		t.Errorf("tombol[0] params = %q", got)
	}
	if got := btns[1].GetName(); got != NFCTAURL {
		t.Errorf("tombol[1] name = %q", got)
	}
}

func TestNativeFlowOmitsEmptyHeaderFooter(t *testing.T) {
	msg := NativeFlow(NFText{Body: "Hanya isi"}, SendLocation(""))
	im := msg.GetInteractiveMessage()
	if im.GetHeader() != nil {
		t.Errorf("header harus nil bila judul/subjudul kosong")
	}
	if im.GetFooter() != nil {
		t.Errorf("footer harus nil bila kosong")
	}
	btns := im.GetNativeFlowMessage().GetButtons()
	if len(btns) != 1 || btns[0].GetButtonParamsJSON() != "{}" {
		t.Errorf("send_location tanpa display harus {\"}\": %+v", btns)
	}
}

// Pastikan helper proto tetap menghasilkan tipe pesan yang benar (bukan
// ExtendedTextMessage dsb) supaya jalur node `biz` hypermeow kena.
func TestNativeFlowMessageKind(t *testing.T) {
	msg := NativeFlow(NFText{Body: "x"}, QuickReply("a", "A"))
	if _, ok := any(msg.GetInteractiveMessage().GetInteractiveMessage()).(*waE2E.InteractiveMessage_NativeFlowMessage_); !ok {
		t.Fatalf("oneof bukan NativeFlowMessage_")
	}
}

// TestNativeFlowHeaderImage memastikan HeaderImage dipakai sebagai header
// bergambar (ImageMessage) dan MENANG atas Thumb — klien mengabaikan
// JPEGThumbnail sebagai header, jadi kartu bergambar wajib lewat jalur ini.
func TestNativeFlowHeaderImage(t *testing.T) {
	im := &waE2E.ImageMessage{URL: proto.String("https://mmg.whatsapp.net/x.enc")}
	msg := NativeFlow(NFText{Body: "Isi", HeaderImage: im, Thumb: []byte{0xFF, 0xD8}}, QuickReply("a", "A"))
	h := msg.GetInteractiveMessage().GetHeader()
	if h == nil {
		t.Fatal("header nil")
	}
	if !h.GetHasMediaAttachment() {
		t.Error("hasMediaAttachment harus true")
	}
	if got := h.GetImageMessage().GetURL(); got != "https://mmg.whatsapp.net/x.enc" {
		t.Errorf("header image URL = %q", got)
	}
	if h.GetJPEGThumbnail() != nil {
		t.Error("JPEGThumbnail tidak boleh dipakai saat HeaderImage ada")
	}
}

// TestNativeFlowHeaderThumbOnly: tanpa HeaderImage, Thumb jatuh ke
// JPEGThumbnail (perilaku lama, cadangan).
func TestNativeFlowHeaderThumbOnly(t *testing.T) {
	thumb := []byte{0xFF, 0xD8, 0xFF, 0xE0}
	msg := NativeFlow(NFText{Body: "Isi", Thumb: thumb}, QuickReply("a", "A"))
	h := msg.GetInteractiveMessage().GetHeader()
	if h == nil {
		t.Fatal("header nil")
	}
	if h.GetImageMessage() != nil {
		t.Error("ImageMessage tidak boleh ada bila hanya Thumb")
	}
	if got := h.GetJPEGThumbnail(); len(got) != len(thumb) {
		t.Errorf("thumbnail = %v, mau %v", got, thumb)
	}
}
