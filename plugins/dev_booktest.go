package plugins

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"wabot-go/internal/messaging"
	"wabot-go/internal/store"
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

// RegisterBooktest mendaftarkan command `.booktest` kategori Dev: mengirim
// satu kartu uji tombol native_flow `booking_confirmation` (pola sama dengan
// .btntest — InteractiveMessage + node biz, tinggal nama tombol dan params
// JSON-nya yang berbeda). Bentuk: `.booktest [varian] [judul]` — varian:
// minimal, tanpa-kelola, tanpa-peta, tanpa-kontak. Tanpa varian = kartu
// penuh, sisa argumen jadi judul (default "Uji Booking Confirmation").
func RegisterBooktest(r *Registry) {
	r.Register(Command{
		Name:        "booktest",
		Description: "Uji kartu booking_confirmation + variannya",
		Category:    CategoryDev,
		Handler:     handleBooktest,
	})
}

// parseBooktestArgs memisah varian + judul dari teks argumen mentah.
func parseBooktestArgs(raw string) (variant, title string) {
	title = strings.TrimSpace(raw)
	if f := strings.Fields(title); len(f) > 0 {
		if v, hit := booktestVariants[strings.ToLower(f[0])]; hit {
			variant = v
			title = strings.TrimSpace(strings.TrimPrefix(title, f[0]))
		}
	}
	if title == "" {
		title = "Uji Booking Confirmation"
	}
	return variant, title
}

func handleBooktest(ctx context.Context, mctx *store.MessageContext) error {
	variant, title := parseBooktestArgs(mctx.RawArgs)
	if err := booktestSend(ctx, mctx, variant, title); err != nil {
		return store.ReplyText(ctx, mctx, "Gagal kirim kartu booking: "+err.Error())
	}
	label := "penuh"
	if variant != "" {
		label = variant
	}
	return store.ReplyText(ctx, mctx, "Kartu booking ("+label+") terkirim. Cek tombol/detail mana yang tampil vs hilang di HP.")
}

// booktestSend mengirim kartu booking_confirmation tanpa balasan ringkasan —
// dipakai handler `.booktest` maupun agregator `.devtest` (yang membalas
// sekali di akhir).
func booktestSend(ctx context.Context, mctx *store.MessageContext, variant, title string) error {
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

	// Params kartu booking (varian menghapus field data tertentu). NFButton
	// dipakai langsung karena varian butuh membuang key, bukan sekadar field
	// kosong seperti messaging.BookingConfirmation.
	btn := messaging.NFButton{Name: messaging.NFBookingConfirmation, Params: booktestParams(variant, start, end)}
	return messaging.SendNativeFlow(ctx, mctx, messaging.NFText{
		Title:    title,
		Subtitle: "Kartu uji — bukan janji sungguhan",
		Body:     "Ketuk kartu untuk melihat tombol kalender/peta/kelola booking, lalu laporkan apa yang terjadi di HP.",
		Footer:   "wabot-go • booktest",
	}, "booktest", "booktest", btn)
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
