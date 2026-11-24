package plugins

import (
	"strings"
	"testing"
)

func TestRCHUsageFormat(t *testing.T) {
	help := rchHelpBlock()
	if !strings.Contains(help, "*Reaksi pesan channel*") {
		t.Fatalf("rchHelpBlock title mismatch: %q", help)
	}
	if !strings.Contains(help, "`Rch`\nFormat : .rch <link pesan> <emoji>") {
		t.Fatalf("rchHelpBlock format mismatch: %q", help)
	}
	if !strings.Contains(help, "link  : link pesan") || !strings.Contains(help, "emoji : maksimal 5") {
		t.Fatalf("rchHelpBlock params mismatch: %q", help)
	}
	if !strings.Contains(help, "`Sesi`\nFormat :\n- .rch login <refresh token>\n- .rch status") {
		t.Fatalf("rchHelpBlock sesi mismatch: %q", help)
	}
}
