package plugins

import (
	"context"
	"fmt"
	"strings"

	"github.com/polymorfa/hypermeow/types"

	"wabot-go/internal/store"
)

// handleTrustSubcommand menangani ".agent trust @user" /
// ".agent untrust @user" (KHUSUS owner). Target diambil dari mention,
// pesan yang di-reply, atau nomor HP di argumen kedua — cermin
// resolveBanTarget, tapi nomor dibaca dari Args[1] (Args[0] = "trust").
func handleTrustSubcommand(ctx context.Context, mctx *store.MessageContext, trust bool) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	action := "trust"
	if !trust {
		action = "untrust"
	}
	target, err := resolveTrustTarget(mctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("%v — contoh: .agent %s @user", err, action))
	}
	if trust {
		if err := store.Groups().TrustUser(ctx, target); err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal memberi kepercayaan: %v", err))
		}
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Oke, %s sekarang dipercaya mengakses Flora.", target.User))
	}
	removed, err := store.Groups().UntrustUser(ctx, target)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal mencabut kepercayaan: %v", err))
	}
	if !removed {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("%s tidak ada di daftar trust.", target.User))
	}
	return store.ReplyText(ctx, mctx, fmt.Sprintf("Kepercayaan %s sudah dicabut.", target.User))
}

// handleTrustListSubcommand menangani ".agent trustlist" (KHUSUS owner):
// menampilkan seluruh user yang dipercaya + waktu ditambahkan.
func handleTrustListSubcommand(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	list, err := store.Groups().ListTrustedUsers(ctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal baca daftar trust: %v", err))
	}
	if len(list) == 0 {
		return store.ReplyText(ctx, mctx, "Daftar trust kosong — belum ada user yang dipercaya.")
	}
	var sb strings.Builder
	sb.WriteString("Daftar trust Flora:\n")
	mentions := make([]types.JID, 0, len(list))
	for i, e := range list {
		// Tag biru butuh pairing "@<User>" di teks + MentionedJID (aturan
		// yang sama dengan tagall/ReplyTextWithMentions). JID yang gagal
		// di-parse tampil polos supaya satu baris rusak tidak menggagalkan
		// seluruh daftar.
		if jid, perr := types.ParseJID(e.JID); perr == nil && jid.User != "" {
			bare := jid.ToNonAD()
			sb.WriteString(fmt.Sprintf("%d. @%s\n", i+1, bare.User))
			mentions = append(mentions, bare)
		} else {
			name := e.Part
			if name == "" {
				name = e.JID
			}
			sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, name))
		}
	}
	if len(mentions) == 0 {
		return store.ReplyText(ctx, mctx, sb.String())
	}
	return store.ReplyTextWithMentions(ctx, mctx, sb.String(), mentions)
}

// resolveTrustTarget mengambil target trust/untrust dari mention, pesan
// reply, atau nomor HP di argumen kedua. Hasil dinormalisasi ToNonAD
// supaya format penyimpanannya konsisten (lihat store.TrustUser).
func resolveTrustTarget(mctx *store.MessageContext) (types.JID, error) {
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
	if len(mctx.Args) > 1 {
		// Gabung SEMUA sisa argumen: nomor ber-spasi ("+62 822-1234-5678")
		// terpecah jadi beberapa Args oleh strings.Fields — cleaner di
		// ParsePhoneToJID membuang spasi/strip/plus-nya kembali.
		jid, err := ParsePhoneToJID(strings.Join(mctx.Args[1:], " "))
		if err != nil {
			return types.JID{}, err
		}
		return jid.ToNonAD(), nil
	}
	return types.JID{}, fmt.Errorf("tag (@user), reply pesan target, atau sertakan nomor")
}
