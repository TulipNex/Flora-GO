package plugins

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types"
	"github.com/polymorfa/hypermeow/types/events"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"wabot-go/internal/store"
	"wabot-go/internal/timex"
)

func init() {
	registerPlugin(RegisterCRM)
}

// RegisterCRM mendaftarkan command .crm: metadata + dokumen JSON pesan.
func RegisterCRM(r *Registry) {
	r.Register(Command{
		Name:        "crm",
		Description: "Arsip pesan",
		Category:    CategoryOwner,
		Handler:     handleCRM,
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
	// Async via antrian arsip terpusat (bukan goroutine per pesan; jalur
	// panas tak pernah menunggu I/O SQLite).
	store.ArchiveIncomingMessage(chat, evt.Info.ID, evt.Info.Sender.String(), evt.Info.Timestamp.Unix(), msg)
}

// crmRowidRe mengenali argumen "#123" (lookup via rowid SQLite).
var crmRowidRe = regexp.MustCompile(`^#(\d+)$`)

// resolveCRMTarget menentukan pesan arsip target: "#rowid" ATAU reply ke
// bubble (StanzaID + chat ini). Mengembalikan baris + label sumber untuk
// pesan error/metadata.
func resolveCRMTarget(mctx *store.MessageContext) (*store.CRMRow, string, error) {
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
			return row, fmt.Sprintf("#%d", rowid), nil
		}
	}
	quotedID := store.ExtractStanzaID(mctx.RawEvt)
	if quotedID == "" {
		return nil, "", fmt.Errorf("usage")
	}
	row, err := store.CRM().LoadByID(quotedID, mctx.Chat.String())
	if err != nil {
		return nil, "", fmt.Errorf("Pesan yang di-reply tidak ada di arsip.")
	}
	return row, fmt.Sprintf("#%d", row.Rowid), nil
}

func crmUsage(total int64) string {
	var sb strings.Builder
	sb.WriteString("*Arsip pesan*\n\n" +
		"`Crm`\n" +
		"Format :\n" +
		"- .crm #rowid\n" +
		"- .crm <reply>\n\n" +
		"```rowid : nomor baris arsip\n" +
		"reply : balas pesan```\n\n" +
		"`Lainnya`\n" +
		"- .insp #rowid : JSON mentah pesan\n" +
		"- .run #rowid : kirim ulang pesan\n" +
		"- .lastchat [n] : N pesan terakhir")
	if total >= 0 {
		fmt.Fprintf(&sb, "\n\n> Arsip: %d/200 pesan.", total)
	}
	return sb.String()
}

func formatCRMMeta(ctx context.Context, row *store.CRMRow, tipe string, client *whatsmeow.Client, ringkasan string) string {
	t := time.Unix(row.TS, 0).In(timex.Zone())
	return formatCRMMetaAt(ctx, row, tipe, client, ringkasan, t)
}

func formatCRMMetaAt(ctx context.Context, row *store.CRMRow, tipe string, client *whatsmeow.Client, ringkasan string, t time.Time) string {
	label := fmt.Sprintf("#%d", row.Rowid)
	if row.Rowid <= 0 {
		label = "reply"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "*CRM %s*\n\n", label)
	fmt.Fprintf(&sb, "```- Tipe     : %s\n", tipe)
	fmt.Fprintf(&sb, "- Chat     : %s\n", crmChatLabel(row.Chat))
	fmt.Fprintf(&sb, "- Pengirim : %s\n", crmSenderName(ctx, client, row.Sender))
	fmt.Fprintf(&sb, "- Waktu    : %s\n", t.Format("02 Jan 2006"))
	fmt.Fprintf(&sb, "- Jam      : %s\n", t.Format("15:04:05 MST"))
	fmt.Fprintf(&sb, "- ID       : %s\n", row.ID)
	fmt.Fprintf(&sb, "- Ukuran   : %d bytes```", len(row.Data))
	if ringkasan != "" && ringkasan != "-" {
		sb.WriteString("\n\n" + ringkasan)
	}
	return sb.String()
}

func handleCRM(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	row, _, err := resolveCRMTarget(mctx)
	if err != nil {
		if err.Error() == "usage" {
			var total int64
			if store.CRM() != nil {
				total = store.CRM().Count()
			}
			return store.ReplyText(ctx, mctx, crmUsage(total))
		}
		return store.ReplyText(ctx, mctx, err.Error())
	}
	decoded, derr := row.Message()
	tipe, ringkasan := "-", "-"
	if derr == nil {
		tipe, ringkasan = store.DescribeMessage(decoded)
	}
	meta := formatCRMMeta(ctx, row, tipe, mctx.Client, ringkasan)
	payload, err := protojson.MarshalOptions{Multiline: true}.Marshal(decoded)
	if err != nil || derr != nil {
		// Decode/marshal gagal (baris corrupt): metadata tetap berguna.
		return store.ReplyText(ctx, mctx, meta+"\n\n(raw: gagal decode)")
	}
	return crmSendDoc(ctx, mctx, fmt.Sprintf("crm-%s-%d.json", tipe, row.Rowid), "application/json", string(payload), meta)
}

// crmSenderName menampilkan identitas pengirim baris arsip: "Bot" untuk
// keluaran bot, nomor HP bila JID-nya PN, atau LID yang di-resolve ke nomor
// HP lewat pemetaan whatsmeow (store.DisplayID). LID yang belum diketahui
// nomornya ditandai "/lid" supaya tidak disangka nomor HP.
func crmSenderName(ctx context.Context, client *whatsmeow.Client, sender string) string {
	if sender == "bot" {
		return "Bot"
	}
	jid, err := types.ParseJID(sender)
	if err != nil || jid.IsEmpty() {
		return sender
	}
	return store.DisplayID(ctx, client, jid)
}

// crmSenderCached sama seperti crmSenderName tapi menyinggahkan hasil per
// pengirim (satu .lastchat bisa memuat sampai 100 baris dari orang yang sama).
func crmSenderCached(ctx context.Context, client *whatsmeow.Client, sender string, cache map[string]string) string {
	if v, ok := cache[sender]; ok {
		return v
	}
	v := crmSenderName(ctx, client, sender)
	cache[sender] = v
	return v
}

// crmChatLabel memendekkan JID chat untuk metadata .crm: nomor HP dipangkas,
// grup ("@g.us") dan bentuk lain dibiarkan utuh supaya konteksnya jelas.
func crmChatLabel(chat string) string {
	if i := strings.Index(chat, "@s.whatsapp.net"); i >= 0 {
		return chat[:i]
	}
	if strings.TrimSpace(chat) == "" {
		return "-"
	}
	return chat
}

// crmItalicLine membungkus preview dengan italic WhatsApp (_teks_). Bila isi
// preview mengandung "_", italic dibatalkan (ditampilkan polos) supaya
// penanda tidak mengacak format pesan.
func crmItalicLine(s string) string {
	if strings.Contains(s, "_") {
		return s
	}
	return "_" + s + "_"
}

// formatCRMTime menulis epoch ke "HH:MM:SS" zona aktif bot (BOT_TIMEZONE,
// via env BOT_TIMEZONE, lihat internal/timex) — konsisten dengan jam
// penjadwalan .voicecall dan label zona lain di bot.
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
