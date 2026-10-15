package plugins

import (
	"context"
	"crypto/rand"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types/events"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"wabot-go/internal/logfmt"
	"wabot-go/internal/store"
	"wabot-go/internal/timex"
)

func init() {
	registerPlugin(RegisterCRM)
}

// RegisterCRM mendaftarkan toolkit arsip pesan ala FIORA/CRM — owner-only
// (CategoryOwner + requireOwner, seperti .get/.insthumb): arsip forensik
// bukan konsumsi publik.
//
//   - .lastchat [n] — N pesan terakhir chat ini (default 10, maks 100)
//   - .crm [#rowid] — metadata + dokumen JSON pesan (reply ke pesan / rowid)
//   - .insp [#rowid] — raw JSON pesan langsung di chat (kebesaran → dokumen)
//
// Diilhami struktur + perintah FIORA by Nixel (ditulis ulang penuh dalam
// Go): yang di-port hanya tabel messages + 3 command di atas — TANPA tabel
// nodes mentah WS (whatsmeow tidak mengeksposnya), tanpa .relay (tak ada
// API relay di whatsmeow), tanpa tombol interaktif/AIRich (tak ada
// infrastrukturnya di bot ini).
func RegisterCRM(r *Registry) {
	r.Register(Command{
		Name:        "crm",
		Description: "Arsip pesan (reply/#rowid → metadata + JSON)",
		Category:    CategoryOwner,
		Handler:     handleCRM,
	})
	r.Register(Command{
		Name:        "insp",
		Description: "Lihat raw JSON pesan arsip",
		Category:    CategoryOwner,
		Handler:     handleInsp,
	})
	r.Register(Command{
		Name:        "lastchat",
		Description: "Pesan terakhir chat ini (.lastchat [n])",
		Category:    CategoryOwner,
		Handler:     handleLastChat,
	})
	r.Register(Command{
		Name:        "run",
		Description: "Kirim ulang pesan (reply JSON/pesan asli, atau #rowid)",
		Category:    CategoryOwner,
		Handler:     handleRun,
	})
}

// ArchiveIncoming mengarsipkan satu pesan MASUK ke data/crm.db. Dipanggil
// handler untuk tiap pesan (setelah gerbang ban). Murni observasi: tak
// mengkonsumsi/mutasi evt, fail-soft total (DB mati = no-op, tak pernah
// error ke user karena arsip bukan interaksi).
func ArchiveIncoming(client *whatsmeow.Client, evt *events.Message) {
	if store.CRM() == nil || evt == nil {
		return
	}
	msg := store.UnwrapMessage(evt.Message)
	if msg == nil || evt.Info.ID == "" {
		return
	}
	chat := evt.Info.Chat.String()
	if chat == "" {
		return
	}
	go func() {
		_ = store.CRM().SaveIncoming(
			chat,
			evt.Info.ID,
			evt.Info.Sender.String(),
			evt.Info.Timestamp.Unix(),
			msg,
		)
	}()
}

// crmRowidRe mengenali argumen "#123" (lookup via rowid SQLite).
var crmRowidRe = regexp.MustCompile(`^#(\d+)$`)

// resolveCRMTarget menentukan pesan arsip target: "#rowid" ATAU reply ke
// bubble (StanzaID + chat ini). Mengembalikan baris + label sumber untuk
// pesan error/metadata.
func resolveCRMTarget(mctx *store.MessageContext) (*store.CRMRow, string, error) {
	if store.CRM() == nil {
		return nil, "", fmt.Errorf("arsip mati (crm.db tidak terbuka)")
	}
	if len(mctx.Args) > 0 {
		if m := crmRowidRe.FindStringSubmatch(strings.TrimSpace(mctx.Args[0])); m != nil {
			rowid, _ := strconv.ParseInt(m[1], 10, 64)
			row, err := store.CRM().LoadByRowid(rowid)
			if err != nil {
				return nil, "", fmt.Errorf("rowid #%d tidak ditemukan", rowid)
			}
			return row, fmt.Sprintf("#%d", rowid), nil
		}
	}
	quotedID := store.ExtractStanzaID(mctx.RawEvt)
	if quotedID == "" {
		return nil, "", fmt.Errorf("usage")
	}
	row, err := store.CRM().LoadByID(quotedID, mctx.Chat.String())
	if err != nil {
		return nil, "", fmt.Errorf("pesan yang di-reply tidak ada di arsip (terlalu lama? melebihi 200 baris? atau dari sebelum fitur aktif)")
	}
	return row, "reply", nil
}

const crmUsage = `Format: .crm [#rowid] (reply ke pesan bila tanpa argumen)
.insp [#rowid] — raw JSON pesan
.run [#rowid] — kirim ulang pesan (reply JSON/pesan asli)
.lastchat [n] — N pesan terakhir chat ini (maks 100)

Contoh:
.crm (reply pesan)
.crm #4825
.insp (reply pesan)
.run (reply bubble JSON .insp)
.lastchat 20`

func handleCRM(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	row, src, err := resolveCRMTarget(mctx)
	if err != nil {
		if err.Error() == "usage" {
			total := store.CRM().Count()
			return store.ReplyText(ctx, mctx, crmUsage+fmt.Sprintf("\n\nArsip: %d/200 pesan", total))
		}
		return store.ReplyText(ctx, mctx, err.Error())
	}
	decoded, derr := row.Message()
	tipe, ringkasan := "-", "-"
	if derr == nil {
		tipe, ringkasan = store.DescribeMessage(decoded)
	}
	meta := fmt.Sprintf(
		"*CRM %s*\nTipe: %s\nChat: %s\nPengirim: %s\nWaktu: %s\nID: %s\nUkuran: %d bytes\nIsi: %s",
		src, tipe, shortJID(row.Chat), shortJID(row.Sender),
		formatCRMTime(row.TS), row.ID, len(row.Data),
		truncateCRMLine(ringkasan, 150),
	)
	payload, err := protojson.MarshalOptions{Multiline: true}.Marshal(decoded)
	if err != nil || derr != nil {
		// Decode/marshal gagal (baris corrupt): metadata tetap berguna.
		return store.ReplyText(ctx, mctx, meta+"\n\n(raw: gagal decode)")
	}
	return crmSendDoc(ctx, mctx, fmt.Sprintf("crm-%s-%d.json", tipe, row.Rowid), "application/json", string(payload), meta)
}

func handleInsp(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	row, src, err := resolveCRMTarget(mctx)
	if err != nil {
		if err.Error() == "usage" {
			return store.ReplyText(ctx, mctx, crmUsage)
		}
		return store.ReplyText(ctx, mctx, err.Error())
	}
	decoded, derr := row.Message()
	if derr != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal decode %s: %v", src, derr))
	}
	payload, err := protojson.MarshalOptions{Multiline: true}.Marshal(decoded)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal marshal %s: %v", src, err))
	}
	// Bubble WA bukan tempat JSON raksasa (preceden maxGetTextChars .get):
	// kebesaran → dokumen + caption ringkas.
	if len(payload) > 20000 {
		tipe, _ := store.DescribeMessage(decoded)
		return crmSendDoc(ctx, mctx, fmt.Sprintf("insp-%s-%d.json", tipe, row.Rowid), "application/json",
			string(payload), fmt.Sprintf("Raw %s (%s, %d bytes — kebesaran untuk bubble)", src, tipe, len(payload)))
	}
	return store.ReplyText(ctx, mctx, string(payload))
}

func handleLastChat(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	n := 10
	if len(mctx.Args) > 0 {
		if v, err := strconv.Atoi(strings.TrimSpace(mctx.Args[0])); err == nil {
			n = v
		}
	}
	if store.CRM() == nil {
		return store.ReplyText(ctx, mctx, "Arsip mati (crm.db tidak terbuka).")
	}
	rows, err := store.CRM().RecentByChat(mctx.Chat.String(), n)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal baca arsip: %v", err))
	}
	if len(rows) == 0 {
		return store.ReplyText(ctx, mctx, "Belum ada pesan terarsip di chat ini.")
	}
	// Terbaru-dulu dari DB → balik kronologis seperti tampilan chat.
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "*LAST CHAT (%d pesan)*\n", len(rows))
	for i, row := range rows {
		sender := shortJID(row.Sender)
		if row.Sender == "bot" {
			sender = "Bot"
		}
		tipe, preview := "-", "-"
		if decoded, derr := row.Message(); derr == nil {
			tipe, preview = store.DescribeMessage(decoded)
		} else {
			preview = "(gagal decode)"
		}
		fmt.Fprintf(&sb, "\n%02d. %s | %s\n%s • #%d\n%s",
			i+1, formatCRMTime(row.TS), sender,
			tipe, row.Rowid, truncateCRMLine(preview, 150))
	}
	return store.ReplyText(ctx, mctx, strings.TrimRight(sb.String(), "\n"))
}

// shortJID memendekkan "628xx@s.whatsapp.net" jadi "628xx" untuk tampilan
// (LID ...@lid dibiarkan utuh supaya tidak dikira nomor HP).
func shortJID(jid string) string {
	if i := strings.Index(jid, "@s.whatsapp.net"); i >= 0 {
		return jid[:i]
	}
	if strings.TrimSpace(jid) == "" {
		return "-"
	}
	return jid
}

// formatCRMTime menulis epoch ke "HH:MM:SS" zona aktif bot (WIB/WITA/WIT
// via env BOT_TIMEZONE, lihat internal/timex) — konsisten dengan jam
// penjadwalan .call dan label zona lain di bot. Sebelumnya fungsi ini
// mengunci Asia/Jakarta (WIB) sehingga arsip .lastchat/.crm/.insp
// menyimpang saat bot diset WITA/WIT.
func formatCRMTime(ts int64) string {
	if ts <= 0 {
		return "--:--:--"
	}
	return time.Unix(ts, 0).In(timex.Zone()).Format("15:04:05")
}

// truncateCRMLine melipat preview multi-baris jadi satu baris pendek.
func truncateCRMLine(s string, max int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if r := []rune(s); len(r) > max {
		return strings.TrimSpace(string(r[:max])) + "..."
	}
	if s == "" {
		return "-"
	}
	return s
}

// crmSendDoc mengupload + mengirim byte sebagai dokumen dengan caption.
// Varian bytes dari pola sendDlDocument downloader (file-based) — arsip
// tak pernah menyentuh disk untuk ekspor. Mime WAJIB sesuai isi (json vs
// xlsx): klien WA menampilkan tipe dari label ini, bukan dari ekstensi
// nama file — salah label = file xlsx terbaca sebagai JSON.
func crmSendDoc(ctx context.Context, mctx *store.MessageContext, filename, mime, content, caption string) error {
	data := []byte(content)
	uploaded, err := mctx.Client.Upload(ctx, data, whatsmeow.MediaDocument)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal upload dokumen: %v", err))
	}
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, &waE2E.Message{
		DocumentMessage: &waE2E.DocumentMessage{
			URL:           proto.String(uploaded.URL),
			DirectPath:    proto.String(uploaded.DirectPath),
			MediaKey:      uploaded.MediaKey,
			Mimetype:      proto.String(mime),
			FileEncSHA256: uploaded.FileEncSHA256,
			FileSHA256:    uploaded.FileSHA256,
			FileLength:    proto.Uint64(uploaded.FileLength),
			FileName:      proto.String(filename),
			Caption:       proto.String(caption),
		},
	})
	if err != nil {
		return err
	}
	store.LogOutgoing(mctx, "document", filename)
	store.TrackBotMessageID(string(resp.ID), filename)
	return nil
}

const runUsage = `Format: .run [#rowid] (atau reply ke bubble JSON .insp / pesan asli)
Mengirim salinan konten ke chat ini.

Contoh:
.run (reply bubble JSON hasil .insp)
.run (reply pesan yang mau diduplikat)
.run #4825

Catatan: file dokumen hasil .crm/.insp tak bisa dipakai via reply
(byte-nya tak tersedia di quote) — pakai #rowid untuk itu.`

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
		return nil, "", fmt.Errorf("arsip mati (crm.db tidak terbuka)")
	}
	if len(mctx.Args) > 0 {
		if m := crmRowidRe.FindStringSubmatch(strings.TrimSpace(mctx.Args[0])); m != nil {
			rowid, _ := strconv.ParseInt(m[1], 10, 64)
			row, err := store.CRM().LoadByRowid(rowid)
			if err != nil {
				return nil, "", fmt.Errorf("rowid #%d tidak ditemukan", rowid)
			}
			msg, derr := row.Message()
			if derr != nil {
				return nil, "", fmt.Errorf("rowid #%d corrupt: %v", rowid, derr)
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
		return nil, "", fmt.Errorf("sumber tak dikenal: bukan JSON valid dan tak ada di arsip — reply ke bubble JSON .insp, pesan aslinya, atau pakai #rowid")
	}
	msg, derr := row.Message()
	if derr != nil {
		return nil, "", fmt.Errorf("baris arsip corrupt: %v", derr)
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
			return nil, "", fmt.Errorf("itu file hasil .crm/.insp — reply ke bubble JSON-nya, pesan aslinya, atau pakai #rowid")
		}
	}
	return msg, "arsip", nil
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
		return nil, "", fmt.Errorf("pesan sumber kosong/tak terbaca")
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
		return nil, "", fmt.Errorf(".run belum mendukung tipe %s (protocol/reaction/vote/event/dll bukan konten duplikat)", tipe)
	}
}
