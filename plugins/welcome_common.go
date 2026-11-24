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

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types"
	"github.com/polymorfa/hypermeow/types/events"
	"google.golang.org/protobuf/proto"
)

const (
	DefaultWelcomeTemplate = "Halo @user, selamat datang di @group!"
	DefaultByeTemplate     = "Selamat tinggal @user."
)

var templateVarRe = regexp.MustCompile(`(?i)@(user|group|desc|periode|time|count)\b`)

var (
	botJoinSeenMu sync.Mutex
	botJoinSeen   = map[string]time.Time{}
)

const syncBurstWindow = 2 * time.Minute

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

func welcomeHelp(isWelcome bool, on bool) string {
	cmdName := "welcome"
	cmdLabel := "Status"
	contoh := ".welcome set Halo @user, selamat datang di @group"
	if !isWelcome {
		cmdName = "bye"
		contoh = ".bye set Selamat tinggal @user."
	}
	status := "nonaktif"
	if on {
		status = "aktif"
	}

	var sb strings.Builder
	sb.WriteString(Title("Pesan sambutan") + "\n\n")
	sb.WriteString("Status : " + status + "\n\n")
	sb.WriteString("`" + cmdLabel + "`\nFormat :\n- ." + cmdName + " on\n- ." + cmdName + " off\n\n")
	sb.WriteString("`Set`\n- ." + cmdName + " set <teks>\n- ." + cmdName + " reset\n\n")
	sb.WriteString("`Simulasi`\n- ." + cmdName + " simulate\n\n")
	sb.WriteString("Contoh : " + contoh + "\n\n")
	sb.WriteString(Code(
		"Variabel",
		"@user   : mention member",
		"@group  : nama grup",
		"@desc   : deskripsi grup",
		"@periode: waktu",
		"@time   : jam",
		"@count  : member ke-N",
	) + "\n\n")
	sb.WriteString(Hint("`reset` mengembalikan template default. `simulate` menampilkan format terbaru."))
	return sb.String()
}

func greetingSubcommand(mctx *store.MessageContext) (string, string) {
	if len(mctx.Args) == 0 {
		return "", ""
	}
	sub := strings.ToLower(strings.TrimSpace(mctx.Args[0]))
	raw := strings.TrimSpace(mctx.RawArgs)
	rest := ""
	if len(raw) >= len(mctx.Args[0]) && strings.EqualFold(raw[:len(mctx.Args[0])], mctx.Args[0]) {
		rest = strings.TrimSpace(raw[len(mctx.Args[0]):])
	} else {
		rest = strings.TrimSpace(strings.Join(mctx.Args[1:], " "))
	}
	return sub, rest
}

func handleSetGreeting(ctx context.Context, mctx *store.MessageContext, rest string, isWelcome bool) error {
	name := "bye"
	if isWelcome {
		name = "welcome"
	}
	if rest == "" {
		gr, err := store.GroupsFor(mctx.Client).GetGreeting(ctx, mctx.Chat.String())
		if err != nil {
			return store.ReplyText(ctx, mctx, ErrMsg("Template", fmt.Sprintf("Gagal baca: %v", err)))
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
		return store.ReplyText(ctx, mctx, Title("Template")+"\n"+Row("Jenis", name)+"\n\n"+current+"\n\n"+setTemplateHelp)
	}
	if strings.EqualFold(rest, "reset") {
		var err error
		if isWelcome {
			err = store.GroupsFor(mctx.Client).SetWelcomeMsg(ctx, mctx.Chat.String(), "")
		} else {
			err = store.GroupsFor(mctx.Client).SetByeMsg(ctx, mctx.Chat.String(), "")
		}
		if err != nil {
			return store.ReplyText(ctx, mctx, ErrMsg("Template", fmt.Sprintf("Gagal reset: %v", err)))
		}
		return store.ReplyText(ctx, mctx, Block("Template", Row("Jenis", name), Row("Status", "Dikembalikan ke default")))
	}
	var err error
	if isWelcome {
		err = store.GroupsFor(mctx.Client).SetWelcomeMsg(ctx, mctx.Chat.String(), rest)
	} else {
		err = store.GroupsFor(mctx.Client).SetByeMsg(ctx, mctx.Chat.String(), rest)
	}
	if err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Template", fmt.Sprintf("Gagal simpan: %v", err)))
	}
	verb := "join"
	if !isWelcome {
		verb = "keluar"
	}
	return store.ReplyText(ctx, mctx, Block("Template", Row("Jenis", name), Row("Status", "Disimpan — berlaku untuk anggota yang "+verb+" berikutnya")))
}

func handleSimulateGreeting(ctx context.Context, mctx *store.MessageContext, isWelcome bool) error {
	if !mctx.IsGroup() {
		return store.ReplyText(ctx, mctx, ErrNotGroup("Simulasi"))
	}
	kind := "bye"
	if isWelcome {
		kind = "welcome"
	}
	gr, err := store.GroupsFor(mctx.Client).GetGreeting(ctx, mctx.Chat.String())
	if err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Template", fmt.Sprintf("Gagal baca: %v", err)))
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

func HandleGroupJoin(client *whatsmeow.Client, evt *events.GroupInfo) {
	if len(evt.Join) == 0 && len(evt.Leave) == 0 {
		return
	}
	ctx := context.Background()

	if self, err := store.GroupsFor(client).IsSelfMode(ctx); err != nil {
		logfmt.Warn("Gagal cek mode untuk greeting %s: %v\n", evt.JID, err)
		return
	} else if self {
		return
	}
	if gbanned, err := store.GroupsFor(client).IsGroupBanned(ctx, evt.JID.String()); err != nil {
		logfmt.Warn("Gagal cek ban grup untuk greeting %s: %v\n", evt.JID, err)
		return
	} else if gbanned {
		return
	}
	gr, err := store.GroupsFor(client).GetGreeting(ctx, evt.JID.String())
	if err != nil {
		logfmt.Warn("Gagal baca greeting %s: %v\n", evt.JID, err)
		return
	}

	groupName, groupDesc, memberCount := "grup ini", "", 0
	if info, err := client.GetGroupInfo(ctx, evt.JID); err != nil {
		logfmt.Warn("Gagal ambil info grup untuk greeting %s: %v\n", evt.JID, err)
	} else {
		if info.Name != "" {
			groupName = info.Name
		}
		groupDesc = info.Topic
		memberCount = info.ParticipantCount
	}

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

func sendGreeting(client *whatsmeow.Client, chat, member types.JID, tmpl, groupName, groupDesc, periode, timeStr string, count int) {
	bare := member.ToNonAD()
	if bare.IsEmpty() || isOwnJID(client, bare) {
		return
	}
	ctx := context.Background()
	alt := types.EmptyJID
	if client.Store != nil {
		if a, err := client.Store.GetAltJID(ctx, bare); err == nil && !a.IsEmpty() {
			alt = a.ToNonAD()
		}
	}
	if banned, err := store.GroupsFor(client).IsUserBanned(ctx, bare, alt); err != nil {
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
	store.LogOutgoing(&store.MessageContext{Client: client, Chat: chat}, "sapaan", text)
}

func isOwnJID(client *whatsmeow.Client, bare types.JID) bool {
	return store.IsBotJID(client, bare)
}

func bareJIDStrings(mentions []types.JID) []string {
	out := make([]string, 0, len(mentions))
	for _, jid := range mentions {
		out = append(out, jid.ToNonAD().String())
	}
	return out
}
