package plugins

import (
	"context"
	"fmt"
	"strings"

	"wabot-go/internal/store"

	"github.com/polymorfa/hypermeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func init() {
	registerPlugin(RegisterTagAll)
}

func RegisterTagAll(r *Registry) {
	r.Register(Command{
		Name:        "tagall",
		Description: "Mention semua member grup, opsional sertakan pesan (.tagall <pesan>)",
		Category:    CategoryGrup,
		Handler:     handleTagAll,
	})
}

func tagallHelp() string {
	return Help(HelpSpec{
		Title:   "Tag semua member",
		Cmd:     "Tagall",
		Formats: []string{".tagall <pesan>"},
		Contoh:  ".tagall rapat jam 19",
		Note:    "Tanpa argumen, bot menampilkan bantuan ini.",
	})
}

func handleTagAll(ctx context.Context, mctx *store.MessageContext) error {
	if !mctx.IsGroup() {
		return store.ReplyText(ctx, mctx, "Command ini hanya bisa dipakai di dalam grup.")
	}
	isSenderAdmin, err := mctx.IsSenderAdmin(ctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, Fail("Gagal cek status admin", err))
	}
	if !isSenderAdmin {
		return store.ReplyText(ctx, mctx, "Hanya admin grup yang boleh pakai command ini.")
	}

	pesan := strings.Join(mctx.Args, " ")
	if pesan == "" {
		return store.ReplyText(ctx, mctx, tagallHelp())
	}

	if _, err := TagAllMembers(ctx, mctx, pesan); err != nil {
		return store.ReplyText(ctx, mctx, Fail("Gagal menandai semua member", err))
	}
	return nil
}

// TagAllMembers mengirim pesan + mention ke SEMUA participant grup.
// Dipakai command .tagall DAN tool AI tag_all.
func TagAllMembers(ctx context.Context, mctx *store.MessageContext, pesan string) (int, error) {
	info, err := mctx.GroupInfo(ctx)
	if err != nil {
		return 0, fmt.Errorf("gagal ambil data grup: %w", err)
	}

	var sb strings.Builder
	sb.WriteString(pesan)
	sb.WriteString("\n\n")

	mentions := make([]string, 0, len(info.Participants))
	for _, p := range info.Participants {
		bare := p.JID.ToNonAD()
		sb.WriteString(fmt.Sprintf("@%s\n", bare.User))
		mentions = append(mentions, bare.String())
	}

	_, err = mctx.Client.SendMessage(ctx, mctx.Chat, &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String(sb.String()),
			ContextInfo: &waE2E.ContextInfo{
				MentionedJID: mentions,
			},
		},
	})
	if err == nil {
		store.LogOutgoing(mctx, "tagall", pesan)
	}
	return len(mentions), err
}
