package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

const logTimeFormat = "15:04"

// logWrapWidth adalah lebar LIPATAN visual per baris log. Ini BUKAN batas
// karakter — tidak ada teks yang dibuang. Baris yang lebih panjang hanya
// dilipat ke baris berikut dengan benang │ yang tetap tersambung ke bawah
// sepanjang teksnya. Pelipatan dihitung per rune (bukan byte) agar emoji
// atau karakter multi-byte tidak terpotong di tengah.
const logWrapWidth = 120

// Layout log memakai gaya timeline (opsi 23): poros waktu di kiri, panah
// ▼ untuk pesan masuk dan ▲ untuk pesan keluar, isi pesan di baris bawah
// yang tersambung benang │. Contoh:
//
// 16:05 │ ▼ Mitra (62853...) → grup 120363 [teks]
//       │   halo bot apa kabar
// 16:05 │ ▲ bot → grup 120363 [teks]
//       │   Halo juga, ada yang bisa dibantu

// LogIncoming mencetak pesan masuk ke console.
//
// Pesan di-unwrap dulu (ephemeral/sekali-lihat) supaya tipenya terdeteksi
// benar — sebelumnya pesan berbungkus selalu tercatat sebagai "other".
func LogIncoming(client *whatsmeow.Client, evt *events.Message) {
	msg := UnwrapMessage(evt.Message)
	tipe, ringkasan := describeMessage(msg)
	ctx := context.Background()
	printLogBlock(
		fmt.Sprintf("%s │ ▼ %s → %s [%s]",
			time.Now().Format(logTimeFormat),
			formatSender(ctx, client, evt.Info.PushName, evt.Info.Sender, evt.Info.SenderAlt),
			formatChat(ctx, client, evt.Info.IsGroup, evt.Info.Chat),
			tipe,
		),
		ringkasan,
	)
}

// LogOutgoing mencetak pesan yang dikirim bot ke console.
// msgType diisi pemanggil; teks ditampilkan UTUH (tanpa batas karakter),
// dilipat otomatis kalau melebihi lebar terminal.
func LogOutgoing(mctx *MessageContext, msgType, text string) {
	printLogBlock(
		fmt.Sprintf("%s │ ▲ bot → %s [%s]",
			time.Now().Format(logTimeFormat),
			formatChat(context.Background(), mctx.Client, mctx.IsGroup(), mctx.Chat),
			msgType,
		),
		text,
	)
}

// printLogBlock mencetak header satu baris lalu isi teks di bawahnya,
// SETIAP baris diawali benang "│" supaya garis pemisah turun ke bawah
// sepanjang teksnya — termasuk template multi-baris dan baris hasil
// lipatan otomatis.
func printLogBlock(header, text string) {
	fmt.Println(header)
	for _, line := range logBodyLines(text) {
		fmt.Printf("      │   %s\n", line)
	}
}

// logBodyLines memecah teks jadi baris-baris siap cetak: baris baru asli
// dipertahankan apa adanya, baris yang melebihi logWrapWidth dilipat.
// Tidak ada teks yang dibuang — teks kosong jadi "-".
func logBodyLines(text string) []string {
	var out []string
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimRight(raw, " \t\r")
		if strings.TrimSpace(line) == "" {
			out = append(out, "")
			continue
		}
		out = append(out, wrapLogLine(line)...)
	}
	if len(out) == 0 {
		return []string{"-"}
	}
	return out
}

// wrapLogLine melipat satu baris panjang ke beberapa baris: dipotong di
// spasi terakhir sebelum batas supaya kata tidak terpotong; kalau tidak
// ada spasi (mis. satu kata raksasa), dipotong keras per rune.
func wrapLogLine(line string) []string {
	var out []string
	runes := []rune(line)
	for len(runes) > logWrapWidth {
		cut := logWrapWidth
		for i := logWrapWidth; i > logWrapWidth-20 && i > 0; i-- {
			if runes[i] == ' ' {
				cut = i
				break
			}
		}
		out = append(out, string(runes[:cut]))
		runes = runes[cut:]
		for len(runes) > 0 && runes[0] == ' ' {
			runes = runes[1:]
		}
	}
	return append(out, string(runes))
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

// formatChat menyusun "grup <id>" atau "privat <id>" — cukup user part JID
// (tanpa @server) supaya baris log pendek dan mudah di-scan. Chat privat
// ber-mode LID ikut di-resolve ke nomor HP bila pemetaannya ada.
func formatChat(ctx context.Context, client *whatsmeow.Client, isGroup bool, chat types.JID) string {
	id := displayID(ctx, client, chat, types.EmptyJID)
	if isGroup {
		return "grup " + id
	}
	return "privat " + id
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

// compactText memadatkan teks multi-baris jadi satu baris (baris baru jadi
// spasi, spasi ganda digabung) lalu memotongnya ke max rune. Dipakai supaya
// template welcome multi-baris atau chat panjang tidak mengacak terminal.
func compactText(text string, max int) string {
	oneLine := strings.Join(strings.Fields(text), " ")
	if oneLine == "" {
		return "-"
	}
	runes := []rune(oneLine)
	if len(runes) <= max {
		return oneLine
	}
	return string(runes[:max]) + "..."
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
	if inv := msg.GetGroupInviteMessage(); inv != nil {
		return "undangan-grup", captionOr("-", inv.GetGroupName())
	}
	if proto := msg.GetProtocolMessage(); proto != nil {
		return "protokol", strings.ToLower(protocolTypeName(proto.GetType()))
	}
	return "lainnya", "-"
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
// apa adanya kalau ada. Perapian (lipat/potong baris) dilakukan terpusat di
// printLogBlock, bukan di sini.
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
