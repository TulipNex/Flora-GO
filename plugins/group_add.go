package plugins

import (
	"context"
	"fmt"
	"strings"

	"wabot-go/internal/store"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/types"
)

func init() {
	registerPlugin(RegisterAdd)
}

func RegisterAdd(r *Registry) {
	r.Register(Command{
		Name:        "add",
		Description: "Tambahkan member ke grup lewat nomor, contoh: .add 6281234567890",
		Category:    CategoryGrup,
		Handler:     handleAdd,
	})
}

func addHelp() string {
	return Help(HelpSpec{
		Title:   "Menambahkan anggota grup",
		Cmd:     "Add",
		Formats: []string{".add <nomor>"},
		Contoh:  ".add 6281234567890",
		Params: []string{
			"nomor : +62 812-3456-7890",
			"        6281234567890",
			"        081234567890",
		},
		Note: "Bisa beberapa nomor sekaligus, dipisah spasi.",
	})
}

func handleAdd(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireGroupAdminGate(ctx, mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	if len(mctx.Args) == 0 {
		return store.ReplyText(ctx, mctx, addHelp())
	}

	targets := make([]types.JID, 0, len(mctx.Args))
	for _, arg := range mctx.Args {
		jid, err := parsePhoneToJID(arg)
		if err != nil {
			return store.ReplyText(ctx, mctx, Sentence(err.Error()))
		}
		targets = append(targets, jid)
	}

	results, err := mctx.Client.UpdateGroupParticipants(ctx, mctx.Chat, targets, whatsmeow.ParticipantChangeAdd)
	if err != nil {
		return store.ReplyText(ctx, mctx, Fail("Gagal menambahkan anggota ke grup", err))
	}

	var sb strings.Builder
	mentions := make([]types.JID, 0, len(results))
	for i, p := range results {
		typed := p.JID.User
		if i < len(mctx.Args) {
			typed = mctx.Args[i]
		}
		switch {
		case p.Error == 0:
			bare := p.JID.ToNonAD()
			if bare.IsEmpty() && i < len(targets) {
				bare = targets[i].ToNonAD()
			}
			sb.WriteString(fmt.Sprintf("@%s", bare.User))
			if typed != "" && typed != bare.User {
				sb.WriteString(fmt.Sprintf(" (%s)", typed))
			}
			sb.WriteString(" telah ditambahkan ke grup.\n")
			mentions = append(mentions, bare)
		case p.AddRequest != nil:
			sb.WriteString(fmt.Sprintf("%s tidak bisa ditambah karena alasan privasi, undangan invite terkirim.\n", typed))
		default:
			sb.WriteString(fmt.Sprintf("%s gagal ditambahkan (kode error %d).\n", typed, p.Error))
		}
	}
	text := strings.TrimRight(sb.String(), "\n")

	if len(mentions) == 0 {
		return store.ReplyText(ctx, mctx, text)
	}
	return store.ReplyTextWithMentions(ctx, mctx, text, mentions)
}
