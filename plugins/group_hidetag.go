package plugins

import (
	"context"
	"strings"

	"wabot-go/internal/store"

	"github.com/polymorfa/hypermeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func init() {
	registerPlugin(RegisterHideTag)
}

func RegisterHideTag(r *Registry) {
	r.Register(Command{
		Name:        "hidetag",
		Aliases:     []string{"ht"},
		Description: "Tag semua member tanpa daftar @ (.hidetag <pesan>)",
		Category:    CategoryGrup,
		Handler:     handleHideTag,
	})
}

func hidetagHelp() string {
	return Help(HelpSpec{
		Title:   "Tag semua member tanpa daftar mention",
		Cmd:     "Hidetag",
		Formats: []string{".hidetag <pesan/reply>"},
		Contoh:  ".hidetag rapat jam 19",
		Params: []string{
			"pesan : teks",
			"reply : balas pesan",
		},
	})
}

func hidetagInputText(mctx *store.MessageContext) string {
	if text := strings.TrimSpace(mctx.RawArgs); text != "" {
		return text
	}

	quoted := store.UnwrapMessage(store.ExtractQuotedMessage(mctx.RawEvt))
	if quoted == nil {
		return ""
	}

	candidates := []string{
		quoted.GetConversation(),
		quoted.GetExtendedTextMessage().GetText(),
		quoted.GetImageMessage().GetCaption(),
		quoted.GetVideoMessage().GetCaption(),
		quoted.GetDocumentMessage().GetCaption(),
	}
	for _, c := range candidates {
		if t := strings.TrimSpace(c); t != "" {
			return t
		}
	}
	return ""
}

func handleHideTag(ctx context.Context, mctx *store.MessageContext) error {
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

	pesan := hidetagInputText(mctx)
	if pesan == "" {
		return store.ReplyText(ctx, mctx, hidetagHelp())
	}

	info, err := mctx.GroupInfo(ctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, Fail("Gagal mengambil data grup", err))
	}

	mentions := make([]string, 0, len(info.Participants))
	for _, p := range info.Participants {
		mentions = append(mentions, p.JID.ToNonAD().String())
	}

	_, err = mctx.Client.SendMessage(ctx, mctx.Chat, &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String(pesan),
			ContextInfo: &waE2E.ContextInfo{
				MentionedJID: mentions,
			},
		},
	})
	if err == nil {
		store.LogOutgoing(mctx, "hidetag", pesan)
	}
	return err
}
