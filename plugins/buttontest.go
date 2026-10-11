package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"wabot-go/internal/logfmt"
	"wabot-go/internal/store"

	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func init() {
	registerPlugin(RegisterButtonTest)
}

// Uji coba tombol + elemen interaktif whatsmeow.
// .btntest = bubble native_flow (7 tombol) · phone = minta nomor.
// CATATAN live: ButtonsMessage/ListMessage lawas DITOLAK server (405),
// TemplateMessage terhidrasi terkirim tapi tak dirender klien (butuh
// templateID bisnis) — ketiganya dihapus dari pengirim, hanya extractor
// responsnya yang dipertahankan. Terisolasi (file ini + hook).
func RegisterButtonTest(r *Registry) {
	r.Register(Command{
		Name:        "btntest",
		Description: "Uji tombol (native/phone)",
		Category:    CategoryAlat,
		Handler:     handleButtonTest,
	})
}

// btnTestSend mengirim satu bubble uji + menandai pending + tracking.
func btnTestSend(ctx context.Context, mctx *store.MessageContext, msg *waE2E.Message, extra whatsmeow.SendRequestExtra, label string) error {
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, msg, extra)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal kirim %s: %v", label, err))
	}
	btnTestArm(mctx.Chat.String(), mctx.Sender.String())
	store.LogOutgoing(mctx, "btntest", label)
	store.TrackBotMessageID(resp.ID, "btntest "+label)
	return nil
}

func handleButtonTest(ctx context.Context, mctx *store.MessageContext) error {
	sub := ""
	if len(mctx.Args) > 0 {
		sub = strings.ToLower(strings.TrimSpace(mctx.Args[0]))
	}
	switch sub {
	case "phone":
		return btnTestSend(ctx, mctx, btnTestPhoneBubble(), whatsmeow.SendRequestExtra{ID: whatsmeow.GenerateMessageID()}, "minta nomor")
	case "", "native", "flow":
		return btnTestSendNative(ctx, mctx)
	default:
		return store.ReplyText(ctx, mctx, "Varian: .btntest [native|phone]")
	}
}

// btnTestPhoneBubble = minta nomor telepon pengguna.
func btnTestPhoneBubble() *waE2E.Message {
	return &waE2E.Message{
		RequestPhoneNumberMessage: &waE2E.RequestPhoneNumberMessage{},
	}
}

const btnTestTTL = 5 * time.Minute

var btnTestPending = struct {
	sync.Mutex
	m map[string]time.Time
}{m: make(map[string]time.Time)}

func btnTestKey(chat, sender string) string { return chat + "|" + sender }

func btnTestArm(chat, sender string) {
	btnTestPending.Lock()
	defer btnTestPending.Unlock()
	btnTestPending.m[btnTestKey(chat, sender)] = time.Now().Add(btnTestTTL)
}

func btnTestPeek(chat, sender string) bool {
	btnTestPending.Lock()
	defer btnTestPending.Unlock()
	exp, ok := btnTestPending.m[btnTestKey(chat, sender)]
	return ok && time.Now().Before(exp)
}

func btnTestConsume(chat, sender string) {
	btnTestPending.Lock()
	defer btnTestPending.Unlock()
	delete(btnTestPending.m, btnTestKey(chat, sender))
}

func btnTestNativeFlowButton(name, paramsJSON string) *waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton {
	return &waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
		Name:             proto.String(name),
		ButtonParamsJSON: proto.String(paramsJSON),
	}
}

// btnTestSendNative = bubble A: 7 tombol native_flow (round-1 + round-2).
func btnTestSendNative(ctx context.Context, mctx *store.MessageContext) error {
	singleSelect, _ := json.Marshal(map[string]any{
		"title": "Pilih Opsi",
		"sections": []map[string]any{{
			"title": "Opsi uji",
			"rows": []map[string]any{
				{"title": "Opsi Satu", "description": "Deskripsi satu", "id": "btntest_opt_1"},
				{"title": "Opsi Dua", "description": "Deskripsi dua", "id": "btntest_opt_2"},
			},
		}},
	})
	copyBtn, _ := json.Marshal(map[string]string{
		"display_text": "Salin Teks",
		"copy_code":    "Halo dari btntest!",
	})
	urlBtn, _ := json.Marshal(map[string]string{
		"display_text": "Buka Example",
		"url":          "https://example.com",
	})
	qr1, _ := json.Marshal(map[string]string{
		"display_text": "Balas Cepat 1",
		"id":           "btntest_qr_1",
	})
	qr2, _ := json.Marshal(map[string]string{
		"display_text": "Balas Cepat 2",
		"id":           "btntest_qr_2",
	})
	// cta_call: format komunitas Baileys = display_text + phone_number.
	// Nomor dummy jelas (tak bisa tersambung) — ganti bila ingin uji nyata.
	callBtn, _ := json.Marshal(map[string]string{
		"display_text": "Call Center (Uji)",
		"phone_number": "6280000000000",
	})
	// send_location: tanpa params (klien menampilkan pemilih lokasi).
	locBtn, _ := json.Marshal(map[string]string{})
	msg := &waE2E.Message{
		InteractiveMessage: &waE2E.InteractiveMessage{
			Header: &waE2E.InteractiveMessage_Header{
				Title:    proto.String("Uji Tombol Native"),
				Subtitle: proto.String("Ketuk salah satu tombol di bawah"),
			},
			Body:   &waE2E.InteractiveMessage_Body{Text: proto.String("Ketuk: daftar/copy/link/balas-cepat/call/lokasi. URL & call tak membalas (wajar); lokasi terkirim sebagai pesan lokasi.")},
			Footer: &waE2E.InteractiveMessage_Footer{Text: proto.String("wabot-go • btntest round-2")},
			InteractiveMessage: &waE2E.InteractiveMessage_NativeFlowMessage_{
				NativeFlowMessage: &waE2E.InteractiveMessage_NativeFlowMessage{
					Buttons: []*waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
						btnTestNativeFlowButton("single_select", string(singleSelect)),
						btnTestNativeFlowButton("cta_copy", string(copyBtn)),
						btnTestNativeFlowButton("cta_url", string(urlBtn)),
						btnTestNativeFlowButton("quick_reply", string(qr1)),
						btnTestNativeFlowButton("quick_reply", string(qr2)),
						btnTestNativeFlowButton("cta_call", string(callBtn)),
						btnTestNativeFlowButton("send_location", string(locBtn)),
					},
				},
			},
		},
	}
	// Node biz (tiruan sendInteractive Baileys): wajib agar klien me-render
	// tombol native_flow.
	nodes := &[]waBinary.Node{{
		Tag: "biz",
		Content: []waBinary.Node{{
			Tag:   "interactive",
			Attrs: waBinary.Attrs{"type": "native_flow", "v": "1"},
			Content: []waBinary.Node{{
				Tag:   "native_flow",
				Attrs: waBinary.Attrs{"v": "9", "name": "mixed"},
			}},
		}},
	}}
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, msg,
		whatsmeow.SendRequestExtra{ID: whatsmeow.GenerateMessageID(), AdditionalNodes: nodes})
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal kirim bubble tombol: %v", err))
	}
	btnTestArm(mctx.Chat.String(), mctx.Sender.String())
	store.LogOutgoing(mctx, "btntest", "bubble tombol")
	store.TrackBotMessageID(resp.ID, "btntest")
	return nil
}

// btnTestPayloadID mengekstrak ID dari tap native_flow DAN respons lawas
// (buttons/list/template). single_select/rows mengembalikan id; sisanya
// params mentah untuk verifikasi.
func btnTestPayloadID(evt *events.Message) (string, bool) {
	unwrapped := store.UnwrapMessage(evt.Message)
	if unwrapped == nil {
		return "", false
	}
	if br := unwrapped.GetButtonsResponseMessage(); br != nil {
		if id := strings.TrimSpace(br.GetSelectedButtonID()); id != "" {
			return id, true
		}
		if t := strings.TrimSpace(br.GetSelectedDisplayText()); t != "" {
			return "teks: " + t, true
		}
		return "", false
	}
	if lr := unwrapped.GetListResponseMessage(); lr != nil {
		if id := strings.TrimSpace(lr.GetSingleSelectReply().GetSelectedRowID()); id != "" {
			return id, true
		}
		if t := strings.TrimSpace(lr.GetTitle()); t != "" {
			return "judul: " + t, true
		}
		return "", false
	}
	if tr := unwrapped.GetTemplateButtonReplyMessage(); tr != nil {
		if id := strings.TrimSpace(tr.GetSelectedID()); id != "" {
			return id, true
		}
		if t := strings.TrimSpace(tr.GetSelectedDisplayText()); t != "" {
			return "teks: " + t, true
		}
		return "", false
	}
	resp := unwrapped.GetInteractiveResponseMessage()
	if resp == nil {
		return "", false
	}
	nf := resp.GetNativeFlowResponseMessage()
	if nf == nil {
		return "", false
	}
	params := strings.TrimSpace(nf.GetParamsJSON())
	if params == "" {
		return "", false
	}
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(params), &p); err != nil || p.ID == "" {
		short := params
		if len([]rune(short)) > 200 {
			short = string([]rune(short)[:200]) + "..."
		}
		return "params: " + short, true
	}
	return p.ID, true
}

// CheckButtonTestLocation mengonfirmasi pesan lokasi masuk saat sesi uji
// aktif (verifikasi tombol send_location). Tidak mengonsumsi pending.
func CheckButtonTestLocation(client *whatsmeow.Client, evt *events.Message) bool {
	msg := store.UnwrapMessage(evt.Message)
	if msg == nil {
		return false
	}
	loc := msg.GetLocationMessage()
	if loc == nil {
		if ll := msg.GetLiveLocationMessage(); ll != nil {
			loc = &waE2E.LocationMessage{
				DegreesLatitude:  proto.Float64(ll.GetDegreesLatitude()),
				DegreesLongitude: proto.Float64(ll.GetDegreesLongitude()),
			}
		}
	}
	if loc == nil {
		return false
	}
	if !btnTestPeek(evt.Info.Chat.String(), evt.Info.Sender.String()) {
		return false
	}
	mctx := &store.MessageContext{Client: client, Chat: evt.Info.Chat, Sender: evt.Info.Sender, RawEvt: evt}
	_ = store.ReplyText(context.Background(), mctx,
		fmt.Sprintf("Lokasi diterima! %.5f, %.5f", loc.GetDegreesLatitude(), loc.GetDegreesLongitude()))
	return true
}

var btnTestTelRe = regexp.MustCompile(`(?i)TEL[^:]*:([+\d][\d\s().-]*)`)

// CheckButtonTestContact mengonfirmasi kontak/nomor masuk saat sesi uji
// aktif (verifikasi RequestPhoneNumberMessage). Tidak mengonsumsi pending.
func CheckButtonTestContact(client *whatsmeow.Client, evt *events.Message) bool {
	msg := store.UnwrapMessage(evt.Message)
	if msg == nil {
		return false
	}
	contact := msg.GetContactMessage()
	if contact == nil {
		return false
	}
	if !btnTestPeek(evt.Info.Chat.String(), evt.Info.Sender.String()) {
		return false
	}
	name := strings.TrimSpace(contact.GetDisplayName())
	if name == "" {
		name = "-"
	}
	number := "-"
	if m := btnTestTelRe.FindStringSubmatch(contact.GetVcard()); m != nil {
		number = strings.TrimSpace(m[1])
	}
	mctx := &store.MessageContext{Client: client, Chat: evt.Info.Chat, Sender: evt.Info.Sender, RawEvt: evt}
	_ = store.ReplyText(context.Background(), mctx,
		fmt.Sprintf("Kontak diterima! %s (%s)", name, number))
	return true
}

// CheckButtonTestTap mencegat tap tombol. Urutan aman: pastikan dulu ini
// tap (jangan telan pesan biasa), baru cek pending. true = ditangani.
func CheckButtonTestTap(client *whatsmeow.Client, evt *events.Message, text string) bool {
	_ = text
	id, ok := btnTestPayloadID(evt)
	if !ok {
		return false
	}
	mctx := &store.MessageContext{Client: client, Chat: evt.Info.Chat, Sender: evt.Info.Sender, RawEvt: evt}
	if !btnTestPeek(evt.Info.Chat.String(), evt.Info.Sender.String()) {
		_ = store.ReplyText(context.Background(), mctx, fmt.Sprintf("Tap tombol terdeteksi (di luar sesi uji): %s", id))
		return true
	}
	btnTestConsume(evt.Info.Chat.String(), evt.Info.Sender.String())
	if err := store.ReplyText(context.Background(), mctx, fmt.Sprintf("Tap diterima! ID tombol: %s", id)); err != nil {
		logfmt.Warn("btntest: balas tap gagal (%v)\n", err)
	}
	return true
}
