package plugins

import (
	"context"
	"fmt"
	"strings"

	"wabot-go/internal/external"
	"wabot-go/internal/logfmt"
	"wabot-go/internal/store"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func init() {
	registerPlugin(RegisterGitClone)
}

// RegisterGitClone mendaftarkan `.gitclone` (alias `.gh`): unduh repo
// GitHub publik sebagai ZIP. Kategori Unduh (sumber file, bukan media).
func RegisterGitClone(r *Registry) {
	r.Register(Command{
		Name:        "gitclone",
		Aliases:     []string{"gh"},
		Description: "Download repo GitHub (.gitclone owner/repo[@branch])",
		Category:    CategoryUnduh,
		Handler:     handleGitClone,
	})
}

func handleGitClone(ctx context.Context, mctx *store.MessageContext) error {
	input := strings.TrimSpace(mctx.RawArgs)
	if input == "" {
		return store.ReplyText(ctx, mctx, Block("GitClone",
			Row("Cara", ".gitclone <owner/repo[@branch] / URL>"),
			Row("Contoh", ".gitclone golang/go"),
		))
	}
	repo, err := external.ParseGitInput(input)
	if err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("GitClone",
			fmt.Sprintf("Input tidak valid (%v).", err)))
	}

	select {
	case dlSem <- struct{}{}:
		defer func() { <-dlSem }()
	default:
		return store.ReplyText(ctx, mctx, QueueFullMsg())
	}
	if _, err := sendDlStatus(ctx, mctx, "Mengunduh repo..."); err != nil {
		return err
	}
	go func() {
		bg := context.Background()
		if rerr := runGitClone(bg, mctx, repo); rerr != nil {
			logfmt.Warn("gitclone gagal: %v\n", rerr)
			_ = store.ReplyText(bg, mctx, ErrMsg("GitClone",
				fmt.Sprintf("Gagal (%v)", rerr)))
		}
	}()
	return nil
}

// runGitClone mengunduh ZIP repo lalu mengirimnya sebagai dokumen.
func runGitClone(ctx context.Context, mctx *store.MessageContext, repo external.GitRepo) error {
	data, meta, err := external.FetchGitRepo(ctx, repo)
	if err != nil {
		return err
	}
	name := strings.ReplaceAll(meta.FullName, "/", "-") + "-" + meta.Branch + ".zip"
	caption := Block(meta.FullName,
		Row("Branch", meta.Branch),
		Row("Stars", fmt.Sprintf("%d", meta.Stars)),
		Row("Ukuran", fmt.Sprintf("%.1fMB", float64(len(data))/(1<<20))),
	)
	if meta.Description != "" {
		caption += "\n" + meta.Description
	}
	uploaded, err := mctx.Client.Upload(ctx, data, whatsmeow.MediaDocument)
	if err != nil {
		return fmt.Errorf("upload gagal: %v", err)
	}
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, &waE2E.Message{
		DocumentMessage: &waE2E.DocumentMessage{
			URL:           proto.String(uploaded.URL),
			DirectPath:    proto.String(uploaded.DirectPath),
			MediaKey:      uploaded.MediaKey,
			Mimetype:      proto.String("application/zip"),
			FileEncSHA256: uploaded.FileEncSHA256,
			FileSHA256:    uploaded.FileSHA256,
			FileLength:    proto.Uint64(uploaded.FileLength),
			FileName:      proto.String(name),
			Caption:       proto.String(caption),
		},
	})
	if err != nil {
		return fmt.Errorf("kirim gagal: %v", err)
	}
	store.LogOutgoing(mctx, "gitclone", name)
	store.TrackBotMessageID(resp.ID, name)
	return nil
}
