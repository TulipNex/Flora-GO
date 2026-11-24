package plugins

import (
	"context"
	"crypto/rand"
	"fmt"
	"strconv"
	"strings"

	"github.com/polymorfa/hypermeow/proto/waE2E"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"wabot-go/internal/logfmt"
	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterRun)
}

func RegisterRun(r *Registry) {
	r.Register(Command{
		Name:        "run",
		Description: "Kirim ulang pesan",
		Category:    CategoryOwner,
		Handler:     handleRun,
	})
}

const runUsage = "*Kirim ulang pesan*\n\n" +
	"`Run`\n" +
	"Format :\n" +
	"- .run #rowid\n" +
	"- .run <reply>\n\n" +
	"```rowid : nomor baris arsip\n" +
	"reply : JSON/pesan asli```\n\n" +
	"> Dokumen hasil .crm/.insp tidak bisa di-reply, gunakan #rowid."

func handleRun(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	src, srclabel, err := resolveRunSource(mctx)
	if err != nil {
		if err.Error() == "usage" {
			return store.ReplyText(ctx, mctx, runUsage)
		}
		return store.ReplyText(ctx, mctx, err.Error())
	}
	out, tipe, err := sanitizeRunMessage(src)
	if err != nil {
		return store.ReplyText(ctx, mctx, err.Error())
	}
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, out)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal kirim ulang (%s): %v", tipe, err))
	}
	store.LogOutgoing(mctx, "run", tipe)
	store.TrackBotMessageID(string(resp.ID), "run: "+tipe)
	return store.ReplyText(ctx, mctx, fmt.Sprintf("Terkirim ulang sebagai %s (dari %s).", tipe, srclabel))
}

// resolveRunSource menentukan pesan sumber untuk .run, prioritas:
//  1. "#rowid" → byte exact arsip (paling robust, tanpa parsing JSON).
//  2. Teks bubble yang di-reply diparse sebagai JSON waE2E.Message —
//     mencakup reply ke output .insp bubble pendek + tempelan JSON mentah.
//  3. Arsip via StanzaID reply (reply ke pesan asli).
func resolveRunSource(mctx *store.MessageContext) (*waE2E.Message, string, error) {
	if store.CRM() == nil {
		return nil, "", fmt.Errorf("Arsip mati (crm.db tidak terbuka).")
	}
	if len(mctx.Args) > 0 {
		if m := crmRowidRe.FindStringSubmatch(strings.TrimSpace(mctx.Args[0])); m != nil {
			rowid, _ := strconv.ParseInt(m[1], 10, 64)
			row, err := store.CRM().LoadByRowid(rowid)
			if err != nil {
				return nil, "", fmt.Errorf("Rowid #%d tidak ditemukan.", rowid)
			}
			msg, derr := row.Message()
			if derr != nil {
				return nil, "", fmt.Errorf("Gagal decode #%d: %v.", rowid, derr)
			}
			return msg, fmt.Sprintf("#%d", rowid), nil
		}
	}
	quoted := store.UnwrapMessage(store.ExtractQuotedMessage(mctx.RawEvt))
	// Teks quote dicoba parse dulu (reply ke bubble JSON .insp / tempelan
	// JSON). Gagal/ tak ada body quote (WA sering hanya mengirim StanzaID
	// tanpa isi quoted) = lanjut ke arsip di bawah, bukan error.
	if quoted != nil {
		if txt := quotedTextOf(quoted); strings.TrimSpace(txt) != "" {
			var jm waE2E.Message
			if err := protojson.Unmarshal([]byte(strings.TrimSpace(txt)), &jm); err == nil && hasRunContent(&jm) {
				return &jm, "json", nil
			}
		}
	}
	quotedID := store.ExtractStanzaID(mctx.RawEvt)
	if quotedID == "" {
		return nil, "", fmt.Errorf("usage")
	}
	row, err := store.CRM().LoadByID(quotedID, mctx.Chat.String())
	if err != nil {
		return nil, "", fmt.Errorf("Sumber tidak dikenal, reply bubble JSON .insp, pesan asli, atau pakai #rowid.")
	}
	msg, derr := row.Message()
	if derr != nil {
		return nil, "", fmt.Errorf("Gagal decode #%d: %v.", row.Rowid, derr)
	}
	// Bubble teks bot sendiri yang memuat JSON (output .insp): jangan
	// kirim ulang sebagai teks — parse isinya. Hasil parse diserahkan ke
	// sanitize: konten allowlist terkirim, sisanya (acara/dll) DITOLAK
	// eksplisit dengan nama tipenya, bukan teks bisu (pelajaran kasus
	// .run atas JSON acara & view-once).
	if row.Sender == "bot" {
		if txt := strings.TrimSpace(quotedTextOf(msg)); strings.HasPrefix(txt, "{") {
			var jm waE2E.Message
			if err := protojson.Unmarshal([]byte(txt), &jm); err == nil {
				return &jm, "json-arsip", nil
			} else {
				logfmt.Warn("run: teks bot terlihat seperti JSON tapi gagal parse (%v)\n", err)
			}
		}
	}
	// File hasil .crm/.insp sendiri tak berguna dikirim ulang — arahkan.
	if doc := msg.GetDocumentMessage(); doc != nil && row.Sender == "bot" {
		if fn := doc.GetFileName(); strings.HasPrefix(fn, "crm-") || strings.HasPrefix(fn, "insp-") {
			return nil, "", fmt.Errorf("File dokumen .crm/.insp tidak bisa di-reply, pakai #rowid.")
		}
	}
	return msg, fmt.Sprintf("#%d", row.Rowid), nil
}

// quotedTextOf mengambil teks bubble (conversation / extended-text) untuk
// dicoba parse sebagai JSON pesan.
func quotedTextOf(msg *waE2E.Message) string {
	if msg == nil {
		return ""
	}
	if s := msg.GetConversation(); s != "" {
		return s
	}
	if ext := msg.GetExtendedTextMessage(); ext != nil {
		return ext.GetText()
	}
	return ""
}

// hasRunContent true bila pesan hasil parse JSON memuat minimal satu field
// yang dimengerti .run — protojson.Unmarshal("{}") sukses dengan pesan
// kosong, dan itu harus ditolak (bukan diteruskan membingungkan).
// Wrapper sekali-lihat/sementara ikut diterima karena sanitizeRunMessage
// meng-unwrap-nya dulu (UnwrapMessage menangani Ephemeral + ViewOnce);
// EditedMessage/album-parent sengaja TIDAK (dukungan unwrap tak pasti —
// biar ditolak eksplisit, itu jujur).
func hasRunContent(msg *waE2E.Message) bool {
	if msg == nil {
		return false
	}
	return msg.Conversation != nil ||
		msg.ExtendedTextMessage != nil ||
		msg.ImageMessage != nil ||
		msg.VideoMessage != nil ||
		msg.AudioMessage != nil ||
		msg.DocumentMessage != nil ||
		msg.StickerMessage != nil ||
		msg.LocationMessage != nil ||
		msg.ContactMessage != nil ||
		msg.ContactsArrayMessage != nil ||
		msg.PollCreationMessage != nil ||
		msg.EphemeralMessage != nil ||
		msg.ViewOnceMessage != nil ||
		msg.ViewOnceMessageV2 != nil ||
		msg.ViewOnceMessageV2Extension != nil
}

// sanitizeRunMessage membangun salinan kirim-ulang dari pesan sumber:
// unwrap ephemeral/viewonce, ambil HANYA field konten (allowlist), buang
// sisanya. YANG WAJIB DIBUANG: senderKeyDistributionMessage (session key
// orang lain — dikirim ulang = error/rusak) dan messageContextInfo
// (stanzaID basi, quote basi, secret lama). Tipe di luar allowlist ditolak
// eksplisit (protocol/reaction/vote-terenkripsi/event/dll bukan konten
// yang layak diduplikat).
func sanitizeRunMessage(src *waE2E.Message) (*waE2E.Message, string, error) {
	m := store.UnwrapMessage(src)
	if m == nil {
		return nil, "", fmt.Errorf("Pesan sumber kosong atau tidak terbaca.")
	}
	switch {
	case m.Conversation != nil:
		return &waE2E.Message{Conversation: m.Conversation}, "teks", nil
	case m.ExtendedTextMessage != nil:
		e := proto.Clone(m.ExtendedTextMessage).(*waE2E.ExtendedTextMessage)
		e.ContextInfo = nil
		return &waE2E.Message{ExtendedTextMessage: e}, "teks", nil
	case m.ImageMessage != nil:
		img := proto.Clone(m.ImageMessage).(*waE2E.ImageMessage)
		img.ContextInfo = nil
		return &waE2E.Message{ImageMessage: img}, "gambar", nil
	case m.VideoMessage != nil:
		vid := proto.Clone(m.VideoMessage).(*waE2E.VideoMessage)
		vid.ContextInfo = nil
		return &waE2E.Message{VideoMessage: vid}, "video", nil
	case m.AudioMessage != nil:
		aud := proto.Clone(m.AudioMessage).(*waE2E.AudioMessage)
		aud.ContextInfo = nil
		kind := "audio"
		if aud.GetPTT() {
			kind = "voice note"
		}
		return &waE2E.Message{AudioMessage: aud}, kind, nil
	case m.DocumentMessage != nil:
		doc := proto.Clone(m.DocumentMessage).(*waE2E.DocumentMessage)
		doc.ContextInfo = nil
		return &waE2E.Message{DocumentMessage: doc}, "dokumen", nil
	case m.StickerMessage != nil:
		stk := proto.Clone(m.StickerMessage).(*waE2E.StickerMessage)
		stk.ContextInfo = nil
		return &waE2E.Message{StickerMessage: stk}, "stiker", nil
	case m.LocationMessage != nil:
		loc := proto.Clone(m.LocationMessage).(*waE2E.LocationMessage)
		loc.ContextInfo = nil
		return &waE2E.Message{LocationMessage: loc}, "lokasi", nil
	case m.ContactMessage != nil:
		return &waE2E.Message{ContactMessage: &waE2E.ContactMessage{
			DisplayName: proto.String(m.ContactMessage.GetDisplayName()),
			Vcard:       proto.String(m.ContactMessage.GetVcard()),
		}}, "kontak", nil
	case m.ContactsArrayMessage != nil:
		return &waE2E.Message{ContactsArrayMessage: &waE2E.ContactsArrayMessage{
			DisplayName: proto.String(m.ContactsArrayMessage.GetDisplayName()),
			Contacts:    m.ContactsArrayMessage.GetContacts(),
		}}, "kontak", nil
	case m.PollCreationMessage != nil:
		p := proto.Clone(m.PollCreationMessage).(*waE2E.PollCreationMessage)
		p.ContextInfo = nil
		// Secret FRESH (32-byte acak) supaya vote ke pesan baru ini bisa
		// didekripsi — secret lama milik pesan asal tak berlaku.
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return nil, "", fmt.Errorf("gagal siapkan polling: %v", err)
		}
		return &waE2E.Message{
			PollCreationMessage: p,
			MessageContextInfo:  &waE2E.MessageContextInfo{MessageSecret: secret},
		}, "polling", nil
	default:
		tipe, _ := store.DescribeMessage(m)
		return nil, "", fmt.Errorf(".run belum mendukung tipe %s.", tipe)
	}
}
