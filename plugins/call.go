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

	"wabot-go/internal/logfmt"
	"wabot-go/internal/store"
	"wabot-go/internal/timex"
)

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
		logfmt.Call("Panggilan masuk dari %s\n", call.Peer().String())

		if err := call.Answer(); err != nil {
			logfmt.Error("Gagal menjawab panggilan: %v\n", err)
			return
		}

		if mp3, err := meowcaller.MP3File("greeting.mp3"); err != nil {
			logfmt.Error("Gagal membuka/decode greeting.mp3: %v\n", err)
		} else if err := call.Play(mp3); err != nil {
			logfmt.Error("Gagal memutar greeting.mp3: %v\n", err)
		}

		call.OnEnd(func(reason string) {
			logfmt.Call("Panggilan berakhir: %s\n", reason)
		})
	})
}

// RegisterCall mendaftarkan command ".call" KHUSUS OWNER untuk melakukan
// panggilan WhatsApp. Target bisa mention (@user), reply pesan target, atau
// nomor langsung (bebas format: 628xxx, 0822..., +62 822-1111-2222).
// Bentuk ".call <target>|<jam>" (mis. "12.50", zona waktu bot) menjadwalkan panggilan
// pada jam tersebut hari ini (atau besok kalau jam itu sudah lewat).
// Jadwal disimpan in-memory saja — hilang kalau bot restart.
func RegisterCall(r *Registry) {
	r.Register(Command{
		Name:        "call",
		Description: "Panggil WhatsApp (owner). .call @user / 628xxx (+|jam jadwal, +list, +cancel <id>)",
		Category:    CategoryOwner,
		Handler: func(ctx context.Context, mctx *store.MessageContext) error {
			if msg := requireOwner(mctx); msg != "" {
				return store.ReplyText(ctx, mctx, msg)
			}
			if callClient == nil {
				return store.ReplyText(ctx, mctx, "Fitur call belum aktif.")
			}

			// Subcommand manajemen jadwal: ".call list" dan ".call cancel
			// <id>" dicek SEBELUM resolve target supaya kata-katanya tidak
			// dianggap nomor telepon.
			if len(mctx.Args) > 0 {
				switch strings.ToLower(strings.TrimSpace(mctx.Args[0])) {
				case "list":
					return handleCallList(ctx, mctx)
				case "cancel":
					return handleCallCancel(ctx, mctx)
				}
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
				"Panggilan ke %s dijadwalkan pukul %s %s (%s lagi, tersimpan — tahan restart).",
				target, at.Format("15:04"), timex.Name(), time.Until(at).Round(time.Second),
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

// normPhoneDigits mengambil digit nomor HP + normalisasi awalan 0 lokal
// jadi 62 (format Indonesia). Dipakai pembanding nomor bot vs target.
func normPhoneDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	d := b.String()
	if strings.HasPrefix(d, "0") {
		d = "62" + d[1:]
	}
	return d
}

// IsBotPhoneTarget true bila nomor target adalah nomor bot sendiri (nomor
// login di Store.ID). Dipakai menolak telepon-diri-sendiri yang sia-sia.
// Fail-open bila identitas bot belum diketahui (belum login): loloskan +
// catat, jangan blokir buta.
func IsBotPhoneTarget(client *whatsmeow.Client, phone string) bool {
	want := normPhoneDigits(phone)
	if want == "" {
		return false
	}
	if client == nil || client.Store == nil {
		return false
	}
	id := client.Store.ID
	if id == nil {
		logfmt.Warn("IsBotPhoneTarget: Store.ID nil, lewatkan cek nomor bot.\n")
		return false
	}
	return normPhoneDigits(id.ToNonAD().User) == want
}

// ParseJamWITA mem-parsing format "HH.MM" (mis. "12.50") menjadi waktu absolut
// dalam zona aktif timex. Kalau jam itu sudah lewat hari ini, dijadwalkan untuk besok.
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

	now := timex.Now()
	target := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, timex.Zone())

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
	// endKind adalah klasifikasi akhir panggilan (audit eskalasi-ditolak):
	// diisi di onEnd supaya pemanggil yang menunggu (CallAndWait) bisa
	// membedakan "ditolak" (user merespons → berhenti) dari "tak terjawab".
	endKind CallEndKind
}

// CallEndKind mengklasifikasikan akhir sebuah panggilan keluar.
type CallEndKind string

const (
	// CallEndAnswered: target mengangkat (user merespons).
	CallEndAnswered CallEndKind = "diangkat"
	// CallEndRejected: target menolak (user merespons — setara dijawab
	// untuk eskalasi: tidak perlu menelepon lagi).
	CallEndRejected CallEndKind = "ditolak"
	// CallEndMissed: tak terjawab/timeout (boleh coba lagi).
	CallEndMissed CallEndKind = "tak-terjawab"
	// CallEndUnknown: tak diketahui (error/gagal awal).
	CallEndUnknown CallEndKind = "tak-diketahui"
)

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
		logfmt.Warn("Gagal edit progres call ke %s: %v\n", p.phone, err)
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

	logfmt.Call("Panggilan ke %s diterima\n", phone)
	p.edit(context.Background(), "Terhubung dengan "+phone+"...")
	if mp3, err := meowcaller.MP3File("greeting.mp3"); err != nil {
		logfmt.Error("Gagal membuka/decode greeting.mp3: %v\n", err)
	} else if err := call.Play(mp3); err != nil {
		logfmt.Error("Gagal memutar greeting.mp3: %v\n", err)
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
	kind := p.endKind
	p.mu.Unlock()

	logfmt.Call("Panggilan ke %s berakhir: %s\n", phone, reason)
	var text string
	if !acceptedAt.IsZero() {
		text = fmt.Sprintf("Panggilan dengan %s berakhir (durasi %s).", phone, formatDuration(time.Since(acceptedAt).Round(time.Second)))
		kind = CallEndAnswered
	} else {
		classified := classifyCallEnd(reason)
		text = fmt.Sprintf("Panggilan ke %s %s.", phone, classified)
		switch classified {
		case "ditolak":
			kind = CallEndRejected
		case "tidak dijawab":
			kind = CallEndMissed
		default:
			kind = CallEndUnknown
		}
	}
	p.mu.Lock()
	p.endKind = kind
	p.mu.Unlock()
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
	// Audit telepon-diri: menolak menelepon nomor bot sendiri (sia-sia).
	if IsBotPhoneTarget(client, target) {
		return fmt.Errorf("tidak bisa menelepon nomor bot sendiri")
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

// CallAndWait melakukan panggilan WhatsApp keluar lalu MENUNGGU sampai
// berakhir, dan melaporkan apakah target MENGANGKAT (answered=true) serta
// klasifikasi akhirnya (endKind: diangkat/ditolak/tak-terjawab/
// tak-diketahui). Dipakai loop eskalasi reminder yang butuh keputusan
// lanjut-vs-berhenti berdasarkan jawaban — beda dari CallWithProgress yang
// kembali seketika setelah panggilan ditempatkan (tanpa tahu hasilnya).
//
// Bubble progres SAMA persis (berdering/terhubung/berakhir) supaya UX
// konsisten. Error hanya untuk kegagalan fatal (fitur mati, bubble tak
// terkirim, context dibatalkan, telepon-diri, timeout); kegagalan
// menempatkan panggilan dilaporkan lewat bubble + return (false,
// CallEndUnknown, nil) agar pemanggil berhenti dengan pesan, bukan retry
// buta.
// callWaitTimeout adalah batas maksimum menunggu status akhir panggilan di
// CallAndWait. Normalnya panggilan selesai (diangkat/ditolak/tak terjawab)
// dalam 1–2 menit; 3 menit memberi ruang untuk nada sambung panjang tanpa
// membiarkan goroutine + lock eskalasi owner macet selamanya.
const callWaitTimeout = 3 * time.Minute

func CallAndWait(ctx context.Context, client *whatsmeow.Client, chat types.JID, target string) (bool, CallEndKind, error) {
	if callClient == nil {
		return false, CallEndUnknown, fmt.Errorf("fitur call belum aktif")
	}
	// Audit telepon-diri: jaring pengaman terakhir (lapis 1 ada di
	// CallWithProgress/ScheduleCall).
	if IsBotPhoneTarget(client, target) {
		return false, CallEndUnknown, fmt.Errorf("tidak bisa menelepon nomor bot sendiri")
	}
	resp, err := client.SendMessage(ctx, chat, &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("Memanggil " + target + "...")},
	})
	if err != nil {
		return false, CallEndUnknown, fmt.Errorf("gagal kirim bubble progres: %w", err)
	}
	p := &callProgress{client: client, chat: chat, msgID: resp.ID, phone: target}
	done := make(chan struct{}, 1)

	call, err := callClient.Call(ctx, target)
	if err != nil {
		p.edit(ctx, "Gagal memanggil "+target+": "+err.Error())
		return false, CallEndUnknown, nil
	}

	call.OnStateChange(func(ph meowcaller.CallPhase) {
		if ph == meowcaller.CallPhaseRinging {
			p.onRinging()
		}
	})
	call.OnPeerAccept(func() { p.onAccept(call) })
	call.OnEnd(func(reason string) {
		p.onEnd(reason)
		select {
		case done <- struct{}{}:
		default:
		}
	})

	select {
	case <-done:
	case <-ctx.Done():
		return false, CallEndUnknown, ctx.Err()
	case <-time.After(callWaitTimeout):
		// Jaring pengaman audit T1: kalau OnEnd tidak pernah fire (bug
		// lib/jaringan gantung), jangan tunggu selamanya sambil memegang
		// lock eskalasi owner — nyatakan gagal supaya lock lepas dan DM
		// "Gagal menelepon" terkirim oleh pemanggil.
		p.edit(ctx, "Panggilan ke "+target+" tidak mendapat respons (timeout).")
		return false, CallEndMissed, fmt.Errorf("panggilan timeout setelah %s tanpa status akhir", callWaitTimeout)
	}
	// acceptedAt/endKind hanya ditulis onAccept/onEnd (dijaga ended+mutex
	// di dalamnya); setelah sinyal done, nilainya final dan aman dibaca.
	p.mu.Lock()
	answered := !p.acceptedAt.IsZero()
	kind := p.endKind
	p.mu.Unlock()
	if answered {
		kind = CallEndAnswered
	} else if kind == "" {
		kind = CallEndUnknown
	}
	return answered, kind, nil
}

// doCall adalah pembungkus CallWithProgress untuk command .call.
func doCall(ctx context.Context, mctx *store.MessageContext, target string) error {
	return CallWithProgress(ctx, mctx.Client, mctx.Chat, target)
}

// handleCallList menangani ".call list": semua jadwal pending di semua chat,
// terurut waktu terdekat, masing-masing dengan ID untuk .call cancel.
func handleCallList(ctx context.Context, mctx *store.MessageContext) error {
	pending, err := store.Groups().ListPendingCalls(ctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal baca jadwal: %v", err))
	}
	if len(pending) == 0 {
		return store.ReplyText(ctx, mctx, "Tidak ada jadwal panggilan. Buat via `.call <target>|<jam>` (WITA).")
	}
	var sb strings.Builder
	sb.WriteString("Jadwal panggilan:\n")
	for _, c := range pending {
		at := time.Unix(c.ScheduledAt, 0).In(timex.Zone())
		left := time.Until(time.Unix(c.ScheduledAt, 0)).Round(time.Minute)
		if left < 0 {
			left = 0
		}
		chat := c.ChatJID
		if i := strings.Index(chat, "@"); i >= 0 {
			chat = chat[:i]
		}
		fmt.Fprintf(&sb, "#%d: %s @ %s (%s lagi) [%s]\n",
			c.ID, c.TargetPhone, at.Format("02 Jan 15:04"), left, chat)
	}
	sb.WriteString("Batalkan via `.call cancel <id>`.")
	return store.ReplyText(ctx, mctx, sb.String())
}

// handleCallCancel menangani ".call cancel <id>": jadwal pending jadi
// cancelled — timer yang sudah terpasang tidak akan menelpon karena
// armScheduledCall mengecek status saat menyala.
func handleCallCancel(ctx context.Context, mctx *store.MessageContext) error {
	if len(mctx.Args) < 2 {
		return store.ReplyText(ctx, mctx, "Sertakan ID jadwal. Contoh: .call cancel 3 (lihat ID via `.call list`).")
	}
	id, err := strconv.ParseInt(strings.TrimSpace(mctx.Args[1]), 10, 64)
	if err != nil || id <= 0 {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("ID tidak valid: %q. Lihat ID via `.call list`.", mctx.Args[1]))
	}
	removed, err := store.Groups().CancelScheduledCall(ctx, id)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal membatalkan: %v", err))
	}
	if !removed {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Jadwal #%d tidak ditemukan (mungkin sudah jalan/lewat/batal).", id))
	}
	return store.ReplyText(ctx, mctx, fmt.Sprintf("Jadwal #%d dibatalkan.", id))
}

// ScheduleCall menyimpan jadwal panggilan ke SQLite (TAHAN RESTART) lalu
// memasang timer-nya. Mengembalikan ID jadwal. Saat waktunya tiba,
// panggilan dijalankan dengan CallWithProgress di chat yang sama.
// Menolak jadwal ke nomor bot sendiri (audit telepon-diri).
func ScheduleCall(chat types.JID, phone string, at time.Time) (int64, error) {
	if waClientForCalls != nil && IsBotPhoneTarget(waClientForCalls, phone) {
		return 0, fmt.Errorf("tidak bisa menelepon nomor bot sendiri")
	}
	ctx := context.Background()
	id, err := store.Groups().AddScheduledCall(ctx, phone, chat.String(), at.Unix())
	if err != nil {
		return 0, err
	}
	armScheduledCall(id, chat, phone, time.Until(at))
	return id, nil
}

// armScheduledCall memasang timer in-memory untuk satu jadwal. Saat timer
// menyala, status dibaca ULANG dari DB — jadwal yang keburu di-cancel
// (.call cancel) dilewati diam-diam (timer tak bisa ditarik mundur, tapi
// panggilan tidak jalan). Status ditulis "done" SEBELUM eksekusi (lihat
// MarkCallStatus) supaya jadwal yang sama tidak pernah jalan dobel.
func armScheduledCall(id int64, chat types.JID, phone string, delay time.Duration) {
	if delay < 0 {
		delay = 0
	}
	go func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		<-timer.C
		ctx := context.Background()
		if status, err := store.Groups().CallStatus(ctx, id); err != nil || status != store.ScheduledCallPending {
			return
		}
		_ = store.Groups().MarkCallStatus(ctx, id, store.ScheduledCallDone)
		if waClientForCalls == nil || callClient == nil {
			logfmt.Warn("Jadwal call #%d ke %s dilewati: fitur call belum aktif.\n", id, phone)
			return
		}
		if err := CallWithProgress(ctx, waClientForCalls, chat, phone); err != nil {
			logfmt.Warn("Jadwal call #%d ke %s gagal start: %v\n", id, phone, err)
		}
		// Eksekusi selesai (sukses/gagal — status sudah done): sapu baris
		// selesai >24 jam supaya tidak menunggu restart berikutnya.
		if n, perr := store.Groups().PruneOldCalls(ctx); perr != nil {
			logfmt.Warn("Gagal prune jadwal call lama: %v\n", perr)
		} else if n > 0 {
			logfmt.Clean("%d jadwal call lama dibersihkan.\n", n)
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
		logfmt.Warn("Gagal baca jadwal call tersimpan: %v\n", err)
		return
	}
	now := time.Now()
	// Bersihkan baris selesai >24 jam (keputusan owner: tanpa riwayat).
	if n, perr := store.Groups().PruneOldCalls(ctx); perr != nil {
		logfmt.Warn("Gagal prune jadwal call lama: %v\n", perr)
	} else if n > 0 {
		logfmt.Clean("%d jadwal call lama dibersihkan.\n", n)
	}
	for _, c := range pending {
		chat, err := types.ParseJID(c.ChatJID)
		if err != nil {
			logfmt.Warn("Jadwal call #%d JID rusak (%s), ditandai missed.\n", c.ID, c.ChatJID)
			_ = store.Groups().MarkCallStatus(ctx, c.ID, store.ScheduledCallMissed)
			continue
		}
		at := time.Unix(c.ScheduledAt, 0)
		if !at.After(now) {
			_ = store.Groups().MarkCallStatus(ctx, c.ID, store.ScheduledCallMissed)
			_, _ = waClientForCalls.SendMessage(ctx, chat, &waE2E.Message{
				ExtendedTextMessage: &waE2E.ExtendedTextMessage{
					Text: proto.String(fmt.Sprintf("Jadwal panggilan ke %s (pukul %s %s) terlewat karena bot mati. Buat ulang kalau masih perlu.", c.TargetPhone, at.In(timex.Zone()).Format("15:04"), timex.Name())),
				},
			})
			continue
		}
		armScheduledCall(c.ID, chat, c.TargetPhone, time.Until(at))
	}
	if len(pending) > 0 {
		logfmt.Plain("%d jadwal panggilan dipulihkan.\n", len(pending))
	}
}
