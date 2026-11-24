package plugins

import (
	"strings"
	"testing"
)

func TestOwnerJoinUsageFormats(t *testing.T) {
	ju := joinUsage()
	if !strings.Contains(ju, "*Masuk grup*") {
		t.Fatalf("joinUsage title mismatch: %q", ju)
	}
	if !strings.Contains(ju, "`Join`\nFormat : .join <link grup>") {
		t.Fatalf("joinUsage format mismatch: %q", ju)
	}
	if !strings.Contains(ju, "Contoh : .join https://chat.whatsapp.com/XXXX") {
		t.Fatalf("joinUsage contoh mismatch: %q", ju)
	}

	lu := leaveUsage()
	if !strings.Contains(lu, "*Keluar grup*") {
		t.Fatalf("leaveUsage title mismatch: %q", lu)
	}
	if !strings.Contains(lu, "`Leave`\nFormat : .leave [nomor]") {
		t.Fatalf("leaveUsage format mismatch: %q", lu)
	}
	if !strings.Contains(lu, "Contoh : .leave 001") {
		t.Fatalf("leaveUsage contoh mismatch: %q", lu)
	}
	if !strings.Contains(lu, "> Nomor urut grup lihat di .listgrup. Di dalam grup bisa tanpa nomor.") {
		t.Fatalf("leaveUsage note mismatch: %q", lu)
	}
}
