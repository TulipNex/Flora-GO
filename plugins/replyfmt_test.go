package plugins

import (
	"errors"
	"strings"
	"testing"
)

// Code: pembuka menempel di baris pertama dan penutup menempel di baris
// terakhir (aturan 5 FORMAT_RESPON_BOT.md).
func TestCodeFenceFlush(t *testing.T) {
	got := Code("- Latensi : 12ms", "- Uptime  : 2h 10m 3s")
	want := "```- Latensi : 12ms\n- Uptime  : 2h 10m 3s```"
	if got != want {
		t.Fatalf("Code() =\n%q\nwant\n%q", got, want)
	}
	if strings.Count(got, "```") != 2 {
		t.Fatalf("Code() harus punya tepat 2 fence, dapat %q", got)
	}
}

// Satu bentuk pemakaian ditulis sebaris ("Format : .add <nomor>"), sesuai
// aturan 3 di FORMAT_RESPON_BOT.md.
func TestHelpSingleFormat(t *testing.T) {
	got := Help(HelpSpec{
		Title:   "Menambahkan anggota grup",
		Cmd:     "Add",
		Formats: []string{".add <nomor>"},
		Contoh:  ".add 6281234567890",
		Params:  []string{"nomor : 6281234567890"},
		Note:    "Bisa beberapa nomor sekaligus.",
	})
	want := "*Menambahkan anggota grup*\n\n" +
		"`Add`\nFormat : .add <nomor>\nContoh : .add 6281234567890\n\n" +
		"```nomor : 6281234567890```\n\n" +
		"> Bisa beberapa nomor sekaligus."
	if got != want {
		t.Fatalf("Help() =\n%q\nwant\n%q", got, want)
	}
	if strings.Contains(got, "Format :\n- ") {
		t.Fatalf("bentuk tunggal tidak boleh jadi daftar: %q", got)
	}
}

// Lebih dari satu bentuk jadi daftar "-" di baris berikutnya.
func TestHelpMultiFormat(t *testing.T) {
	got := Help(HelpSpec{
		Title:   "Keluarkan anggota grup",
		Cmd:     "Kick",
		Formats: []string{".kick @user", ".kick <reply>"},
		Contoh:  ".kick @gibran",
		Params:  []string{"@user : tag target", "reply : balas pesan target"},
		Note:    "Bot harus admin dan target bukan owner bot.",
	})
	want := "*Keluarkan anggota grup*\n\n" +
		"`Kick`\nFormat :\n- .kick @user\n- .kick <reply>\nContoh : .kick @gibran\n\n" +
		"```@user : tag target\nreply : balas pesan target```\n\n" +
		"> Bot harus admin dan target bukan owner bot."
	if got != want {
		t.Fatalf("Help() =\n%q\nwant\n%q", got, want)
	}
}

// Saklar: status tampil sebagai fence tepat di bawah judul, sebelum sub-label.
func TestHelpWithStatus(t *testing.T) {
	got := Help(HelpSpec{
		Title:   "Auto download",
		Status:  "nonaktif",
		Cmd:     "Set",
		Formats: []string{".autodownload <state>"},
		Contoh:  ".autodownload off",
	})
	want := "*Auto download*\n\n```Status : nonaktif```\n\n" +
		"`Set`\nFormat : .autodownload <state>\nContoh : .autodownload off"
	if got != want {
		t.Fatalf("Help() =\n%q\nwant\n%q", got, want)
	}
	if strings.Contains(got, "ON") || strings.Contains(got, "mati") {
		t.Fatalf("istilah status harus aktif/nonaktif: %q", got)
	}
}

// Field kosong tidak boleh meninggalkan baris/fence kosong.
func TestHelpOmitsEmptyParts(t *testing.T) {
	got := Help(HelpSpec{Title: "Pong"})
	if got != "*Pong*" {
		t.Fatalf("Help() tanpa isi = %q, want *Pong*", got)
	}
	got = Help(HelpSpec{Title: "Tag", Cmd: "Tagall", Formats: []string{".tagall <pesan>"}})
	if strings.Contains(got, "```") || strings.Contains(got, "Contoh") {
		t.Fatalf("field kosong tidak boleh dirangkai: %q", got)
	}
}

// Fail: satu kalimat, titik akhir tunggal walau error sudah bertitik.
func TestFailNoDoubleDot(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{errors.New("invalid jid"), "Gagal mengangkat admin: invalid jid."},
		{errors.New("server menolak."), "Gagal mengangkat admin: server menolak."},
	}
	for _, c := range cases {
		if got := Fail("Gagal mengangkat admin", c.err); got != c.want {
			t.Fatalf("Fail() = %q, want %q", got, c.want)
		}
	}
}

// Sentence: kapital di awal, titik di akhir, tanpa titik ganda.
func TestSentence(t *testing.T) {
	cases := map[string]string{
		"nomor tidak boleh kosong":    "Nomor tidak boleh kosong.",
		"Nomor tidak valid.":          "Nomor tidak valid.",
		"  gagal membaca data grup  ": "Gagal membaca data grup.",
		"":                            "",
	}
	for in, want := range cases {
		if got := Sentence(in); got != want {
			t.Fatalf("Sentence(%q) = %q, want %q", in, got, want)
		}
	}
}

// Keluaran nyata command yang sudah dimigrasi (menu -> add), dikunci persis
// seperti FORMAT_RESPON_BOT.md bagian 4.1-4.2 supaya tidak melenceng lagi.
func TestMigratedCommandBlocks(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{
			name: "kickHelp",
			got:  kickHelp(),
			want: "*Keluarkan anggota grup*\n\n`Kick`\nFormat :\n- .kick @user\n- .kick <reply>\n" +
				"Contoh : .kick @gibran\n\n```@user : tag target\nreply : balas pesan target```\n\n" +
				"> Bot harus admin dan target bukan owner bot.",
		},
		{
			name: "tagallHelp",
			got:  tagallHelp(),
			want: "*Tag semua member*\n\n`Tagall`\nFormat : .tagall <pesan>\n" +
				"Contoh : .tagall rapat jam 19\n\n> Tanpa argumen, bot menampilkan bantuan ini.",
		},
		{
			name: "addHelp",
			got:  addHelp(),
			want: "*Menambahkan anggota grup*\n\n`Add`\nFormat : .add <nomor>\n" +
				"Contoh : .add 6281234567890\n\n```nomor : +62 812-3456-7890\n" +
				"        6281234567890\n        081234567890```\n\n" +
				"> Bisa beberapa nomor sekaligus, dipisah spasi.",
		},
		{
			name: "autodownloadHelp(nonaktif)",
			got:  autodownloadHelp(false),
			want: "*Auto download*\n\n```Status : nonaktif```\n\n`Set`\n" +
				"Format : .autodownload <state>\nContoh : .autodownload off\n\n" +
				"```state : on atau off\non    : aktif\noff   : nonaktif```",
		},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Fatalf("%s =\n%q\nwant\n%q", c.name, c.got, c.want)
		}
	}
}
