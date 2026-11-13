package identity

import (
	"context"
	"testing"

	"wabot-go/internal/config"

	"github.com/polymorfa/hypermeow/types"
)

// TestMatchOwnerNumber memastikan kunci nomor owner yang dipakai command
// keuangan & tool AI selalu sama: digit user-part, awalan 0 dianggap 62, dan
// nomor yang dikembalikan adalah bentuk dari config (bukan input user).
func TestMatchOwnerNumber(t *testing.T) {
	old := config.OwnerNumbers
	config.OwnerNumbers = []string{"628123456789", "628999"}
	defer func() { config.OwnerNumbers = old }()

	cases := []struct{ in, want string }{
		{"628123456789", "628123456789"},
		{"628123456789@s.whatsapp.net", "628123456789"},
		{"628123456789:12@s.whatsapp.net", "628123456789"},
		{"08123456789", "628123456789"},
		{"+62 812-3456-789", "628123456789"},
		{"628999@lid", "628999"},
		{"628000111222", ""},
		{"", ""},
		{"bukan-nomor", ""},
	}
	for _, c := range cases {
		if got := MatchOwnerNumber(c.in); got != c.want {
			t.Errorf("MatchOwnerNumber(%q) = %q, mau %q", c.in, got, c.want)
		}
	}
}

// TestOwnerNumberAlt memastikan JID alternatif (format seberang) ikut dicek —
// owner yang muncul sebagai @lid saat nomornya tersimpan sebagai PN tetap
// dikenali, dan nomor yang dikembalikan adalah nomor owner dari config.
func TestOwnerNumberAlt(t *testing.T) {
	old := config.OwnerNumbers
	config.OwnerNumbers = []string{"628123456789"}
	defer func() { config.OwnerNumbers = old }()

	lid := types.NewJID("221203734204662", types.HiddenUserServer)
	pn := types.NewJID("628123456789", types.DefaultUserServer)

	num, ok := OwnerNumber(context.Background(), nil, lid, pn)
	if !ok || num != "628123456789" {
		t.Errorf("OwnerNumber(lid, alt=pn) = %q, %v; mau 628123456789", num, ok)
	}
	if _, ok := OwnerNumber(context.Background(), nil, lid, types.JID{}); ok {
		t.Error("OwnerNumber(lid tanpa alt) harus false (fail-closed)")
	}
}
