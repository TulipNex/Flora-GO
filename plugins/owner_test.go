package plugins

import (
	"testing"

	"wabot-go/internal/config"
	"wabot-go/internal/store"

	"github.com/polymorfa/hypermeow/types"
	"github.com/polymorfa/hypermeow/types/events"
)

// ownerPN adalah nomor owner fiktif untuk tes; LID di bawah dipetakan
// kepadanya lewat SenderAlt (skenario DM mode-LID).
const (
	ownerPN  = "6285324023198"
	ownerLID = "221203734204662"
)

// withOwners mengganti daftar owner selama tes lalu mengembalikannya.
func withOwners(t *testing.T, nums ...string) {
	t.Helper()
	old := config.OwnerNumbers
	config.OwnerNumbers = nums
	t.Cleanup(func() { config.OwnerNumbers = old })
}

func TestIsJIDOwner(t *testing.T) {
	withOwners(t, ownerPN)

	cases := []struct {
		name string
		jid  types.JID
		want bool
	}{
		{"PN owner", types.NewJID(ownerPN, types.DefaultUserServer), true},
		{"PN owner + device (ToNonAD)", types.NewADJID(ownerPN, 0, 3), true},
		{"LID bukan owner", types.NewJID(ownerLID, types.HiddenUserServer), false},
		{"PN lain", types.NewJID("6281111111111", types.DefaultUserServer), false},
		{"kosong", types.JID{}, false},
	}
	for _, tc := range cases {
		if got := isJIDOwner(tc.jid); got != tc.want {
			t.Errorf("%s: isJIDOwner(%s) = %v, mau %v", tc.name, tc.jid.String(), got, tc.want)
		}
	}
}

// TestIsOwnerSenderForms memastikan owner dikenali dari PN langsung, dari
// SenderAlt (LID→PN bawaan event), dan tetap menolak LID tanpa alt/client.
func TestIsOwnerSenderForms(t *testing.T) {
	withOwners(t, ownerPN)

	// 1. Sender PN owner.
	pn := &store.MessageContext{Sender: types.NewJID(ownerPN, types.DefaultUserServer)}
	if !IsOwner(pn) {
		t.Error("Sender PN owner harus dikenali")
	}

	// 2. Sender LID + SenderAlt PN owner (DM mode-LID).
	alt := &store.MessageContext{
		Sender: types.NewJID(ownerLID, types.HiddenUserServer),
		RawEvt: &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{
			Sender:    types.NewJID(ownerLID, types.HiddenUserServer),
			SenderAlt: types.NewJID(ownerPN, types.DefaultUserServer),
		}}},
	}
	if !IsOwner(alt) {
		t.Error("owner via SenderAlt harus dikenali")
	}

	// 3. LID tanpa alt & tanpa client → bukan owner (fail-closed).
	noAlt := &store.MessageContext{Sender: types.NewJID(ownerLID, types.HiddenUserServer)}
	if IsOwner(noAlt) {
		t.Error("LID tanpa SenderAlt/client tidak boleh dianggap owner")
	}

	// 4. Sender bukan owner sama sekali.
	other := &store.MessageContext{Sender: types.NewJID("6281111111111", types.DefaultUserServer)}
	if IsOwner(other) {
		t.Error("pengirim bukan owner tidak boleh lolos")
	}
}

func TestResolveSenderPN(t *testing.T) {
	withOwners(t, ownerPN)
	// Tanpa client: tidak bisa resolve, harus ok=false (bukan panic).
	if _, ok := resolveSenderPN(&store.MessageContext{
		Sender: types.NewJID(ownerLID, types.HiddenUserServer),
	}); ok {
		t.Error("resolveSenderPN tanpa client harus ok=false")
	}
	// Sender PN: bukan LID → ok=false.
	if _, ok := resolveSenderPN(&store.MessageContext{
		Sender: types.NewJID(ownerPN, types.DefaultUserServer),
	}); ok {
		t.Error("resolveSenderPN untuk PN harus ok=false")
	}
}

// TestBackupAcceptsLIDDM melindungi regresi gerbang DM .backup: DM mode-LID
// (server "lid") harus DIANGGAP chat pribadi, bukan grup.
func TestBackupAcceptsLIDDM(t *testing.T) {
	dm := &store.MessageContext{Chat: types.NewJID(ownerLID, types.HiddenUserServer)}
	if dm.IsGroup() {
		t.Fatal("DM @lid tidak boleh dianggap grup")
	}
	grp := &store.MessageContext{Chat: types.NewJID("120363431832768630", types.GroupServer)}
	if !grp.IsGroup() {
		t.Fatal("chat @g.us harus dianggap grup")
	}
}
