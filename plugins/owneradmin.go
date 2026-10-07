package plugins

import (
	"context"
	"fmt"
	"strings"

	"wabot-go/internal/config"
	"wabot-go/internal/logfmt"
	"wabot-go/internal/store"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func init() {
	registerPlugin(RegisterOwnerAdmin)
}

// IsOwner mengecek apakah pengirim pesan adalah salah satu owner bot
// (config.OwnerNumbers dari .env).
// SenderAlt ikut dicek karena di grup mode LID pengirim bisa muncul sebagai
// @lid sementara nomor owner tersimpan sebagai PN — tanpa cek alt ini owner
// justru terkunci di luar command miliknya sendiri.
func IsOwner(mctx *store.MessageContext) bool {
	if isJIDOwner(mctx.Sender) {
		return true
	}
	if mctx.RawEvt != nil && isJIDOwner(mctx.RawEvt.Info.SenderAlt) {
		return true
	}
	return false
}

// IsOwnerEvent versi berbasis events.Message mentah, dipakai handler untuk
// enforcement ban/mode SEBELUM MessageContext sempat dibangun.
func IsOwnerEvent(evt *events.Message) bool {
	if isJIDOwner(evt.Info.Sender) {
		return true
	}
	return isJIDOwner(evt.Info.SenderAlt)
}

func isJIDOwner(jid types.JID) bool {
	if jid.IsEmpty() || len(config.OwnerNumbers) == 0 {
		return false
	}
	user := jid.ToNonAD().User
	for _, owner := range config.OwnerNumbers {
		if user == owner {
			return true
		}
	}
	return false
}

// requireOwner adalah gerbang semua command di file ini: mengembalikan pesan
// error siap-kirim kalau pemanggil bukan owner, atau string kosong kalau lolos.
func requireOwner(mctx *store.MessageContext) string {
	if !IsOwner(mctx) {
		return "Command ini khusus owner."
	}
	return ""
}

// IsOwnerOrTrusted lolos kalau pengirim adalah owner ATAU user yang dipercaya
// mengakses Flora (daftar trust, lihat store.GroupDB.TrustUser). Dipakai
// command .agent dan trigger natural AI. SenderAlt ikut dicek seperti
// IsOwner supaya trust tetap mempan di grup mode LID. DB error = tolak
// non-owner (fail-closed untuk kontrol akses) + catat ke console.
func IsOwnerOrTrusted(ctx context.Context, mctx *store.MessageContext) bool {
	if IsOwner(mctx) {
		return true
	}
	var senderAlt types.JID
	if mctx.RawEvt != nil {
		senderAlt = mctx.RawEvt.Info.SenderAlt
	}
	ok, err := store.Groups().IsUserTrusted(ctx, mctx.Sender, senderAlt)
	if err != nil {
		logfmt.Error("Gagal cek trust Flora untuk %s: %v\n", mctx.Sender.String(), err)
		return false
	}
	return ok
}

// IsOwnerOrTrustedEvent adalah versi berbasis events.Message mentah (untuk
// handler, sebelum MessageContext dibangun). IsOwnerEvent sudah mencakup
// SenderAlt; untuk trust keduanya diteruskan ke IsUserTrusted.
func IsOwnerOrTrustedEvent(ctx context.Context, evt *events.Message) bool {
	if IsOwnerEvent(evt) {
		return true
	}
	ok, err := store.Groups().IsUserTrusted(ctx, evt.Info.Sender, evt.Info.SenderAlt)
	if err != nil {
		logfmt.Error("Gagal cek trust Flora untuk %s: %v\n", evt.Info.Sender.String(), err)
		return false
	}
	return ok
}

// resolveBanTarget mengambil target ban/unban dari mention, pesan reply,
// atau nomor HP di argumen pertama. Hasil selalu dinormalisasi ToNonAD
// supaya format penyimpanannya konsisten (lihat store.BanUser).
func resolveBanTarget(mctx *store.MessageContext) (types.JID, error) {
	if mentioned := mctx.MentionedJIDs(); len(mentioned) > 0 {
		jid, err := types.ParseJID(mentioned[0])
		if err != nil {
			return types.JID{}, fmt.Errorf("JID mention tidak valid")
		}
		return jid.ToNonAD(), nil
	}
	if quoted := mctx.QuotedParticipant(); quoted != "" {
		jid, err := types.ParseJID(quoted)
		if err != nil {
			return types.JID{}, fmt.Errorf("JID reply tidak valid")
		}
		return jid.ToNonAD(), nil
	}
	if len(mctx.Args) > 0 {
		// Reuse parser nomor yang sama dengan .add supaya format yang
		// diterima konsisten di semua command.
		jid, err := ParsePhoneToJID(mctx.Args[0])
		if err != nil {
			return types.JID{}, err
		}
		return jid.ToNonAD(), nil
	}
	return types.JID{}, fmt.Errorf("tag (@user), reply pesan target, atau sertakan nomor — contoh: .ban 6281234567890")
}

// RegisterOwnerAdmin mendaftarkan command khusus owner: ban/unban user,
// ban/unban grup, dan pengaturan mode self/public. Semua command digate
// requireOwner DAN disembunyikan dari .menu non-owner (lihat menu.go).
func RegisterOwnerAdmin(r *Registry) {
	r.Register(Command{
		Name:        "ban",
		Description: "Ban user (tag/reply/nomor), bot abaikan semua pesannya",
		Category:    CategoryOwner,
		Handler:     handleBan,
	})
	r.Register(Command{
		Name:        "unban",
		Description: "Cabut ban user (tag/reply/nomor)",
		Category:    CategoryOwner,
		Handler:     handleUnban,
	})
	r.Register(Command{
		Name:        "banlist",
		Description: "Lihat daftar user yang di-ban",
		Category:    CategoryOwner,
		Handler:     handleBanList,
	})
	r.Register(Command{
		Name:        "bangroup",
		Description: "Ban grup ini, bot abaikan semua pesan dari sini",
		Category:    CategoryOwner,
		Handler:     handleBanGroup,
	})
	r.Register(Command{
		Name:        "unbangroup",
		Description: "Cabut ban grup ini",
		Category:    CategoryOwner,
		Handler:     handleUnbanGroup,
	})
	r.Register(Command{
		Name:        "bangrouplist",
		Description: "Lihat daftar grup yang di-ban",
		Category:    CategoryOwner,
		Handler:     handleBanGroupList,
	})
	r.Register(Command{
		Name:        "mode",
		Description: "Lihat/atur mode bot (.mode / .mode self / .mode public)",
		Category:    CategoryOwner,
		Handler:     handleMode,
	})
}

func handleBan(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	target, err := resolveBanTarget(mctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, err.Error())
	}
	// Owner tidak boleh mem-ban dirinya sendiri — kalau sampai terjadi,
	// satu-satunya yang bisa unban (owner) ikut terkunci di mode self.
	if isJIDOwner(target) {
		return store.ReplyText(ctx, mctx, "Tidak bisa mem-ban owner sendiri.")
	}
	if err := store.Groups().BanUser(ctx, target); err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal ban: %v", err))
	}
	return store.ReplyTextWithMentions(ctx, mctx,
		fmt.Sprintf("@%s telah di-ban. Bot tidak akan merespons pesannya lagi.", target.User),
		[]types.JID{target})
}

func handleUnban(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	target, err := resolveBanTarget(mctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, err.Error())
	}
	removed, err := store.Groups().UnbanUser(ctx, target)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal unban: %v", err))
	}
	// Sama seperti .ban: "@..." harus berpasangan dengan MentionedJID supaya
	// jadi tag beneran, bukan teks polos.
	if !removed {
		return store.ReplyTextWithMentions(ctx, mctx,
			fmt.Sprintf("@%s tidak ada di daftar ban.", target.User),
			[]types.JID{target})
	}
	return store.ReplyTextWithMentions(ctx, mctx,
		fmt.Sprintf("@%s sudah di-unban, bot meresponsnya lagi.", target.User),
		[]types.JID{target})
}

func handleBanList(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	list, err := store.Groups().ListBannedUsers(ctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal baca daftar ban: %v", err))
	}
	if len(list) == 0 {
		return store.ReplyText(ctx, mctx, "Daftar ban user kosong.")
	}
	// Tiap baris memakai "@<User>" yang berpasangan dengan MentionedJID
	// (metode yang sama dengan tagall) supaya jadi tag beneran. Baris yang
	// JID-nya tidak bisa di-parse ditulis polos sebagai fallback — daftar
	// tetap tampil lengkap walau satu baris datanya rusak.
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("*Daftar Ban User* (%d)\n\n", len(list)))
	mentions := make([]types.JID, 0, len(list))
	for i, raw := range list {
		bare, err := types.ParseJID(raw)
		if err != nil || bare.IsEmpty() {
			sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, raw))
			continue
		}
		bare = bare.ToNonAD()
		sb.WriteString(fmt.Sprintf("%d. @%s\n", i+1, bare.User))
		mentions = append(mentions, bare)
	}
	if len(mentions) == 0 {
		return store.ReplyText(ctx, mctx, sb.String())
	}
	return store.ReplyTextWithMentions(ctx, mctx, sb.String(), mentions)
}

func handleBanGroup(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	if !mctx.IsGroup() {
		return store.ReplyText(ctx, mctx, "Command ini hanya bisa dipakai di dalam grup yang mau di-ban.")
	}
	if err := store.Groups().BanGroup(ctx, mctx.Chat.String()); err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal ban grup: %v", err))
	}
	return store.ReplyText(ctx, mctx, "Grup ini telah di-ban. Bot tidak akan merespons pesan dari sini lagi (kecuali command owner).")
}

func handleUnbanGroup(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	if !mctx.IsGroup() {
		return store.ReplyText(ctx, mctx, "Command ini hanya bisa dipakai di dalam grup yang mau di-unban.")
	}
	removed, err := store.Groups().UnbanGroup(ctx, mctx.Chat.String())
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal unban grup: %v", err))
	}
	if !removed {
		return store.ReplyText(ctx, mctx, "Grup ini tidak ada di daftar ban.")
	}
	return store.ReplyText(ctx, mctx, "Ban grup ini sudah dicabut, bot merespons lagi.")
}

func handleBanGroupList(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	list, err := store.Groups().ListBannedGroups(ctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal baca daftar ban grup: %v", err))
	}
	if len(list) == 0 {
		return store.ReplyText(ctx, mctx, "Daftar ban grup kosong.")
	}
	// Tampilkan NAMA grup (bukan JID) supaya langsung dikenali owner.
	// Nama diambil live via GetGroupInfo; kalau gagal (mis. bot sudah keluar
	// dari grup itu) fallback ke JID mentah — daftar tetap lengkap, cuma
	// satu baris yang tampil sebagai ID. Fail-soft per baris supaya satu
	// grup yang error tidak menggagalkan seluruh daftar.
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("*Daftar Ban Grup* (%d)\n\n", len(list)))
	for i, raw := range list {
		name := groupNameOrFallback(ctx, mctx, raw)
		sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, name))
	}
	return store.ReplyText(ctx, mctx, sb.String())
}

// groupNameOrFallback mengambil nama grup dari WhatsApp untuk ditampilkan di
// daftar ban. Mengembalikan JID mentah kalau JID tidak valid atau info grup
// tidak bisa diambil.
func groupNameOrFallback(ctx context.Context, mctx *store.MessageContext, raw string) string {
	gjid, err := types.ParseJID(raw)
	if err != nil || gjid.IsEmpty() {
		return raw
	}
	info, err := mctx.Client.GetGroupInfo(ctx, gjid)
	if err != nil || info.Name == "" {
		return raw
	}
	return info.Name
}

func handleMode(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	arg := strings.ToLower(strings.TrimSpace(strings.Join(mctx.Args, " ")))
	if arg == "" {
		mode, err := store.Groups().GetBotMode(ctx)
		if err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal baca mode: %v", err))
		}
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Mode bot saat ini: *%s*\n\n- self: hanya owner yang dilayani\n- public: semua orang dilayani", mode))
	}
	if err := store.Groups().SetBotMode(ctx, arg); err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("%v", err))
	}
	if arg == store.BotModeSelf {
		return store.ReplyText(ctx, mctx, "Mode *self* aktif — bot hanya merespons owner sekarang.")
	}
	return store.ReplyText(ctx, mctx, "Mode *public* aktif — bot merespons semua orang lagi.")
}
