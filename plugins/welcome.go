package plugins

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"wabot-go/internal/logfmt"
	"wabot-go/internal/store"
	"wabot-go/internal/timex"

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
// mengeset template kustom via .welcome set/.bye set. Variabel yang tersedia:
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

// botJoinSeen mencatat kapan bot pertama terlihat join tiap grup — untuk
// mendeteksi burst sinkronisasi awal: WhatsApp mengirim Join berisi bot +
// puluhan member LAMA sekaligus saat bot baru dimasukkan ke grup besar.
// Map in-memory + mutex karena HandleGroupJoin jalan dari dispatcher event.
// Entri lama (>1 jam) dibersihkan oportunistik tiap ada join bot.
var (
	botJoinSeenMu sync.Mutex
	botJoinSeen   = map[string]time.Time{}
)

// syncBurstWindow: burst Join besar dalam jendela ini setelah bot masuk
// dianggap sisa sinkronisasi awal (yang pecah jadi beberapa event).
const syncBurstWindow = 2 * time.Minute

// dayPeriod menentukan periode waktu Indonesia berdasarkan jam (zona aktif
// timex, konsisten dengan penjadwalan .call): pagi 04–10, siang 11–14,
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
// .welcome set <format>/reset, .welcome simulate (dan pasangan .bye-nya).
// Command lama .setwelcome/.setbye SUDAH DIHAPUS (keputusan owner).
// Gate-nya sama dengan moderasi (admin grup ATAU owner).
func RegisterWelcome(r *Registry) {
	r.Register(Command{
		Name:        "welcome",
		Description: "Sambutan anggota baru (on/off/set/simulate)",
		Category:    CategoryGrup,
		Handler:     handleWelcomeCmd,
	})
	r.Register(Command{
		Name:        "bye",
		Description: "Perpisahan anggota keluar (on/off/set/simulate)",
		Category:    CategoryGrup,
		Handler:     handleByeCmd,
	})
}

func handleWelcomeCmd(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireModGate(ctx, mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	sub, rest := greetingSubcommand(mctx)
	switch sub {
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
	case "set":
		return handleSetGreeting(ctx, mctx, rest, true)
	case "simulate", "simulasi", "test", "tes":
		return handleSimulateGreeting(ctx, mctx, true)
	default:
		gr, err := store.Groups().GetGreeting(ctx, mctx.Chat.String())
		if err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal baca status welcome: %v", err))
		}
		status := "mati"
		if gr.WelcomeOn {
			status = "aktif"
		}
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Sambutan anggota baru: *%s*\n\n`.welcome on` / `.welcome off` — ubah status.\n`.welcome set <format>` — ganti teks (multi-baris, `reset` = default).\n`.welcome simulate` — kirim contoh dengan kamu sebagai @user.", status))
	}
}

func handleByeCmd(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireModGate(ctx, mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	sub, rest := greetingSubcommand(mctx)
	switch sub {
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
	case "set":
		return handleSetGreeting(ctx, mctx, rest, false)
	case "simulate", "simulasi", "test", "tes":
		return handleSimulateGreeting(ctx, mctx, false)
	default:
		gr, err := store.Groups().GetGreeting(ctx, mctx.Chat.String())
		if err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal baca status bye: %v", err))
		}
		status := "mati"
		if gr.ByeOn {
			status = "aktif"
		}
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Perpisahan anggota keluar: *%s*\n\n`.bye on` / `.bye off` — ubah status.\n`.bye set <format>` — ganti teks (multi-baris, `reset` = default).\n`.bye simulate` — kirim contoh dengan kamu sebagai @user.", status))
	}
}

// greetingSubcommand memecah argumen jadi (subcommand, sisa-teks). Subcommand
// = kata pertama (case-insensitive); sisa = teks mentah setelahnya dengan
// baris baru utuh (untuk template multi-baris .welcome set).
func greetingSubcommand(mctx *store.MessageContext) (string, string) {
	if len(mctx.Args) == 0 {
		return "", ""
	}
	sub := strings.ToLower(strings.TrimSpace(mctx.Args[0]))
	// RawArgs diawali tepat setelah nama command; potong sepanjang token
	// pertama yang diketik user (Args[0]) supaya sisa multi-baris utuh.
	// Fallback ke gabungan Args bila bentuknya tak terduga.
	raw := strings.TrimSpace(mctx.RawArgs)
	rest := ""
	if len(raw) >= len(mctx.Args[0]) && strings.EqualFold(raw[:len(mctx.Args[0])], mctx.Args[0]) {
		rest = strings.TrimSpace(raw[len(mctx.Args[0]):])
	} else {
		rest = strings.TrimSpace(strings.Join(mctx.Args[1:], " "))
	}
	return sub, rest
}

// handleSetGreeting menangani ".welcome set <format>" / ".bye set <format>"
// (isWelcome memilih tabelnya). Tanpa format = tampilkan template saat ini
// + bantuan; "reset" = kembali ke default.
func handleSetGreeting(ctx context.Context, mctx *store.MessageContext, rest string, isWelcome bool) error {
	name := "bye"
	if isWelcome {
		name = "welcome"
	}
	if rest == "" {
		gr, err := store.Groups().GetGreeting(ctx, mctx.Chat.String())
		if err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal baca template: %v", err))
		}
		current := gr.ByeMsg
		if isWelcome {
			current = gr.WelcomeMsg
		}
		if current == "" {
			if isWelcome {
				current = DefaultWelcomeTemplate + "\n(default)"
			} else {
				current = DefaultByeTemplate + "\n(default)"
			}
		}
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Template %s saat ini:\n\n%s\n\n%s", name, current, setTemplateHelp))
	}
	if strings.EqualFold(rest, "reset") {
		var err error
		if isWelcome {
			err = store.Groups().SetWelcomeMsg(ctx, mctx.Chat.String(), "")
		} else {
			err = store.Groups().SetByeMsg(ctx, mctx.Chat.String(), "")
		}
		if err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal reset template: %v", err))
		}
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Template %s dikembalikan ke default.", name))
	}
	var err error
	if isWelcome {
		err = store.Groups().SetWelcomeMsg(ctx, mctx.Chat.String(), rest)
	} else {
		err = store.Groups().SetByeMsg(ctx, mctx.Chat.String(), rest)
	}
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal simpan template: %v", err))
	}
	verb := "join"
	if !isWelcome {
		verb = "keluar"
	}
	return store.ReplyText(ctx, mctx, fmt.Sprintf("Template %s disimpan.\n\nBerlaku untuk anggota yang %s berikutnya.", name, verb))
}

// handleSimulateGreeting menangani ".welcome simulate" / ".bye simulate":
// merender template AKTUAL (kustom/default) dengan pengetik command sebagai
// @user + data grup nyata, lalu mengirimnya lewat jalur yang SAMA PERSIS
// dengan sapaan sungguhan (sendGreeting). Status on/off diabaikan —
// namanya juga tes. Hanya di grup (di private chat tidak ada @group/@count
// yang bermakna).
func handleSimulateGreeting(ctx context.Context, mctx *store.MessageContext, isWelcome bool) error {
	if !mctx.IsGroup() {
		return store.ReplyText(ctx, mctx, "Simulasi hanya bisa di dalam grup.")
	}
	kind := "bye"
	if isWelcome {
		kind = "welcome"
	}
	gr, err := store.Groups().GetGreeting(ctx, mctx.Chat.String())
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal baca template: %v", err))
	}
	tmpl := gr.ByeMsg
	if isWelcome {
		tmpl = gr.WelcomeMsg
	}
	if tmpl == "" {
		if isWelcome {
			tmpl = DefaultWelcomeTemplate
		} else {
			tmpl = DefaultByeTemplate
		}
	}
	groupName, groupDesc, memberCount := "grup ini", "", 0
	if info, err := mctx.Client.GetGroupInfo(ctx, mctx.Chat); err == nil {
		if info.Name != "" {
			groupName = info.Name
		}
		groupDesc = info.Topic
		memberCount = info.ParticipantCount
	}
	now := timex.Now()
	sendGreeting(mctx.Client, mctx.Chat, mctx.Sender.ToNonAD(), tmpl, groupName, groupDesc, dayPeriod(now), now.Format("15:04:05"), memberCount)
	return store.ReplyText(ctx, mctx, fmt.Sprintf("Terkirim contoh %s di atas (kamu sebagai @user).", kind))
}

// setTemplateHelp adalah teks bantuan variabel yang dipakai ".welcome set"
// dan ".bye set" tanpa argumen. Baris baru di sini adalah bagian dari
// format — jangan digabung jadi satu paragraf. Var (bukan const) supaya
// label zona mengikuti BOT_TIMEZONE aktif.
var setTemplateHelp = "Variabel yang bisa dipakai:\n" +
	"@user - tag member yang join/keluar\n" +
	"@group - nama grup ini\n" +
	"@desc - deskripsi grup ini\n" +
	"@periode - pagi/siang/sore/malam (" + timex.Name() + ")\n" +
	"@time - jam saat ini HH:MM:SS (" + timex.Name() + ")\n" +
	"@count - member ke-N saat ini\n" +
	"\n" +
	"Contoh:\n" +
	".welcome set halo kak @user\n" +
	"selamat datang di grup @group\n" +
	"silakan baca aturan grup ini: @desc\n" +
	"\n" +
	"Ketik `.welcome set reset` untuk kembali ke teks default."

// HandleGroupJoin dipanggil handler untuk tiap event perubahan info grup.
// Bereaksi hanya untuk JOIN (sambutan) dan LEAVE (perpisahan) — event lain
// (ganti nama/deskripsi, promote) diabaikan.
//
// Filter berlapis sebelum mengirim (semuanya fail-soft, log saja):
// mode self → diam (sapaan adalah broadcast ke non-owner),
// grup di-ban → diam (konsisten dengan pesan biasa),
// bot sendiri yang join → tidak menyapa diri sendiri,
// burst sync-awal (bot ada di batch Join > 1, atau burst > 3 dalam 2 menit
// setelah bot masuk) → skip semua, member lama tidak disapa,
// anggota keluar karena ditendang bot (actor = bot) → tidak ada bye
// (command kick/warn sudah mengirim pesannya sendiri),
// anggota di-ban → tidak disapa maupun diantar.
func HandleGroupJoin(client *whatsmeow.Client, evt *events.GroupInfo) {
	if len(evt.Join) == 0 && len(evt.Leave) == 0 {
		return
	}
	ctx := context.Background()

	if self, err := store.Groups().IsSelfMode(ctx); err != nil {
		logfmt.Warn("Gagal cek mode untuk greeting %s: %v\n", evt.JID, err)
		return
	} else if self {
		return
	}
	if gbanned, err := store.Groups().IsGroupBanned(ctx, evt.JID.String()); err != nil {
		logfmt.Warn("Gagal cek ban grup untuk greeting %s: %v\n", evt.JID, err)
		return
	} else if gbanned {
		return
	}
	gr, err := store.Groups().GetGreeting(ctx, evt.JID.String())
	if err != nil {
		logfmt.Warn("Gagal baca greeting %s: %v\n", evt.JID, err)
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
		logfmt.Warn("Gagal ambil info grup untuk greeting %s: %v\n", evt.JID, err)
	} else {
		// GroupName/GroupTopic di-embed sebagai struct, jadi field-nya
		// ter-promote langsung: info.Name dan info.Topic berupa string.
		if info.Name != "" {
			groupName = info.Name
		}
		groupDesc = info.Topic
		memberCount = info.ParticipantCount
	}

	// Periode & jam untuk variabel @periode/@time — zona aktif timex (WIB/
	// WITA/WIT via env BOT_TIMEZONE), sama dengan jam penjadwalan .call.
	now := timex.Now()
	periode := dayPeriod(now)
	timeStr := now.Format("15:04:05")

	if len(evt.Join) > 0 && gr.WelcomeOn && !isSyncBurst(client, evt) {
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

// isSyncBurst mendeteksi burst sinkronisasi awal saat bot baru dimasukkan
// ke grup besar: WhatsApp mengirim Join berisi bot + puluhan member LAMA
// (bukan join sungguhan), kadang pecah jadi beberapa event berurutan.
// Ciri 1: JID bot sendiri ada di batch dan batch > 1 → skip semua + catat
// waktu masuk (bot tetap diam, tanpa pesan perkenalan — keputusan owner).
// Ciri 2: burst besar (>3 JID, tanpa bot) dalam 2 menit setelah bot masuk
// → sisa sync pecahan, skip. Join asli (1–3 orang, di luar jendela) lolos.
func isSyncBurst(client *whatsmeow.Client, evt *events.GroupInfo) bool {
	if len(evt.Join) == 0 {
		return false
	}
	chatKey := evt.JID.String()
	selfInBatch := false
	for _, j := range evt.Join {
		if isOwnJID(client, j.ToNonAD()) {
			selfInBatch = true
			break
		}
	}
	botJoinSeenMu.Lock()
	defer botJoinSeenMu.Unlock()
	if selfInBatch {
		// Bersihkan entri basi + catat momen masuk grup ini.
		for k, t := range botJoinSeen {
			if time.Since(t) > time.Hour {
				delete(botJoinSeen, k)
			}
		}
		botJoinSeen[chatKey] = time.Now()
		if len(evt.Join) > 1 {
			logfmt.Skip("greeting %s: skip burst sync-awal (%d JID termasuk bot).\n", chatKey, len(evt.Join))
			return true
		}
		return false
	}
	if len(evt.Join) > 3 {
		if since, ok := botJoinSeen[chatKey]; ok && time.Since(since) < syncBurstWindow {
			logfmt.Skip("greeting %s: skip burst susulan (%d JID, %.0fs setelah bot masuk).\n",
				chatKey, len(evt.Join), time.Since(since).Seconds())
			return true
		}
	}
	return false
}

// sendGreeting merakit satu pesan perkenalan/perpisahan untuk satu member
// lalu mengirimnya sebagai pesan lepas (bukan reply — tidak ada pesan yang di-quote) dengan
// mention beneran kalau template memakai @user.
func sendGreeting(client *whatsmeow.Client, chat, member types.JID, tmpl, groupName, groupDesc, periode, timeStr string, count int) {
	bare := member.ToNonAD()
	if bare.IsEmpty() || isOwnJID(client, bare) {
		return
	}
	ctx := context.Background()
	if banned, err := store.Groups().IsUserBanned(ctx, bare, types.EmptyJID); err != nil {
		logfmt.Warn("Gagal cek ban untuk greeting %s: %v\n", bare, err)
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
		logfmt.Warn("Gagal kirim greeting ke %s: %v\n", chat, err)
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
