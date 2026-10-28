package userdir

import (
	"testing"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/types"
)

func TestParticipantIdentity(t *testing.T) {
	pt := types.GroupParticipant{
		JID:         types.NewJID("123", types.HiddenUserServer),
		LID:         types.NewJID("123", types.HiddenUserServer),
		PhoneNumber: types.NewJID("6281", types.DefaultUserServer),
		Username:    "alice",
	}
	id := participantIdentity(pt)
	if id.LID != "123" || id.PN != "6281" {
		t.Fatalf("bad identity: %+v", id)
	}
	if id.JID == "" || id.Username != "alice" {
		t.Fatalf("bad jid/username: %+v", id)
	}
	_ = whatsmeow.ErrClientIsNil
}
