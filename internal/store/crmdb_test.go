package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/polymorfa/hypermeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

// TestArchiveQueuePersists memverifikasi penulisan arsip lewat antrian
// (ArchiveIncomingMessage) benar-benar tersimpan oleh worker, dan bisa
// dibaca kembali utuh. DB temporer — TIDAK menyentuh data/.
func TestArchiveQueuePersists(t *testing.T) {
	dir := t.TempDir()
	if err := InitCRMDB(filepath.Join(dir, "crm.db")); err != nil {
		t.Fatalf("InitCRMDB: %v", err)
	}
	defer CRM().db.Close()

	msg := &waE2E.Message{Conversation: proto.String("halo arsip")}
	ArchiveIncomingMessage("chat@s.whatsapp.net", "MSG1", "user@s.whatsapp.net", 1700000000, msg)

	// Worker asinkron: tunggu sampai baris muncul (tenggat longgar).
	deadline := time.Now().Add(3 * time.Second)
	for CRM().Count() != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("arsip tak tersimpan (count=%d)", CRM().Count())
		}
		time.Sleep(10 * time.Millisecond)
	}

	row, err := CRM().LoadByID("MSG1", "chat@s.whatsapp.net")
	if err != nil {
		t.Fatalf("LoadByID: %v", err)
	}
	got, err := row.Message()
	if err != nil {
		t.Fatalf("decode pesan: %v", err)
	}
	if got.GetConversation() != "halo arsip" {
		t.Fatalf("conversation = %q, mau %q", got.GetConversation(), "halo arsip")
	}
	if row.Sender != "user@s.whatsapp.net" {
		t.Fatalf("sender = %q", row.Sender)
	}
}
