package store

import (
	"context"
	"fmt"

	"go.mau.fi/whatsmeow/types"
)

// IsGroup mengecek apakah chat tujuan pesan ini adalah grup (bukan chat pribadi).
func (mctx *MessageContext) IsGroup() bool {
	return mctx.Chat.Server == types.GroupServer
}

// GroupInfo mengambil metadata grup terbaru langsung dari WhatsApp
// (nama, deskripsi, daftar participant + status admin masing-masing, dll).
// Dipanggil langsung tiap butuh, tidak di-cache, supaya data admin selalu akurat
// (status admin bisa berubah kapan saja dari perangkat lain).
func (mctx *MessageContext) GroupInfo(ctx context.Context) (*types.GroupInfo, error) {
	if !mctx.IsGroup() {
		return nil, fmt.Errorf("chat ini bukan grup")
	}
	return mctx.Client.GetGroupInfo(ctx, mctx.Chat)
}

// participantMatchesJID membandingkan satu participant grup dengan sebuah JID
// target, TANPA terjebak masalah dual-format WhatsApp (LID vs nomor telepon/PN).
// Sebuah participant dan sebuah JID dianggap "orang yang sama" kalau salah satu
// dari: .User mentah cocok, ATAU field PhoneNumber cocok, ATAU field LID cocok.
// Ini perlu karena di grup dengan AddressingMode "lid", p.JID bisa berupa
// @lid sementara JID yang mau dibandingkan (misal dari Store.ID atau mention)
// berupa @s.whatsapp.net, atau sebaliknya.
func participantMatchesJID(p types.GroupParticipant, target types.JID) bool {
	if p.JID.User == target.User {
		return true
	}
	if !p.PhoneNumber.IsEmpty() && p.PhoneNumber.User == target.User {
		return true
	}
	if !p.LID.IsEmpty() && p.LID.User == target.User {
		return true
	}
	return false
}

// IsSenderAdmin mengecek apakah pengirim pesan (mctx.Sender) adalah admin
// atau superadmin di grup ini. Dipakai untuk gerbang akses command seperti
// .kick, .promote, .setname, dll.
func (mctx *MessageContext) IsSenderAdmin(ctx context.Context) (bool, error) {
	info, err := mctx.GroupInfo(ctx)
	if err != nil {
		return false, err
	}
	for _, p := range info.Participants {
		if participantMatchesJID(p, mctx.Sender) {
			return p.IsAdmin || p.IsSuperAdmin, nil
		}
	}
	return false, nil
}

// IsBotAdmin mengecek apakah bot sendiri adalah admin di grup ini — dibutuhkan
// sebelum melakukan aksi seperti kick/promote/demote/setname, karena WhatsApp
// menolak aksi tersebut kalau bot bukan admin.
//
// Dibandingkan dengan DUA identitas bot sekaligus (Store.ID dan Store.LID kalau
// ada), karena WhatsApp bisa mendaftarkan bot di daftar participant grup memakai
// salah satu dari dua format itu tergantung AddressingMode grup — membandingkan
// cuma satu bisa false-negative walau bot sungguhan admin.
func (mctx *MessageContext) IsBotAdmin(ctx context.Context) (bool, error) {
	info, err := mctx.GroupInfo(ctx)
	if err != nil {
		return false, err
	}

	botID := mctx.Client.Store.ID
	botLID := mctx.Client.Store.LID // types.JID kosong (User == "") kalau belum ada LID

	for _, p := range info.Participants {
		matched := false
		if botID != nil && participantMatchesJID(p, *botID) {
			matched = true
		}
		if !matched && !botLID.IsEmpty() && participantMatchesJID(p, botLID) {
			matched = true
		}
		if matched {
			return p.IsAdmin || p.IsSuperAdmin, nil
		}
	}

	// Bot tidak ditemukan sama sekali di daftar participant — biasanya berarti
	// cache participant grup basi. Bukan error fatal, tapi dicatat supaya
	// kelihatan di log kalau ini yang jadi penyebab false-negative.
	fmt.Printf("⚠️  IsBotAdmin: bot (ID=%v, LID=%v) tidak ditemukan di participant grup %s\n", botID, botLID, mctx.Chat)
	return false, nil
}

// MentionedJIDs mengambil daftar JID yang di-mention (@tag) di pesan ini,
// dipakai oleh command seperti .kick @user / .promote @user.
func (mctx *MessageContext) MentionedJIDs() []string {
	msg := mctx.RawEvt.Message
	if msg == nil {
		return nil
	}
	// Cek di ExtendedTextMessage (pesan teks biasa dengan mention)
	if ext := msg.GetExtendedTextMessage(); ext != nil && ext.GetContextInfo() != nil {
		return ext.GetContextInfo().GetMentionedJID()
	}
	return nil
}

// QuotedParticipant mengambil JID pengirim pesan yang di-reply (kalau command
// dipakai lewat reply, bukan mention langsung), misal ".kick" sambil reply
// pesan target. Mengembalikan string kosong kalau tidak ada pesan yang di-reply.
func (mctx *MessageContext) QuotedParticipant() string {
	msg := mctx.RawEvt.Message
	if msg == nil {
		return ""
	}
	if ext := msg.GetExtendedTextMessage(); ext != nil && ext.GetContextInfo() != nil {
		return ext.GetContextInfo().GetParticipant()
	}
	return ""
}
