package plugins

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestEventTestBuildMessage(t *testing.T) {
	title := "Uji Acara Spesial"
	thumb := []byte("fake-jpeg-data")
	now := time.Now()

	msg := eventTestBuildMessage(title, thumb, now)
	if msg == nil {
		t.Fatal("Message nil")
	}
	ev := msg.GetEventInviteMessage()
	if ev == nil {
		t.Fatal("EventInviteMessage nil")
	}

	if ev.GetEventTitle() != title {
		t.Fatalf("EventTitle = %q, mau %q", ev.GetEventTitle(), title)
	}
	if string(ev.GetJPEGThumbnail()) != string(thumb) {
		t.Fatalf("JPEGThumbnail berbeda")
	}
	if ev.GetStartTime() != now.Unix() {
		t.Fatalf("StartTime = %d, mau %d", ev.GetStartTime(), now.Unix())
	}
	// Pastikan EventID adalah UUID valid
	if _, err := uuid.Parse(ev.GetEventID()); err != nil {
		t.Fatalf("EventID %q bukan UUID valid: %v", ev.GetEventID(), err)
	}
}

func TestEventTestExtra(t *testing.T) {
	extra := eventTestExtra()
	if extra.AdditionalNodes == nil || len(*extra.AdditionalNodes) == 0 {
		t.Fatal("AdditionalNodes kosong")
	}
	nodes := *extra.AdditionalNodes
	found := false
	for _, n := range nodes {
		if n.Tag == "meta" && n.Attrs != nil && n.Attrs["context_source"] == "event_invitation" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("Node <meta context_source=\"event_invitation\"/> tidak ditemukan di AdditionalNodes: %+v", nodes)
	}
}
