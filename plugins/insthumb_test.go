package plugins

import (
	"strings"
	"testing"

	"wabot-go/internal/external"
)

func TestInsthumbFormats(t *testing.T) {
	help := Help(HelpSpec{
		Title:   "Metadata link",
		Cmd:     "Insthumb",
		Formats: []string{".insthumb <url>"},
		Contoh:  ".insthumb https://example.com/artikel",
		Note:    "Bisa juga reply pesan berisi link.",
	})
	if !strings.Contains(help, "*Metadata link*") {
		t.Errorf("missing title, got:\n%s", help)
	}

	meta := &external.PageMeta{
		Title:       "Test Page",
		Description: "A description",
		ImageURL:    "https://example.com/img.png",
		FaviconURL:  "https://example.com/favicon.ico",
	}
	out := formatInsThumb("https://example.com", meta)
	if !strings.Contains(out, "*Metadata*") {
		t.Errorf("missing *Metadata*, got:\n%s", out)
	}
	if !strings.Contains(out, "- Link : https://example.com") {
		t.Errorf("missing link row, got:\n%s", out)
	}
	if !strings.Contains(out, "- Judul : Test Page") {
		t.Errorf("missing title row, got:\n%s", out)
	}
}
