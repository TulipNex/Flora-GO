package bridge

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"wabot-go/internal/ai"
	"wabot-go/internal/logfmt"
	"wabot-go/internal/store"
	"wabot-go/internal/timex"
	"wabot-go/plugins"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types"
	"google.golang.org/protobuf/proto"
)

// Batas pengaman tool reminder.
const (
	reminderMaxPendingPerChat = 10
	reminderMaxMessageRunes   = 500
	reminderMaxHorizon        = 30 * 24 * time.Hour // 30 hari
	reminderReplyWindow       = 60 * time.Second    // jeda fire -> cek balasan DM
	reminderCallGap           = 10 * time.Second    // jeda antar percobaan telepon
	reminderMaxCallAttempts   = 3
)

// Zona tampil jam (WIB/WITA/WIT) terpusat di internal/timex via env
// BOT_TIMEZONE — jam disimpan unix absolut, zona hanya soal tampil.

// waClientForReminders adalah client whatsmeow untuk pengiriman DM + telepon
// eskalasi dari goroutine timer (yang tidak punya mctx). Diisi sekali via
// InitReminderClient di main.go, pola sama seperti waClientForCalls milik
// fitur call (plugins/call.go).
var waClientForReminders *whatsmeow.Client

// InitReminderClient memasang client untuk scheduler reminder. WAJIB
// dipanggil di main.go (seperti InitCallClient) — tanpanya timer fire hanya
// bisa menandai status tanpa mengirim.
func InitReminderClient(wa *whatsmeow.Client) {
	waClientForReminders = wa
}

// reminderReplySeen mencatat unix timestamp DM terakhir per JID owner,
// untuk mendeteksi "owner membalas" dalam window eskalasi. In-memory saja
// (hilang saat restart — acceptable: eskalasi yang sedang berjalan juga
// in-memory; reminder pending-nya tetap survive via DB).
var (
	reminderReplyMu   sync.Mutex
	reminderReplySeen = map[string]int64{}
)

// NoteOwnerDMActivity mencatat aktivitas DM dari satu JID (dipanggil
// handler untuk tiap pesan DM owner, sebelum routing — lihat
// internal/handler/handler.go). SenderAlt ikut dicatat supaya format JID
// yang berbeda tetap cocok dengan creator_jid tersimpan.
func NoteOwnerDMActivity(sender, senderAlt types.JID) {
	now := time.Now().Unix()
	reminderReplyMu.Lock()
	defer reminderReplyMu.Unlock()
	if s := sender.String(); s != "" {
		reminderReplySeen[s] = now
	}
	if !senderAlt.IsEmpty() {
		if s := senderAlt.String(); s != "" {
			reminderReplySeen[s] = now
		}
	}
}

// ownerRepliedSince true kalau JID (atau rain-format-nya) tercatat aktif
// setelah timestamp acuan.
func ownerRepliedSince(jidStr string, sinceUnix int64) bool {
	reminderReplyMu.Lock()
	defer reminderReplyMu.Unlock()
	if t, ok := reminderReplySeen[jidStr]; ok && t >= sinceUnix {
		return true
	}
	// Coba cocokkan user-part juga (LID vs PN flip-flop).
	for k, t := range reminderReplySeen {
		if t >= sinceUnix && sameBareUser(k, jidStr) {
			return true
		}
	}
	return false
}

// sameBareUser true kalau dua string JID punya user-part sama (abaikan
// server/device) — toleransi flip LID/PN.
func sameBareUser(a, b string) bool {
	ua := a
	if i := strings.Index(ua, "@"); i >= 0 {
		ua = ua[:i]
	}
	ub := b
	if i := strings.Index(ub, "@"); i >= 0 {
		ub = ub[:i]
	}
	ua = strings.SplitN(ua, ":", 2)[0]
	ub = strings.SplitN(ub, ":", 2)[0]
	return ua != "" && ua == ub
}

func init() {
	ai.RegisterTool(ai.ToolDefinition{
		Name: "reminder",
		Description: "Pengingat OWNER bot (wajib owner). " +
			"JANGAN panggil sebelum info lengkap — waktu/isi/perlu-telepon belum jelas? TANYAKAN dulu satu per satu. " +
			"Actions: set (message + when; call_after=true bila minta ditelepon), list, edit (sebutkan id), cancel (id). " +
			"Terkirim ke DM pembuat saat tiba; call_after=true + owner diam 1 menit → telepon s/d 3x.",
		Parameters: ai.ParameterSchema{
			Type: "OBJECT",
			Properties: map[string]ai.PropertySchema{
				"action": {
					Type:        "STRING",
					Description: "Wajib: set, list, edit, atau cancel.",
				},
				"message": {
					Type:        "STRING",
					Description: "Isi pengingat (set; edit: kosongkan = tak diubah). Cth: rapat dengan Budi",
				},
				"when": {
					Type:        "STRING",
					Description: "Waktu (set; edit opsional): HH.MM/HH:MM zona bot hari ini (atau besok bila lewat, cth 17.00), atau relatif: '10 menit lagi', '2 jam lagi'.",
				},
				"call_after": {
					Type:        "BOOLEAN",
					Description: "True bila minta ditelepon juga (set; edit: kosongkan = tak diubah).",
				},
				"id": {
					Type:        "NUMBER",
					Description: "ID pengingat (untuk edit/cancel; lihat dari list).",
				},
			},
			Required: []string{"action"},
		},
		Execute: executeReminder,
	})
}

// executeReminder mengeksekusi action reminder untuk owner pembuat.
func executeReminder(actx *ai.AgentContext, args map[string]interface{}) (string, error) {
	mctx, err := extractMctx(actx)
	if err != nil {
		return "", err
	}
	if err := requireOwnerTool(mctx); err != nil {
		return "", err
	}
	ctx := actx.Context
	if ctx == nil {
		ctx = context.Background()
	}
	creatorJID := mctx.Sender.String()

	action, _ := args["action"].(string)
	action = strings.ToLower(strings.TrimSpace(action))

	switch action {
	case "set":
		return reminderSet(ctx, mctx, creatorJID, args)
	case "list":
		return reminderList(ctx, creatorJID)
	case "edit":
		return reminderEdit(ctx, creatorJID, args)
	case "cancel":
		return reminderCancel(ctx, creatorJID, args)
	default:
		return "", fmt.Errorf("action tidak dikenal %q (pakai set/list/edit/cancel)", action)
	}
}

// reminderSet membuat pengingat baru: validasi → resolve nomor HP kreator
// (untuk eskalasi telepon) → simpan → pasang timer.
func reminderSet(ctx context.Context, mctx *store.MessageContext, creatorJID string, args map[string]interface{}) (string, error) {
	message, _ := args["message"].(string)
	message = strings.TrimSpace(message)
	if message == "" {
		return "", fmt.Errorf("message kosong — tanyakan dulu mau diingatkan apa")
	}
	if runes := []rune(message); len(runes) > reminderMaxMessageRunes {
		return "", fmt.Errorf("message kepanjangan (%d), maks %d karakter", len(runes), reminderMaxMessageRunes)
	}
	whenRaw, _ := args["when"].(string)
	at, err := parseReminderTime(strings.TrimSpace(whenRaw))
	if err != nil {
		return "", fmt.Errorf("waktu tidak dikenali (%v) — tanyakan format HH.MM atau 'X menit/jam lagi'", err)
	}
	if time.Until(at) <= 0 {
		return "", fmt.Errorf("waktu sudah lewat — tanyakan waktu yang akan datang")
	}
	if time.Until(at) > reminderMaxHorizon {
		return "", fmt.Errorf("maksimal 30 hari ke depan")
	}
	n, err := store.Groups().CountPendingReminders(ctx, mctx.Chat.String())
	if err != nil {
		return "", fmt.Errorf("gagal cek kuota: %w", err)
	}
	if n >= reminderMaxPendingPerChat {
		return "", fmt.Errorf("sudah ada %d pengingat pending di chat ini (maks %d), batalkan dulu yang lama", n, reminderMaxPendingPerChat)
	}

	callAfter := parseBoolArg(args["call_after"])

	// Resolve nomor HP kreator SEKARANG (mctx segar) supaya eskalasi nanti
	// tidak perlu resolve — LID grup dipetakan via store, PN dipakai langsung.
	creatorPhone := ""
	if callAfter {
		phone, rerr := resolveOwnerPhone(ctx, mctx)
		if rerr != nil {
			return "", fmt.Errorf("butuh telepon tapi nomor tak terpetakan: %v", rerr)
		}
		creatorPhone = phone
	}

	id, err := store.Groups().AddReminder(ctx, mctx.Chat.String(), creatorJID, creatorPhone, message, at.Unix(), callAfter)
	if err != nil {
		return "", fmt.Errorf("gagal menyimpan pengingat: %w", err)
	}
	armReminder(id, time.Until(at))

	callNote := ""
	if callAfter {
		callNote = " + telepon bila diam 1 menit"
	}
	return fmt.Sprintf("Berhasil: pengingat #%d \"%s\" pada %s %s%s, dikirim ke DM kamu saat tiba.",
		id, message, at.In(timex.Zone()).Format("02-01 15:04"), timex.Name(), callNote), nil
}

// reminderList menampilkan pengingat pending milik owner ini.
func reminderList(ctx context.Context, creatorJID string) (string, error) {
	list, err := store.Groups().ListPendingReminders(ctx, creatorJID)
	if err != nil {
		return "", fmt.Errorf("gagal baca pengingat: %w", err)
	}
	if len(list) == 0 {
		return "Tidak ada pengingat aktif milikmu.", nil
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Pengingat aktif milikmu (%d):", len(list)))
	for _, r := range list {
		at := time.Unix(r.TriggerAt, 0).In(timex.Zone())
		callMark := ""
		if r.CallAfter {
			callMark = " +telp"
		}
		fmt.Fprintf(&sb, "\n#%d • %s %s%s • %s", r.ID, at.Format("02-01 15:04"), timex.Name(), callMark, r.Message)
	}
	return sb.String(), nil
}

// reminderEdit mengubah pengingat pending milik owner ini.
func reminderEdit(ctx context.Context, creatorJID string, args map[string]interface{}) (string, error) {
	id := argID(args["id"])
	if id <= 0 {
		return "", fmt.Errorf("id tidak valid — lihat dari list dulu")
	}
	message, _ := args["message"].(string)
	message = strings.TrimSpace(message)
	if runes := []rune(message); message != "" && len(runes) > reminderMaxMessageRunes {
		return "", fmt.Errorf("message kepanjangan (%d), maks %d karakter", len(runes), reminderMaxMessageRunes)
	}
	var triggerAt int64
	whenRaw, hasWhen := args["when"].(string)
	if hasWhen && strings.TrimSpace(whenRaw) != "" {
		at, err := parseReminderTime(strings.TrimSpace(whenRaw))
		if err != nil {
			return "", fmt.Errorf("waktu tidak dikenali (%v)", err)
		}
		if time.Until(at) <= 0 {
			return "", fmt.Errorf("waktu sudah lewat")
		}
		if time.Until(at) > reminderMaxHorizon {
			return "", fmt.Errorf("maksimal 30 hari ke depan")
		}
		triggerAt = at.Unix()
	}
	callAfter, callSet := false, false
	if raw, ok := args["call_after"]; ok && raw != nil {
		callAfter, callSet = parseBoolArg(raw), true
	}
	// Timer lama tidak perlu dicabut: saat fire, baris dibaca ULANG dari DB
	// (lihat fireReminder), sehingga edit selalu memakai data terbaru.
	// Bila waktu diubah, pasang timer tambahan untuk jadwal baru (timer lama
	// akan no-op karena trigger_at di DB sudah beda — dicek via token).
	if triggerAt > 0 {
		armReminderAt(id, triggerAt, time.Until(time.Unix(triggerAt, 0)))
	}
	ok, err := store.Groups().UpdateReminder(ctx, id, creatorJID, message, triggerAt, callAfter, callSet)
	if err != nil {
		return "", fmt.Errorf("gagal ubah pengingat: %w", err)
	}
	if !ok {
		return "", fmt.Errorf("pengingat #%d tidak ditemukan / bukan milikmu / sudah tidak pending", id)
	}
	return fmt.Sprintf("Berhasil: pengingat #%d diperbarui.", id), nil
}

// reminderCancel membatalkan pengingat milik owner ini.
func reminderCancel(ctx context.Context, creatorJID string, args map[string]interface{}) (string, error) {
	id := argID(args["id"])
	if id <= 0 {
		return "", fmt.Errorf("id tidak valid — lihat dari list dulu")
	}
	ok, err := store.Groups().CancelReminder(ctx, id, creatorJID)
	if err != nil {
		return "", fmt.Errorf("gagal batalkan: %w", err)
	}
	if !ok {
		return "", fmt.Errorf("pengingat #%d tidak ditemukan / bukan milikmu / sudah tidak pending", id)
	}
	return fmt.Sprintf("Berhasil: pengingat #%d dibatalkan.", id), nil
}

// argID membaca id dari NUMBER (float64 JSON) maupun STRING angka.
func argID(raw interface{}) int64 {
	switch v := raw.(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return n
	default:
		return 0
	}
}

// parseBoolArg membaca boolean dari bool Go maupun string ("true"/"1"/"ya"/"on"...).
func parseBoolArg(raw interface{}) bool {
	switch v := raw.(type) {
	case bool:
		return v
	case string:
		s := strings.ToLower(strings.TrimSpace(v))
		return s == "true" || s == "1" || s == "ya" || s == "yes" || s == "on" || s == "aktif"
	default:
		return false
	}
}

// relTimeRe mengenali "10 menit lagi", "2 jam lagi", "1 hari lagi"
// (angka + satuan + kata "lagi" opsional).
var relTimeRe = regexp.MustCompile(`(?i)^\s*(\d+)\s*(detik|menit|jam|hari)\b`)

// colonTimeRe mengenali "17:00" (kebiasaan Indonesia) untuk dinormalisasi
// jadi "17.00" sebelum ParseJam (audit M3).
var colonTimeRe = regexp.MustCompile(`^(\d{1,2}):(\d{2})$`)

// parseReminderTime mengubah teks waktu jadi time.Time absolut (zona aktif
// timex untuk format jam): "HH.MM"/"HH:MM" (hari ini, atau besok bila lewat
// — reuse plugins.ParseJam) atau relatif "N menit/jam/hari [lagi]".
func parseReminderTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, fmt.Errorf("waktu kosong")
	}
	if m := colonTimeRe.FindStringSubmatch(raw); m != nil {
		raw = m[1] + "." + m[2]
	}
	if m := relTimeRe.FindStringSubmatch(raw); m != nil {
		n, _ := strconv.Atoi(m[1])
		if n <= 0 {
			return time.Time{}, fmt.Errorf("harus lebih dari 0")
		}
		var d time.Duration
		switch strings.ToLower(m[2]) {
		case "detik":
			d = time.Duration(n) * time.Second
		case "menit":
			d = time.Duration(n) * time.Minute
		case "jam":
			d = time.Duration(n) * time.Hour
		case "hari":
			d = time.Duration(n) * 24 * time.Hour
		}
		return time.Now().Add(d), nil
	}
	return plugins.ParseJam(raw)
}

// resolveOwnerPhone memetakan JID kreator jadi nomor HP (digit) untuk
// telepon eskalasi. PN dipakai langsung; LID dipetakan via store (pola
// ResolveCallPhone). Gagal = error jelas (eskalasi butuh nomor asli).
func resolveOwnerPhone(ctx context.Context, mctx *store.MessageContext) (string, error) {
	sender := mctx.Sender
	if sender.IsEmpty() && mctx.RawEvt != nil {
		sender = mctx.RawEvt.Info.Sender
	}
	bare := sender.ToNonAD()
	// LID (HiddenUserServer) dipetakan ke nomor via store (pola
	// ResolveCallPhone); PN dipakai langsung.
	if bare.Server == types.HiddenUserServer {
		if mctx.Client != nil && mctx.Client.Store != nil {
			if pn, err := mctx.Client.Store.GetAltJID(ctx, bare); err == nil && !pn.IsEmpty() {
				bare = pn.ToNonAD()
			} else {
				return "", fmt.Errorf("JID LID tanpa pemetaan nomor")
			}
		} else {
			return "", fmt.Errorf("JID LID tanpa pemetaan nomor")
		}
	}
	phone := ""
	for _, r := range bare.User {
		if r >= '0' && r <= '9' {
			phone += string(r)
		}
	}
	if phone == "" {
		return "", fmt.Errorf("tidak ada digit nomor")
	}
	return phone, nil
}

// armReminder memasang timer in-memory untuk satu pengingat. Saat timer
// menyala, baris dibaca ULANG dari DB — pengingat yang keburu di-cancel/
// diedit(total berubah) dilewati atau dipakai data baru; timer tak bisa
// ditarik mundur, tapi eksekusi tidak pernah memakai data basi.
func armReminder(id int64, delay time.Duration) {
	if delay < 0 {
		delay = 0
	}
	armReminderAt(id, 0, delay)
}

// armReminderAt varian dengan token trigger_at eksplisit: bila trigger di
// DB sudah beda (karena diedit setelah timer dipasang), timer ini no-op.
// Token 0 = tanpa cek (untuk jadwal baru yang trigger-nya belum bisa basi).
func armReminderAt(id int64, expectTrigger int64, delay time.Duration) {
	if delay < 0 {
		delay = 0
	}
	go func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		<-timer.C
		ctx := context.Background()
		row, err := store.Groups().GetReminderByID(ctx, id)
		if err != nil || row.Status != store.ReminderPending {
			return
		}
		if expectTrigger > 0 && row.TriggerAt != expectTrigger {
			return
		}
		fireReminder(row, false)
	}()
}

// fireReminder mengeksekusi pengingat: klaim atomik (H1), buat teks
// natural via provider, kirim ke DM pembuat, lalu eskalasi telepon bila
// diminta dan BUKAN fire telat-restore. Berjalan di goroutine timer —
// tidak boleh panic/gagal diam-diam tanpa jejak.
func fireReminder(row *store.Reminder, isLate bool) {
	ctx := context.Background()
	if waClientForReminders == nil {
		logfmt.Warn("reminder #%d dilewati: fitur reminder belum aktif.\n", row.ID)
		return
	}
	// Klaim atomik: hanya yang berhasil mengubah pending->done yang boleh
	// lanjut (audit H1 — timer ganda/cancel balapan abort di sini).
	claimed, err := store.Groups().MarkReminderStatus(ctx, row.ID, store.ReminderDone)
	if err != nil {
		logfmt.Warn("reminder #%d gagal tandai done: %v\n", row.ID, err)
		return
	}
	if !claimed {
		return
	}

	text := strings.TrimSpace(row.Message)
	if natural, err := naturalReminderText(ctx, text); err == nil && strings.TrimSpace(natural) != "" {
		text = strings.TrimSpace(natural)
	} else if err != nil {
		logfmt.Warn("reminder #%d naturalisasi gagal, pakai teks asli: %v\n", row.ID, err)
	}

	creatorJID, err := types.ParseJID(row.CreatorJID)
	if err != nil || creatorJID.IsEmpty() {
		logfmt.Warn("reminder #%d JID pembuat invalid (%q): %v\n", row.ID, row.CreatorJID, err)
		return
	}
	sentUnix := time.Now().Unix()
	if _, err := waClientForReminders.SendMessage(ctx, creatorJID.ToNonAD(), &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("⏰ " + text)},
	}); err != nil {
		logfmt.Warn("reminder #%d gagal kirim DM: %v\n", row.ID, err)
		return
	}
	// Fire sukses: sapu baris selesai >24 jam (bot jalan lama tanpa restart
	// tetap bersih; murah — 1 DELETE per fire).
	if n, perr := store.Groups().PruneOldReminders(ctx); perr != nil {
		logfmt.Warn("Gagal prune pengingat lama: %v\n", perr)
	} else if n > 0 {
		logfmt.Clean("%d pengingat lama dibersihkan.\n", n)
	}

	if !row.CallAfter {
		return
	}
	if isLate {
		// Audit H2: fire telat-restore (bot sempat mati) TIDAK eskalasi
		// telepon — membangunkan owner tengah malam untuk info basi
		// adalah bug, bukan fitur.
		logfmt.Info("reminder #%d: fire telat, eskalasi telepon dilewati.\n", row.ID)
		return
	}
	reminderEscalate(row, creatorJID, sentUnix)
}

// naturalReminderText merumuskan ulang isi pengingat jadi 1-2 kalimat
// santai natural ala teman (pola ExtractFacts: 1 panggilan provider tanpa
// tools). Gagal = pemanggil pakai teks asli (fail-soft di sana).
func naturalReminderText(ctx context.Context, message string) (string, error) {
	provider, err := ai.GetActiveProvider(ctx)
	if err != nil {
		return "", err
	}
	result, err := provider.Generate(ctx, ai.GenerateParams{
		SystemPrompt: "Kamu Flora, bot WhatsApp yang santai seperti teman dekat. " +
			"Tugasmu hanya satu: rumuskan ulang info pengingat berikut menjadi 1-2 kalimat pengingat yang natural " +
			"dan hangat dalam bahasa Indonesia sehari-hari (boleh panggil 'Mit' bila cocok). " +
			"JANGAN tambah info baru, JANGAN bertanya, JANGAN panggil tool apa pun — keluarkan hanya kalimat pengingatnya.",
		History: []ai.Content{
			{Role: "user", Parts: []ai.Part{{Text: "Ingatkan: " + message}}},
		},
	})
	if err != nil {
		return "", err
	}
	if len(result.ToolCalls) > 0 {
		return "", fmt.Errorf("model meminta tool saat naturalisasi")
	}
	ai.LogSide("naturalisasi", []ai.AIField{
		{K: "asli", V: message},
	}, result.Text)
	return strings.TrimSpace(result.Text), nil
}

// escalationLocks memastikan eskalasi telepon per owner berjalan SERIAL
// (audit H3): dua pengingat yang fire berdekatan tidak menelpon nomor yang
// sama secara paralel (kacau + boros) — loop kedua antre hingga pertama
// selesai. Map kecil (satu entry per owner), tidak perlu dibersihkan.
var (
	escalationMu    sync.Mutex
	escalationLocks = map[string]*sync.Mutex{}
)

// escalationLock mengembalikan mutex milik satu owner (dibuat on-demand).
func escalationLock(key string) *sync.Mutex {
	escalationMu.Lock()
	defer escalationMu.Unlock()
	if lk, ok := escalationLocks[key]; ok {
		return lk
	}
	lk := &sync.Mutex{}
	escalationLocks[key] = lk
	return lk
}

// reminderEscalate menjalankan eskalasi telepon: tunggu balasan DM 60 detik,
// lalu telepon s/d 3x (jeda 10 detik), berhenti saat diangkat / ada balasan /
// habis 3x. Berjalan di goroutine fire — tidak memblokir apa pun.
func reminderEscalate(row *store.Reminder, creatorJID types.JID, sentUnix int64) {
	ctx := context.Background()
	creatorKey := creatorJID.ToNonAD().String()

	lk := escalationLock(creatorKey)
	lk.Lock()
	defer lk.Unlock()

	time.Sleep(reminderReplyWindow)
	if ownerRepliedSince(creatorKey, sentUnix) || ownerRepliedSince(row.CreatorJID, sentUnix) {
		logfmt.Info("reminder #%d: owner membalas, eskalasi batal.\n", row.ID)
		return
	}

	phone := strings.TrimSpace(row.CreatorPhone)
	if phone == "" {
		_, _ = waClientForReminders.SendMessage(ctx, creatorJID.ToNonAD(), &waE2E.Message{
			ExtendedTextMessage: &waE2E.ExtendedTextMessage{
				Text: proto.String("Pengingat tadi diminta +telepon, tapi nomormu tak terpetakan. Balas chat ini ya!"),
			},
		})
		return
	}

	dmChat := creatorJID.ToNonAD()
	for attempt := 1; attempt <= reminderMaxCallAttempts; attempt++ {
		if ownerRepliedSince(creatorKey, sentUnix) || ownerRepliedSince(row.CreatorJID, sentUnix) {
			logfmt.Info("reminder #%d: owner membalas, eskalasi batal.\n", row.ID)
			return
		}
		logfmt.Call("reminder #%d: telepon percobaan %d/%d ke %s.\n", row.ID, attempt, reminderMaxCallAttempts, phone)
		answered, endKind, err := plugins.CallAndWait(ctx, waClientForReminders, dmChat, phone)
		if err != nil {
			_, _ = waClientForReminders.SendMessage(ctx, dmChat, &waE2E.Message{
				ExtendedTextMessage: &waE2E.ExtendedTextMessage{
					Text: proto.String(fmt.Sprintf("Gagal menelepon (%v), eskalasi berhenti.", err)),
				},
			})
			return
		}
		if answered {
			_, _ = waClientForReminders.SendMessage(ctx, dmChat, &waE2E.Message{
				ExtendedTextMessage: &waE2E.ExtendedTextMessage{
					Text: proto.String("Sip, sudah tersambung. 👍"),
				},
			})
			return
		}
		// Audit eskalasi-ditolak: penolakan = user merespons (setara
		// diangkat untuk eskalasi) → berhenti, tidak menelepon lagi.
		// Hanya tak-terjawab/tak-diketahui yang lanjut ke percobaan berikut.
		if endKind == plugins.CallEndRejected {
			_, _ = waClientForReminders.SendMessage(ctx, dmChat, &waE2E.Message{
				ExtendedTextMessage: &waE2E.ExtendedTextMessage{
					Text: proto.String("Oke, panggilannya ditolak — anggap sudah baca pengingatnya ya."),
				},
			})
			return
		}
		if attempt < reminderMaxCallAttempts {
			time.Sleep(reminderCallGap)
		}
	}
	_, _ = waClientForReminders.SendMessage(ctx, dmChat, &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String(fmt.Sprintf("Sudah %d kali menelepon tapi tak terjawab. Balas chat ini kalau sudah baca pengingatnya ya!", reminderMaxCallAttempts)),
		},
	})
}

// RestoreReminders dipasang ulang sekali saat startup (main.go, setelah
// connect — berdampingan RestoreScheduledCalls): SEMUA yang overdue langsung
// ditandai missed dan DIAM TOTAL (keputusan owner: tidak ada toleransi kirim
// telat, tidak ada DM "terlewat" — pembuat tidak diberi tahu apa pun).
// Satu-satunya jejak adalah log console. Yang belum waktunya dijadwal ulang.
func RestoreReminders() {
	if waClientForReminders == nil {
		return
	}
	ctx := context.Background()
	pending, err := store.Groups().ListPendingReminders(ctx, "")
	if err != nil {
		logfmt.Warn("Gagal baca pengingat tersimpan: %v\n", err)
		return
	}
	now := time.Now()
	restored := 0
	// Bersihkan baris selesai >24 jam (keputusan owner: tanpa riwayat).
	if n, perr := store.Groups().PruneOldReminders(ctx); perr != nil {
		logfmt.Warn("Gagal prune pengingat lama: %v\n", perr)
	} else if n > 0 {
		logfmt.Clean("%d pengingat lama dibersihkan.\n", n)
	}
	for _, r := range pending {
		r := r
		at := time.Unix(r.TriggerAt, 0)
		if !at.After(now) {
			// Guard H1 berlaku di sini juga: hanya yang masih pending
			// yang ditandai missed (bukan menimpa done/cancelled).
			ok, merr := store.Groups().MarkReminderStatus(ctx, r.ID, store.ReminderMissed)
			if merr != nil {
				logfmt.Warn("reminder #%d gagal tandai missed: %v\n", r.ID, merr)
				continue
			}
			if ok {
				logfmt.Info("reminder #%d missed saat restore (diam, tanpa DM).\n", r.ID)
			}
			continue
		}
		armReminder(r.ID, time.Until(at))
		restored++
	}
	if restored > 0 || len(pending) > 0 {
		logfmt.Plain("%d pengingat dipulihkan (%d dijadwal ulang).\n", len(pending), restored)
	}
}
