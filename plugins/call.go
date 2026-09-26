package plugins

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/purpshell/meowcaller"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	"wabot-go/internal/store"
)

// witaLocation adalah zona waktu WITA (UTC+8), dipakai untuk menjadwalkan
// panggilan dengan format ".call <nomor>|<jam>" (mis. ".call 628xxx|12.50").
var witaLocation = time.FixedZone("WITA", 8*60*60)

// callClient membungkus whatsmeow.Client untuk fitur panggilan WhatsApp.
// Diisi oleh InitCallClient, dipanggil dari main.go SEBELUM waClient.Connect().
var callClient *meowcaller.Client

// waClientForCalls adalah client whatsmeow mentah untuk pengiriman progres
// dan eksekusi jadwal (yang tidak punya MessageContext). Diisi bersamaan
// dengan callClient di InitCallClient.
var waClientForCalls *whatsmeow.Client

func init() {
	registerPlugin(RegisterCall)
}

// InitCallClient memasang meowcaller di atas whatsmeow client.
// PENTING: harus dipanggil SEBELUM waClient.Connect(), karena meowcaller perlu
// memasang interceptor <call>/<ack> sebelum receive loop whatsmeow mulai jalan.
func InitCallClient(wa *whatsmeow.Client) {
	waClientForCalls = wa
	callClient = meowcaller.NewClient(wa)

	callClient.OnIncomingCall(func(call *meowcaller.Call) {
		fmt.Printf("📞 Panggilan masuk dari %s\n", call.Peer().String())

		if err := call.Answer(); err != nil {
			fmt.Printf("❌ Gagal menjawab panggilan: %v\n", err)
			return
		}

		if mp3, err := meowcaller.MP3File("greeting.mp3"); err != nil {
			fmt.Printf("❌ Gagal membuka/decode greeting.mp3: %v\n", err)
		} else if err := call.Play(mp3); err != nil {
			fmt.Printf("❌ Gagal memutar greeting.mp3: %v\n", err)
		}

		call.OnEnd(func(reason string) {
			fmt.Printf("📴 Panggilan berakhir: %s\n", reason)
		})
	})
}

// RegisterCall mendaftarkan command ".call" KHUSUS OWNER untuk melakukan
// panggilan WhatsApp. Target bisa mention (@user), reply pesan target, atau
// nomor langsung (bebas format: 628xxx, 0822..., +62 822-1111-2222).
// Bentuk ".call <target>|<jam>" (mis. "12.50", WITA) menjadwalkan panggilan
// pada jam tersebut hari ini (atau besok kalau jam itu sudah lewat).
// Jadwal disimpan in-memory saja — hilang kalau bot restart.
func RegisterCall(r *Registry) {
	r.Register(Command{
		Name:        "call",
		Description: "Panggil WhatsApp (owner). .call @user / 628xxx / +62 822-... (+|jam untuk jadwal)",
		Category:    CategoryOwner,
		Handler: func(ctx context.Context, mctx *store.MessageContext) error {
			if msg := requireOwner(mctx); msg != "" {
				return store.ReplyText(ctx, mctx, msg)
			}
			if callClient == nil {
				return store.ReplyText(ctx, mctx, "Fitur call belum aktif.")
			}

			// Pisahkan "|<jam>" dulu dari teks MENTAH (RawArgs) supaya nomor
			// ber-spasi seperti "+62 822-1111-2222" tidak terpecah Args.
			raw := strings.TrimSpace(mctx.RawArgs)
			if raw == "" && len(mctx.Args) > 0 {
				raw = strings.Join(mctx.Args, " ")
			}
			parts := strings.SplitN(raw, "|", 2)
			targetText := strings.TrimSpace(parts[0])

			target, err := resolveCallTarget(ctx, mctx, targetText)
			if err != nil {
				return store.ReplyText(ctx, mctx, fmt.Sprintf("Format salah: %v. Contoh: .call @user, .call 628123456789, .call +62 822-1111-2222", err))
			}

			// Tidak ada "|" -> panggil langsung dengan progres satu bubble
			if len(parts) == 1 {
				return doCall(ctx, mctx, target)
			}

			// Ada "|<jam>" -> jadwalkan PERSISTEN (tahan restart, lihat
			// ScheduleCall). Konfirmasi ke peminta, progres menyusul di
			// chat ini saat waktunya tiba.
			at, err := ParseJamWITA(strings.TrimSpace(parts[1]))
			if err != nil {
				return store.ReplyText(ctx, mctx, fmt.Sprintf("Format jam salah (%v). Contoh: .call 628123456789|12.50", err))
			}
			if _, err := ScheduleCall(mctx.Chat, target, at); err != nil {
				return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal menyimpan jadwal: %v", err))
			}

			return store.ReplyText(ctx, mctx, fmt.Sprintf(
				"Panggilan ke %s dijadwalkan pukul %s WITA (%s lagi, tersimpan — tahan restart).",
				target, at.Format("15:04"), time.Until(at).Round(time.Second),
			))
		},
	})
}

// resolveCallTarget menentukan nomor HP tujuan panggilan. Prioritas:
// mention langsung, lalu reply pesan target, terakhir teks nomor mentah.
// Mengembalikan nomor bersih (digit + kode negara, mis. "62822...").
func resolveCallTarget(ctx context.Context, mctx *store.MessageContext, targetText string) (string, error) {
	if mentioned := mctx.MentionedJIDs(); len(mentioned) > 0 {
		if jid, err := types.ParseJID(mentioned[0]); err == nil {
			return ResolveCallPhone(ctx, mctx, jid)
		}
	}
	if quoted := mctx.QuotedParticipant(); quoted != "" {
		if jid, err := types.ParseJID(quoted); err == nil {
			return ResolveCallPhone(ctx, mctx, jid)
		}
	}
	if targetText == "" {
		return "", fmt.Errorf("tag @user, reply pesan target, atau sertakan nomor")
	}
	jid, err := ParsePhoneToJID(targetText)
	if err != nil {
		return "", err
	}
	return jid.User, nil
}

// ResolveCallPhone mengubah JID target (hasil mention/reply, bisa PN maupun
// LID) menjadi nomor HP yang bisa dihubungi meowcaller. Dipakai command
// .call DAN tool AI make_call supaya aturannya identik: PN langsung dipakai,
// LID di-resolve dulu via participant grup, lalu via pemetaan LID↔PN
// whatsmeow. Gagal resolve = error jelas, bukan nomor ngawur yang ditelpon.
func ResolveCallPhone(ctx context.Context, mctx *store.MessageContext, target types.JID) (string, error) {
	bare := target.ToNonAD()
	if bare.IsEmpty() {
		return "", fmt.Errorf("target tidak valid")
	}
	if bare.Server == types.DefaultUserServer {
		return bare.User, nil
	}
	if mctx.IsGroup() {
		if info, err := mctx.GroupInfo(ctx); err == nil {
			for _, p := range info.Participants {
				if store.ParticipantMatchesJID(p, target) && !p.PhoneNumber.IsEmpty() {
					return p.PhoneNumber.User, nil
				}
			}
		}
	}
	if pn, err := mctx.Client.Store.GetAltJID(ctx, bare); err == nil && pn.ToNonAD().Server == types.DefaultUserServer {
		return pn.ToNonAD().User, nil
	}
	return "", fmt.Errorf("nomor HP target tidak ditemukan (hanya %s yang tersedia)", bare.User)
}

// ParseJamWITA mem-parsing format "HH.MM" (mis. "12.50") menjadi waktu absolut
// dalam zona WITA. Kalau jam itu sudah lewat hari ini, dijadwalkan untuk besok.
// Di-export supaya tool AI make_call memakai aturan jadwal yang SAMA PERSIS
// dengan command .call (tidak ada duplikasi parsing jam).
func ParseJamWITA(jamStr string) (time.Time, error) {
	hm := strings.SplitN(jamStr, ".", 2)
	if len(hm) != 2 {
		return time.Time{}, fmt.Errorf("gunakan format HH.MM, contoh 12.50")
	}

	hour, err := strconv.Atoi(hm[0])
	if err != nil || hour < 0 || hour > 23 {
		return time.Time{}, fmt.Errorf("jam tidak valid: %q", hm[0])
	}
	minute, err := strconv.Atoi(hm[1])
	if err != nil || minute < 0 || minute > 59 {
		return time.Time{}, fmt.Errorf("menit tidak valid: %q", hm[1])
	}

	now := time.Now().In(witaLocation)
	target := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, witaLocation)

	if !target.After(now) {
		target = target.AddDate(0, 0, 1) // sudah lewat -> jadwalkan besok
	}

	return target, nil
}

// callProgress melacak satu panggilan keluar untuk dilaporkan ke chat dalam
// SATU bubble yang diedit tiap status berubah (bukan banyak bubble baru).
// Callback meowcaller bisa datang dari goroutine berbeda, jadi semua akses
// state dikunci mutex.
type callProgress struct {
	mu         sync.Mutex
	client     *whatsmeow.Client
	chat       types.JID
	msgID      types.MessageID
	phone      string
	acceptedAt time.Time
	ended      bool
}

// edit menulis ulang bubble progres. Gagal edit (mis. pesan terlalu lama
// melewati window edit WA ~15 menit) hanya dicatat ke console — progres
// tetap tercatat di sana, tidak menggagalkan panggilan.
func (p *callProgress) edit(ctx context.Context, text string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.client.SendMessage(ctx, p.chat, p.client.BuildEdit(p.chat, p.msgID, &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String(text)},
	}))
	if err != nil {
		fmt.Printf("⚠️ Gagal edit progres call ke %s: %v\n", p.phone, err)
	}
}

// onRinging dipanggil saat fase RINGING (HP target berdering).
func (p *callProgress) onRinging() {
	p.mu.Lock()
	ringing := p.acceptedAt.IsZero() && !p.ended
	p.mu.Unlock()
	if ringing {
		p.edit(context.Background(), "Berdering ke "+p.phone+"...")
	}
}

// onAccept dipanggil saat target MENGANGKAT: catat waktu, kabari, putar greeting.
func (p *callProgress) onAccept(call *meowcaller.Call) {
	p.mu.Lock()
	if p.ended {
		p.mu.Unlock()
		return
	}
	p.acceptedAt = time.Now()
	phone := p.phone
	p.mu.Unlock()

	fmt.Printf("✅ Panggilan ke %s diterima\n", phone)
	p.edit(context.Background(), "Terhubung dengan "+phone+"...")
	if mp3, err := meowcaller.MP3File("greeting.mp3"); err != nil {
		fmt.Printf("❌ Gagal membuka/decode greeting.mp3: %v\n", err)
	} else if err := call.Play(mp3); err != nil {
		fmt.Printf("❌ Gagal memutar greeting.mp3: %v\n", err)
	}
}

// onEnd dipanggil saat panggilan BERAKHIR dalam kondisi apapun. Kalau sempat
// terhubung, laporkan durasinya; kalau tidak, klasifikasikan alasannya
// (ditolak / tidak dijawab / lainnya) dari reason string server.
func (p *callProgress) onEnd(reason string) {
	p.mu.Lock()
	if p.ended {
		p.mu.Unlock()
		return
	}
	p.ended = true
	acceptedAt := p.acceptedAt
	phone := p.phone
	p.mu.Unlock()

	fmt.Printf("📴 Panggilan ke %s berakhir: %s\n", phone, reason)
	var text string
	if !acceptedAt.IsZero() {
		text = fmt.Sprintf("Panggilan dengan %s berakhir (durasi %s).", phone, formatDuration(time.Since(acceptedAt).Round(time.Second)))
	} else {
		text = fmt.Sprintf("Panggilan ke %s %s.", phone, classifyCallEnd(reason))
	}
	p.edit(context.Background(), text)
}

// classifyCallEnd memetakan reason string server jadi bahasa manusia.
// Reason adalah string protokol bebas (bisa beda tiap kasus), jadi dicocokkan
// longgar per kata kunci — yang tidak dikenali dilaporkan apa adanya supaya
// tetap informatif dan gampang dipetakan belakangan.
func classifyCallEnd(reason string) string {
	r := strings.ToLower(strings.TrimSpace(reason))
	switch {
	case r == "":
		return "berakhir sebelum terhubung"
	case strings.Contains(r, "reject") || strings.Contains(r, "declin") || strings.Contains(r, "busy"):
		return "ditolak"
	case strings.Contains(r, "timeout") || strings.Contains(r, "no-answer") || strings.Contains(r, "noanswer") ||
		strings.Contains(r, "missed") || strings.Contains(r, "unavail") || strings.Contains(r, "not-answered"):
		return "tidak dijawab"
	default:
		return fmt.Sprintf("berakhir sebelum terhubung (alasan: %s)", reason)
	}
}

// CallWithProgress melakukan panggilan WhatsApp keluar ke nomor HP target
// dan melaporkan SELURUH progresnya (memanggil → berdering → terhubung →
// berakhir + durasi / ditolak / tidak dijawab) dalam SATU bubble di chat
// yang diedit tiap status berubah. Dipakai command .call, jadwal tersimpan,
// DAN tool AI make_call — satu implementasi, tiga pintu masuk.
func CallWithProgress(ctx context.Context, client *whatsmeow.Client, chat types.JID, target string) error {
	if callClient == nil {
		return fmt.Errorf("fitur call belum aktif")
	}
	resp, err := client.SendMessage(ctx, chat, &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("Memanggil " + target + "...")},
	})
	if err != nil {
		return fmt.Errorf("gagal kirim bubble progres: %w", err)
	}
	p := &callProgress{client: client, chat: chat, msgID: resp.ID, phone: target}

	call, err := callClient.Call(ctx, target)
	if err != nil {
		p.edit(ctx, "Gagal memanggil "+target+": "+err.Error())
		return nil // sudah dijelaskan di bubble, jangan error ganda
	}

	call.OnStateChange(func(ph meowcaller.CallPhase) {
		if ph == meowcaller.CallPhaseRinging {
			p.onRinging()
		}
	})
	call.OnPeerAccept(func() { p.onAccept(call) })
	call.OnEnd(func(reason string) { p.onEnd(reason) })
	return nil
}

// doCall adalah pembungkus CallWithProgress untuk command .call.
func doCall(ctx context.Context, mctx *store.MessageContext, target string) error {
	return CallWithProgress(ctx, mctx.Client, mctx.Chat, target)
}

// ScheduleCall menyimpan jadwal panggilan ke SQLite (TAHAN RESTART) lalu
// memasang timer-nya. Mengembalikan ID jadwal. Saat waktunya tiba,
// panggilan dijalankan dengan CallWithProgress di chat yang sama.
func ScheduleCall(chat types.JID, phone string, at time.Time) (int64, error) {
	ctx := context.Background()
	id, err := store.Groups().AddScheduledCall(ctx, phone, chat.String(), at.Unix())
	if err != nil {
		return 0, err
	}
	armScheduledCall(id, chat, phone, time.Until(at))
	return id, nil
}

// armScheduledCall memasang timer in-memory untuk satu jadwal. Status
// ditulis "done" SEBELUM eksekusi (lihat MarkCallStatus) supaya jadwal yang
// sama tidak pernah jalan dobel.
func armScheduledCall(id int64, chat types.JID, phone string, delay time.Duration) {
	if delay < 0 {
		delay = 0
	}
	go func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		<-timer.C
		ctx := context.Background()
		_ = store.Groups().MarkCallStatus(ctx, id, store.ScheduledCallDone)
		if waClientForCalls == nil || callClient == nil {
			fmt.Printf("⚠️ Jadwal call #%d ke %s dilewati: fitur call belum aktif.\n", id, phone)
			return
		}
		if err := CallWithProgress(ctx, waClientForCalls, chat, phone); err != nil {
			fmt.Printf("⚠️ Jadwal call #%d ke %s gagal start: %v\n", id, phone, err)
		}
	}()
}

// RestoreScheduledCalls dibaca sekali saat startup (main.go, setelah
// connect): semua jadwal pending dipasang ulang timernya. Jadwal yang
// waktunya sudah lewat selama bot mati ditandai "missed" + diberitahu ke
// chat-nya (TIDAK langsung ditelpon — menelpon diam-diam di jam yang salah
// lebih buruk daripada memberi tahu).
func RestoreScheduledCalls() {
	if waClientForCalls == nil {
		return
	}
	ctx := context.Background()
	pending, err := store.Groups().ListPendingCalls(ctx)
	if err != nil {
		fmt.Printf("⚠️ Gagal baca jadwal call tersimpan: %v\n", err)
		return
	}
	now := time.Now()
	for _, c := range pending {
		chat, err := types.ParseJID(c.ChatJID)
		if err != nil {
			fmt.Printf("⚠️ Jadwal call #%d JID rusak (%s), ditandai missed.\n", c.ID, c.ChatJID)
			_ = store.Groups().MarkCallStatus(ctx, c.ID, store.ScheduledCallMissed)
			continue
		}
		at := time.Unix(c.ScheduledAt, 0)
		if !at.After(now) {
			_ = store.Groups().MarkCallStatus(ctx, c.ID, store.ScheduledCallMissed)
			_, _ = waClientForCalls.SendMessage(ctx, chat, &waE2E.Message{
				ExtendedTextMessage: &waE2E.ExtendedTextMessage{
					Text: proto.String(fmt.Sprintf("Jadwal panggilan ke %s (pukul %s WITA) terlewat karena bot mati. Buat ulang kalau masih perlu.", c.TargetPhone, at.In(witaLocation).Format("15:04"))),
				},
			})
			continue
		}
		armScheduledCall(c.ID, chat, c.TargetPhone, time.Until(at))
	}
	if len(pending) > 0 {
		fmt.Printf("⏳ %d jadwal panggilan dipulihkan.\n", len(pending))
	}
}
