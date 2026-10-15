package plugins

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/proto/waMsgApplication"
	hstore "github.com/polymorfa/hypermeow/store"
	"github.com/polymorfa/hypermeow/types"
	"github.com/polymorfa/hypermeow/types/events"
	groupRecord "github.com/polymorfa/libsignal-protocol-go/groups/state/record"
	"google.golang.org/protobuf/proto"

	"wabot-go/internal/logfmt"
	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterSPR)
}

// RegisterSPR mendaftarkan command ".spr" (Send Private Reply, owner-only).
// Seperti .call, command ini masuk CategoryOwner: otomatis hidden dari menu
// non-owner dan diblokir di clone (jadibot).
func RegisterSPR(r *Registry) {
	r.Register(Command{
		Name:        "spr",
		Description: "Kirim private reply grup per-target (owner). .spr @target <pesan> [| <decoy>]",
		Category:    CategoryOwner,
		Handler:     handleSPR,
	})
}

// Batas dan perilaku SPR.
const (
	// sprMaxTargets membatasi jumlah target per command. Tiap target =
	// satu pengiriman grup dengan rotasi sender key sendiri, berurutan —
	// tanpa batas, grup besar bisa kena rate-limit / ban.
	sprMaxTargets = 25
	// sprSendDelay jeda antar pengiriman per-target (lihat alasan di atas).
	sprSendDelay = 400 * time.Millisecond
	// sprEntryTTL umur entri decoy/retry. Retry WhatsApp normalnya masuk
	// dalam hitungan detik/menit; 24 jam batas aman sebelum map dibersihkan.
	sprEntryTTL = 24 * time.Hour
	// sprMaxEntries pagu entri in-memory (fail-safe kalau bot dipakai agresif).
	sprMaxEntries = 500
)

// sprEntry menyimpan konteks satu pesan SPR yang sudah dikirim, dipakai
// untuk menjawab retry receipt: target → isi asli, non-target → decoy
// (atau retry-nya ditolak bila tanpa decoy).
type sprEntry struct {
	// targets berisi bare-User (tanpa device/server) semua target, dari
	// KEDUA format identitas (LID dan PN) supaya cocok dengan Sender
	// maupun SenderAlt di receipt.
	targets  map[string]bool
	original *waE2E.Message
	decoy    *waE2E.Message // nil bila tanpa decoy
	hasDecoy bool
	// perSender, bila non-nil (mode SBPR: satu message ID, isi beda per
	// target), memetakan bare-User → pesan personal target itu. Dipakai
	// sebelum logika target/decoy biasa.
	perSender map[string]*waE2E.Message
	created   time.Time
}

var (
	sprMu      sync.Mutex
	sprEntries = map[string]*sprEntry{}
)

func sprKey(chat types.JID, id types.MessageID) string {
	return chat.String() + "\x00" + string(id)
}

// sprRememberTargetUsers membangun set bare-User dari daftar JID target
// (kedua sisi LID/PN bila bisa dipecahkan dari info grup).
func sprRememberTargetUsers(targets []types.JID) map[string]bool {
	out := make(map[string]bool, len(targets)*2)
	for _, t := range targets {
		bare := t.ToNonAD()
		if !bare.IsEmpty() {
			out[bare.User] = true
		}
	}
	return out
}

// sprSenderIsTarget true bila pengirim receipt adalah salah satu target
// (cocok Sender atau SenderAlt — grup mode LID vs PN).
func sprSenderIsTarget(e *sprEntry, sender, senderAlt types.JID) bool {
	if !sender.IsEmpty() && e.targets[sender.ToNonAD().User] {
		return true
	}
	return !senderAlt.IsEmpty() && e.targets[senderAlt.ToNonAD().User]
}

// sprPruneLocked menghapus entri kadaluarsa. Pemanggil WAJIB pegang sprMu.
func sprPruneLocked() {
	if len(sprEntries) < sprMaxEntries {
		return
	}
	now := time.Now()
	for k, e := range sprEntries {
		if now.Sub(e.created) > sprEntryTTL {
			delete(sprEntries, k)
		}
	}
	// Masih penuh (serbuan < TTL): buang yang tertua sampai di bawah pagu.
	for len(sprEntries) >= sprMaxEntries {
		var oldest string
		var oldestAt time.Time
		first := true
		for k, e := range sprEntries {
			if first || e.created.Before(oldestAt) {
				oldest, oldestAt, first = k, e.created, false
			}
		}
		delete(sprEntries, oldest)
	}
}

// InitSPR memasang hook retry untuk SPR pada client. WAJIB dipanggil dari
// main.go sebelum Connect (sejajar InitCallClient): membungkus
// PreRetryCallback (tukar isi per penerima retry) dan GetMessageForRetry
// (fallback isi asli bila cache library sudah terbuang). Callback lama
// yang sudah terpasang tetap dirantai, bukan dioverwrite buta.
var (
	sprInitMu   sync.Mutex
	sprInitDone = map[*whatsmeow.Client]bool{}
)

func InitSPR(wa *whatsmeow.Client) {
	sprInitMu.Lock()
	defer sprInitMu.Unlock()
	if sprInitDone[wa] {
		return
	}
	sprInitDone[wa] = true

	prevRetry := wa.PreRetryCallback
	wa.PreRetryCallback = func(receipt *events.Receipt, id types.MessageID, retryCount int, msg *waE2E.Message) bool {
		if done, allow := sprHandleRetry(receipt, id, msg); done {
			return allow
		}
		if prevRetry != nil {
			return prevRetry(receipt, id, retryCount, msg)
		}
		return true
	}

	prevGet := wa.GetMessageForRetry
	wa.GetMessageForRetry = func(requester, to types.JID, id types.MessageID) *waE2E.Message {
		if m := sprContentFor(requester, to, id); m != nil {
			return m
		}
		if prevGet != nil {
			return prevGet(requester, to, id)
		}
		return nil
	}
}

// sprHandleRetry menjawab satu retry receipt untuk pesan SPR.
// Balikan done=false berarti bukan pesan SPR (teruskan ke callback lama).
//
// PENTING: isi *msg SELALU ditulis ulang eksplisit (bukan cuma saat decoy),
// karena msg adalah pointer ke cache library — jawaban retry sebelumnya
// untuk penerima lain bisa saja sudah menukar isinya (cache pollution).
func sprHandleRetry(receipt *events.Receipt, id types.MessageID, msg *waE2E.Message) (done, allow bool) {
	if msg == nil {
		return false, true
	}
	sprMu.Lock()
	e, ok := sprEntries[sprKey(receipt.Chat, id)]
	sprMu.Unlock()
	if !ok {
		return false, true
	}
	if sprSenderIsTarget(e, receipt.Sender, receipt.SenderAlt) {
		// Mode batch (SBPR): konten personal per pengirim.
		if e.perSender != nil {
			if c := sprLookupPerSender(e, receipt.Sender, receipt.SenderAlt); c != nil {
				skdm := msg.SenderKeyDistributionMessage
				sprSwapMessage(msg, c)
				msg.SenderKeyDistributionMessage = skdm
				return true, true
			}
			// Target yang kontennya belum dibangun (batch masih berjalan):
			// tolak, klien akan retry lagi nanti.
			return true, false
		}
		// Pertahankan SKDM yang ditempel library (target berhak atas kunci).
		skdm := msg.SenderKeyDistributionMessage
		sprSwapMessage(msg, e.original)
		msg.SenderKeyDistributionMessage = skdm
	} else if e.hasDecoy {
		sprSwapMessage(msg, e.decoy)
		// Cabut SKDM kunci terkini yang ditempel library SEBELUM callback
		// (retry.go menempelkannya dulu, baru memanggil PreRetryCallback):
		// untuk 1 target / target terakhir, itu persis kunci yang mengunci
		// bubble asli — kalau ikut terkirim, penerima decoy bisa
		// mendekripsi bubble aslinya juga (bocor lewat pintu belakang).
		msg.SenderKeyDistributionMessage = nil
		logfmt.Warn("SPR: retry %s dari non-target %s dijawab decoy\n", id, receipt.Sender.String())
	} else {
		// Tanpa decoy: retry non-target DITOLAK (tidak dijawab apa-apa).
		// Non-target stuck di "menunggu pesan" selamanya — tidak pernah
		// mendapat isi maupun kunci. Isi cache tidak disentuh supaya retry
		// target berikutnya tetap mendapat pesan asli. Counter retry di
		// library membatasi badai retry (~10x per pengirim per pesan).
		logfmt.Warn("SPR: retry %s dari non-target %s ditolak (tanpa decoy)\n", id, receipt.Sender.String())
		return true, false
	}
	return true, true
}

// sprSwapMessage menukar isi pesan retry dengan salinan src TANPA
// menyalin mutex internal protobuf (go vet copylocks): Reset + Merge.
func sprSwapMessage(dst, src *waE2E.Message) {
	dst.Reset()
	proto.Merge(dst, proto.Clone(src))
}

// sprLookupPerSender mencari konten personal untuk pengirim receipt
// (cocok Sender dulu, lalu SenderAlt). Nil bila tidak ada.
func sprLookupPerSender(e *sprEntry, sender, senderAlt types.JID) *waE2E.Message {
	if !sender.IsEmpty() {
		if c, ok := e.perSender[sender.ToNonAD().User]; ok {
			return c
		}
	}
	if !senderAlt.IsEmpty() {
		if c, ok := e.perSender[senderAlt.ToNonAD().User]; ok {
			return c
		}
	}
	return nil
}

// sprContentFor mengembalikan salinan konten untuk fallback
// GetMessageForRetry: konten personal bila requester terdaftar di batch,
// kalau tidak isi asli entri (nil bila bukan pesan SPR/batch).
func sprContentFor(requester, chat types.JID, id types.MessageID) *waE2E.Message {
	sprMu.Lock()
	defer sprMu.Unlock()
	e, ok := sprEntries[sprKey(chat, id)]
	if !ok {
		return nil
	}
	if e.perSender != nil && !requester.IsEmpty() {
		if c, hit := e.perSender[requester.ToNonAD().User]; hit {
			return proto.Clone(c).(*waE2E.Message)
		}
		return nil
	}
	if e.original == nil {
		return nil
	}
	return proto.Clone(e.original).(*waE2E.Message)
}

// ---------------------------------------------------------------------------
// Helper bersama SPR/SBPR: resolve target, kanonikalisasi, ownID
// ---------------------------------------------------------------------------

// sprParseMentions memetakan @user → JID beneran dari metadata mention.
func sprParseMentions(mctx *store.MessageContext) []types.JID {
	var mentions []types.JID
	for _, m := range mctx.MentionedJIDs() {
		if jid, err := types.ParseJID(m); err == nil {
			mentions = append(mentions, jid)
		}
	}
	return mentions
}

// sprResolveTokenList mengubah token-token target mentah jadi JID.
// Token yang tak dikenal dikembalikan di fails (bukan error fatal).
func sprResolveTokenList(mctx *store.MessageContext, mentions []types.JID, toks []string) ([]types.JID, []string) {
	var jids []types.JID
	var fails []string
	for _, t := range toks {
		jid, err := sprResolveToken(mctx, mentions, t)
		if err != nil {
			fails = append(fails, t)
			continue
		}
		jids = append(jids, jid)
	}
	return jids, fails
}

// sprIsBotJID true bila JID adalah akun bot sendiri — delegasi ke
// store.IsBotJID (toleran dual-format LID/PN).
func sprIsBotJID(client *whatsmeow.Client, jid types.JID) bool {
	return store.IsBotJID(client, jid)
}

// sprCanonicalJID memetakan JID ke bentuk participant grup (@lid/@pn sesuai
// mode grup). False bila bukan member.
func sprCanonicalJID(info *types.GroupInfo, jid types.JID) (types.JID, bool) {
	for _, pt := range info.Participants {
		if store.ParticipantMatchesJID(pt, jid) {
			return pt.JID, true
		}
	}
	return types.JID{}, false
}

// sprOwnID memilih identitas pengirim mengikuti addressing mode grup
// (cermin logika SendMessage publik). False bila identitas belum siap.
func sprOwnID(client *whatsmeow.Client, info *types.GroupInfo) (types.JID, bool) {
	var ownID types.JID
	if client.Store.ID != nil {
		ownID = *client.Store.ID
	}
	if info.AddressingMode == types.AddressingModeLID && !client.Store.LID.IsEmpty() {
		ownID = client.Store.LID
	}
	return ownID, !ownID.IsEmpty()
}

// sprBuildTargets membangun daftar target final dari hasil parse + resolve:
//   - hasExclude: basis = includes (bila hasInclude) atau semua member,
//     minus excludes.
//   - selain itu: includes apa adanya.
//
// Tiap target dikanonikalisasi ke JID participant, dideduplikasi, dan bot
// sendiri dibuang. Balikan kedua = jumlah token yang dilewati
// (bukan member/duplikat/bot); token tak dikenal (fails) dihitung pemanggil.
func sprBuildTargets(client *whatsmeow.Client, info *types.GroupInfo, p sprParsed, includes, excludes []types.JID) ([]types.JID, int) {
	var targets []types.JID
	seen := map[string]bool{}
	skipped := 0
	add := func(jid types.JID) {
		if jid.IsEmpty() || sprIsBotJID(client, jid) {
			skipped++
			return
		}
		c, ok := sprCanonicalJID(info, jid)
		if !ok {
			skipped++
			return
		}
		key := c.ToNonAD().User + "/" + string(c.ToNonAD().Server)
		if seen[key] {
			skipped++
			return
		}
		seen[key] = true
		targets = append(targets, c)
	}
	if p.hasExclude {
		isExcluded := func(pt types.GroupParticipant) bool {
			for _, ex := range excludes {
				if store.ParticipantMatchesJID(pt, ex) {
					return true
				}
			}
			return false
		}
		if p.hasInclude {
			for _, jid := range includes {
				add(jid)
			}
		} else {
			for _, pt := range info.Participants {
				if sprIsBotJID(client, pt.JID) || isExcluded(pt) {
					continue
				}
				add(pt.JID)
			}
		}
	} else {
		for _, jid := range includes {
			add(jid)
		}
	}
	return targets, skipped
}

// ---------------------------------------------------------------------------
// Pengiriman selektif-SKDM
// ---------------------------------------------------------------------------
//
// sendGroup internal hypermeow selalu: rotasi sender key (builder.Create),
// enkripsi skmsg, sebar SKDM pairwise ke `participants`, lalu kirim stanza
// ke grup. Dengan participants = [satu target], hanya target yang dapat
// SKDM — anggota lain menerima skmsg yang tak bisa didekripsi ("setiap
// target dikirim berurutan dengan rotasi sender key sendiri").
//
// Jalur ini diakses lewat DangerousInternals karena API publik SendMessage
// selalu memakai daftar member penuh. Satu-satunya parameter tak-eksport
// (nodeExtraParams) diisi zero value via reflection; field-fieldnya
// (bot/meta/additionalNodes/addressingMode/peerRecipientPN) tidak relevan
// untuk pesan teks grup biasa. Signature divalidasi tiap panggilan supaya
// ketidakcocokan versi hypermeow gagal eksplisit, bukan diam-diam.
func sprSendGroupSelective(ctx context.Context, client *whatsmeow.Client, ownID, chat, target types.JID, id types.MessageID, message *waE2E.Message) (string, error) {
	di := client.DangerousInternals()
	mv := reflect.ValueOf(di).MethodByName("SendGroup")
	if !mv.IsValid() {
		return "", fmt.Errorf("hypermeow tak punya DangerousInternals.SendGroup (versi library berubah?)")
	}
	mt := mv.Type()
	if mt.NumIn() != 8 || mt.NumOut() != 3 {
		return "", fmt.Errorf("signature SendGroup berubah (%d in/%d out), batal kirim aman", mt.NumIn(), mt.NumOut())
	}
	timings := reflect.New(mt.In(6).Elem()) // *MessageDebugTimings non-nil (dipakai internal)
	out := mv.Call([]reflect.Value{
		reflect.ValueOf(ctx),
		reflect.ValueOf(ownID),
		reflect.ValueOf(chat),
		reflect.ValueOf([]types.JID{target}),
		reflect.ValueOf(id),
		reflect.ValueOf(message),
		timings,
		reflect.Zero(mt.In(7)), // nodeExtraParams zero
	})
	if errVal := out[2]; !errVal.IsNil() {
		return "", errVal.Interface().(error)
	}
	phash, _ := out[0].Interface().(string)
	// Daftarkan ke recent-message store supaya retry receipt bisa dijawab
	// (setara addRecentMessage di jalur SendMessage publik).
	if err := di.AddRecentMessage(ctx, chat, id, message, (*waMsgApplication.MessageApplication)(nil)); err != nil {
		logfmt.Warn("SPR: gagal catat recent message %s: %v\n", id, err)
	}
	return phash, nil
}

// sprResetSenderKey mereset record sender key bot untuk grup ini sehingga
// builder.Create berikutnya terpaksa mint kunci BARU (rotasi beneran).
//
// Latar (wajib dibaca sebelum mengubah): Create di library itu get-or-create,
// BUKAN rotate. Tanpa reset, kiriman "selektif" me-reuse kunci lama yang
// sudah dipegang semua member dari traffic normal sebelumnya → SKDM selektif
// jadi pajangan dan semua orang dekripsi langsung (insiden: semua HP lihat
// isi asli). Trik Put(nil) GAGAL (kolom sender_key NOT NULL, error 1299),
// jadi yang ditulis adalah record KOSONG yang valid: serializer JSON
// me-round-trip-nya jadi `{"SenderKeyStates":[]}` → Load mem-parse kembali
// jadi record kosong → IsEmpty()==true → Create mint baru (lihat
// store/signal.go:214-223 dan groups/state/record/SenderKeyRecord.go).
// Nama record direplika persis dari sendGroup internal: group = JID chat,
// sender = signal address milik sendiri (getOwnLID = LID bila ada).
func sprResetSenderKey(ctx context.Context, client *whatsmeow.Client, chat types.JID) error {
	own := client.Store.LID
	if own.IsEmpty() && client.Store.ID != nil {
		own = *client.Store.ID
	}
	if own.IsEmpty() {
		return fmt.Errorf("identitas bot kosong")
	}
	ser := hstore.SignalProtobufSerializer
	empty := groupRecord.NewSenderKey(ser.SenderKeyRecord, ser.SenderKeyState)
	blob := empty.Serialize()
	if len(blob) == 0 {
		return fmt.Errorf("serialisasi record kosong gagal")
	}
	sender := own.SignalAddress().String()
	return client.Store.SenderKeys.PutSenderKey(ctx, chat.String(), sender, blob)
}

// ---------------------------------------------------------------------------
// Parsing argumen (murni, diuji unit di spr_test.go)
// ---------------------------------------------------------------------------

// sprParsed hasil parse RawArgs .spr.
type sprParsed struct {
	isDebug           bool
	hasInclude        bool
	hasExclude        bool
	includes          []string // token target mentah (sesudah -i / posisi default)
	excludes          []string // token target mentah (sesudah -e)
	message           string   // pesan asli (sudah tanpa segmen decoy)
	decoy             string
	hasDecoy          bool
	quotedOnly        bool // tak ada token target sama sekali → pakai reply
}

// sprIsTargetToken true bila token terlihat seperti target: @mention,
// @me, nomor/LID digit, atau JID eksplisit.
func sprIsTargetToken(tok string) bool {
	t := strings.Trim(tok, ",;")
	if t == "" {
		return false
	}
	if strings.HasPrefix(t, "@") {
		return len(t) > 1
	}
	if strings.Contains(t, "@") {
		return true // mis. 123@lid / 123@s.whatsapp.net
	}
	digits := strings.TrimPrefix(t, "+")
	if len(digits) >= 8 {
		allDigit := true
		for _, r := range digits {
			if r < '0' || r > '9' {
				allDigit = false
				break
			}
		}
		if allDigit {
			return true
		}
	}
	return false
}

// parseSPRArgs memecah RawArgs menjadi flag, target, dan pesan.
// Aturan: -i menampung token-target sampai flag berikutnya / token biasa;
// -e sama; tanpa flag, token-target di awal jadi target default; sisanya
// pesan (diambil dari raw untuk menjaga whitespace/baris baru asli).
// Decoy dipisah di karakter '|' PERTAMA pada pesan.
func parseSPRArgs(raw string) sprParsed {
	tokens := strings.Fields(raw)
	if len(tokens) == 1 && strings.EqualFold(tokens[0], "debug") {
		return sprParsed{isDebug: true}
	}
	var p sprParsed
	consumed := 0 // jumlah token terpakai (flag + target)
	i := 0
	inInclude, inExclude := false, false // posisi flag terakhir terlihat
	sawFlag := false
	for i < len(tokens) {
		t := tokens[i]
		if t == "-i" {
			p.hasInclude, inInclude, inExclude, sawFlag = true, true, false, true
			consumed++
			i++
			continue
		}
		if t == "-e" {
			p.hasExclude, inInclude, inExclude, sawFlag = true, false, true, true
			consumed++
			i++
			continue
		}
		if !sprIsTargetToken(t) {
			break // mulai pesan
		}
		// Token target tanpa flag -i/-e yang sedang aktif HANYA sah di
		// posisi awal (target default). Kalau sudah ada flag tapi...
		// (inInclude/inExclude menampungnya di bawah).
		if !sawFlag && (inInclude || inExclude) {
			break
		}
		switch {
		case inExclude:
			p.excludes = append(p.excludes, t)
		case inInclude:
			p.includes = append(p.includes, t)
		default:
			// Tanpa flag: hanya deretan awal yang jadi target default.
			p.includes = append(p.includes, t)
		}
		consumed++
		i++
	}
	if len(p.includes) == 0 && len(p.excludes) == 0 && !p.hasInclude && !p.hasExclude {
		p.quotedOnly = true
	}
	// Iris pesan dari raw mentah mulai sesudah token terpakai, supaya
	// baris baru/spasi ganda di dalam pesan tidak hancur.
	msgRaw := raw
	off := 0
	for k := 0; k < consumed; k++ {
		pos := strings.Index(msgRaw[off:], tokens[k])
		if pos < 0 {
			break
		}
		off += pos + len(tokens[k])
	}
	msgRaw = strings.TrimSpace(msgRaw[off:])
	if idx := strings.Index(msgRaw, "|"); idx >= 0 {
		p.message = strings.TrimSpace(msgRaw[:idx])
		p.decoy = strings.TrimSpace(msgRaw[idx+1:])
		p.hasDecoy = true
	} else {
		p.message = msgRaw
	}
	return p
}

// sprResolveToken mengubah satu token target jadi JID.
func sprResolveToken(mctx *store.MessageContext, mentions []types.JID, tok string) (types.JID, error) {
	t := strings.Trim(tok, ",;")
	if strings.EqualFold(t, "@me") {
		return mctx.Sender, nil
	}
	if strings.HasPrefix(t, "@") {
		body := t[1:]
		if strings.Contains(body, "@") {
			return types.ParseJID(body) // JID eksplisit, mis. 123@lid
		}
		for _, mj := range mentions {
			if mj.User == body {
				return mj, nil
			}
		}
		if sprIsDigits(body) {
			// Mention mentah tanpa metadata (ketik manual): anggap PN.
			// Grup @lid wajib pakai @mention asli (lihat bantuan).
			return types.NewJID(body, types.DefaultUserServer), nil
		}
		return types.JID{}, fmt.Errorf("target %q tak dikenal (pakai @mention dari daftar member)", tok)
	}
	if strings.Contains(t, "@") {
		return types.ParseJID(t)
	}
	digits := strings.TrimPrefix(t, "+")
	if sprIsDigits(digits) {
		return types.NewJID(digits, types.DefaultUserServer), nil
	}
	return types.JID{}, fmt.Errorf("target %q tak dikenal", tok)
}

func sprIsDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// sprExtractGroupFlag memindai pasangan "-g <JID grup>" di argumen mentah,
// melepasnya, dan mengembalikan sisa argumen + JID grup (kosong = tanpa
// flag, jalan di chat asal). "-g" tanpa pasangan JID grup valid → error
// (lebih baik menolak daripada salah sasaran diam-diam). Posisi bebas;
// pemotongan dari raw mentah supaya whitespace pesan tetap utuh.
func sprExtractGroupFlag(raw string) (string, types.JID, error) {
	tokens := strings.Fields(raw)
	idx := -1
	for i, t := range tokens {
		if t == "-g" {
			idx = i
			break
		}
	}
	if idx < 0 {
		return raw, types.JID{}, nil
	}
	if idx+1 >= len(tokens) {
		return "", types.JID{}, fmt.Errorf("flag -g butuh JID grup, contoh: -g 1203...@g.us (lihat .listgrup)")
	}
	jid, perr := types.ParseJID(tokens[idx+1])
	if perr != nil || jid.Server != types.GroupServer || jid.User == "" {
		return "", types.JID{}, fmt.Errorf("nilai -g harus JID grup (@g.us), contoh: -g 1203...@g.us (lihat .listgrup)")
	}
	// Potong pasangan dari raw mentah (pindai sekuensial agar token
	// yang teksnya kembar tetap sejajar).
	off, start := 0, -1
	for k := 0; k <= idx+1; k++ {
		pos := strings.Index(raw[off:], tokens[k])
		if pos < 0 {
			kept := append(append([]string{}, tokens[:idx]...), tokens[idx+2:]...)
			return strings.Join(kept, " "), jid, nil
		}
		if k == idx {
			start = off + pos
		}
		off += pos + len(tokens[k])
	}
	return strings.TrimSpace(strings.TrimRight(raw[:start], " \t\r\n") + " " + strings.TrimLeft(raw[off:], " \t\r\n")), jid, nil
}

// sprGroupInfo mengambil metadata grup tujuan: cache 10 detik bila chat
// asal (hemat fetch), langsung bila -g (cache milik chat lain tak berlaku).
func sprGroupInfo(ctx context.Context, mctx *store.MessageContext, chat types.JID) (*types.GroupInfo, error) {
	if chat == mctx.Chat {
		return mctx.GroupInfo(ctx)
	}
	return mctx.Client.GetGroupInfo(ctx, chat)
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

const sprUsage = "*SPR — Send Private Reply* (khusus owner)\n" +
	"Bisik ke target di dalam grup. Target baca isi asli,\n" +
	"anggota lain tidak.\n" +
	"\n" +
	"*Target* (satu cara saja):\n" +
	"- @mention → .spr @Budi halo\n" +
	"- reply pesan target → .spr halo\n" +
	"- nomor / LID → .spr 62812xxx halo\n" +
	"- @me → ke dirimu sendiri\n" +
	"Di grup mode @lid wajib pakai @mention.\n" +
	"\n" +
	"*Pesan*:\n" +
	"- .spr @target <pesan>\n" +
	"  non-target stuck di \"menunggu pesan\"\n" +
	"- .spr @target <asli> | <decoy>\n" +
	"  non-target melihat decoy\n" +
	"\n" +
	"*Multi-target* (maks 25, dikirim berurutan):\n" +
	"- -i @t1 @t2 → hanya ke yang disebut\n" +
	"- -e @ex1 → ke semua kecuali yang disebut\n" +
	"- -i @t1 -e @ex1 → kombinasi\n" +
	"\n" +
	"Lainnya: .spr debug (info teknis grup)\n" +
	"\n" +
	"Dari PC: tambah -g <JID grup> (lihat .listgrup).\n" +
	"Target dari PC: digit/LID saja."

func handleSPR(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	raw := strings.TrimSpace(mctx.RawArgs)
	if raw == "" {
		return store.ReplyText(ctx, mctx, sprUsage)
	}
	// Flag -g: eksekusi di grup lain dari PC — command tak muncul di grup.
	raw, groupJID, gerr := sprExtractGroupFlag(raw)
	if gerr != nil {
		return store.ReplyText(ctx, mctx, gerr.Error())
	}
	if groupJID.IsEmpty() && !mctx.IsGroup() {
		return store.ReplyText(ctx, mctx, "Command ini hanya bisa dipakai di dalam grup (atau tambah -g <JID grup>, lihat .listgrup).")
	}
	chat := mctx.Chat
	if !groupJID.IsEmpty() {
		chat = groupJID
	}
	p := parseSPRArgs(raw)
	if p.isDebug {
		return sprReplyDebug(ctx, mctx, chat)
	}

	info, err := sprGroupInfo(ctx, mctx, chat)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal ambil data grup: %v", err))
	}

	// Mention terparse untuk pemetaan @user → JID beneran.
	mentions := sprParseMentions(mctx)

	includes, failInc := sprResolveTokenList(mctx, mentions, p.includes)
	excludes, failExc := sprResolveTokenList(mctx, mentions, p.excludes)

	// Tanpa token target sama sekali: pakai pesan yang di-reply.
	if p.quotedOnly {
		if q := mctx.QuotedParticipant(); q != "" {
			if jid, err := types.ParseJID(q); err == nil {
				includes = append(includes, jid)
			}
		}
	}
	if len(includes) == 0 && !p.hasExclude {
		detail := ""
		if len(failInc) > 0 {
			detail = " Target tak dikenal: " + strings.Join(failInc, " ")
		}
		return store.ReplyText(ctx, mctx, "Tak ada target valid. Tag member (@user) atau reply pesannya."+detail+"\n\n"+sprUsage)
	}
	if p.message == "" && !(p.hasDecoy && p.decoy != "") {
		return store.ReplyText(ctx, mctx, "Pesan kosong.\n\n"+sprUsage)
	}

	// Bangun target final: kanonikalisasi ke JID participant grup (bentuk
	// @lid/@pn sesuai mode grup) + saring non-member dan bot sendiri.
	targets, skippedNonMember := sprBuildTargets(mctx.Client, info, p, includes, excludes)
	skipped := skippedNonMember + len(failInc) + len(failExc)

	if len(targets) == 0 {
		return store.ReplyText(ctx, mctx, "Tak ada target valid (mungkin bukan member grup atau hanya bot).")
	}
	if len(targets) > sprMaxTargets {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Maksimal %d target per command (diminta %d). Bagi jadi beberapa perintah.", sprMaxTargets, len(targets)))
	}

	// ownID mengikuti addressing mode grup (cermin logika SendMessage).
	ownID, ok := sprOwnID(mctx.Client, info)
	if !ok {
		return store.ReplyText(ctx, mctx, "Identitas bot belum siap, coba lagi sebentar.")
	}

	realMsg := &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String(p.message)},
	}
	var decoyMsg *waE2E.Message
	if p.hasDecoy {
		decoyMsg = &waE2E.Message{
			ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String(p.decoy)},
		}
	}

	// Kirim berurutan — tiap target dirotasi sender key-nya sendiri: reset
	// dulu (paksa kunci baru yang belum dipegang siapa pun), baru kirim
	// selektif. Tanpa reset, Create me-reuse kunci lama yang sudah shared.
	targetUsers := sprRememberTargetUsers(targets)
	var okIDs []string
	var failed []string
	for i, tgt := range targets {
		if i > 0 {
			time.Sleep(sprSendDelay)
		}
		msgID := mctx.Client.GenerateMessageID()
		if err := sprResetSenderKey(ctx, mctx.Client, chat); err != nil {
			failed = append(failed, displayNumber(info, tgt))
			logfmt.Error("SPR: gagal rotasi kunci untuk %s: %v\n", displayNumber(info, tgt), err)
			continue
		}
		if _, err := sprSendGroupSelective(ctx, mctx.Client, ownID, chat, tgt, msgID, proto.Clone(realMsg).(*waE2E.Message)); err != nil {
			failed = append(failed, displayNumber(info, tgt))
			logfmt.Error("SPR: gagal kirim ke %s: %v\n", tgt.String(), err)
			continue
		}
		sprMu.Lock()
		sprPruneLocked()
		sprEntries[sprKey(chat, msgID)] = &sprEntry{
			targets:  targetUsers,
			original: proto.Clone(realMsg).(*waE2E.Message),
			decoy:    decoyMsg,
			hasDecoy: p.hasDecoy,
			created:  time.Now(),
		}
		sprMu.Unlock()
		okIDs = append(okIDs, string(msgID))
		store.LogOutgoing(mctx, "spr", p.message)
	}

	if len(okIDs) == 0 {
		_ = sprResetSenderKey(ctx, mctx.Client, chat) // putus rantai juga saat gagal total
		return store.ReplyText(ctx, mctx, "SPR gagal total: "+strings.Join(failed, ", "))
	}
	// Putus rantai: kunci SPR terakhir wajib diganti sebelum traffic normal
	// berikutnya — kalau pesan biasa me-reuse-nya, SKDM kunci SPR tersebar ke
	// semua orang dan bubble tadi ikut terbaca (bocor tertunda).
	if err := sprResetSenderKey(ctx, mctx.Client, chat); err != nil {
		logfmt.Warn("SPR: gagal reset kunci pasca-kirim: %v\n", err)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "SPR terkirim ke %d target", len(okIDs))
	if p.hasDecoy {
		sb.WriteString(" (mode decoy on)")
	} else {
		sb.WriteString(" (tanpa decoy: non-target tak melihat apa-apa)")
	}
	sb.WriteString(".")
	if len(failed) > 0 {
		fmt.Fprintf(&sb, " Gagal: %s.", strings.Join(failed, ", "))
	}
	if skipped > 0 {
		fmt.Fprintf(&sb, " %d token dilewati (tak dikenal/bukan member/duplikat).", skipped)
	}
	return store.ReplyText(ctx, mctx, sb.String())
}

// sprReplyDebug menjawab ".spr debug": info teknis grup + status SPR.
// Read-only — tidak memutar atau memutasi sender key.
func sprReplyDebug(ctx context.Context, mctx *store.MessageContext, chat types.JID) error {
	info, err := sprGroupInfo(ctx, mctx, chat)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal ambil data grup: %v", err))
	}
	lidCount := 0
	for _, pt := range info.Participants {
		if pt.JID.Server == types.HiddenUserServer {
			lidCount++
		}
	}
	sprMu.Lock()
	active := 0
	for k, e := range sprEntries {
		if strings.HasPrefix(k, chat.String()+"\x00") && time.Since(e.created) <= sprEntryTTL {
			active++
		}
	}
	sprMu.Unlock()
	hook := "off"
	if mctx.Client.PreRetryCallback != nil {
		hook = "on"
	}
	mode := string(info.AddressingMode)
	if mode == "" {
		mode = "pn (default)"
	}
	botLID := mctx.Client.Store.LID.String()
	if mctx.Client.Store.LID.IsEmpty() {
		botLID = "-"
	}
	botID := "-"
	if mctx.Client.Store.ID != nil {
		botID = mctx.Client.Store.ID.String()
	}
	out := fmt.Sprintf("SPR debug grup %s:\n- addressing: %s\n- member: %d (@lid: %d)\n- bot PN: %s\n- bot LID: %s\n- retry-hook: %s\n- entri SPR aktif: %d\n- rotasi: sender key baru per target (maks %d target)",
		chat.User, mode, len(info.Participants), lidCount, botID, botLID, hook, active, sprMaxTargets)
	return store.ReplyText(ctx, mctx, out)
}
