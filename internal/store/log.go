package store

import (
	"context"
	"fmt"
	"strings"

	"wabot-go/internal/logfmt"
	"wabot-go/internal/timex"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types"
	"github.com/polymorfa/hypermeow/types/events"
)

const logTimeFormat = "15:04"

// Format blok MESSAGE (keputusan owner): header + baris kunci + isi mentah.
// Contoh masuk:
//   [16:05 - MESSAGE]
//   from : Mitra (6285324023198)
//   at   : private 6285324023198
//   type : text
//   aim  : agent
//   content :
//   halo flo
// Isi dicetak APA ADANYA tanpa prefix/lipat supaya bisa disalin langsung.
// Timestamp = jam zona aktif timex.

// LogIncoming mencetak pesan masuk dengan format blok MESSAGE. aim adalah
// hasil klasifikasi handler ("command (.x)" / "agent" / "none") — maksud
// pesan, bukan hasil akhirnya (gerbang ban/lock bisa tetap menggugurkan).
//
// Pesan di-unwrap dulu (ephemeral/sekali-lihat) supaya tipenya terdeteksi
// benar — sebelumnya pesan berbungkus selalu tercatat sebagai "other".
func LogIncoming(client *whatsmeow.Client, evt *events.Message, aim string) {
	msg := UnwrapMessage(evt.Message)
	tipe, ringkasan := describeMessage(msg)
	ctx := context.Background()
	fmt.Println(logfmt.PaintCode("32", fmt.Sprintf("[%s - MESSAGE]",
		timex.Now().Format(logTimeFormat))))
	fmt.Printf("from : %s\nat   : %s\ntype : %s\naim  : %s\ncontent :\n%s\n",
		formatSender(ctx, client, evt.Info.PushName, evt.Info.Sender, evt.Info.SenderAlt),
		formatChatEn(ctx, client, evt.Info.IsGroup, evt.Info.Chat),
		normMsgType(tipe),
		aimOrDash(aim),
		ringkasan,
	)
	fmt.Println(logfmt.Divider())
}

// LogOutgoing mencetak pesan keluar bot dengan format blok MESSAGE
// (agent=false). Untuk balasan AI pakai LogOutgoingAgent.
func LogOutgoing(mctx *MessageContext, msgType, text string) {
	logOutgoingAs(mctx, msgType, text, false)
}

// LogOutgoingAgent sama seperti LogOutgoing tapi menandai agent:true —
// dipakai HANYA jalur balasan AI (bubble teks / voice note Flora).
func LogOutgoingAgent(mctx *MessageContext, msgType, text string) {
	logOutgoingAs(mctx, msgType, text, true)
}

func logOutgoingAs(mctx *MessageContext, msgType, text string, agent bool) {
	agentText := "false"
	if agent {
		agentText = "true"
	}
	fmt.Println(logfmt.PaintCode("34", fmt.Sprintf("[%s - MESSAGE]",
		timex.Now().Format(logTimeFormat))))
	fmt.Printf("from : Bot\nat   : %s\ntype : %s\nagent: %s\ncontent :\n%s\n",
		formatChatEn(context.Background(), mctx.Client, mctx.IsGroup(), mctx.Chat),
		normOutgoingType(msgType),
		agentText,
		text,
	)
	fmt.Println(logfmt.Divider())
}

// aimOrDash mengembalikan "-" bila aim kosong (jangan tampil kosong).
func aimOrDash(aim string) string {
	if strings.TrimSpace(aim) == "" {
		return "-"
	}
	return aim
}

// formatChatEn menyusun "private <id>" atau "group <id>" (format blok
// MESSAGE memakai bahasa Inggris pendek).
func formatChatEn(ctx context.Context, client *whatsmeow.Client, isGroup bool, chat types.JID) string {
	id := displayID(ctx, client, chat, types.EmptyJID)
	if isGroup {
		return "group " + id
	}
	return "private " + id
}

// normMsgType memetakan label tipe Indonesia describeMessage ke token
// Inggris pendek untuk baris type: (mis. "teks"→"text", "vn 12s"→"voicenote").
func normMsgType(tipe string) string {
	base := strings.ToLower(strings.TrimSpace(tipe))
	if i := strings.Index(base, " "); i >= 0 {
		base = base[:i]
	}
	switch base {
	case "teks":
		return "text"
	case "gambar":
		return "image"
	case "video":
		return "video"
	case "gif":
		return "gif"
	case "audio":
		return "audio"
	case "vn":
		return "voicenote"
	case "dokumen":
		return "document"
	case "stiker":
		return "sticker"
	case "kontak":
		return "contact"
	case "lokasi":
		return "location"
	case "reaksi":
		return "reaction"
	case "polling":
		return "poll"
	case "undangan-grup":
		return "group-invite"
	case "protokol":
		return "protocol"
	default:
		return "other"
	}
}

// normOutgoingType memetakan label internal pengirim ke token type:.
// Label teks/balasan → text; label media → jenis medianya; sisanya (sapaan,
// moderasi, tagall, dl-status, ...) adalah pesan teks → text.
func normOutgoingType(msgType string) string {
	switch strings.ToLower(strings.TrimSpace(msgType)) {
	case "text", "text-mention", "":
		return "text"
	case "voicenote":
		return "voicenote"
	case "gif":
		return "gif"
	case "sticker", "image->sticker":
		return "sticker"
	case "dl-video", "video":
		return "video"
	case "dl-image", "image":
		return "image"
	case "dl-audio", "audio":
		return "audio"
	case "dl-document", "get-document", "document":
		return "document"
	case "contact":
		return "contact"
	case "location":
		return "location"
	case "poll":
		return "poll"
	case "event":
		return "event"
	case "rsvp":
		return "rsvp"
	default:
		return "text"
	}
}

// formatSender menyusun "Nama (id)" — PushName (nama kontak pengirim) di
// depan karena paling mudah dibaca manusia, nomor HP di belakang untuk
// identifikasi pasti. LID di-resolve ke PN dulu (lihat resolvePN); suffix
// /lid hanya muncul kalau nomornya memang tidak diketahui.
func formatSender(ctx context.Context, client *whatsmeow.Client, pushName string, sender, senderAlt types.JID) string {
	id := displayID(ctx, client, sender, senderAlt)
	name := strings.TrimSpace(pushName)
	if name == "" {
		return id
	}
	return fmt.Sprintf("%s (%s)", name, id)
}

// displayID mengembalikan identitas pendek untuk log: nomor HP (PN) kalau
// JID-nya memang PN; untuk JID LID diusahakan resolve ke PN dulu lewat
// (1) JID alt bawaan event (gratis, tanpa DB), lalu (2) pemetaan LID↔PN
// whatsmeow yang tersimpan. Kalau nomornya tidak diketahui, tampilkan LID
// dengan suffix /lid supaya tidak dikira nomor HP.
func displayID(ctx context.Context, client *whatsmeow.Client, jid, alt types.JID) string {
	bare := jid.ToNonAD()
	if bare.IsEmpty() {
		return "-"
	}
	if bare.Server == types.DefaultUserServer {
		return bare.User
	}
	if bare.Server == types.HiddenUserServer {
		if alt.ToNonAD().Server == types.DefaultUserServer {
			return alt.ToNonAD().User
		}
		if client != nil && client.Store != nil {
			if pn, err := client.Store.GetAltJID(ctx, bare); err == nil && pn.ToNonAD().Server == types.DefaultUserServer {
				return pn.ToNonAD().User
			}
		}
		return bare.User + "/lid"
	}
	if bare.User != "" {
		return bare.User
	}
	return jid.String()
}

// describeMessage menentukan tipe + ringkasan isi pesan untuk log. Mencakup
// tipe yang sebelumnya jatuh ke "other"/"(tanpa teks": audio, stiker,
// lokasi, reaksi, polling, undangan grup, dan revoke/protokol.
func describeMessage(msg *waE2E.Message) (string, string) {
	if msg == nil {
		return "lainnya", "-"
	}
	if text := firstText(msg); text != "" {
		return "teks", text
	}
	if img := msg.GetImageMessage(); img != nil {
		return "gambar", captionOr("-", img.GetCaption())
	}
	if vid := msg.GetVideoMessage(); vid != nil {
		label := "video"
		if vid.GetGifPlayback() {
			label = "gif"
		}
		if s := vid.GetSeconds(); s > 0 {
			label = fmt.Sprintf("%s %ds", label, s)
		}
		return label, captionOr("-", vid.GetCaption())
	}
	if aud := msg.GetAudioMessage(); aud != nil {
		label := "audio"
		if aud.GetPTT() {
			label = "vn"
		}
		if s := aud.GetSeconds(); s > 0 {
			label = fmt.Sprintf("%s %ds", label, s)
		}
		return label, "-"
	}
	if doc := msg.GetDocumentMessage(); doc != nil {
		name := doc.GetFileName()
		if name == "" {
			name = doc.GetTitle()
		}
		return "dokumen", captionOr("-", name)
	}
	if stk := msg.GetStickerMessage(); stk != nil {
		if stk.GetIsAnimated() {
			return "stiker", "animasi"
		}
		return "stiker", "-"
	}
	if contact := msg.GetContactMessage(); contact != nil {
		return "kontak", captionOr("-", contact.GetDisplayName())
	}
	if loc := msg.GetLocationMessage(); loc != nil {
		if loc.GetIsLive() {
			return "lokasi", "live"
		}
		if name := strings.TrimSpace(loc.GetName()); name != "" {
			return "lokasi", name
		}
		return "lokasi", fmt.Sprintf("%.5f,%.5f", loc.GetDegreesLatitude(), loc.GetDegreesLongitude())
	}
	if react := msg.GetReactionMessage(); react != nil {
		emoji := react.GetText()
		if emoji == "" {
			emoji = "(hapus reaksi)"
		}
		return "reaksi", emoji
	}
	if poll := msg.GetPollCreationMessage(); poll != nil {
		return "polling", captionOr("-", poll.GetName())
	}
	if em := msg.GetEventMessage(); em != nil {
		if em.GetIsCanceled() {
			return "acara", "dibatalkan"
		}
		return "acara", captionOr("-", em.GetName())
	}
	// RSVP acara (Hadir/Tidak/Mungkin) masuk terenkripsi dan tak bisa
	// dibaca versi whatsmeow ini — cukup catat keberadaannya di log.
	if msg.GetEncEventResponseMessage() != nil {
		return "rsvp", "-"
	}
	if inv := msg.GetGroupInviteMessage(); inv != nil {
		return "undangan-grup", captionOr("-", inv.GetGroupName())
	}
	if proto := msg.GetProtocolMessage(); proto != nil {
		return "protokol", strings.ToLower(protocolTypeName(proto.GetType()))
	}
	return "lainnya", "-"
}

// DescribeMessage mengekspor describeMessage untuk dipakai fitur arsip
// (.lastchat/.crm): tipe Indonesia + ringkasan satu baris dari sebuah
// pesan protobuf. Nil-guard sudah ditangani describeMessage, jadi cukup
// delegasi (baris arsip bisa corrupt / msg nil).
func DescribeMessage(msg *waE2E.Message) (string, string) {
	return describeMessage(msg)
}

// firstText mengambil teks utama pesan (teks biasa atau extended).
func firstText(msg *waE2E.Message) string {
	if msg.GetConversation() != "" {
		return msg.GetConversation()
	}
	if ext := msg.GetExtendedTextMessage(); ext != nil {
		return ext.GetText()
	}
	return ""
}

// captionOr mengembalikan fallback ("-") kalau caption kosong, atau caption
// apa adanya kalau ada (isi log tidak dilipat/dipotong agar bisa disalin).
func captionOr(fallback, caption string) string {
	if strings.TrimSpace(caption) == "" {
		return fallback
	}
	return caption
}

// protocolTypeName mengubah enum ProtocolMessage_Type jadi kata kecil
// untuk log (mis. "revoke" untuk pesan yang dihapus).
func protocolTypeName(t waE2E.ProtocolMessage_Type) string {
	return strings.ToLower(t.String())
}
