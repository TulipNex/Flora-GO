package plugins

import (
	"strings"
	"testing"
)

func TestFlaticonFormat(t *testing.T) {
	help := Help(HelpSpec{
		Title:   "Link CDN ikon Flaticon",
		Cmd:     "Flaticon",
		Formats: []string{".flaticon <ID/link ikon>"},
		Contoh:  ".flaticon 4661257",
	})
	if !strings.Contains(help, "*Link CDN ikon Flaticon*") {
		t.Errorf("missing title, got:\n%s", help)
	}
	if !strings.Contains(help, "`Flaticon`") {
		t.Errorf("missing cmd label, got:\n%s", help)
	}
}
