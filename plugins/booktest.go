package plugins

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"wabot-go/internal/store"

	"github.com/polymorfa/hypermeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func init() {
	registerPlugin(RegisterBooktest)
}

// booktestDefaults adalah nilai uji tombol booking_confirmation. Nomor dan
// email SENGAJA dummy — jangan isi data asli di command uji, karena kartu
// booking menampilkan keduanya ke semua penerima.
const (
	booktestPhone     = "6280000000000"
	booktestEmail     = "test@example.com"
	booktestURL       = "https://example.com/booking"
	booktestManageURL = "https://example.com/booking/kelola"
	booktestMaxTitle  = 100
)

// booktestVariant adalah varian kartu uji (kata pertama argumen).
// "" = kartu penuh (default). Varian menguji tombol kondisional kartu:
// field data mana yang menyembunyikan tombolnya bila dikosongkan.
var booktestVariants = map[string]string{
	"minimal":      "minimal",
	"tanpa-kelola": "tanpa-kelola", "nomanage": "tanpa-kelola",
	"tanpa-peta": "tanpa-peta", "nomaps": "tanpa-peta",
	"tanpa-kontak": "tanpa-kontak", "nocontact": "tanpa-kontak",
}

// RegisterBooktest mendaftarkan command `.booktest` kategori Owner: mengirim
// satu kartu uji tombol native_flow `booking_confirmation` (pola sama dengan
// .btntest — InteractiveMessage + node biz, tinggal nama tombol dan params
// JSON-nya yang berbeda). Bentuk: `.booktest [varian] [judul]` — varian:
// minimal, tanpa-kelola, tanpa-peta, tanpa-kontak. Tanpa varian = kartu
// penuh, sisa argumen jadi judul (default "Uji Booking Confirmation").
func RegisterBooktest(r *Registry) {
	r.Register(Command{
		Name:        "booktest",
		Description: "Uji kartu booking_confirmation + variannya (owner)",
		Category:    CategoryOwner,
		Handler:     handleBooktest,
	})
}

func handleBooktest(ctx context.Context, mctx *store.MessageContext) error {
	variant := ""
	title := strings.TrimSpace(mctx.RawArgs)
	if f := strings.Fields(title); len(f) > 0 {
		if v, hit := booktestVariants[strings.ToLower(f[0])]; hit {
			variant = v
			title = strings.TrimSpace(strings.TrimPrefix(title, f[0]))
		}
	}
	if title == "" {
		title = "Uji Booking Confirmation"
	}
	if utf8.RuneCountInString(title) > booktestMaxTitle {
		title = string([]rune(title)[:booktestMaxTitle])
	}

	// Jendela janji selalu di masa depan (sekarang +1 jam, durasi 10 menit)
	// supaya tombol tambah-ke-kalender valid kapan pun diuji.
	start := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	end := start.Add(10 * time.Minute)
	params, err := json.Marshal(booktestParams(variant, start, end))
	if err != nil {
		return store.ReplyText(ctx, mctx, "Gagal siapkan params booking: "+err.Error())
	}

	msg := &waE2E.Message{
		InteractiveMessage: &waE2E.InteractiveMessage{
			Header: &waE2E.InteractiveMessage_Header{
				Title:    proto.String(title),
				Subtitle: proto.String("Kartu uji — bukan janji sungguhan"),
			},
			Body:   &waE2E.InteractiveMessage_Body{Text: proto.String("Ketuk kartu untuk melihat tombol kalender/peta/kelola booking, lalu laporkan apa yang terjadi di HP.")},
			Footer: &waE2E.InteractiveMessage_Footer{Text: proto.String("wabot-go • booktest")},
			InteractiveMessage: &waE2E.InteractiveMessage_NativeFlowMessage_{
				NativeFlowMessage: &waE2E.InteractiveMessage_NativeFlowMessage{
					Buttons: []*waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
						btnTestNativeFlowButton("booking_confirmation", string(params)),
					},
				},
			},
		},
	}

	// Tanpa node `biz` manual: hypermeow menempelkannya otomatis (clone
	// ter-patch selalu mengirim name="mixed").
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, msg, btnTestBizExtra())
	if err != nil {
		return store.ReplyText(ctx, mctx, "Gagal kirim kartu booking: "+err.Error())
	}
	store.LogOutgoing(mctx, "booktest", "kartu booking")
	store.TrackBotMessageID(resp.ID, "booktest")
	label := "penuh"
	if variant != "" {
		label = variant
	}
	return store.ReplyText(ctx, mctx, "Kartu booking ("+label+") terkirim. Cek tombol/detail mana yang tampil vs hilang di HP.")
}

// booktestParams menyusun params JSON kartu sesuai varian. Kartu penuh
// memakai semua field; varian menghapus field data tertentu untuk melihat
// tombol mana yang ikut hilang (teks display dipertahankan supaya status
// fallback-nya — mis. "tak didukung" — ikut teramati).
func booktestParams(variant string, start, end time.Time) map[string]any {
	if variant == "minimal" {
		return map[string]any{
			"start_datetime": start.Format(time.RFC3339),
			"end_datetime":   end.Format(time.RFC3339),
			"display_text":   "Lihat Booking",
		}
	}
	p := map[string]any{
		"start_datetime":         start.Format(time.RFC3339),
		"end_datetime":           end.Format(time.RFC3339),
		"location":               "Indonesia",
		"booking_url":            booktestURL,
		"phone_number":           booktestPhone,
		"booking_management_url": booktestManageURL,
		"description":            "Kartu uji booking dari wabot-go (bukan janji sungguhan).",
		"email":                  booktestEmail,
		"display_text":           "Lihat Booking",
		"display_content": map[string]string{
			"display_language":                          "id",
			"display_meeting_type":                      "Uji Coba",
			"display_bottom_sheet_header":               "Detail Booking (Uji)",
			"display_add_to_calendar_cta_text":          "KALENDER",
			"display_view_on_maps_cta_text":             "LIHAT PETA",
			"display_manage_booking_cta_text":           "KELOLA BOOKING",
			"display_manage_booking_not_supported_text": "Kelola booking tak didukung",
			"display_read_more":                         "Selengkapnya",
		},
	}
	switch variant {
	case "tanpa-kelola":
		delete(p, "booking_management_url")
	case "tanpa-peta":
		delete(p, "location")
	case "tanpa-kontak":
		delete(p, "phone_number")
		delete(p, "email")
	}
	return p
}
