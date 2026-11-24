package plugins

import (
	"context"
	"fmt"

	"wabot-go/internal/config"
	"wabot-go/internal/identity"
	"wabot-go/internal/logfmt"
	"wabot-go/internal/store"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/types"
	"github.com/polymorfa/hypermeow/types/events"
)

// IsOwner mengecek apakah pengirim pesan adalah salah satu owner bot.
func IsOwner(mctx *store.MessageContext) bool {
	if isJIDOwner(mctx.Sender) {
		return true
	}
	if mctx.RawEvt != nil && isJIDOwner(mctx.RawEvt.Info.SenderAlt) {
		return true
	}
	if pn, ok := resolveSenderPN(mctx); ok && isJIDOwner(pn) {
		return true
	}
	return isSelfSender(mctx) && SelfCommandEnabled()
}

func resolveSenderPN(mctx *store.MessageContext) (types.JID, bool) {
	var alt types.JID
	if mctx.RawEvt != nil {
		alt = mctx.RawEvt.Info.SenderAlt
	}
	return identity.ResolvePN(context.Background(), mctx.Client, mctx.Sender, alt)
}

func IsOwnerEvent(evt *events.Message) bool {
	if isJIDOwner(evt.Info.Sender) {
		return true
	}
	return isJIDOwner(evt.Info.SenderAlt)
}

func isJIDOwner(jid types.JID) bool {
	if len(config.OwnerNumbers) == 0 {
		return false
	}
	user := identity.UserPart(jid)
	if user == "" {
		return false
	}
	for _, owner := range config.OwnerNumbers {
		if user == owner {
			return true
		}
	}
	return false
}

func requireOwner(mctx *store.MessageContext) string {
	if !IsOwner(mctx) {
		return "Command ini khusus owner."
	}
	return ""
}

func IsOwnerOrTrusted(ctx context.Context, mctx *store.MessageContext) bool {
	if IsOwner(mctx) {
		return true
	}
	var senderAlt types.JID
	if mctx.RawEvt != nil {
		senderAlt = mctx.RawEvt.Info.SenderAlt
	}
	ok, err := store.GroupsFor(mctx.Client).IsUserTrusted(ctx, mctx.Sender, senderAlt)
	if err != nil {
		logfmt.Error("Gagal cek trust Flora untuk %s: %v\n", mctx.Sender.String(), err)
		return false
	}
	return ok
}

func IsOwnerOrTrustedEvent(ctx context.Context, client *whatsmeow.Client, evt *events.Message) bool {
	if IsOwnerEvent(evt) {
		return true
	}
	ok, err := store.GroupsFor(client).IsUserTrusted(ctx, evt.Info.Sender, evt.Info.SenderAlt)
	if err != nil {
		logfmt.Error("Gagal cek trust Flora untuk %s: %v\n", evt.Info.Sender.String(), err)
		return false
	}
	return ok
}

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
		jid, err := ParsePhoneToJID(mctx.Args[0])
		if err != nil {
			return types.JID{}, err
		}
		return jid.ToNonAD(), nil
	}
	return types.JID{}, fmt.Errorf("tag (@user), reply pesan target, atau sertakan nomor — contoh: .ban 6281234567890")
}
