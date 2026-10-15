package plugins

import (
	"context"
	"fmt"

	"wabot-go/internal/store"

	"github.com/polymorfa/hypermeow/types"
)

func init() {
	registerPlugin(RegisterDelete)
}

// RegisterDelete mendaftarkan command `.delete` (alias `.del`): hapus pesan
// yang di-reply. Berlaku untuk pesan bot sendiri (siapa saja boleh, di
// private maupun grup) dan pesan orang lain (khusus grup, pengirim harus
// admin/owner + bot harus admin — WhatsApp menolak selain itu).
func RegisterDelete(r *Registry) {
	r.Register(Command{
		Name:        "delete",
		Aliases:     []string{"del"},
		Description: "Hapus pesan yang di-reply",
		Category:    CategoryAlat,
		Handler:     handleDelete,
	})
}

func handleDelete(ctx context.Context, mctx *store.MessageContext) error {
	quotedSender := mctx.QuotedParticipant()
	if quotedSender == "" {
		return store.ReplyText(ctx, mctx, "Reply pesan yang mau dihapus, lalu ketik .delete")
	}
	quotedJID, err := types.ParseJID(quotedSender)
	if err != nil {
		return store.ReplyText(ctx, mctx, "JID pesan yang di-reply tidak valid.")
	}
	msgID := store.ExtractStanzaID(mctx.RawEvt)
	if msgID == "" {
		return store.ReplyText(ctx, mctx, "ID pesan yang di-reply tidak ditemukan.")
	}

	// Pesan bot sendiri: siapa saja boleh hapus, di mana saja (termasuk
	// private) — bot selalu boleh menarik pesannya sendiri.
	if !isBotJID(mctx, quotedJID) {
		// Pesan orang lain: hanya di grup, dan butuh hak hapus.
		if !mctx.IsGroup() {
			return store.ReplyText(ctx, mctx, "Pesan orang lain hanya bisa dihapus di dalam grup.")
		}
		if IsOwner(mctx) {
			if admin, err := mctx.IsBotAdmin(ctx); err != nil {
				return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal cek status admin bot: %v", err))
			} else if !admin {
				return store.ReplyText(ctx, mctx, "Bot harus jadi admin dulu supaya bisa menghapus pesan orang lain.")
			}
		} else if msg := requireGroupAdminGate(ctx, mctx); msg != "" {
			return store.ReplyText(ctx, mctx, msg)
		}
	}

	// BuildRevoke dengan sender = pengirim pesan asli: untuk pesan sendiri
	// BuildMessageKey otomatis menandai FromMe, untuk pesan orang lain
	// menyertakan participant-nya (wajib agar server menerima revoke admin).
	if _, err := mctx.Client.SendMessage(ctx, mctx.Chat, mctx.Client.BuildRevoke(mctx.Chat, quotedJID, types.MessageID(msgID))); err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal hapus pesan: %v", err))
	}
	return nil
}

// isBotJID true kalau JID adalah bot sendiri (ID maupun LID), dibandingkan
// dalam bentuk bare (ToNonAD) supaya beda device/agent tidak menggagalkan.
func isBotJID(mctx *store.MessageContext, jid types.JID) bool {
	bare := jid.ToNonAD().User
	if bare == "" {
		return false
	}
	if id := mctx.Client.Store.ID; id != nil && id.ToNonAD().User == bare {
		return true
	}
	if lid := mctx.Client.Store.LID; !lid.IsEmpty() && lid.ToNonAD().User == bare {
		return true
	}
	return false
}
