package plugins

import (
	"strings"
	"testing"
)

func TestOwnerAdminUsageFormats(t *testing.T) {
	ban := banUsage()
	if !strings.Contains(ban, "*Blokir user*") {
		t.Fatalf("banUsage title mismatch: %q", ban)
	}
	if !strings.Contains(ban, "`Ban`\nFormat :\n- .ban @user\n- .ban <nomor>") {
		t.Fatalf("banUsage format mismatch: %q", ban)
	}
	if !strings.Contains(ban, "Contoh : .ban @gibran") {
		t.Fatalf("banUsage contoh mismatch: %q", ban)
	}
	if !strings.Contains(ban, "> Owner tidak bisa di-ban.") {
		t.Fatalf("banUsage note mismatch: %q", ban)
	}

	unban := unbanUsage()
	if !strings.Contains(unban, "*Buka blokir user*") {
		t.Fatalf("unbanUsage title mismatch: %q", unban)
	}
	if !strings.Contains(unban, "`Unban`\nFormat :\n- .unban @user\n- .unban <nomor>") {
		t.Fatalf("unbanUsage format mismatch: %q", unban)
	}
	if !strings.Contains(unban, "Contoh : .unban @gibran") {
		t.Fatalf("unbanUsage contoh mismatch: %q", unban)
	}

	bg := bangroupUsage()
	if !strings.Contains(bg, "*Blokir grup*") || !strings.Contains(bg, "Format : .bangroup") {
		t.Fatalf("bangroupUsage mismatch: %q", bg)
	}

	ubg := unbangroupUsage()
	if !strings.Contains(ubg, "*Buka blokir grup*") || !strings.Contains(ubg, "Format : .unbangroup") {
		t.Fatalf("unbangroupUsage mismatch: %q", ubg)
	}

	mu := modeUsage("public")
	if !strings.Contains(mu, "*Mode bot*") {
		t.Fatalf("modeUsage title mismatch: %q", mu)
	}
	if !strings.Contains(mu, "Status : publik") {
		t.Fatalf("modeUsage status mismatch: %q", mu)
	}
	if !strings.Contains(mu, "`Set`\nFormat : .mode <state>") {
		t.Fatalf("modeUsage format mismatch: %q", mu)
	}
	if !strings.Contains(mu, "public  : akses umum") || !strings.Contains(mu, "private : khusus owner") {
		t.Fatalf("modeUsage params mismatch: %q", mu)
	}
}
