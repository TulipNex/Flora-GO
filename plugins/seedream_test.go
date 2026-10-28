package plugins

import (
	"strings"
	"testing"
	"time"

	"wabot-go/internal/timex"
)

// TestExpiryWhen memastikan jam masa berlaku cookie arena.ai ditampilkan di
// zona aktif bot (BOT_TIMEZONE) dan menyebut nama zonanya — bukan time.Local,
// yang di panel berjalan di UTC dan membuat jamnya meleset 7-9 jam. Sisa
// waktunya ditampilkan terpisah di baris "Sisa".
func TestExpiryWhen(t *testing.T) {
	expiry := time.Now().Add(90 * time.Minute)
	got := expiryWhen(expiry)

	if !strings.Contains(got, timex.Name()) {
		t.Errorf("label %q tidak memuat nama zona %q", got, timex.Name())
	}
	local := expiry.In(timex.Zone()).Format("02 Jan 15:04")
	if !strings.Contains(got, local) {
		t.Errorf("label %q tidak memuat jam zona aktif %q", got, local)
	}
	if strings.Contains(got, "(") {
		t.Errorf("label %q seharusnya tanpa durasi (dipisah ke baris Sisa)", got)
	}
	// Baris "Sisa" memakai durasiRingkas yang diuji terpisah.
	if sisa := durasiRingkas(time.Until(expiry)); !strings.Contains(sisa, "jam") {
		t.Errorf("sisa waktu = %q, mau menyebut jam", sisa)
	}
}

// TestSeedreamHelpBlock memastikan petunjuk .seedream memakai format dua
// tingkat (kunci + "> contoh") dan hanya menyebut satu command.
func TestSeedreamHelpBlock(t *testing.T) {
	got := seedreamHelpBlock()
	want := "*Seedream*\n" +
		"• Generate :\n> .seedream <prompt>\n" +
		"• Edit :\n> .seedream <prompt> + reply gambar\n" +
		"• Cookie :\n> .seedream cookie <cookie>"
	if got != want {
		t.Errorf("blok petunjuk = %q, mau %q", got, want)
	}
}

// TestSeedreamSubcommand memastikan hanya "cookie" yang dianggap subcommand,
// case-insensitive, dan sisa baris cookie dipakai utuh.
func TestSeedreamSubcommand(t *testing.T) {
	cases := []struct {
		args string
		kind string
		rest string
	}{
		{"cookie a=1; b=2", "cookie", "a=1; b=2"},
		{"COOKIE abc", "cookie", "abc"},
		{"cookie", "cookie", ""},
		{"kucing oranye", "", "kucing oranye"},
		{"cookies enak", "", "cookies enak"},
		{"", "", ""},
	}
	for _, tc := range cases {
		kind, rest := seedreamSubcommand(tc.args)
		if kind != tc.kind || rest != tc.rest {
			t.Errorf("seedreamSubcommand(%q) = (%q, %q), mau (%q, %q)",
				tc.args, kind, rest, tc.kind, tc.rest)
		}
	}
}

// TestSeedreamStripVerb memastikan kata kerja opsional di awal prompt
// dibuang, sementara prompt biasa (termasuk yang kebetulan diawali kata
// mirip) tidak ikut terpotong.
func TestSeedreamStripVerb(t *testing.T) {
	cases := map[string]string{
		"generate kucing":       "kucing",
		"Generate a cat":        "a cat",
		"edit ubah latar":       "ubah latar",
		"gen kucing":            "kucing",
		"kucing oranye":         "kucing oranye",
		"editorial foto kucing": "editorial foto kucing",
		"":                      "",
	}
	for in, want := range cases {
		if got := seedreamStripVerb(in); got != want {
			t.Errorf("seedreamStripVerb(%q) = %q, mau %q", in, got, want)
		}
	}
}

// TestSeedreamRegistry memastikan hanya satu command arena.ai yang
// terdaftar: .seedream dengan alias .sd. Command lama (.sedit/.scookie/.s5/
// .imagine/.simagine) tidak boleh diam-diam kembali.
func TestSeedreamRegistry(t *testing.T) {
	r := NewRegistry()
	RegisterAll(r)

	for _, key := range []string{"seedream", "sd"} {
		cmd, ok := r.Get(key)
		if !ok || cmd.Name != "seedream" {
			t.Fatalf("%q harus menunjuk seedream", key)
		}
	}
	for _, key := range []string{"sedit", "simagine", "scookie", "s5", "imagine"} {
		if cmd, ok := r.Get(key); ok {
			t.Errorf("%q seharusnya sudah dihapus, masih menunjuk %q", key, cmd.Name)
		}
	}

	// .menu hanya menampilkan satu baris untuk command ini.
	count := 0
	for _, cmd := range r.All() {
		if cmd.Name == "seedream" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("seedream muncul %d kali di menu, mau 1", count)
	}
}

// TestSeedreamCaption memastikan caption hasil gambar sesuai kontrak:
// baris pertama "Generate/Edit With <model>", baris kedua "> prompt", tanpa
// branding penyedia, prompt multi-baris dirapatkan, dan prompt kepanjangan
// dipotong tanpa melewati batas caption WhatsApp.
func TestSeedreamCaption(t *testing.T) {
	if got, want := seedreamCaption("", "seedream-5.0-pro", "kucing oranye"),
		"Generate With seedream-5.0-pro\n> kucing oranye"; got != want {
		t.Errorf("caption generate = %q, mau %q", got, want)
	}
	if got, want := seedreamCaption("edit", "seedream-5.0-pro", "ubah latar jadi biru"),
		"Edit With seedream-5.0-pro\n> ubah latar jadi biru"; got != want {
		t.Errorf("caption edit = %q, mau %q", got, want)
	}

	multi := seedreamCaption("", "m", "baris satu\nbaris   dua")
	if strings.Contains(multi, "\n\n") || !strings.Contains(multi, "> baris satu baris dua") {
		t.Errorf("prompt multi-baris tidak dirapatkan: %q", multi)
	}

	if got := seedreamCaption("", "m", ""); got != "Generate With m" {
		t.Errorf("prompt kosong = %q", got)
	}

	long := seedreamCaption("", "seedream-5.0-pro", strings.Repeat("a", 5000))
	if n := len([]rune(long)); n > seedreamCaptionMaxRunes {
		t.Errorf("caption %d rune, melewati batas %d", n, seedreamCaptionMaxRunes)
	}
	if !strings.HasSuffix(long, "…") {
		t.Errorf("caption panjang tidak dipotong dengan elipsis: %q", long[len(long)-20:])
	}
	if strings.Contains(long, "arena.ai") {
		t.Error("caption tidak boleh menyebut arena.ai")
	}

	// Model kosong jatuh ke model default, bukan menghasilkan spasi ganda.
	if got := seedreamCaption("", "", "x"); !strings.HasPrefix(got, "Generate With ") ||
		strings.Contains(got, "  ") {
		t.Errorf("model kosong tidak memakai default: %q", got)
	}
}

// TestSeedreamWaitText memastikan keterangan tunggu tanpa emoji dan tanpa
// angka/tautan yang bisa basi.
func TestSeedreamWaitText(t *testing.T) {
	if got, want := seedreamWaitText(""), "Sedang membuat gambar, tunggu sebentar..."; got != want {
		t.Errorf("wait generate = %q, mau %q", got, want)
	}
	if got, want := seedreamWaitText("edit"), "Sedang mengedit foto, tunggu sebentar..."; got != want {
		t.Errorf("wait edit = %q, mau %q", got, want)
	}
}

// TestDurasiRingkas memastikan format durasi ringkas yang dipakai status
// cookie tetap masuk akal untuk rentang yang biasa muncul.
func TestDurasiRingkas(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "kurang dari 1 menit"},
		{58 * time.Minute, "58 menit"},
		{2 * time.Hour, "2 jam"},
		{3*time.Hour + 12*time.Minute, "3 jam 12 menit"},
		{50 * time.Hour, "2 hari"},
	}
	for _, tc := range cases {
		if got := durasiRingkas(tc.d); got != tc.want {
			t.Errorf("durasiRingkas(%v) = %q, mau %q", tc.d, got, tc.want)
		}
	}
}
