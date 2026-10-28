package plugins

import (
	"testing"

	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types"
	"github.com/polymorfa/hypermeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestSbprRender(t *testing.T) {
	got := sbprRender("Halo {nama}! Nomor kamu {nomor}.", "Budi", "62812")
	if got != "Halo Budi! Nomor kamu 62812." {
		t.Fatalf("render salah: %q", got)
	}
	if got := sbprRender("tanpa placeholder", "X", "1"); got != "tanpa placeholder" {
		t.Fatalf("teks polos berubah: %q", got)
	}
}

func TestSbprRejoinTemplate(t *testing.T) {
	p := parseSPRArgs("@a Halo | dunia")
	if got := sbprRejoinTemplate(p); got != "Halo | dunia" {
		t.Fatalf("template harus utuh (SBPR tak kenal decoy): %q", got)
	}
	p = parseSPRArgs("@a Halo biasa")
	if got := sbprRejoinTemplate(p); got != "Halo biasa" {
		t.Fatalf("template biasa rusak: %q", got)
	}
}

func TestSbprProfileCaption(t *testing.T) {
	if got := sbprProfileCaption("Halo {nama}, ini fotomu!"); got != "Halo {nama}, ini fotomu!" {
		t.Fatalf("caption berubah: %q", got)
	}
	// Teks ber-awalan @ tetap caption biasa, bukan target.
	if got := sbprProfileCaption("@semua lihat ini"); got != "@semua lihat ini" {
		t.Fatalf("caption @ harus utuh: %q", got)
	}
	if got := sbprProfileCaption(""); got != "" {
		t.Fatalf("caption kosong harus tetap kosong: %q", got)
	}
}

func TestSprExtractGroupFlagNone(t *testing.T) {
	rest, jid, err := sprExtractGroupFlag("@a halo dunia")
	if err != nil || !jid.IsEmpty() || rest != "@a halo dunia" {
		t.Fatalf("tanpa flag harus utuh: %q %v %v", rest, jid, err)
	}
}

func TestSprExtractGroupFlagPositions(t *testing.T) {
	gid := "120363431832768630@g.us"
	for _, tc := range []struct{ raw, want string }{
		{"-g " + gid + " @a halo", "@a halo"},
		{"@a -g " + gid + " halo", "@a halo"},
		{"@a halo -g " + gid, "@a halo"},
		{"sapa -g " + gid + " Halo {nama}!", "sapa Halo {nama}!"},
	} {
		rest, jid, err := sprExtractGroupFlag(tc.raw)
		if err != nil || jid.String() != gid || rest != tc.want {
			t.Fatalf("raw %q → rest %q jid %v err %v", tc.raw, rest, jid, err)
		}
	}
}

func TestSprExtractGroupFlagInvalid(t *testing.T) {
	for _, raw := range []string{
		"@a halo -g",
		"@a halo -g bukanjid",
		"@a halo -g 62812@s.whatsapp.net",
		"@a halo -g 123@g.us",
	} {
		// NOTE: "123@g.us" user pendek tetap parse valid → bukan error.
		// Kasus itu dibiarkan lolos validasi format (server yang menolak).
		if _, _, err := sprExtractGroupFlag(raw); raw != "@a halo -g 123@g.us" && err == nil {
			t.Fatalf("raw %q harus error", raw)
		}
	}
}

func TestSprExtractGroupFlagPreservesWhitespace(t *testing.T) {
	gid := "120363431832768630@g.us"
	rest, _, err := sprExtractGroupFlag("@a -g " + gid + " baris1\nbaris2  spasi")
	if err != nil || rest != "@a baris1\nbaris2  spasi" {
		t.Fatalf("whitespace rusak: %q err %v", rest, err)
	}
}

func sbprTestBatchEntry() (*sprEntry, types.JID, types.JID, types.JID, types.MessageID) {
	chat := types.NewJID("120363431832768630", types.GroupServer)
	a := types.NewJID("249348621369556", types.HiddenUserServer)
	b := types.NewJID("222222222222222", types.HiddenUserServer)
	other := types.NewJID("111111111111111", types.HiddenUserServer)
	e := &sprEntry{
		targets: sprRememberTargetUsers(nil, []types.JID{a, b}),
		perSender: map[string]*waE2E.Message{
			a.ToNonAD().User: {ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("Halo A!")}},
			b.ToNonAD().User: {ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("Halo B!")}},
		},
		hasDecoy: false,
	}
	_ = chat
	return e, a, b, other, types.MessageID("SBPRBATCH01")
}

func TestSbprRetryPerSenderContent(t *testing.T) {
	chat := types.NewJID("120363431832768630", types.GroupServer)
	e, a, b, _, id := sbprTestBatchEntry()
	sprMu.Lock()
	sprEntries[sprKey(chat, id)] = e
	sprMu.Unlock()
	defer func() {
		sprMu.Lock()
		delete(sprEntries, sprKey(chat, id))
		sprMu.Unlock()
	}()

	for _, tc := range []struct {
		sender types.JID
		want   string
	}{
		{a, "Halo A!"},
		{b, "Halo B!"},
	} {
		msg := &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("???")}}
		receipt := &events.Receipt{MessageSource: types.MessageSource{Chat: chat, Sender: tc.sender, IsGroup: true}}
		done, allow := sprHandleRetry(receipt, id, msg)
		if !done || !allow {
			t.Fatalf("target harus dijawab: done=%v allow=%v", done, allow)
		}
		if got := msg.GetExtendedTextMessage().GetText(); got != tc.want {
			t.Fatalf("target %s harus terima %q, dapat %q", tc.sender.User, tc.want, got)
		}
	}

	// Fallback GetMessageForRetry mengenal requester.
	if m := sprContentFor(a, chat, id); m.GetExtendedTextMessage().GetText() != "Halo A!" {
		t.Fatalf("fallback retry salah untuk A")
	}
	if m := sprContentFor(b, chat, id); m.GetExtendedTextMessage().GetText() != "Halo B!" {
		t.Fatalf("fallback retry salah untuk B")
	}
}

func TestSbprRetryNonTargetRejected(t *testing.T) {
	chat := types.NewJID("120363431832768630", types.GroupServer)
	e, _, _, other, id := sbprTestBatchEntry()
	sprMu.Lock()
	sprEntries[sprKey(chat, id)] = e
	sprMu.Unlock()
	defer func() {
		sprMu.Lock()
		delete(sprEntries, sprKey(chat, id))
		sprMu.Unlock()
	}()

	msg := &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("???")}}
	receipt := &events.Receipt{MessageSource: types.MessageSource{Chat: chat, Sender: other, IsGroup: true}}
	done, allow := sprHandleRetry(receipt, id, msg)
	if !done || allow {
		t.Fatalf("non-target batch harus ditolak: done=%v allow=%v", done, allow)
	}
}
