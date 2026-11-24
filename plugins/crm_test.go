package plugins

import (
	"context"
	"strings"
	"testing"
	"time"

	"wabot-go/internal/store"
)

func TestCRMUsageFormat(t *testing.T) {
	got := crmUsage(142)
	want := "*Arsip pesan*\n\n" +
		"`Crm`\n" +
		"Format :\n" +
		"- .crm #rowid\n" +
		"- .crm <reply>\n\n" +
		"```rowid : nomor baris arsip\n" +
		"reply : balas pesan```\n\n" +
		"`Lainnya`\n" +
		"- .insp #rowid : JSON mentah pesan\n" +
		"- .run #rowid : kirim ulang pesan\n" +
		"- .lastchat [n] : N pesan terakhir\n\n" +
		"> Arsip: 142/200 pesan."

	if got != want {
		t.Errorf("crmUsage tidak sesuai.\nGot:\n%s\n\nWant:\n%s", got, want)
	}
}

func TestRunUsageFormat(t *testing.T) {
	want := "*Kirim ulang pesan*\n\n" +
		"`Run`\n" +
		"Format :\n" +
		"- .run #rowid\n" +
		"- .run <reply>\n\n" +
		"```rowid : nomor baris arsip\n" +
		"reply : JSON/pesan asli```\n\n" +
		"> Dokumen hasil .crm/.insp tidak bisa di-reply, gunakan #rowid."

	if runUsage != want {
		t.Errorf("runUsage tidak sesuai.\nGot:\n%s\n\nWant:\n%s", runUsage, want)
	}
}

func TestCRMMetaFormat(t *testing.T) {
	wib := time.FixedZone("WIB", 7*3600)
	fixedTime := time.Date(2026, 11, 13, 10, 12, 5, 0, wib)

	row := &store.CRMRow{
		Rowid:  4825,
		ID:     "ABC123",
		Chat:   "628xxx@s.whatsapp.net",
		Sender: "bot",
		TS:     fixedTime.Unix(),
		Data:   make([]byte, 420),
	}

	got := formatCRMMetaAt(context.Background(), row, "text", nil, "halo", fixedTime)

	wantLines := []string{
		"*CRM #4825*",
		"",
		"```- Tipe     : text",
		"- Chat     : 628xxx",
		"- Pengirim : Bot",
		"- Waktu    : 13 Nov 2026",
		"- Jam      : 10:12:05 WIB",
		"- ID       : ABC123",
		"- Ukuran   : 420 bytes```",
		"",
		"halo",
	}
	want := strings.Join(wantLines, "\n")

	if got != want {
		t.Errorf("formatCRMMetaAt tidak sesuai.\nGot:\n%s\n\nWant:\n%s", got, want)
	}
}
