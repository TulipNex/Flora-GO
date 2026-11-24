package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestUsersDBUpsertAndMerge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "users.db")
	if err := InitUsersDB(path); err != nil {
		t.Fatal(err)
	}
	defer Users().db.Close()
	defer func() { _ = os.Remove(path) }()
	u := Users()
	ctx := context.Background()

	// Insert via LID only.
	id1, err := u.UpsertUserIdentity(ctx, UserIdentity{LID: "1001", PushName: "Alice"})
	if err != nil {
		t.Fatal(err)
	}
	if id1 == 0 {
		t.Fatal("id 0")
	}
	// Same person via PN should merge, not create new row.
	id2, err := u.UpsertUserIdentity(ctx, UserIdentity{PN: "6281", LID: "1001", PushName: "Alice W"})
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id1 {
		t.Fatalf("expected merge, got new id %d vs %d", id2, id1)
	}
	n, err := u.CountUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 user, got %d", n)
	}
	rec, err := u.GetUser(ctx, id1)
	if err != nil {
		t.Fatal(err)
	}
	if rec.LID != "1001" || rec.PN != "6281" {
		t.Fatalf("merge missing keys: %+v", rec)
	}
	if rec.PushName != "Alice W" {
		t.Fatalf("pushname not updated: %s", rec.PushName)
	}
}

func TestUsersDBFindAndPhoto(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "users.db")
	if err := InitUsersDB(path); err != nil {
		t.Fatal(err)
	}
	defer Users().db.Close()
	u := Users()
	ctx := context.Background()
	id, _ := u.UpsertUserIdentity(ctx, UserIdentity{LID: "2002", PN: "6282", PushName: "Bob"})
	if err := u.SetUserPhoto(ctx, id, "ok", "pp1", "abc", "data/pp/abc.jpg", 123, 1000); err != nil {
		t.Fatal(err)
	}
	r, err := u.FindUserByLID(ctx, "2002")
	if err != nil || r == nil {
		t.Fatalf("find by lid: %v", err)
	}
	if r.PPStatus != "ok" || r.PPSha != "abc" {
		t.Fatalf("photo meta wrong: %+v", r)
	}
	if err := u.ClearUserPhoto(ctx, id); err != nil {
		t.Fatal(err)
	}
	r2, _ := u.GetUser(ctx, id)
	if r2.PPStatus != "" || r2.PPPath != "" {
		t.Fatalf("clear failed: %+v", r2)
	}
}

func TestUsersDBSearch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "users.db")
	if err := InitUsersDB(path); err != nil {
		t.Fatal(err)
	}
	defer Users().db.Close()
	u := Users()
	ctx := context.Background()
	_, _ = u.UpsertUserIdentity(ctx, UserIdentity{LID: "3003", PN: "6283", PushName: "Charlie"})
	res, err := u.SearchUsers(ctx, "char", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("expected 1 search hit, got %d", len(res))
	}
}

func TestUsersDBUpsertBatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "users_batch.db")
	if err := InitUsersDB(path); err != nil {
		t.Fatal(err)
	}
	defer Users().db.Close()
	u := Users()
	ctx := context.Background()

	batch := []UserIdentity{
		{LID: "4001", PN: "62841", PushName: "David"},
		{LID: "4002", PN: "62842", PushName: "Eva"},
		{LID: "4003", PN: "62843", PushName: "Frank"},
	}
	results, err := u.UpsertUserIdentitiesBatch(ctx, batch)
	if err != nil {
		t.Fatalf("batch upsert failed: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	for i, r := range results {
		if !r.IsNew {
			t.Errorf("item %d expected IsNew=true, got false", i)
		}
		if r.ID == 0 {
			t.Errorf("item %d expected ID > 0", i)
		}
	}

	// Batch update existing
	batchUpdate := []UserIdentity{
		{LID: "4001", PushName: "David Updated"},
		{LID: "4004", PN: "62844", PushName: "Grace"},
	}
	res2, err := u.UpsertUserIdentitiesBatch(ctx, batchUpdate)
	if err != nil {
		t.Fatalf("batch update failed: %v", err)
	}
	if len(res2) != 2 {
		t.Fatalf("expected 2 results, got %d", len(res2))
	}
	if res2[0].IsNew {
		t.Errorf("David should be updated, not new")
	}
	if !res2[1].IsNew {
		t.Errorf("Grace should be new")
	}

	total, _ := u.CountUsers(ctx)
	if total != 4 {
		t.Fatalf("expected 4 total users, got %d", total)
	}
}
