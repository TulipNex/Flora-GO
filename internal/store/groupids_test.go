package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestGroupIDsStable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "group.db")
	if err := InitGroupDB(path); err != nil {
		t.Fatal(err)
	}
	defer Groups().db.Close()
	defer func() { _ = os.Remove(path) }()

	g := Groups()
	ctx := context.Background()

	seq1, err := g.UpsertGroupID(ctx, "a@g.us", "Grup A")
	if err != nil {
		t.Fatal(err)
	}
	seq2, err := g.UpsertGroupID(ctx, "b@g.us", "Grup B")
	if err != nil {
		t.Fatal(err)
	}
	if seq2 <= seq1 {
		t.Fatalf("seq not increasing: %d %d", seq1, seq2)
	}
	// Re-upsert keeps seq.
	seq1b, err := g.UpsertGroupID(ctx, "a@g.us", "Grup A2")
	if err != nil {
		t.Fatal(err)
	}
	if seq1b != seq1 {
		t.Fatalf("seq changed on re-upsert: %d vs %d", seq1b, seq1)
	}
	entry, err := g.GroupIDBySeq(ctx, seq1)
	if err != nil || entry == nil || entry.ChatJID != "a@g.us" {
		t.Fatalf("lookup by seq failed: %+v %v", entry, err)
	}
	// absent marking
	if err := g.MarkGroupIDsAbsent(ctx, []string{"b@g.us"}); err != nil {
		t.Fatal(err)
	}
	ids, err := g.ListGroupIDs(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0].ChatJID != "b@g.us" {
		t.Fatalf("present filter wrong: %+v", ids)
	}
}
