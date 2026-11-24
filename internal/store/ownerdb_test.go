package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/polymorfa/hypermeow/types"
)

func TestOwnerDBCacheAndSync(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "group_owner_test.db")
	db, err := openGroupDBFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	g := newGroupDB(db)
	ctx := context.Background()

	// 1. Test Bot Mode Cache
	mode, err := g.GetBotMode(ctx)
	if err != nil || mode != BotModePublic {
		t.Fatalf("expected initial mode public, got %v (%v)", mode, err)
	}
	if self, err := g.IsSelfMode(ctx); err != nil || self {
		t.Fatalf("expected initial self mode false, got %v (%v)", self, err)
	}

	if err := g.SetBotMode(ctx, BotModeSelf); err != nil {
		t.Fatal(err)
	}
	if self, err := g.IsSelfMode(ctx); err != nil || !self {
		t.Fatalf("expected self mode true after update, got %v (%v)", self, err)
	}

	// Buat instance baru untuk pastikan persistent ke DB
	g2 := newGroupDB(db)
	if self, err := g2.IsSelfMode(ctx); err != nil || !self {
		t.Fatalf("expected self mode true from fresh DB instance, got %v (%v)", self, err)
	}

	// 2. Test Ban User Cache
	userJID := types.NewJID("628123456789", types.DefaultUserServer)
	if banned, err := g.IsUserBanned(ctx, userJID, types.EmptyJID); err != nil || banned {
		t.Fatalf("expected user not banned, got %v (%v)", banned, err)
	}

	if err := g.BanUser(ctx, userJID); err != nil {
		t.Fatal(err)
	}
	if banned, err := g.IsUserBanned(ctx, userJID, types.EmptyJID); err != nil || !banned {
		t.Fatalf("expected user banned, got %v (%v)", banned, err)
	}

	// Cek kecocokan via user_part (duplikasi LID / PN)
	userLID := types.NewJID("628123456789", "lid")
	if banned, err := g.IsUserBanned(ctx, userLID, types.EmptyJID); err != nil || !banned {
		t.Fatalf("expected user banned via user_part, got %v (%v)", banned, err)
	}

	// Unban
	ok, err := g.UnbanUser(ctx, userJID)
	if err != nil || !ok {
		t.Fatalf("expected unban true, got %v (%v)", ok, err)
	}
	if banned, err := g.IsUserBanned(ctx, userJID, types.EmptyJID); err != nil || banned {
		t.Fatalf("expected user not banned after unban, got %v (%v)", banned, err)
	}

	// 3. Test Ban Group Cache
	groupChat := "120363000000000000@g.us"
	if banned, err := g.IsGroupBanned(ctx, groupChat); err != nil || banned {
		t.Fatalf("expected group not banned, got %v (%v)", banned, err)
	}

	if err := g.BanGroup(ctx, groupChat); err != nil {
		t.Fatal(err)
	}
	if banned, err := g.IsGroupBanned(ctx, groupChat); err != nil || !banned {
		t.Fatalf("expected group banned, got %v (%v)", banned, err)
	}

	ok, err = g.UnbanGroup(ctx, groupChat)
	if err != nil || !ok {
		t.Fatalf("expected unban group true, got %v (%v)", ok, err)
	}
	if banned, err := g.IsGroupBanned(ctx, groupChat); err != nil || banned {
		t.Fatalf("expected group not banned after unban, got %v (%v)", banned, err)
	}

	// 4. Test Trusted User Cache
	if trusted, err := g.IsUserTrusted(ctx, userJID, types.EmptyJID); err != nil || trusted {
		t.Fatalf("expected user not trusted, got %v (%v)", trusted, err)
	}
	if err := g.TrustUser(ctx, userJID); err != nil {
		t.Fatal(err)
	}
	if trusted, err := g.IsUserTrusted(ctx, userJID, types.EmptyJID); err != nil || !trusted {
		t.Fatalf("expected user trusted, got %v (%v)", trusted, err)
	}
	ok, err = g.UntrustUser(ctx, userJID)
	if err != nil || !ok {
		t.Fatalf("expected untrust true, got %v (%v)", ok, err)
	}
	if trusted, err := g.IsUserTrusted(ctx, userJID, types.EmptyJID); err != nil || trusted {
		t.Fatalf("expected user not trusted after untrust, got %v (%v)", trusted, err)
	}
}
