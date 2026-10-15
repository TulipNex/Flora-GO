package store

import (
	"testing"

	whatsmeow "github.com/polymorfa/hypermeow"
	hstore "github.com/polymorfa/hypermeow/store"
	"github.com/polymorfa/hypermeow/types"
)

// clientWithBotIDs membuat *whatsmeow.Client minimal dengan identitas bot
// (Store.ID = nomor telepon, Store.LID = LID) untuk menguji IsBotJID tanpa
// koneksi/log.
func clientWithBotIDs(id, lid string) *whatsmeow.Client {
	dev := &hstore.Device{}
	if id != "" {
		j := types.NewJID(id, types.DefaultUserServer)
		dev.ID = &j
	}
	if lid != "" {
		dev.LID = types.NewJID(lid, types.HiddenUserServer)
	}
	return &whatsmeow.Client{Store: dev}
}

// TestIsBotJID memastikan deteksi JID bot sendiri mengenali DUA identitas
// (nomor telepon/PN dan LID), toleran suffix device (ToNonAD), dan aman
// terhadap client/Store/JID kosong.
func TestIsBotJID(t *testing.T) {
	client := clientWithBotIDs("628111", "99887766")

	// JID bentuk AD (dengan device), mis. hasil ParseJID("...:5@...").
	adWithDevice, err := types.ParseJID("628111:5@s.whatsapp.net")
	if err != nil {
		t.Fatalf("ParseJID AD: %v", err)
	}

	cases := []struct {
		name   string
		client *whatsmeow.Client
		jid    types.JID
		want   bool
	}{
		{"match nomor telepon", client, types.NewJID("628111", types.DefaultUserServer), true},
		{"match LID", client, types.NewJID("99887766", types.HiddenUserServer), true},
		{"match PN dengan suffix device", client, adWithDevice, true},
		{"bukan bot", client, types.NewJID("628999", types.DefaultUserServer), false},
		{"JID kosong", client, types.JID{}, false},
		{"client nil", nil, types.NewJID("628111", types.DefaultUserServer), false},
		{"store nil", &whatsmeow.Client{}, types.NewJID("628111", types.DefaultUserServer), false},
	}
	for _, c := range cases {
		if got := IsBotJID(c.client, c.jid); got != c.want {
			t.Errorf("%s: IsBotJID = %v, mau %v", c.name, got, c.want)
		}
	}

	// Bot belum punya LID (belum ada LID): hanya PN yang cocok.
	noLID := clientWithBotIDs("628222", "")
	if !IsBotJID(noLID, types.NewJID("628222", types.DefaultUserServer)) {
		t.Errorf("PN harus cocok walau LID kosong")
	}
	if IsBotJID(noLID, types.NewJID("99887766", types.HiddenUserServer)) {
		t.Errorf("LID asing tidak boleh cocok saat bot belum punya LID")
	}
}
