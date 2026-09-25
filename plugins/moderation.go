package plugins

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"wabot-go/internal/store"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func init() {
	registerPlugin(RegisterModeration)
}

// MaxWarns adalah batas warn sebelum user otomatis di-kick dari grup.
// Berlaku sama untuk warn otomatis (antilink) dan manual (.warn) karena
// keduanya lewat giveWarn yang sama.
const MaxWarns = 3

// inviteLinkRe mendeteksi link invite grup WhatsApp dalam teks pesan.
// Hanya pola chat.whatsapp.com/<kode> yang ditindak — link lain (instagram,
// tiktok, dll) BUKAN sasaran fitur ini sesuai permintaan.
var inviteLinkRe = regexp.MustCompile(`(?i)chat\.whatsapp\.com/([A-Za-z0-9]+)`)

// requireModGate adalah gerbang command moderasi: boleh diakses admin grup
// ATAU owner. Owner yang bukan admin grup tetap lolos (cek sender-admin
// dilewati), tapi syarat bot harus admin tetap berlaku karena aksi seperti
// kick/hapus pesan mustahil tanpa itu.
func requireModGate(ctx context.Context, mctx *store.MessageContext) string {
	if !mctx.IsGroup() {
		return "Command ini hanya bisa dipakai di dalam grup."
	}
	if IsOwner(mctx) {
		isBotAdmin, err := mctx.IsBotAdmin(ctx)
		if err != nil {
			return fmt.Sprintf("Gagal cek status admin bot: %v", err)
		}
		if !isBotAdmin {
			return "Bot harus jadi admin dulu di grup ini supaya bisa melakukan aksi tersebut."
		}
		return ""
	}
	return requireGroupAdminGate(ctx, mctx)
}

// RegisterModeration mendaftarkan command moderasi grup: .antilink, .warn,
// .unwarn, .warnlist. Kategori Grup (bukan Owner) supaya konsisten dengan
// command grup lain — pembatasan aksesnya lewat gate, bukan kategori.
func RegisterModeration(r *Registry) {
	r.Register(Command{
		Name:        "antilink",
		Description: "Atur antilink grup (.antilink on/off)",
		Category:    CategoryGrup,
		Handler:     handleAntilink,
	})
	r.Register(Command{
		Name:        "warn",
		Description: "Beri warn ke member (.warn @user)",
		Category:    CategoryGrup,
		Handler:     handleWarnCmd,
	})
	r.Register(Command{
		Name:        "unwarn",
		Description: "Reset warn member (.unwarn @user)",
		Category:    CategoryGrup,
		Handler:     handleUnwarn,
	})
	r.Register(Command{
		Name:        "warnlist",
		Description: "Lihat daftar warn di grup ini",
		Category:    CategoryGrup,
		Handler:     handleWarnList,
	})
}

func handleAntilink(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireModGate(ctx, mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	arg := strings.ToLower(strings.TrimSpace(strings.Join(mctx.Args, " ")))
	switch arg {
	case "on", "aktif", "enable":
		if err := store.Groups().SetAntilink(ctx, mctx.Chat.String(), true); err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal menyalakan antilink: %v", err))
		}
		return store.ReplyText(ctx, mctx, "Antilink *aktif* — link grup WhatsApp lain otomatis dihapus + warn (link grup ini sendiri tetap boleh).")
	case "off", "mati", "disable":
		if err := store.Groups().SetAntilink(ctx, mctx.Chat.String(), false); err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal mematikan antilink: %v", err))
		}
		return store.ReplyText(ctx, mctx, "Antilink *mati*.")
	default:
		on, err := store.Groups().IsAntilinkOn(ctx, mctx.Chat.String())
		if err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal baca status antilink: %v", err))
		}
		status := "mati"
		if on {
			status = "aktif"
		}
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Antilink grup ini: *%s*\n\nPakai `.antilink on` / `.antilink off` untuk mengubah.", status))
	}
}

func handleWarnCmd(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireModGate(ctx, mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	target, err := resolveTargetJID(mctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, err.Error())
	}
	return giveWarn(ctx, mctx.Client, mctx.Chat, mctx.Sender, target.ToNonAD(), mctx.RawEvt.Info.ID, mctx.RawEvt.Message, "manual oleh admin")
}

func handleUnwarn(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireModGate(ctx, mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	target, err := resolveTargetJID(mctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, err.Error())
	}
	bare := target.ToNonAD()
	count, err := store.Groups().GetWarnCount(ctx, mctx.Chat.String(), bare.String())
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal baca warn: %v", err))
	}
	if count == 0 {
		return store.ReplyTextWithMentions(ctx, mctx,
			fmt.Sprintf("@%s tidak punya warn.", bare.User),
			[]types.JID{bare})
	}
	if err := store.Groups().ResetWarn(ctx, mctx.Chat.String(), bare.String()); err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal reset warn: %v", err))
	}
	return store.ReplyTextWithMentions(ctx, mctx,
		fmt.Sprintf("Warn @%s direset (sebelumnya %d).", bare.User, count),
		[]types.JID{bare})
}

func handleWarnList(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireModGate(ctx, mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	list, err := store.Groups().ListWarns(ctx, mctx.Chat.String())
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal baca daftar warn: %v", err))
	}
	if len(list) == 0 {
		return store.ReplyText(ctx, mctx, "Tidak ada warn di grup ini.")
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("*Daftar Warn* (maks %d)\n\n", MaxWarns))
	mentions := make([]types.JID, 0, len(list))
	for i, e := range list {
		bare, perr := types.ParseJID(e.UserJID)
		if perr != nil || bare.IsEmpty() {
			sb.WriteString(fmt.Sprintf("%d. %s — %d/%d\n", i+1, e.UserJID, e.Count, MaxWarns))
			continue
		}
		bare = bare.ToNonAD()
		sb.WriteString(fmt.Sprintf("%d. @%s — %d/%d\n", i+1, bare.User, e.Count, MaxWarns))
		mentions = append(mentions, bare)
	}
	if len(mentions) == 0 {
		return store.ReplyText(ctx, mctx, sb.String())
	}
	return store.ReplyTextWithMentions(ctx, mctx, sb.String(), mentions)
}

// giveWarn adalah SATU-SATUNYA jalur pemberian warn — dipakai deteksi antilink
// otomatis maupun command .warn manual, makanya hitungannya selalu sinkron.
// Kalau total mencapai MaxWarns, user langsung di-kick dan catatannya
// di-reset supaya mulai dari nol lagi kalau dia join kembali.
//
// quoteID adalah ID pesan yang di-quote di balasan (pesan pelanggaran untuk
// jalur otomatis, pesan command untuk jalur manual). Reply dikirim dengan
// mention beneran (metode tagall) supaya target dapat notif.
func giveWarn(ctx context.Context, client *whatsmeow.Client, chat types.JID, replier types.JID, target types.JID, quoteID string, quoted *waE2E.Message, reason string) error {
	count, err := store.Groups().AddWarn(ctx, chat.String(), target.String())
	if err != nil {
		fmt.Printf("⚠️ Gagal simpan warn %s di %s: %v\n", target, chat, err)
		return err
	}

	sendMentionReply := func(text string) error {
		return sendModerationReply(ctx, client, chat, replier, quoteID, quoted, text, []types.JID{target})
	}

	if count >= MaxWarns {
		// Batas tercapai: kick dulu, kalau kick gagal catatannya TETAP
		// dipertahankan (jangan reset) supaya warn tidak hilang percuma dan
		// admin bisa coba tendang manual.
		if _, err := client.UpdateGroupParticipants(ctx, chat, []types.JID{target}, whatsmeow.ParticipantChangeRemove); err != nil {
			fmt.Printf("⚠️ Gagal kick %s (warn %d): %v\n", target, count, err)
			return sendMentionReply(fmt.Sprintf("@%s mencapai %d/%d warn (%s), tapi gagal dikeluarkan otomatis: %v", target.User, count, MaxWarns, reason, err))
		}
		_ = store.Groups().ResetWarn(ctx, chat.String(), target.String())
		return sendMentionReply(fmt.Sprintf("@%s mencapai %d/%d warn (%s) dan dikeluarkan dari grup.", target.User, count, MaxWarns, reason))
	}

	return sendMentionReply(fmt.Sprintf("@%s diberi warn %d/%d (%s).", target.User, count, MaxWarns, reason))
}

// sendModerationReply mengirim balasan quote + mention tanpa butuh
// *store.MessageContext penuh — jalur otomatis (handler) tidak membangun
// mctx, jadi pesan dirakit langsung di sini dengan bentuk yang sama seperti
// store.ReplyTextWithMentions (StanzaID + MentionedJID).
func sendModerationReply(ctx context.Context, client *whatsmeow.Client, chat, replier types.JID, quoteID string, quoted *waE2E.Message, text string, mentions []types.JID) error {
	bare := make([]string, 0, len(mentions))
	for _, jid := range mentions {
		bare = append(bare, jid.ToNonAD().String())
	}
	_, err := client.SendMessage(ctx, chat, moderationReplyMessage(replier, quoteID, text, bare, quoted))
	if err != nil {
		fmt.Printf("⚠️ Gagal kirim balasan moderasi ke %s: %v\n", chat, err)
		return err
	}
	// Balasan moderasi dikirim langsung via client (tanpa mctx), jadi
	// dicatat manual supaya tetap muncul di log terminal seperti pesan
	// keluar lain. Konteks minimal cukup karena LogOutgoing hanya butuh
	// Client (nama bot) dan Chat.
	store.LogOutgoing(&store.MessageContext{Client: client, Chat: chat}, "moderasi", text)
	return nil
}

// moderationReplyMessage merakit pesan quote + mention dengan bentuk yang sama
// seperti store.ReplyTextWithMentions — dipisah supaya sendModerationReply
// tetap ringkas. QuotedMessage memakai pesan asli (evt.Message) supaya
// balasan menempel ke pesan pelanggaran, bukan pesan lepas.
func moderationReplyMessage(replier types.JID, quoteID, text string, mentioned []string, quotedProto *waE2E.Message) *waE2E.Message {
	return &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String(text),
			ContextInfo: &waE2E.ContextInfo{
				StanzaID:      proto.String(quoteID),
				Participant:   proto.String(replier.String()),
				QuotedMessage: quotedProto,
				MentionedJID:  mentioned,
			},
		},
	}
}

// CheckAntilink dipanggil handler untuk tiap pesan grup non-command. Mengembalikan
// true kalau pesan ditindak (supaya handler tidak meneruskannya ke AI trigger).
//
// Alur: antilink aktif? → ada link invite? → pengirim owner/admin (bebas)?
// → link asing (bukan link grup ini)? → hapus pesan + warn. Semua langkah
// DB/jaringan yang gagal bersifat fail-soft: log dan lewati, jangan sampai
// salah hapus pesan orang.
func CheckAntilink(client *whatsmeow.Client, evt *events.Message, text string) bool {
	ctx := context.Background()
	chat := evt.Info.Chat
	sender := evt.Info.Sender

	on, err := store.Groups().IsAntilinkOn(ctx, chat.String())
	if err != nil {
		fmt.Printf("⚠️ Gagal cek antilink %s: %v\n", chat, err)
		return false
	}
	if !on {
		return false
	}

	codes := foreignInviteCodes(ctx, client, chat, text)
	if len(codes) == 0 {
		return false
	}

	// Pengecualian: owner dan admin grup bebas kirim link grup lain.
	if IsOwnerEvent(evt) {
		return false
	}
	mctx := &store.MessageContext{Client: client, Chat: chat, Sender: sender, RawEvt: evt}
	if admin, err := mctx.IsSenderAdmin(ctx); err != nil {
		fmt.Printf("⚠️ Gagal cek admin untuk antilink %s: %v\n", chat, err)
		return false
	} else if admin {
		return false
	}

	// Hapus pesan pelanggaran — bot harus admin; kalau gagal (bukan admin /
	// pesan terlalu lama), TETAP lanjut ke warn supaya pelanggar tercatat.
	if _, err := client.SendMessage(ctx, chat, client.BuildRevoke(chat, sender, evt.Info.ID)); err != nil {
		fmt.Printf("⚠️ Gagal hapus pesan link dari %s: %v\n", sender, err)
	}

	_ = giveWarn(ctx, client, chat, sender, sender.ToNonAD(), evt.Info.ID, evt.Message,
		fmt.Sprintf("kirim link grup lain (%d link)", len(codes)))
	return true
}

// foreignInviteCodes mengembalikan kode invite dari teks yang BUKAN milik grup
// ini. Link grup sendiri (dibandingkan via GetGroupInviteLink) selalu lolos.
// Kalau link grup sendiri tidak bisa diambil (error), kembalikan kosong supaya
// tidak ada salah hapus — lebih baik lolos satu link asing daripada menghapus
// link grup sendiri.
func foreignInviteCodes(ctx context.Context, client *whatsmeow.Client, chat types.JID, text string) []string {
	matches := inviteLinkRe.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}

	ownLink, err := client.GetGroupInviteLink(ctx, chat, false)
	if err != nil {
		fmt.Printf("⚠️ Gagal ambil invite link grup %s untuk antilink: %v\n", chat, err)
		return nil
	}
	ownCode := strings.TrimSpace(strings.TrimPrefix(ownLink, "https://chat.whatsapp.com/"))

	var foreign []string
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		if m[1] != ownCode {
			foreign = append(foreign, m[1])
		}
	}
	return foreign
}
