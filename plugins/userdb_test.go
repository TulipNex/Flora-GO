package plugins

import (
	"strings"
	"testing"
)

func TestUserDBUsageFormat(t *testing.T) {
	help := userdbUsage()
	if !strings.Contains(help, "*Direktori user*") {
		t.Fatalf("userdbUsage title mismatch: %q", help)
	}
	if !strings.Contains(help, "`Sync`\nFormat :\n- .userdb sync <id|user>\n- .userdb syncpp <id|user>") {
		t.Fatalf("userdbUsage sync format mismatch: %q", help)
	}
	if !strings.Contains(help, "sync   : simpan identitas") || !strings.Contains(help, "syncpp : simpan foto") {
		t.Fatalf("userdbUsage sync params mismatch: %q", help)
	}
	if !strings.Contains(help, "`Lainnya`\nFormat :") {
		t.Fatalf("userdbUsage lainnya format mismatch: %q", help)
	}
	if !strings.Contains(help, "status : progres sinkronisasi") || !strings.Contains(help, "info   : profil detail") {
		t.Fatalf("userdbUsage lainnya params mismatch: %q", help)
	}
}

func TestUserDBSyncSummaryHierarchy(t *testing.T) {
	got := userdbSubBlock("Member", [][2]string{
		{"Total", "150"},
		{"Baru", "12"},
		{"Update", "138"},
		{"Keluar", "0"},
	})
	want := "- *Member*\n```  - Total  : 150\n  - Baru   : 12\n  - Update : 138\n  - Keluar : 0```"
	if got != want {
		t.Fatalf("userdbSubBlock mismatch.\nGot:\n%s\nWant:\n%s", got, want)
	}
}
