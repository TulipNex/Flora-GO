package plugins

import (
	"context"
	"fmt"
	"strings"

	"wabot-go/internal/store"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

func init() {
	registerPlugin(RegisterGroupManage)
}

// RegisterGroupManage mendaftarkan command dasar pengelolaan member grup:
// .kick, .promote, .demote, .tagall, .add
func RegisterGroupManage(r *Registry) {
	r.Register(Command{
		Name:        "kick",
		Description: "Keluarkan member dari grup (mention atau reply pesan target)",
		Category:    CategoryGrup,
		Handler:     handleKick,
	})
	r.Register(Command{
		Name:        "promote",
		Description: "Jadikan member sebagai admin grup (mention atau reply pesan target)",
		Category:    CategoryGrup,
		Handler:     handlePromote,
	})
	r.Register(Command{
		Name:        "demote",
		Description: "Turunkan admin grup jadi member biasa (mention atau reply pesan target)",
		Category:    CategoryGrup,
		Handler:     handleDemote,
	})
	r.Register(Command{
		Name:        "tagall",
		Description: "Mention semua member grup, opsional sertakan pesan (.tagall <pesan>)",
		Category:    CategoryGrup,
		Handler:     handleTagAll,
	})
	r.Register(Command{
		Name:        "add",
		Description: "Tambahkan member ke grup lewat nomor, contoh: .add 6281234567890",
		Category:    CategoryGrup,
		Handler:     handleAdd,
	})
}

// resolveTargetJID mengambil target command dari mention (@user) atau dari
// pesan yang di-reply. Mengembalikan error kalau keduanya tidak ada, supaya
// tiap handler tidak perlu menulis ulang pesan error yang sama.
func resolveTargetJID(mctx *store.MessageContext) (types.JID, error) {
	if mentioned := mctx.MentionedJIDs(); len(mentioned) > 0 {
		return types.ParseJID(mentioned[0])
	}
	if quoted := mctx.QuotedParticipant(); quoted != "" {
		return types.ParseJID(quoted)
	}
	return types.JID{}, fmt.Errorf("tag member yang dimaksud (@user) atau reply pesannya")
}

// displayNumber mencari nomor telepon (PN) participant yang cocok dengan JID
// target, untuk ditampilkan ke user di pesan hasil command. Ini perlu karena
// di grup dengan AddressingMode "lid", whatsmeow bisa mengembalikan/menyimpan
// JID participant dalam bentuk @lid (mis. "258149730799767@lid") padahal user
// mengenal member itu lewat nomor teleponnya (mis. "6285591068997"). Tanpa
// resolusi ini, pesan hasil command akan menampilkan LID yang tidak dikenali
// user sama sekali.
//
// Pencocokan participant->target memakai store.ParticipantMatchesJID — logika
// LID vs PN yang SAMA dengan IsSenderAdmin/IsBotAdmin (sebelumnya ada salinan
// lokal participantMatchesJIDLocal yang diduplikasi baris per baris, dihapus
// oleh audit duplikasi supaya cuma ada satu sumber kebenaran).
//
// info boleh nil (mis. gagal fetch GroupInfo) — dalam hal itu fallback ke
// target.User apa adanya, lebih baik dari pada error total.
func displayNumber(info *types.GroupInfo, target types.JID) string {
	if info != nil {
		for _, p := range info.Participants {
			if store.ParticipantMatchesJID(p, target) && !p.PhoneNumber.IsEmpty() {
				return p.PhoneNumber.User
			}
		}
	}
	return target.User
}

// requireGroupAdminGate melakukan pengecekan yang sama dipakai berulang di
// kick/promote/demote: harus di grup, pengirim harus admin, bot harus admin.
// Mengembalikan pesan error siap-kirim kalau salah satu syarat tidak terpenuhi,
// atau string kosong kalau semua syarat lolos.
func requireGroupAdminGate(ctx context.Context, mctx *store.MessageContext) string {
	if !mctx.IsGroup() {
		return "⚠️ Command ini hanya bisa dipakai di dalam grup."
	}
	isSenderAdmin, err := mctx.IsSenderAdmin(ctx)
	if err != nil {
		return fmt.Sprintf("⚠️ Gagal cek status admin: %v", err)
	}
	if !isSenderAdmin {
		return "🚫 Hanya admin grup yang boleh pakai command ini."
	}
	isBotAdmin, err := mctx.IsBotAdmin(ctx)
	if err != nil {
		return fmt.Sprintf("⚠️ Gagal cek status admin bot: %v", err)
	}
	if !isBotAdmin {
		return "🚫 Bot harus jadi admin dulu di grup ini supaya bisa melakukan aksi tersebut."
	}
	return ""
}

// RequireGroupAdminGate adalah versi exported dari requireGroupAdminGate,
// dibuat khusus supaya tool AI (internal/ai/tools/bridge) bisa memakai
// validasi admin yang SAMA PERSIS dengan command .kick/.promote/.demote,
// tanpa duplikasi logic. Perilaku command yang sudah ada TIDAK berubah —
// ini murni pintu masuk tambahan ke fungsi private yang sama.
func RequireGroupAdminGate(ctx context.Context, mctx *store.MessageContext) string {
	return requireGroupAdminGate(ctx, mctx)
}

// canonicalMentionJID mengembalikan JID primer participant yang cocok dengan
// target (p.JID dari GroupInfo — yaitu LID di grup mode LID) supaya teks
// "@..." dan MentionedJID berpasangan persis seperti di handleTagAll.
// Fallback ke target apa adanya kalau GroupInfo nil / tidak ketemu, lebih
// baik kirim sesuatu daripada gagal total.
func canonicalMentionJID(info *types.GroupInfo, target types.JID) types.JID {
	if info != nil {
		for _, p := range info.Participants {
			if store.ParticipantMatchesJID(p, target) {
				return p.JID.ToNonAD()
			}
		}
	}
	return target.ToNonAD()
}

// replySingleMention mengirim hasil aksi single-target (kick/promote/demote)
// sebagai reply dengan mention beneran (highlight + notif), memakai metode
// yang sama dengan handleTagAll: teks "@<bare.User>" + MentionedJID bare.
// Nomor PN (kalau berbeda dari bare.User, yaitu di grup LID) ditampilkan
// dalam kurung di luar "@..." sebagai info baca manusia — tidak mengganggu
// pairing karena pairing hanya melihat bagian "@..." vs MentionedJID.
func replySingleMention(ctx context.Context, mctx *store.MessageContext, info *types.GroupInfo, target types.JID, suffix string) error {
	bare := canonicalMentionJID(info, target)
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("✅ @%s", bare.User))
	if pn := displayNumber(info, target); pn != "" && pn != bare.User {
		sb.WriteString(fmt.Sprintf(" (%s)", pn))
	}
	sb.WriteString(suffix)
	return store.ReplyTextWithMentions(ctx, mctx, sb.String(), []types.JID{bare})
}

func handleKick(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireGroupAdminGate(ctx, mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	target, err := resolveTargetJID(mctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, "⚠️ "+err.Error())
	}

	// Resolve JID primer SEBELUM aksi kick dijalankan — setelah member
	// dikeluarkan, dia tidak lagi ada di GroupInfo.Participants sehingga
	// pemetaan ke JID primer tidak akan berhasil lagi.
	// Catatan: mention ke member yang baru di-kick memang tidak akan
	// menghasilkan notif (dia sudah keluar), tapi pairing tetap dibuat benar
	// supaya tampil highlight konsisten dengan command lain.
	infoBefore, _ := mctx.GroupInfo(ctx)

	_, err = mctx.Client.UpdateGroupParticipants(ctx, mctx.Chat, []types.JID{target}, whatsmeow.ParticipantChangeRemove)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("❌ Gagal kick: %v", err))
	}
	return replySingleMention(ctx, mctx, infoBefore, target, " berhasil dikeluarkan dari grup.")
}

func handlePromote(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireGroupAdminGate(ctx, mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	target, err := resolveTargetJID(mctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, "⚠️ "+err.Error())
	}

	infoBefore, _ := mctx.GroupInfo(ctx)

	_, err = mctx.Client.UpdateGroupParticipants(ctx, mctx.Chat, []types.JID{target}, whatsmeow.ParticipantChangePromote)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("❌ Gagal promote: %v", err))
	}
	return replySingleMention(ctx, mctx, infoBefore, target, " sekarang jadi admin grup.")
}

func handleDemote(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireGroupAdminGate(ctx, mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	target, err := resolveTargetJID(mctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, "⚠️ "+err.Error())
	}

	infoBefore, _ := mctx.GroupInfo(ctx)

	_, err = mctx.Client.UpdateGroupParticipants(ctx, mctx.Chat, []types.JID{target}, whatsmeow.ParticipantChangeDemote)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("❌ Gagal demote: %v", err))
	}
	return replySingleMention(ctx, mctx, infoBefore, target, " diturunkan jadi member biasa.")
}

func handleTagAll(ctx context.Context, mctx *store.MessageContext) error {
	if !mctx.IsGroup() {
		return store.ReplyText(ctx, mctx, "⚠️ Command ini hanya bisa dipakai di dalam grup.")
	}
	isSenderAdmin, err := mctx.IsSenderAdmin(ctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("⚠️ Gagal cek status admin: %v", err))
	}
	if !isSenderAdmin {
		return store.ReplyText(ctx, mctx, "🚫 Hanya admin grup yang boleh pakai command ini.")
	}

	info, err := mctx.GroupInfo(ctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("⚠️ Gagal ambil data grup: %v", err))
	}

	pesan := strings.Join(mctx.Args, " ")
	if pesan == "" {
		pesan = "📢 Panggilan untuk semua member grup"
	}

	var sb strings.Builder
	sb.WriteString(pesan)
	sb.WriteString("\n\n")

	mentions := make([]string, 0, len(info.Participants))
	for _, p := range info.Participants {
		// Teks "@xxx" WAJIB memakai User dari JID yang SAMA dengan yang
		// dimasukkan ke MentionedJID — klien WhatsApp memasangkan keduanya
		// berdasarkan kecocokan User. Sebelumnya teks memakai PhoneNumber
		// (@628...) tapi MentionedJID memakai p.JID (@lid di grup LID),
		// sehingga tidak ada pasangan yang cocok dan tag gagal total
		// (hanya tampil teks polos, tanpa highlight/notif).
		// Di grup LID teks memang terlihat seperti "@18189..." (bukan nomor),
		// tapi itulah yang diminta protokol — klien yang me-render-nya jadi
		// nama kontak + highlight. ToNonAD dipakai supaya device/agent
		// tidak ikut terbawa ke teks maupun MentionedJID.
		bare := p.JID.ToNonAD()
		sb.WriteString(fmt.Sprintf("@%s\n", bare.User))
		mentions = append(mentions, bare.String())
	}

	_, err = mctx.Client.SendMessage(ctx, mctx.Chat, &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String(sb.String()),
			ContextInfo: &waE2E.ContextInfo{
				MentionedJID: mentions,
			},
		},
	})
	if err == nil {
		store.LogOutgoing(mctx, "tagall", pesan)
	}
	return err
}

// parsePhoneToJID menerima nomor telepon mentah dari argumen command
// (misal "6281234567890", boleh diawali "+", boleh ada spasi/strip) dan
// mengubahnya jadi types.JID di server default (@s.whatsapp.net).
func parsePhoneToJID(raw string) (types.JID, error) {
	cleaned := strings.NewReplacer("+", "", " ", "", "-", "").Replace(raw)
	if cleaned == "" {
		return types.JID{}, fmt.Errorf("nomor tidak boleh kosong")
	}
	for _, r := range cleaned {
		if r < '0' || r > '9' {
			return types.JID{}, fmt.Errorf("nomor %q mengandung karakter tidak valid, pakai format 62xxxxxxxxxx", raw)
		}
	}
	return types.NewJID(cleaned, types.DefaultUserServer), nil
}

// ParsePhoneToJID adalah versi exported dari parsePhoneToJID, dibuat khusus
// supaya tool AI (internal/ai/tools/bridge) bisa mem-validasi & mengubah
// nomor telepon dari argumen tool jadi types.JID dengan logic yang SAMA
// PERSIS dengan command .add — tanpa duplikasi.
func ParsePhoneToJID(raw string) (types.JID, error) {
	return parsePhoneToJID(raw)
}

func handleAdd(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireGroupAdminGate(ctx, mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	if len(mctx.Args) == 0 {
		return store.ReplyText(ctx, mctx, "⚠️ Isi nomor yang mau ditambahkan, contoh: .add 6281234567890")
	}

	// Dukung banyak nomor sekaligus, dipisah spasi: .add 6281111 6282222
	targets := make([]types.JID, 0, len(mctx.Args))
	for _, arg := range mctx.Args {
		jid, err := parsePhoneToJID(arg)
		if err != nil {
			return store.ReplyText(ctx, mctx, "⚠️ "+err.Error())
		}
		targets = append(targets, jid)
	}

	results, err := mctx.Client.UpdateGroupParticipants(ctx, mctx.Chat, targets, whatsmeow.ParticipantChangeAdd)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("❌ Gagal menambahkan member: %v", err))
	}

	// PENTING: whatsmeow bisa mengembalikan p.JID dalam bentuk LID untuk member
	// yang baru ditambahkan (bukan PN yang barusan diketik user di .add), kalau
	// grup memakai AddressingMode "lid". UpdateGroupParticipants menjamin
	// urutan results sejajar dengan urutan targets yang dikirim, jadi nomor
	// ketikan user diambil dari mctx.Args berdasarkan index.
	//
	// Supaya tag berfungsi (metode yang sama dengan tagall/kick/promote),
	// baris sukses memakai "@<bare.User>" dari JID hasil server + MentionedJID
	// yang berpasangan, dengan nomor ketikan user ditampilkan dalam kurung
	// sebagai info baca. Baris gagal/invite tidak di-mention (member belum
	// tentu ada di grup) — tampil nomor ketikan saja seperti sebelumnya.
	var sb strings.Builder
	sb.WriteString("📋 Hasil penambahan member:\n")
	mentions := make([]types.JID, 0, len(results))
	for i, p := range results {
		typed := p.JID.User
		if i < len(mctx.Args) {
			typed = mctx.Args[i]
		}
		switch {
		case p.Error == 0:
			bare := p.JID.ToNonAD()
			// Kalau server tidak mengembalikan JID valid, fallback ke JID
			// yang tadi dikirim (targets sejajar index dengan results).
			if bare.IsEmpty() && i < len(targets) {
				bare = targets[i].ToNonAD()
			}
			sb.WriteString(fmt.Sprintf("✅ @%s", bare.User))
			if typed != "" && typed != bare.User {
				sb.WriteString(fmt.Sprintf(" (%s)", typed))
			}
			sb.WriteString(" berhasil ditambahkan\n")
			mentions = append(mentions, bare)
		case p.AddRequest != nil:
			sb.WriteString(fmt.Sprintf("📨 %s tidak bisa ditambah langsung (privasi), undangan invite terkirim\n", typed))
		default:
			sb.WriteString(fmt.Sprintf("❌ %s gagal ditambahkan (kode error %d)\n", typed, p.Error))
		}
	}

	if len(mentions) == 0 {
		return store.ReplyText(ctx, mctx, sb.String())
	}
	return store.ReplyTextWithMentions(ctx, mctx, sb.String(), mentions)
}
