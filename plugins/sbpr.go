package plugins

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types"
	"google.golang.org/protobuf/proto"

	"wabot-go/internal/logfmt"
	"wabot-go/internal/messaging"
	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterSBPR)
}

// RegisterSBPR mendaftarkan command ".sbpr" (Send Batch Private Relay) dan
// ".sbprprofile" (shortcut getpp ke semua member). Owner-only seperti SPR:
// CategoryOwner = hidden dari non-owner + diblokir di clone.
func RegisterSBPR(r *Registry) {
	r.Register(Command{
		Name:        "sbpr",
		Description: "Batch private relay 1 ID (owner). .sbpr @a Halo {nama}! | sapa | getpp | status",
		Category:    CategoryOwner,
		Handler:     handleSBPR,
	})
	r.Register(Command{
		Name:        "sbprprofile",
		Description: "SBPR foto profil per penonton (owner). Sama dengan .sbpr getpp semua member",
		Category:    CategoryOwner,
		Handler:     handleSBPRProfile,
	})
}

const (
	// sbprMaxTargets batas target per batch (spec fitur asli).
	sbprMaxTargets = 300
	// sbprMaxAttempts relay per target — tiap relay diawali reset kunci
	// (= rotasi sender key tiap relay, sesuai spec).
	sbprMaxAttempts = 3
	// sbprSendDelay jeda antar relay (300 target × ini ≈ 90 dtk; getpp
	// jauh lebih lama karena fetch+upload per target).
	sbprSendDelay = 300 * time.Millisecond
	// sbprDefaultSapa template default .sbpr sapa.
	sbprDefaultSapa = "Halo {nama}!"
	// sbprPPTimeout batas unduh foto profil per target.
	sbprPPTimeout = 20 * time.Second
	// sbprMaxPPBytes pagu ukuran foto profil (PP asli kecil; ini jaring pengaman).
	sbprMaxPPBytes = 8 << 20
)

const sbprUsage = "*SBPR — Send Batch Private Relay* (khusus owner)\n" +
	"Satu batch, satu message ID: non-target cuma lihat SATU\n" +
	"gelembung \"menunggu pesan\", tiap target baca isinya sendiri.\n" +
	"\n" +
	"*Teks + template*:\n" +
	"- .sbpr @target <teks>\n" +
	"- .sbpr @a @b Halo {nama}!\n" +
	"- {nama} = nama target, {nomor} = nomor target\n" +
	"\n" +
	"*Subcommand*:\n" +
	"- .sbpr sapa [teks] — sapa semua member\n" +
	"  (default: Halo {nama}!)\n" +
	"- .sbpr getpp [@target] — kirim foto profil target\n" +
	"  (tiap penonton lihat fotonya sendiri)\n" +
	"- .sbpr status [-g <JID>] — progres batch\n" +
	"  (di PC tanpa -g: semua grup)\n" +
	"\n" +
	"Target: @mention, reply, atau nomor/LID.\n" +
	"Di grup @lid selalu pakai @mention. Maks 300 target.\n" +
	"\n" +
	"Dari PC: tambah -g <JID grup> (lihat .listgrup).\n" +
	"Target dari PC: digit/LID saja."

// sbprBatch melacak progres satu batch (dibaca .sbpr status).
type sbprBatch struct {
	id      int
	mode    string // "teks" | "sapa" | "getpp"
	total   int
	sent    int
	failed  int
	done    bool
	started time.Time
	end     time.Time
}

var (
	sbprMu      sync.Mutex
	sbprBatches = map[string]*sbprBatch{} // key: chat.String() → batch terakhir
	sbprSeq     int
)

func sbprMarkSent(batch *sbprBatch) {
	sbprMu.Lock()
	batch.sent++
	sbprMu.Unlock()
}

func sbprMarkFailed(batch *sbprBatch) {
	sbprMu.Lock()
	batch.failed++
	sbprMu.Unlock()
}

func handleSBPR(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	raw := strings.TrimSpace(mctx.RawArgs)
	if raw == "" {
		return store.ReplyText(ctx, mctx, sbprUsage)
	}
	// Flag -g: eksekusi di grup lain dari PC — command tak muncul di grup.
	raw, groupJID, gerr := sprExtractGroupFlag(raw)
	if gerr != nil {
		return store.ReplyText(ctx, mctx, gerr.Error())
	}
	chat := mctx.Chat
	if !groupJID.IsEmpty() {
		chat = groupJID
	}
	if strings.TrimSpace(raw) == "" {
		return store.ReplyText(ctx, mctx, sbprUsage)
	}
	fields := strings.Fields(raw)
	sub := strings.ToLower(fields[0])
	// Status boleh dari mana saja (dari PC tanpa -g = semua grup),
	// jadi dicek SEBELUM gate grup.
	if sub == "status" {
		return sbprReplyStatus(ctx, mctx, chat)
	}
	if groupJID.IsEmpty() && !mctx.IsGroup() {
		return store.ReplyText(ctx, mctx, "Command ini hanya bisa dipakai di dalam grup (atau tambah -g <JID grup>, lihat .listgrup).")
	}

	info, err := sprGroupInfo(ctx, mctx, chat)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal ambil data grup: %v", err))
	}
	mentions := sprParseMentions(mctx)

	switch sub {
	case "sapa":
		template := strings.TrimSpace(raw[len(fields[0]):])
		if template == "" {
			template = sbprDefaultSapa
		}
		allTargets, allSkipped := sbprAllMembers(mctx.Client, info)
		return sbprStartBatch(ctx, mctx, chat, info, "sapa", template, allTargets, allSkipped)
	case "getpp":
		p := parseSPRArgs(strings.TrimSpace(raw[len(fields[0]):]))
		targets, skipped, failMsg := sbprResolveFromParse(ctx, mctx, info, mentions, p)
		if len(targets) == 0 {
			// Tanpa target eksplisit: semua member.
			targets, skipped = sbprAllMembers(mctx.Client, info)
		}
		return sbprStartBatchWithCaption(ctx, mctx, chat, info, "getpp", sbprRejoinTemplate(p), targets, skipped, failMsg)
	default:
		p := parseSPRArgs(raw)
		targets, skipped, failMsg := sbprResolveFromParse(ctx, mctx, info, mentions, p)
		if len(targets) == 0 {
			detail := ""
			if failMsg != "" {
				detail = " " + failMsg
			}
			return store.ReplyText(ctx, mctx, "Tak ada target valid. Tag member (@user) atau reply pesannya."+detail+"\n\n"+sbprUsage)
		}
		template := sbprRejoinTemplate(p)
		if template == "" {
			return store.ReplyText(ctx, mctx, "Teks kosong.\n\n"+sbprUsage)
		}
		return sbprStartBatchWithCaption(ctx, mctx, chat, info, "teks", template, targets, skipped, failMsg)
	}
}

// handleSBPRProfile = .sbpr getpp ke semua member (caption = seluruh teks).
func handleSBPRProfile(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	raw := strings.TrimSpace(mctx.RawArgs)
	raw, groupJID, gerr := sprExtractGroupFlag(raw)
	if gerr != nil {
		return store.ReplyText(ctx, mctx, gerr.Error())
	}
	chat := mctx.Chat
	if !groupJID.IsEmpty() {
		chat = groupJID
	}
	if groupJID.IsEmpty() && !mctx.IsGroup() {
		return store.ReplyText(ctx, mctx, "Command ini hanya bisa dipakai di dalam grup (atau tambah -g <JID grup>, lihat .listgrup).")
	}
	info, err := sprGroupInfo(ctx, mctx, chat)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal ambil data grup: %v", err))
	}
	targets, _ := sbprAllMembers(mctx.Client, info)
	// Seluruh teks setelah command = caption biasa (bukan target):
	// ".sbprprofile Halo {nama}" → caption "Halo {nama}" untuk semua.
	return sbprStartBatchWithCaption(ctx, mctx, chat, info, "getpp", sbprProfileCaption(raw), targets, 0, "")
}

// sbprAllMembers mengembalikan semua participant minus bot.
func sbprAllMembers(client *whatsmeow.Client, info *types.GroupInfo) ([]types.JID, int) {
	var all []types.JID
	for _, pt := range info.Participants {
		all = append(all, pt.JID)
	}
	return sprBuildTargets(client, info, sprParsed{}, all, nil)
}

// sbprResolveFromParse resolve target dari hasil parse + fallback reply.
// Balikan: targets, token-dilewati, pesan-fail ("" bila tak ada).
func sbprResolveFromParse(ctx context.Context, mctx *store.MessageContext, info *types.GroupInfo, mentions []types.JID, p sprParsed) ([]types.JID, int, string) {
	_ = ctx
	includes, failInc := sprResolveTokenList(mctx, mentions, p.includes)
	excludes, failExc := sprResolveTokenList(mctx, mentions, p.excludes)
	if p.quotedOnly {
		if q := mctx.QuotedParticipant(); q != "" {
			if jid, err := types.ParseJID(q); err == nil {
				includes = append(includes, jid)
			}
		}
	}
	targets, skipped := sprBuildTargets(mctx.Client, info, p, includes, excludes)
	failMsg := ""
	if fails := append(append([]string{}, failInc...), failExc...); len(fails) > 0 {
		failMsg = "Target tak dikenal: " + strings.Join(fails, " ")
	}
	return targets, skipped + len(failInc) + len(failExc), failMsg
}

// sbprRejoinTemplate mengembalikan teks template apa adanya. parseSPRArgs
// memisahkan segmen decoy "|", tapi SBPR tak mengenal decoy — segmennya
// digabung kembali supaya teks user utuh.
func sbprRejoinTemplate(p sprParsed) string {
	if p.hasDecoy {
		return strings.TrimSpace(p.message + " | " + p.decoy)
	}
	return p.message
}

// sbprProfileCaption: seluruh argumen .sbprprofile adalah caption biasa
// (termasuk teks ber-awalan "@": tetap teks, BUKAN target).
func sbprProfileCaption(rawArgs string) string {
	return strings.TrimSpace(rawArgs)
}

// sbprStartBatch jalan pintas untuk mode sapa (tanpa caption/fail terpisah).
func sbprStartBatch(ctx context.Context, mctx *store.MessageContext, chat types.JID, info *types.GroupInfo, mode, template string, targets []types.JID, skipped int) error {
	return sbprStartBatchWithCaption(ctx, mctx, chat, info, mode, template, targets, skipped, "")
}

func sbprStartBatchWithCaption(ctx context.Context, mctx *store.MessageContext, chat types.JID, info *types.GroupInfo, mode, template string, targets []types.JID, skipped int, failMsg string) error {
	if len(targets) == 0 {
		detail := ""
		if failMsg != "" {
			detail = " " + failMsg
		}
		return store.ReplyText(ctx, mctx, "Tak ada target valid."+detail)
	}
	if len(targets) > sbprMaxTargets {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Maksimal %d target per batch (diminta %d). Bagi jadi beberapa perintah.", sbprMaxTargets, len(targets)))
	}
	ownID, ok := sprOwnID(mctx.Client, info)
	if !ok {
		return store.ReplyText(ctx, mctx, "Identitas bot belum siap, coba lagi sebentar.")
	}
	msgID := mctx.Client.GenerateMessageID()

	// Daftarkan entri retry SEBELUM relay pertama: satu message ID untuk
	// semua relay (klien me-render satu gelembung), isi personal diisi
	// bertahap per target yang selesai dibangun.
	targetUsers := sprRememberTargetUsers(targets)
	for _, tgt := range targets {
		if pn := sbprParticipantPN(info, tgt); pn != "" {
			targetUsers[pn] = true
		}
	}
	entry := &sprEntry{targets: targetUsers, perSender: make(map[string]*waE2E.Message), created: time.Now()}
	sprMu.Lock()
	sprPruneLocked()
	sprEntries[sprKey(chat, msgID)] = entry
	sprMu.Unlock()

	sbprMu.Lock()
	sbprSeq++
	batch := &sbprBatch{id: sbprSeq, mode: mode, total: len(targets), started: time.Now()}
	sbprBatches[chat.String()] = batch
	// Jaring pengaman memori: batch selesai yang tua dibuang oportunistik.
	if len(sbprBatches) > 50 {
		var oldestKey string
		var oldest time.Time
		first := true
		for k, b := range sbprBatches {
			if b.done && (first || b.end.Before(oldest)) {
				oldestKey, oldest, first = k, b.end, false
			}
		}
		if oldestKey != "" {
			delete(sbprBatches, oldestKey)
		}
	}
	sbprMu.Unlock()

	extra := ""
	if skipped > 0 {
		extra = fmt.Sprintf(" %d token dilewati.", skipped)
	}
	if failMsg != "" {
		extra += " " + failMsg
	}
	// Lifecycle ke console saja (chat bersih): mulai + selesai tidak
	// mengirim bubble. Progres on-demand via .sbpr status.
	logfmt.Info("SBPR batch #%d dimulai: %d target (mode %s, chat %s).%s\n", batch.id, len(targets), mode, chat.String(), extra)

	build := sbprTextBuilder(template)
	if mode == "getpp" {
		build = sbprPPBuilder(template)
	}
	go sbprRunBatch(mctx.Client, mctx, chat, info, ownID, msgID, batch, entry, targets, build)
	return nil
}

// sbprContentBuilder membangun pesan personal untuk satu target.
type sbprContentBuilder func(ctx context.Context, client *whatsmeow.Client, info *types.GroupInfo, tgt types.JID) (*waE2E.Message, error)

func sbprTextBuilder(template string) sbprContentBuilder {
	return func(ctx context.Context, client *whatsmeow.Client, info *types.GroupInfo, tgt types.JID) (*waE2E.Message, error) {
		name, nomor := sbprTargetName(ctx, client, info, tgt)
		return &waE2E.Message{
			ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String(sbprRender(template, name, nomor))},
		}, nil
	}
}

func sbprPPBuilder(captionTemplate string) sbprContentBuilder {
	return func(ctx context.Context, client *whatsmeow.Client, info *types.GroupInfo, tgt types.JID) (*waE2E.Message, error) {
		name, nomor := sbprTargetName(ctx, client, info, tgt)
		caption := ""
		if captionTemplate != "" {
			caption = sbprRender(captionTemplate, name, nomor)
		}
		return sbprBuildPP(ctx, client, tgt, caption, name, nomor)
	}
}

// sbprRunBatch mengeksekusi batch di background: tiap target dibangun
// kontennya, dirotasi kuncinya, di-relay (maks 3x), didaftarkan isinya
// untuk jawaban retry. Di akhir: putus rantai kunci + ringkasan.
func sbprRunBatch(client *whatsmeow.Client, mctx *store.MessageContext, chat types.JID, info *types.GroupInfo, ownID types.JID, msgID types.MessageID, batch *sbprBatch, entry *sprEntry, targets []types.JID, build sbprContentBuilder) {
	ctx := context.Background()
	for i, tgt := range targets {
		if i > 0 {
			time.Sleep(sbprSendDelay)
		}
		content, berr := build(ctx, client, info, tgt)
		if berr != nil {
			sbprMarkFailed(batch)
			logfmt.Error("SBPR #%d: gagal bangun konten untuk %s: %v\n", batch.id, tgt.String(), berr)
			continue
		}
		var serr error
		sent := false
		for a := 0; a < sbprMaxAttempts; a++ {
			if a > 0 {
				time.Sleep(sbprSendDelay)
			}
			if rerr := sprResetSenderKey(ctx, client, chat); rerr != nil {
				serr = rerr
				continue
			}
			if _, serr = sprSendGroupSelective(ctx, client, ownID, chat, tgt, msgID, content); serr != nil {
				continue
			}
			sent = true
			break
		}
		if !sent {
			sbprMarkFailed(batch)
			logfmt.Error("SBPR #%d: gagal relay ke %s setelah %dx: %v\n", batch.id, tgt.String(), sbprMaxAttempts, serr)
			continue
		}
		sprMu.Lock()
		for _, k := range sbprSenderKeys(info, tgt) {
			entry.perSender[k] = content
		}
		sprMu.Unlock()
		sbprMarkSent(batch)
		store.LogOutgoing(mctx, "sbpr", fmt.Sprintf("batch#%d", batch.id))
	}
	// Putus rantai seperti SPR: kunci terakhir jangan dipakai traffic normal.
	if err := sprResetSenderKey(ctx, client, chat); err != nil {
		logfmt.Warn("SBPR #%d: gagal reset kunci pasca-batch: %v\n", batch.id, err)
	}
	sbprMu.Lock()
	batch.done = true
	batch.end = time.Now()
	sbprMu.Unlock()
	logfmt.Info("SBPR batch #%d selesai: %d/%d terkirim, %d gagal (chat %s).\n",
		batch.id, batch.sent, batch.total, batch.failed, chat.String())
}

// sbprReplyStatus menjawab ".sbpr status": progres batch terakhir.
// Tanpa -g di PC: ringkasan semua grup.
func sbprReplyStatus(ctx context.Context, mctx *store.MessageContext, chat types.JID) error {
	sbprMu.Lock()
	if !mctx.IsGroup() && chat == mctx.Chat {
		// Mode PC tanpa -g: ringkas semua batch yang pernah ada.
		if len(sbprBatches) == 0 {
			sbprMu.Unlock()
			return store.ReplyText(ctx, mctx, "Belum ada batch SBPR.\n\n"+sbprUsage)
		}
		keys := make([]string, 0, len(sbprBatches))
		for k := range sbprBatches {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var sb strings.Builder
		sb.WriteString("Batch SBPR per grup:")
		for _, k := range keys {
			b := sbprBatches[k]
			state := "berjalan"
			if b.done {
				state = "selesai"
			}
			fmt.Fprintf(&sb, "\n- %s: #%d %s %d/%d (%s)", k, b.id, b.mode, b.sent, b.total, state)
		}
		sbprMu.Unlock()
		return store.ReplyText(ctx, mctx, sb.String())
	}
	b := sbprBatches[chat.String()]
	sbprMu.Unlock()
	if b == nil {
		return store.ReplyText(ctx, mctx, "Belum ada batch SBPR di grup ini.\n\n"+sbprUsage)
	}
	status := fmt.Sprintf("SBPR batch #%d (mode %s): %d/%d terkirim, %d gagal, ",
		b.id, b.mode, b.sent, b.total, b.failed)
	if b.done {
		status += fmt.Sprintf("selesai dalam %s.", b.end.Sub(b.started).Round(time.Second))
	} else {
		status += fmt.Sprintf("berjalan %s…", time.Since(b.started).Round(time.Second))
	}
	return store.ReplyText(ctx, mctx, status)
}

// sbprRender substitusi placeholder per target.
func sbprRender(template, name, nomor string) string {
	r := strings.ReplaceAll(template, "{nama}", name)
	return strings.ReplaceAll(r, "{nomor}", nomor)
}

// sbprParticipantPN mencari nomor telepon (PN) participant yang cocok.
func sbprParticipantPN(info *types.GroupInfo, tgt types.JID) string {
	for _, pt := range info.Participants {
		if store.ParticipantMatchesJID(pt, tgt) && !pt.PhoneNumber.IsEmpty() {
			return pt.PhoneNumber.User
		}
	}
	return ""
}

// sbprSenderKeys semua kunci bare-User untuk target (LID + PN bila ada),
// dipakai agar retry dari format identitas mana pun ketemu kontennya.
func sbprSenderKeys(info *types.GroupInfo, tgt types.JID) []string {
	keys := []string{tgt.ToNonAD().User}
	if pn := sbprParticipantPN(info, tgt); pn != "" && pn != keys[0] {
		keys = append(keys, pn)
	}
	return keys
}

// sbprTargetName resolving nama + nomor target. Nama: pushname kontak →
// nomor → user JID. Tak pernah "" (template tidak boleh berlubang).
func sbprTargetName(ctx context.Context, client *whatsmeow.Client, info *types.GroupInfo, tgt types.JID) (name, nomor string) {
	nomor = sbprParticipantPN(info, tgt)
	if nomor == "" {
		if b := tgt.ToNonAD(); b.Server == types.DefaultUserServer {
			nomor = b.User
		}
	}
	try := []types.JID{tgt}
	if nomor != "" {
		try = append(try, types.NewJID(nomor, types.DefaultUserServer))
	}
	for _, j := range try {
		if c, err := client.Store.Contacts.GetContact(ctx, j); err == nil && c.Found {
			switch {
			case c.PushName != "":
				return c.PushName, nomor
			case c.FullName != "":
				return c.FullName, nomor
			case c.FirstName != "":
				return c.FirstName, nomor
			}
		}
	}
	if nomor != "" {
		return nomor, nomor
	}
	return tgt.ToNonAD().User, ""
}

// sbprBuildPP membangun pesan gambar foto profil target. Gagal fetch/unduh/
// upload → fallback teks personal (target tetap dapat konten; dihitung
// terkirim, dicatat di log).
func sbprBuildPP(ctx context.Context, client *whatsmeow.Client, tgt types.JID, caption, name, nomor string) (*waE2E.Message, error) {
	fallback := func() *waE2E.Message {
		text := caption
		if text == "" {
			text = sbprRender(sbprDefaultSapa, name, nomor)
		}
		return &waE2E.Message{
			ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String(text)},
		}
	}
	ppi, err := client.GetProfilePictureInfo(ctx, tgt, nil)
	if err != nil || ppi == nil || ppi.URL == "" {
		logfmt.Warn("SBPR: PP %s tak tersedia (%v), fallback teks\n", tgt.String(), err)
		return fallback(), nil
	}
	data, err := sbprDownloadBytes(ppi.URL)
	if err != nil {
		logfmt.Warn("SBPR: unduh PP %s gagal (%v), fallback teks\n", tgt.String(), err)
		return fallback(), nil
	}
	if err := messaging.CheckMediaBytes(data); err != nil {
		logfmt.Warn("SBPR: PP %s invalid (%v), fallback teks\n", tgt.String(), err)
		return fallback(), nil
	}
	uploaded, err := client.Upload(ctx, data, whatsmeow.MediaImage)
	if err != nil {
		return nil, fmt.Errorf("upload PP: %w", err)
	}
	im := &waE2E.ImageMessage{
		URL:               proto.String(uploaded.URL),
		DirectPath:        proto.String(uploaded.DirectPath),
		MediaKey:          uploaded.MediaKey,
		MediaKeyTimestamp: proto.Int64(time.Now().Unix()),
		Mimetype:          proto.String("image/jpeg"),
		FileEncSHA256:     uploaded.FileEncSHA256,
		FileSHA256:        uploaded.FileSHA256,
		FileLength:        proto.Uint64(uploaded.FileLength),
		Caption:           proto.String(caption),
		JPEGThumbnail:     messaging.InlineThumbForImage(data),
	}
	if cfg, _, cerr := image.DecodeConfig(bytes.NewReader(data)); cerr == nil && cfg.Width > 0 {
		im.Width = proto.Uint32(uint32(cfg.Width))
		im.Height = proto.Uint32(uint32(cfg.Height))
	}
	return &waE2E.Message{ImageMessage: im}, nil
}

// sbprDownloadBytes mengunduh URL ke memori dengan batas ukuran.
func sbprDownloadBytes(url string) ([]byte, error) {
	httpClient := &http.Client{Timeout: sbprPPTimeout}
	resp, err := httpClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, sbprMaxPPBytes+1))
}
