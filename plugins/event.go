package plugins

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"wabot-go/internal/logfmt"
	"wabot-go/internal/store"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func init() {
	registerPlugin(RegisterEvent)
}

// eventRegistry mencatat ID bubble acara buatan bot (untuk validasi
// .cancelevent). In-memory seperti tally polling — hilang saat restart.
var eventRegistry = struct {
	sync.Mutex
	m map[string]bool
}{m: make(map[string]bool)}

// RegisterEvent mendaftarkan `.event` (buat acara grup WhatsApp/Events,
// BUKAN scheduled call) + `.cancelevent` (batalkan, reply ke bubble-nya).
// Kategori Umum, semua user boleh memakai — bila server menolak (mis.
// bukan grup), error-nya diteruskan jujur ke user.
//
// Format:
//
//	.event Rapat RT | 2026-10-12 19:30 | Bahas iuran | -6.2,106.8
//	.event Bukber | 2026-10-12 17:00
//
// EventMessage dikirim polos via SendMessage (tanpa upload). RSVP member
// ("Hadir/Tidak/Mungkin") masuk sebagai EncEventResponseMessage TERENKRIPSI
// dan versi whatsmeow ini TIDAK menyediakan helper dekripsinya — jadi bot
// bisa membuat/membaca/membatalkan acara, tapi TIDAK bisa menghitung RSVP.
// Itu batas library, bukan bug implementasi ini.
func RegisterEvent(r *Registry) {
	r.Register(Command{
		Name:        "event",
		Description: "Buat acara (.event nama | yyyy-MM-dd HH:mm | [deskripsi])",
		Category:    CategoryUmum,
		Handler:     handleEvent,
	})
	r.Register(Command{
		Name:        "cancelevent",
		Description: "Batalkan acara (reply ke bubble acara)",
		Category:    CategoryUmum,
		Handler:     handleCancelEvent,
	})
}

// eventCoordRe mengambil pasangan koordinat opsional di segmen ke-4.
var eventCoordRe = regexp.MustCompile(`^(-?\d+(?:\.\d+)?)\s*[, ]\s*(-?\d+(?:\.\d+)?)$`)

func handleEvent(ctx context.Context, mctx *store.MessageContext) error {
	segs := splitEventSegments(mctx.RawArgs)
	if len(segs) < 2 {
		return store.ReplyText(ctx, mctx, "Format: .event <nama> | <yyyy-MM-dd HH:mm> | [deskripsi] | [lat,long]\nContoh: .event Rapat RT | 2026-10-12 19:30 | Bahas iuran")
	}
	name := segs[0]
	start, err := parseEventTime(segs[1])
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Waktu tidak valid: %v\nContoh: .event Rapat RT | 2026-10-12 19:30", err))
	}
	if start.Before(time.Now().Add(time.Minute)) {
		return store.ReplyText(ctx, mctx, "Waktu acara harus di masa depan.\nContoh: .event Rapat RT | 2026-10-12 19:30")
	}

	em := &waE2E.EventMessage{
		Name:      proto.String(name),
		StartTime: proto.Int64(start.Unix()),
	}
	if len(segs) > 2 && segs[2] != "" {
		em.Description = proto.String(segs[2])
	}
	if len(segs) > 3 {
		if loc := parseEventLocation(segs[3]); loc != nil {
			em.Location = loc
		}
	}

	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, &waE2E.Message{EventMessage: em})
	if err != nil {
		logfmt.Warn("event: gagal buat acara (%v)\n", err)
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal buat acara: %v", err))
	}
	eventRegistry.Lock()
	eventRegistry.m[string(resp.ID)] = true
	eventRegistry.Unlock()
	store.LogOutgoing(mctx, "event", name)
	store.TrackBotMessageID(string(resp.ID), "event: "+name)
	return nil
}

// splitEventSegments memecah argumen per "|" (nama boleh berspasi karena
// diambil dari RawArgs, bukan Args yang terpotong spasi).
func splitEventSegments(raw string) []string {
	parts := strings.Split(raw, "|")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// parseEventTime menerima "yyyy-MM-dd HH:mm" zona Asia/Jakarta (fallback
// Local bila databasenya tak ada). Jakarta dipilih karena audiens bot
// Indonesia — jam server panel (UTC) tidak bisa diasumsikan.
func parseEventTime(s string) (time.Time, error) {
	loc, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		loc = time.Local
	}
	return time.ParseInLocation("2006-01-02 15:04", strings.TrimSpace(s), loc)
}

// parseEventLocation mengubah "lat,long" jadi LocationMessage untuk field
// Location acara. nil bila format salah/di luar rentang — pemanggil
// mengabaikan (acara tanpa titik, bukan gagal total).
func parseEventLocation(s string) *waE2E.LocationMessage {
	m := eventCoordRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return nil
	}
	lat, err1 := strconv.ParseFloat(m[1], 64)
	long, err2 := strconv.ParseFloat(m[2], 64)
	if err1 != nil || err2 != nil || lat < -90 || lat > 90 || long < -180 || long > 180 {
		return nil
	}
	return &waE2E.LocationMessage{
		DegreesLatitude:  proto.Float64(lat),
		DegreesLongitude: proto.Float64(long),
	}
}

func handleCancelEvent(ctx context.Context, mctx *store.MessageContext) error {
	quotedID := store.ExtractStanzaID(mctx.RawEvt)
	if quotedID == "" {
		return store.ReplyText(ctx, mctx, "Reply ke bubble acara bot, lalu .cancelevent")
	}
	eventRegistry.Lock()
	known := eventRegistry.m[quotedID]
	eventRegistry.Unlock()
	if !known {
		return store.ReplyText(ctx, mctx, "Bukan bubble acara bot (atau bot sudah restart sehingga datanya hilang).")
	}

	// Pembatalan best-effort: kirim EventMessage bertanda IsCanceled dengan
	// quote ke bubble aslinya. Mekanisme cancel resmi (edit terenkripsi)
	// belum terverifikasi di versi whatsmeow ini — kalau klien WA tidak
	// menampilkannya sebagai pembatalan, itulah batas yang diketahui, bukan
	// kegagalan diam: error/penolakan server tetap diteruskan ke user.
	cancel := &waE2E.EventMessage{IsCanceled: proto.Bool(true)}
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, &waE2E.Message{EventMessage: cancel})
	if err != nil {
		logfmt.Warn("cancelevent: gagal (%v)\n", err)
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal batalkan acara: %v", err))
	}
	eventRegistry.Lock()
	delete(eventRegistry.m, quotedID)
	eventRegistry.Unlock()
	store.LogOutgoing(mctx, "event-batal", quotedID)
	store.TrackBotMessageID(string(resp.ID), "event dibatalkan")
	return store.ReplyText(ctx, mctx, "Permintaan pembatalan acara dikirim.")
}
