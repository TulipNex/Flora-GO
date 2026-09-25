package plugins

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"wabot-go/internal/store"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func init() {
	registerPlugin(RegisterWelcome)
}

// Template default sambutan & perpisahan, dipakai kalau admin belum
// mengeset template kustom via .setwelcome/.setbye. Variabel yang tersedia:
// @user (mention member), @group (nama grup), @desc (deskripsi grup),
// @periode (pagi/siang/sore/malam), @time (jam HH:MM:SS), @count (member ke-N).
const (
	DefaultWelcomeTemplate = "Halo @user, selamat datang di @group!\nSemoga betah ya~"
	DefaultByeTemplate     = "Selamat tinggal @user, terima kasih sudah mampir di @group~"
)

// templateVarRe mengenali variabel template. Case-insensitive supaya
// "@User" atau "@GROUP" karangan admin tetap jalan — yang disisipkan ke
// teks selalu format kanonis ("@"+bare.User untuk user).
var templateVarRe = regexp.MustCompile(`(?i)@(user|group|desc|periode|time|count)\b`)

// dayPeriod menentukan periode waktu Indonesia berdasarkan jam (zona WITA,
// konsisten dengan penjadwalan .call): pagi 04–10, siang 11–14,
// sore 15–17, malam 18–03.
func dayPeriod(t time.Time) string {
	switch h := t.Hour(); {
	case h >= 4 && h < 11:
		return "pagi"
	case h >= 11 && h < 15:
		return "siang"
	case h >= 15 && h < 18:
		return "sore"
	default:
		return "malam"
	}
}

// renderGreeting mengisi template dengan data sungguhan. @user disisipkan
// sebagai "@<bare.User>" (format kanonis) dan JID-nya dikembalikan untuk
// MentionedJID — pairing mention selalu benar apa pun kapitalisasi yang
// ditulis admin. Variabel lain disisipkan sebagai teks polos.
func renderGreeting(tmpl string, member types.JID, groupName, groupDesc, periode, timeStr string, count int) (string, []types.JID) {
	var mentions []types.JID
	out := templateVarRe.ReplaceAllStringFunc(tmpl, func(m string) string {
		switch strings.ToLower(m) {
		case "@user":
			mentions = append(mentions, member)
			return "@" + member.User
		case "@group":
			return groupName
		case "@desc":
			return groupDesc
		case "@periode":
			return periode
		case "@time":
			return timeStr
		case "@count":
			return fmt.Sprintf("%d", count)
		}
		return m
	})
	return out, mentions
}

// RegisterWelcome mendaftarkan command sapaan grup: .welcome on/off,
// .bye on/off, .setwelcome <format>/reset, .setbye <format>/reset.
// Gate-nya sama dengan moderasi (admin grup ATAU owner).
func RegisterWelcome(r *Registry) {
	r.Register(Command{
		Name:        "welcome",
		Description: "Atur sambutan anggota baru (.welcome on/off)",
		Category:    CategoryGrup,
		Handler:     handleWelcomeCmd,
	})
	r.Register(Command{
		Name:        "bye",
		Description: "Atur perpisahan anggota keluar (.bye on/off)",
		Category:    CategoryGrup,
		Handler:     handleByeCmd,
	})
	r.Register(Command{
		Name:        "setwelcome",
		Description: "Atur teks sambutan, variabel: @user @group @desc",
		Category:    CategoryGrup,
		Handler:     handleSetWelcome,
	})
	r.Register(Command{
		Name:        "setbye",
		Description: "Atur teks perpisahan, variabel: @user @group @desc",
		Category:    CategoryGrup,
		Handler:     handleSetBye,
	})
}

func handleWelcomeCmd(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireModGate(ctx, mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	arg := strings.ToLower(strings.TrimSpace(strings.Join(mctx.Args, " ")))
	switch arg {
	case "on", "aktif", "enable":
		if err := store.Groups().SetWelcome(ctx, mctx.Chat.String(), true); err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal menyalakan welcome: %v", err))
		}
		return store.ReplyText(ctx, mctx, "Sambutan anggota baru *aktif* di grup ini.")
	case "off", "mati", "disable":
		if err := store.Groups().SetWelcome(ctx, mctx.Chat.String(), false); err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal mematikan welcome: %v", err))
		}
		return store.ReplyText(ctx, mctx, "Sambutan anggota baru *mati* di grup ini.")
	default:
		gr, err := store.Groups().GetGreeting(ctx, mctx.Chat.String())
		if err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal baca status welcome: %v", err))
		}
		status := "mati"
		if gr.WelcomeOn {
			status = "aktif"
		}
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Sambutan anggota baru: *%s*\n\nPakai `.welcome on` / `.welcome off` untuk mengubah.\nPakai `.setwelcome <format>` untuk ganti teks.", status))
	}
}

func handleByeCmd(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireModGate(ctx, mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	arg := strings.ToLower(strings.TrimSpace(strings.Join(mctx.Args, " ")))
	switch arg {
	case "on", "aktif", "enable":
		if err := store.Groups().SetBye(ctx, mctx.Chat.String(), true); err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal menyalakan bye: %v", err))
		}
		return store.ReplyText(ctx, mctx, "Perpisahan anggota keluar *aktif* di grup ini.")
	case "off", "mati", "disable":
		if err := store.Groups().SetBye(ctx, mctx.Chat.String(), false); err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal mematikan bye: %v", err))
		}
		return store.ReplyText(ctx, mctx, "Perpisahan anggota keluar *mati* di grup ini.")
	default:
		gr, err := store.Groups().GetGreeting(ctx, mctx.Chat.String())
		if err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal baca status bye: %v", err))
		}
		status := "mati"
		if gr.ByeOn {
			status = "aktif"
		}
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Perpisahan anggota keluar: *%s*\n\nPakai `.bye on` / `.bye off` untuk mengubah.\nPakai `.setbye <format>` untuk ganti teks.", status))
	}
}

// setTemplateHelp adalah teks bantuan variabel yang dipakai .setwelcome
// dan .setbye saat dipanggil tanpa argumen. Baris baru di sini adalah bagian
// dari format — jangan digabung jadi satu paragraf.
const setTemplateHelp = "Variabel yang bisa dipakai:\n" +
	"@user - tag member yang join/keluar\n" +
	"@group - nama grup ini\n" +
	"@desc - deskripsi grup ini\n" +
	"@periode - pagi/siang/sore/malam (WITA)\n" +
	"@time - jam saat ini HH:MM:SS (WITA)\n" +
	"@count - member ke-N saat ini\n" +
	"\n" +
	"Contoh:\n" +
	".setwelcome halo kak @user\n" +
	"selamat datang di grup @group\n" +
	"silakan baca aturan grup ini: @desc\n" +
	"\n" +
	"Ketik `.setwelcome reset` untuk kembali ke teks default."

// templateRawArgs mengambil teks template dari pesan command dengan baris
// baru yang utuh (mctx.RawArgs). Fallback ke gabungan Args untuk konteks
// yang tidak lewat handler normal (tidak terjadi di production, tapi aman
// untuk diuji).
func templateRawArgs(mctx *store.MessageContext) string {
	if mctx.RawArgs != "" {
		return mctx.RawArgs
	}
	return strings.TrimSpace(strings.Join(mctx.Args, " "))
}

func handleSetWelcome(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireModGate(ctx, mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	raw := templateRawArgs(mctx)
	if raw == "" {
		gr, err := store.Groups().GetGreeting(ctx, mctx.Chat.String())
		if err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal baca template: %v", err))
		}
		current := gr.WelcomeMsg
		if current == "" {
			current = DefaultWelcomeTemplate + "\n(default)"
		}
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Template welcome saat ini:\n\n%s\n\n%s", current, setTemplateHelp))
	}
	if strings.EqualFold(raw, "reset") {
		if err := store.Groups().SetWelcomeMsg(ctx, mctx.Chat.String(), ""); err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal reset template: %v", err))
		}
		return store.ReplyText(ctx, mctx, "Template welcome dikembalikan ke default.")
	}
	if err := store.Groups().SetWelcomeMsg(ctx, mctx.Chat.String(), raw); err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal simpan template: %v", err))
	}
	return store.ReplyText(ctx, mctx, "Template welcome disimpan.\n\nBerlaku untuk anggota yang join berikutnya.")
}

func handleSetBye(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireModGate(ctx, mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	raw := templateRawArgs(mctx)
	if raw == "" {
		gr, err := store.Groups().GetGreeting(ctx, mctx.Chat.String())
		if err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal baca template: %v", err))
		}
		current := gr.ByeMsg
		if current == "" {
			current = DefaultByeTemplate + "\n(default)"
		}
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Template bye saat ini:\n\n%s\n\n%s", current, setTemplateHelp))
	}
	if strings.EqualFold(raw, "reset") {
		if err := store.Groups().SetByeMsg(ctx, mctx.Chat.String(), ""); err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal reset template: %v", err))
		}
		return store.ReplyText(ctx, mctx, "Template bye dikembalikan ke default.")
	}
	if err := store.Groups().SetByeMsg(ctx, mctx.Chat.String(), raw); err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal simpan template: %v", err))
	}
	return store.ReplyText(ctx, mctx, "Template bye disimpan.\n\nBerlaku untuk anggota yang keluar berikutnya.")
}

// HandleGroupJoin dipanggil handler untuk tiap event perubahan info grup.
// Bereaksi hanya untuk JOIN (sambutan) dan LEAVE (perpisahan) — event lain
// (ganti nama/deskripsi, promote) diabaikan.
//
// Filter berlapis sebelum mengirim (semuanya fail-soft, log saja):
// mode self → diam (sapaan adalah broadcast ke non-owner),
// grup di-ban → diam (konsisten dengan pesan biasa),
// bot sendiri yang join → tidak menyapa diri sendiri,
// anggota keluar karena ditendang bot (actor = bot) → tidak ada bye
// (command kick/warn sudah mengirim pesannya sendiri),
// anggota di-ban → tidak disapa maupun diantar.
func HandleGroupJoin(client *whatsmeow.Client, evt *events.GroupInfo) {
	if len(evt.Join) == 0 && len(evt.Leave) == 0 {
		return
	}
	ctx := context.Background()

	if self, err := store.Groups().IsSelfMode(ctx); err != nil {
		fmt.Printf("⚠️ Gagal cek mode untuk greeting %s: %v\n", evt.JID, err)
		return
	} else if self {
		return
	}
	if gbanned, err := store.Groups().IsGroupBanned(ctx, evt.JID.String()); err != nil {
		fmt.Printf("⚠️ Gagal cek ban grup untuk greeting %s: %v\n", evt.JID, err)
		return
	} else if gbanned {
		return
	}
	gr, err := store.Groups().GetGreeting(ctx, evt.JID.String())
	if err != nil {
		fmt.Printf("⚠️ Gagal baca greeting %s: %v\n", evt.JID, err)
		return
	}

	// Nama, deskripsi & jumlah member grup untuk variabel @group/@desc/@count —
	// diambil sekali untuk event ini. Gagal ambil = fallback generik, sapaan
	// tetap dikirim supaya member tidak dicuekin (@count jadi 0).
	// Catatan: @count adalah jumlah member grup SAAT INI (termasuk yang baru
	// join), bukan nomor urut historis — member yang keluar lalu join lagi
	// akan mendapat angka sesuai kondisi grup saat itu.
	groupName, groupDesc, memberCount := "grup ini", "", 0
	if info, err := client.GetGroupInfo(ctx, evt.JID); err != nil {
		fmt.Printf("⚠️ Gagal ambil info grup untuk greeting %s: %v\n", evt.JID, err)
	} else {
		// GroupName/GroupTopic di-embed sebagai struct, jadi field-nya
		// ter-promote langsung: info.Name dan info.Topic berupa string.
		if info.Name != "" {
			groupName = info.Name
		}
		groupDesc = info.Topic
		memberCount = info.ParticipantCount
	}

	// Periode & jam untuk variabel @periode/@time — zona WITA supaya sama
	// dengan jam penjadwalan .call (lihat witaLocation di call.go).
	now := time.Now().In(witaLocation)
	periode := dayPeriod(now)
	timeStr := now.Format("15:04:05")

	if len(evt.Join) > 0 && gr.WelcomeOn {
		tmpl := gr.WelcomeMsg
		if tmpl == "" {
			tmpl = DefaultWelcomeTemplate
		}
		for _, j := range evt.Join {
			sendGreeting(client, evt.JID, j, tmpl, groupName, groupDesc, periode, timeStr, memberCount)
		}
	}

	if len(evt.Leave) > 0 && gr.ByeOn {
		// Actor-nya bot sendiri (hasil .kick / warn 3x) → skip, supaya
		// tidak dobel dengan pesan kick yang sudah dikirim command-nya.
		if evt.Sender != nil && isOwnJID(client, evt.Sender.ToNonAD()) {
			return
		}
		tmpl := gr.ByeMsg
		if tmpl == "" {
			tmpl = DefaultByeTemplate
		}
		for _, j := range evt.Leave {
			sendGreeting(client, evt.JID, j, tmpl, groupName, groupDesc, periode, timeStr, memberCount)
		}
	}
}

// sendGreeting merender satu template untuk satu member lalu mengirimnya
// sebagai pesan lepas (bukan reply — tidak ada pesan yang di-quote) dengan
// mention beneran kalau template memakai @user.
func sendGreeting(client *whatsmeow.Client, chat, member types.JID, tmpl, groupName, groupDesc, periode, timeStr string, count int) {
	bare := member.ToNonAD()
	if bare.IsEmpty() || isOwnJID(client, bare) {
		return
	}
	ctx := context.Background()
	if banned, err := store.Groups().IsUserBanned(ctx, bare, types.EmptyJID); err != nil {
		fmt.Printf("⚠️ Gagal cek ban untuk greeting %s: %v\n", bare, err)
	} else if banned {
		return
	}

	text, mentions := renderGreeting(tmpl, bare, groupName, groupDesc, periode, timeStr, count)
	if _, err := client.SendMessage(ctx, chat, &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String(text),
			ContextInfo: &waE2E.ContextInfo{
				MentionedJID: bareJIDStrings(mentions),
			},
		},
	}); err != nil {
		fmt.Printf("⚠️ Gagal kirim greeting ke %s: %v\n", chat, err)
		return
	}
	// Sapaan dikirim langsung via client (tanpa mctx), jadi dicatat manual
	// supaya muncul di log terminal seperti pesan keluar lain.
	store.LogOutgoing(&store.MessageContext{Client: client, Chat: chat}, "sapaan", text)
}

// isOwnJID true kalau JID adalah bot sendiri (ID maupun LID), supaya bot
// tidak menyapa dirinya sendiri saat baru dimasukkan ke grup.
func isOwnJID(client *whatsmeow.Client, bare types.JID) bool {
	if id := client.Store.ID; id != nil && id.ToNonAD().User == bare.User {
		return true
	}
	if lid := client.Store.LID; !lid.IsEmpty() && lid.ToNonAD().User == bare.User {
		return true
	}
	return false
}

// bareJIDStrings mengubah daftar JID jadi string bare untuk MentionedJID.
// Teks "@..." memakai bare.User yang sama (lihat renderGreeting), jadi
// pairing mention tetap benar (metode tagall).
func bareJIDStrings(mentions []types.JID) []string {
	out := make([]string, 0, len(mentions))
	for _, jid := range mentions {
		out = append(out, jid.ToNonAD().String())
	}
	return out
}
