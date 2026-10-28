package plugins

import (
	"testing"

	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types"
	"github.com/polymorfa/hypermeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestParseSPRBasic(t *testing.T) {
	p := parseSPRArgs("@12345 halo dunia")
	if p.isDebug || p.hasInclude || p.hasExclude || p.hasDecoy {
		t.Fatalf("flag tak terduga: %+v", p)
	}
	if len(p.includes) != 1 || p.includes[0] != "@12345" {
		t.Fatalf("includes salah: %+v", p)
	}
	if p.message != "halo dunia" {
		t.Fatalf("pesan salah: %q", p.message)
	}
}

func TestParseSPRDecoy(t *testing.T) {
	p := parseSPRArgs("@12345 pesan asli | pesan decoy")
	if !p.hasDecoy {
		t.Fatalf("decoy tak terdeteksi: %+v", p)
	}
	if p.message != "pesan asli" || p.decoy != "pesan decoy" {
		t.Fatalf("split decoy salah: %q | %q", p.message, p.decoy)
	}
}

func TestParseSPRDecoyFirstPipeOnly(t *testing.T) {
	p := parseSPRArgs("@a x | y | z")
	if !p.hasDecoy || p.message != "x" || p.decoy != "y | z" {
		t.Fatalf("split harus di | pertama: %+v", p)
	}
}

func TestParseSPRIncludeExclude(t *testing.T) {
	p := parseSPRArgs("-i @t1 @t2 halo")
	if !p.hasInclude || p.hasExclude {
		t.Fatalf("flag salah: %+v", p)
	}
	if len(p.includes) != 2 || p.message != "halo" {
		t.Fatalf("parse -i salah: %+v", p)
	}

	p = parseSPRArgs("-e @ex1 @ex2 halo semua")
	if !p.hasExclude || p.hasInclude || len(p.excludes) != 2 || p.message != "halo semua" {
		t.Fatalf("parse -e salah: %+v", p)
	}

	p = parseSPRArgs("-i @t1 -e @ex1 halo")
	if !p.hasInclude || !p.hasExclude {
		t.Fatalf("flag kombinasi salah: %+v", p)
	}
	if len(p.includes) != 1 || p.includes[0] != "@t1" || len(p.excludes) != 1 || p.excludes[0] != "@ex1" || p.message != "halo" {
		t.Fatalf("parse kombinasi salah: %+v", p)
	}
}

func TestParseSPRMeAndNumbers(t *testing.T) {
	p := parseSPRArgs("-i @me 6281234567890 halo")
	if len(p.includes) != 2 || p.includes[0] != "@me" || p.includes[1] != "6281234567890" {
		t.Fatalf("parse @me/nomor salah: %+v", p)
	}
}

func TestParseSPRReplyOnly(t *testing.T) {
	p := parseSPRArgs("halo ini reply")
	if !p.quotedOnly || len(p.includes) != 0 || p.message != "halo ini reply" {
		t.Fatalf("mode reply salah: %+v", p)
	}
}

func TestParseSPRDebug(t *testing.T) {
	if p := parseSPRArgs("debug"); !p.isDebug {
		t.Fatalf("debug tak terdeteksi: %+v", p)
	}
	if p := parseSPRArgs("DEBUG"); !p.isDebug {
		t.Fatalf("debug case-insensitive gagal: %+v", p)
	}
	if p := parseSPRArgs("debug x"); p.isDebug {
		t.Fatalf("debug dengan pesan tak boleh jadi debug: %+v", p)
	}
}

func TestParseSPRMultilinePreserved(t *testing.T) {
	p := parseSPRArgs("@a baris1\nbaris2  spasi-ganda")
	if p.message != "baris1\nbaris2  spasi-ganda" {
		t.Fatalf("whitespace pesan rusak: %q", p.message)
	}
}

func TestSprIsTargetToken(t *testing.T) {
	yes := []string{"@123", "@me", "62812345678", "+62812345678", "123@lid", "123@s.whatsapp.net"}
	for _, tok := range yes {
		if !sprIsTargetToken(tok) {
			t.Fatalf("%q harusnya target", tok)
		}
	}
	no := []string{"halo", "halo|decoy", "1000", "-i", ""}
	for _, tok := range no {
		if sprIsTargetToken(tok) {
			t.Fatalf("%q tak boleh jadi target", tok)
		}
	}
}

func sprTestEntry() (*sprEntry, types.JID, types.JID) {
	target := types.NewJID("249348621369556", types.HiddenUserServer)
	other := types.NewJID("111111111111111", types.HiddenUserServer)
	e := &sprEntry{
		targets:  sprRememberTargetUsers(nil, []types.JID{target}),
		original: &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("asli")}},
		decoy:    &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("decoy")}},
		hasDecoy: true,
	}
	return e, target, other
}

func TestSprRetryNonTargetGetsDecoyWithoutSKDM(t *testing.T) {
	chat := types.NewJID("120363431832768630", types.GroupServer)
	e, _, other := sprTestEntry()
	id := types.MessageID("TESTMSG001")
	sprMu.Lock()
	sprEntries[sprKey(chat, id)] = e
	sprMu.Unlock()
	defer func() {
		sprMu.Lock()
		delete(sprEntries, sprKey(chat, id))
		sprMu.Unlock()
	}()

	msg := &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("asli")},
		SenderKeyDistributionMessage: &waE2E.SenderKeyDistributionMessage{
			GroupID: proto.String(chat.String()),
		},
	}
	receipt := &events.Receipt{MessageSource: types.MessageSource{Chat: chat, Sender: other, IsGroup: true}}
	done, allow := sprHandleRetry(receipt, id, msg)
	if !done || !allow {
		t.Fatalf("harus ditangani + diizinkan, dapat done=%v allow=%v", done, allow)
	}
	if got := msg.GetExtendedTextMessage().GetText(); got != "decoy" {
		t.Fatalf("non-target harus terima decoy, dapat %q", got)
	}
	if msg.SenderKeyDistributionMessage != nil {
		t.Fatalf("SKDM harus dicabut dari jawaban decoy (kalau tidak, penerima bisa dekripsi bubble asli)")
	}
}

func TestSprRetryTargetKeepsOriginalWithSKDM(t *testing.T) {
	chat := types.NewJID("120363431832768630", types.GroupServer)
	e, target, _ := sprTestEntry()
	id := types.MessageID("TESTMSG002")
	sprMu.Lock()
	sprEntries[sprKey(chat, id)] = e
	sprMu.Unlock()
	defer func() {
		sprMu.Lock()
		delete(sprEntries, sprKey(chat, id))
		sprMu.Unlock()
	}()

	msg := &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("asli")},
		SenderKeyDistributionMessage: &waE2E.SenderKeyDistributionMessage{
			GroupID: proto.String(chat.String()),
		},
	}
	receipt := &events.Receipt{MessageSource: types.MessageSource{Chat: chat, Sender: target, IsGroup: true}}
	done, allow := sprHandleRetry(receipt, id, msg)
	if !done || !allow {
		t.Fatalf("harus ditangani + diizinkan, dapat done=%v allow=%v", done, allow)
	}
	if got := msg.GetExtendedTextMessage().GetText(); got != "asli" {
		t.Fatalf("target harus terima isi asli, dapat %q", got)
	}
	if msg.SenderKeyDistributionMessage == nil {
		t.Fatalf("SKDM harus dipertahankan untuk target")
	}
}

func TestSprRetryUnknownMessageIgnored(t *testing.T) {
	chat := types.NewJID("120363431832768630", types.GroupServer)
	msg := &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("x")}}
	receipt := &events.Receipt{MessageSource: types.MessageSource{Chat: chat, Sender: types.NewJID("1", types.HiddenUserServer)}}
	done, _ := sprHandleRetry(receipt, types.MessageID("BUKAN-SPR"), msg)
	if done {
		t.Fatalf("pesan non-SPR tidak boleh disentuh")
	}
	if got := msg.GetExtendedTextMessage().GetText(); got != "x" {
		t.Fatalf("pesan non-SPR berubah: %q", got)
	}
}

func TestSprRetryNonTargetNoDecoyRejected(t *testing.T) {
	chat := types.NewJID("120363431832768630", types.GroupServer)
	e, _, other := sprTestEntry()
	e.hasDecoy = false
	e.decoy = nil
	id := types.MessageID("TESTMSG003")
	sprMu.Lock()
	sprEntries[sprKey(chat, id)] = e
	sprMu.Unlock()
	defer func() {
		sprMu.Lock()
		delete(sprEntries, sprKey(chat, id))
		sprMu.Unlock()
	}()

	msg := &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("asli")},
		SenderKeyDistributionMessage: &waE2E.SenderKeyDistributionMessage{
			GroupID: proto.String(chat.String()),
		},
	}
	receipt := &events.Receipt{MessageSource: types.MessageSource{Chat: chat, Sender: other, IsGroup: true}}
	done, allow := sprHandleRetry(receipt, id, msg)
	if !done || allow {
		t.Fatalf("retry non-target tanpa decoy harus ditolak, dapat done=%v allow=%v", done, allow)
	}
	if got := msg.GetExtendedTextMessage().GetText(); got != "asli" {
		t.Fatalf("cache untuk target jangan sampai kotor: %q", got)
	}
	if msg.SenderKeyDistributionMessage == nil {
		t.Fatalf("SKDM jangan sampai tercabut saat penolakan")
	}
}
