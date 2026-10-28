package plugins

import (
	"errors"
	"strings"
	"testing"

	"wabot-go/internal/reactionwa"
)

// TestRCHHelpBlock memastikan petunjuk .rch memakai format dua tingkat dan
// menyebut ketiga pemakaian (react/status/login) tanpa contoh token asli.
func TestRCHHelpBlock(t *testing.T) {
	got := rchHelpBlock()
	want := "*RCH*\n" +
		"• React :\n> .rch <link pesan> <emoji>\n" +
		"• Status :\n> .rch status\n" +
		"• Login :\n> .rch login <refresh token>"
	if got != want {
		t.Errorf("blok petunjuk = %q, mau %q", got, want)
	}
}

// TestRCHRegistry memastikan .rch terdaftar sekali, khusus owner, dan tidak
// punya alias yang tidak disengaja.
func TestRCHRegistry(t *testing.T) {
	r := NewRegistry()
	RegisterAll(r)

	cmd, ok := r.Get("rch")
	if !ok {
		t.Fatal(".rch belum terdaftar")
	}
	if cmd.Name != "rch" || cmd.Category != CategoryOwner {
		t.Errorf("command rch = %+v, mau Name rch + Category Owner", cmd)
	}
	if len(cmd.Aliases) != 0 {
		t.Errorf("rch punya alias tak terduga: %v", cmd.Aliases)
	}
	if !cmd.CloneBlocked() {
		t.Error("rch harus diblokir di bot clone (CategoryOwner)")
	}
}

// TestRCHErrorText memastikan setiap error bertipe diterjemahkan menjadi
// kalimat yang memberi tahu langkah berikutnya.
func TestRCHErrorText(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{reactionwa.ErrNoToken, "login"},
		{reactionwa.ErrTokenRejected, "refresh token baru"},
		{reactionwa.ErrLimit, "Jatah reaksi habis"},
		{reactionwa.ErrCaptcha, "captcha gagal"},
		{reactionwa.ErrLink, "link pesan channel"},
		{errors.New("koneksi putus"), "koneksi putus"},
	}
	for _, tc := range cases {
		got := strings.ToLower(rchErrorText(tc.err))
		if !strings.Contains(got, strings.ToLower(tc.want)) {
			t.Errorf("rchErrorText(%v) = %q, mau memuat %q", tc.err, got, tc.want)
		}
	}
	if got := rchErrorText(reactionwa.ErrBrowser); !strings.Contains(got, "Chrome") {
		t.Errorf("ErrBrowser harus menyebut Chrome: %q", got)
	}
}

func TestRCHResultStatus(t *testing.T) {
	if got := rchResultStatus(nil); got != "diproses" {
		t.Errorf("nil = %q", got)
	}
	if got := rchResultStatus(&reactionwa.Result{}); got != "masuk antrean" {
		t.Errorf("kosong = %q", got)
	}
	if got := rchResultStatus(&reactionwa.Result{Message: "masuk antrean"}); got != "masuk antrean" {
		t.Errorf("pesan = %q", got)
	}
}
