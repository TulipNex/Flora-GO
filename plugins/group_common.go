package plugins

import (
	"context"
	"fmt"
	"strings"

	"wabot-go/internal/store"

	"github.com/polymorfa/hypermeow/types"
)

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
// target, untuk ditampilkan ke user di pesan hasil command.
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
func requireGroupAdminGate(ctx context.Context, mctx *store.MessageContext) string {
	if !mctx.IsGroup() {
		return "Command ini hanya bisa dipakai di dalam grup."
	}
	isSenderAdmin, err := mctx.IsSenderAdmin(ctx)
	if err != nil {
		return Fail("Gagal cek status admin", err)
	}
	if !isSenderAdmin {
		return "Hanya admin grup yang boleh pakai command ini."
	}
	isBotAdmin, err := mctx.IsBotAdmin(ctx)
	if err != nil {
		return Fail("Gagal cek status admin bot", err)
	}
	if !isBotAdmin {
		return "Bot harus jadi admin dulu di grup ini."
	}
	return ""
}

// RequireGroupAdminGate adalah versi exported dari requireGroupAdminGate untuk tool AI.
func RequireGroupAdminGate(ctx context.Context, mctx *store.MessageContext) string {
	return requireGroupAdminGate(ctx, mctx)
}

// canonicalMentionJID mengembalikan JID primer participant yang cocok dengan target.
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
// sebagai reply dengan mention beneran (highlight + notif).
func replySingleMention(ctx context.Context, mctx *store.MessageContext, info *types.GroupInfo, target types.JID, suffix string) error {
	bare := canonicalMentionJID(info, target)
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("@%s", bare.User))
	if pn := displayNumber(info, target); pn != "" && pn != bare.User {
		sb.WriteString(fmt.Sprintf(" (%s)", pn))
	}
	sb.WriteString(suffix)
	return store.ReplyTextWithMentions(ctx, mctx, sb.String(), []types.JID{bare})
}

// isBotTarget true kalau target adalah akun bot sendiri.
func isBotTarget(mctx *store.MessageContext, target types.JID) bool {
	return store.IsBotJID(mctx.Client, target)
}

// IsBotTarget adalah versi exported dari isBotTarget untuk tool AI.
func IsBotTarget(mctx *store.MessageContext, target types.JID) bool {
	return isBotTarget(mctx, target)
}

// isOwnerTarget true kalau target adalah owner bot.
func isOwnerTarget(ctx context.Context, mctx *store.MessageContext, target types.JID) bool {
	if isJIDOwner(target) {
		return true
	}
	if mctx.Client != nil && mctx.Client.Store != nil {
		if alt, err := mctx.Client.Store.GetAltJID(ctx, target); err == nil && !alt.IsEmpty() && isJIDOwner(alt) {
			return true
		}
	}
	return false
}

// IsOwnerTarget adalah versi exported dari isOwnerTarget untuk tool AI.
func IsOwnerTarget(ctx context.Context, mctx *store.MessageContext, target types.JID) bool {
	return isOwnerTarget(ctx, mctx, target)
}

// parsePhoneToJID menerima nomor telepon mentah dan mengubahnya jadi types.JID.
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
	if strings.HasPrefix(cleaned, "0") {
		cleaned = "62" + cleaned[1:]
	}
	return types.NewJID(cleaned, types.DefaultUserServer), nil
}

// ParsePhoneToJID adalah versi exported dari parsePhoneToJID untuk tool AI.
func ParsePhoneToJID(raw string) (types.JID, error) {
	return parsePhoneToJID(raw)
}
