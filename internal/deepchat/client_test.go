package deepchat

import (
	"context"
	"testing"
)

func TestDeepChatSmoke(t *testing.T) {
	c := New()
	ctx := context.Background()

	// 1. Test chat
	res, err := c.Chat(ctx, "Halo, katakan 'oke' saja", nil)
	if err != nil {
		t.Fatalf("Chat error: %v", err)
	}
	if res.Reply == "" {
		t.Fatalf("Expected non-empty reply")
	}
	t.Logf("Chat reply: %s (used: %d, remaining: %d)", res.Reply, res.CreditsUsed, res.CreditsRemaining)

	// 2. Test humanize
	hum, err := c.Humanize(ctx, "This is artificial intelligence test.", "medium")
	if err != nil {
		t.Fatalf("Humanize error: %v", err)
	}
	if hum.HumanizedText == "" {
		t.Fatalf("Expected non-empty humanized text")
	}
	t.Logf("Humanize text: %s", hum.HumanizedText)
}
