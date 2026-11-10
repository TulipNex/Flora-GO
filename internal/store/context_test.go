package store

import (
	"testing"

	"github.com/polymorfa/hypermeow/types"
	"github.com/polymorfa/hypermeow/types/events"
)

func TestSenderPushName(t *testing.T) {
	// 1. With pushname
	mctx1 := &MessageContext{
		Sender: types.NewJID("62812345", types.DefaultUserServer),
		RawEvt: &events.Message{
			Info: types.MessageInfo{
				PushName: "Ahmad",
			},
		},
	}
	if got := mctx1.SenderPushName(); got != "Ahmad" {
		t.Errorf("got %q, want %q", got, "Ahmad")
	}

	// 2. Empty pushname, fallback to Sender.User
	mctx2 := &MessageContext{
		Sender: types.NewJID("62812345", types.DefaultUserServer),
		RawEvt: &events.Message{
			Info: types.MessageInfo{
				PushName: "   ",
			},
		},
	}
	if got := mctx2.SenderPushName(); got != "62812345" {
		t.Errorf("got %q, want %q", got, "62812345")
	}

	// 3. Nil RawEvt, fallback to Sender.User
	mctx3 := &MessageContext{
		Sender: types.NewJID("62899999", types.DefaultUserServer),
	}
	if got := mctx3.SenderPushName(); got != "62899999" {
		t.Errorf("got %q, want %q", got, "62899999")
	}
}
