package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestGroupMembersDB(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "group.db")
	if err := InitGroupDB(path); err != nil {
		t.Fatal(err)
	}
	defer Groups().db.Close()
	defer func() { _ = os.Remove(path) }()
	g := Groups()
	ctx := context.Background()
	chat := "120@g.us"

	if err := g.UpsertGroupMember(ctx, GroupMember{ChatJID: chat, UserID: 1, PN: "6281", PushName: "A", IsAdmin: true, Present: true}); err != nil {
		t.Fatal(err)
	}
	if err := g.UpsertGroupMember(ctx, GroupMember{ChatJID: chat, UserID: 2, LID: "999", PushName: "B", Present: true}); err != nil {
		t.Fatal(err)
	}
	// Mark user 2 absent.
	if err := g.MarkGroupMembersAbsent(ctx, chat, []int64{1}, 1000); err != nil {
		t.Fatal(err)
	}
	members, err := g.ListGroupMembers(ctx, chat, false, 10, 0)
	if err != nil || len(members) != 2 {
		t.Fatalf("list all: %v %d", err, len(members))
	}
	present, _ := g.CountGroupMembers(ctx, chat, true)
	if present != 1 {
		t.Fatalf("expected 1 present, got %d", present)
	}
	gm, err := g.FindGroupMember(ctx, chat, "6281")
	if err != nil || gm == nil || gm.UserID != 1 {
		t.Fatalf("find by pn: %v", err)
	}
	gm2, err := g.FindGroupMember(ctx, chat, "999")
	if err != nil || gm2 == nil || gm2.Present {
		t.Fatalf("absent flag not set: %+v %v", gm2, err)
	}
}
