package plugins

import (
	"context"
	"strings"
	"testing"

	"github.com/polymorfa/hypermeow/types"
)

func TestParseIDArg(t *testing.T) {
	if jid, err := parseIDArg("6285324023198"); err != nil || jid.Server != types.DefaultUserServer || jid.User != "6285324023198" {
		t.Errorf("nomor polos: got %v err=%v", jid, err)
	}
	if jid, err := parseIDArg("221203734204662@lid"); err != nil || jid.Server != types.HiddenUserServer {
		t.Errorf("JID lid: got %v err=%v", jid, err)
	}
	if _, err := parseIDArg("   "); err == nil {
		t.Error("input kosong harus error")
	}
}

func TestIDReport(t *testing.T) {
	ctx := context.Background()
	// Tanpa client: PN tetap dikenali langsung, LID tak bisa resolve.
	out := idReport(ctx, nil, "Input", types.NewJID("6285324023198", types.DefaultUserServer), types.JID{})
	for _, want := range []string{"*Identitas Input*", "PN      : 6285324023198", "Kanonik : 6285324023198"} {
		if !strings.Contains(out, want) {
			t.Errorf("report tidak memuat %q:\n%s", want, out)
		}
	}
}
