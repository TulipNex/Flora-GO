package plugins

import (
	"testing"
)

func TestDeepCutPrefix(t *testing.T) {
	cases := []struct {
		input    string
		wantRest string
		wantOK   bool
	}{
		{"deep halo apa kabar", "halo apa kabar", true},
		{"DEEP: tolong bantu saya", "tolong bantu saya", true},
		{"deep -coba", "coba", true},
		{"deep /humanize teks ini", "/humanize teks ini", true},
		{"deep /imagine kucing lucu", "/imagine kucing lucu", true},
		{"deep", "", true},
		{"deepchat halo", "", false}, // Bukan prefix "deep "
		{"deeply interested", "", false},
		{"halo deep", "", false},
	}

	for _, tc := range cases {
		rest, ok := deepCutPrefix(tc.input)
		if ok != tc.wantOK || rest != tc.wantRest {
			t.Errorf("deepCutPrefix(%q) = (%q, %v); want (%q, %v)", tc.input, rest, ok, tc.wantRest, tc.wantOK)
		}
	}
}

func TestDeepTriggerOf(t *testing.T) {
	cases := []struct {
		input   string
		active  bool
		wantCmd string
		wantArg string
		wantOK  bool
	}{
		{"deep /humanize ini teks", false, "/humanize", "ini teks", true},
		{"deep /imagine gunung indah", false, "/imagine", "gunung indah", true},
		{"deep /edit ganti warna baju", false, "/edit", "ganti warna baju", true},
		{"deep /swap", false, "/swap", "", true},
		{"deep /opt buatkan prompt", false, "/opt", "buatkan prompt", true},
		{"deep halo semuanya", false, "chat", "halo semuanya", true},
		{"halo bot", true, "", "", false}, // tanpa reply
	}

	for _, tc := range cases {
		cmd, arg, ok := deepTriggerOf(nil, tc.input, tc.active)
		if ok != tc.wantOK || cmd != tc.wantCmd || arg != tc.wantArg {
			t.Errorf("deepTriggerOf(%q, %v) = (%q, %q, %v); want (%q, %q, %v)",
				tc.input, tc.active, cmd, arg, ok, tc.wantCmd, tc.wantArg, tc.wantOK)
		}
	}
}
