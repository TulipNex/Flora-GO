package plugins

import (
	"context"
	"fmt"

	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterToMp3)
}

func RegisterToMp3(r *Registry) {
	r.Register(Command{
		Name:        "tomp3",
		Aliases:     []string{"toaudio"},
		Description: "Ekstrak audio video jadi MP3 (reply/kirim video)",
		Category:    CategoryMedia,
		Handler:     handleToMp3,
	})
}

func handleToMp3(ctx context.Context, mctx *store.MessageContext) error {
	_, vid, err := extractStickerMedia(mctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, "Reply video atau kirim video dengan caption .tomp3.")
	}
	if vid == nil {
		return store.ReplyText(ctx, mctx, "Reply video atau kirim video dengan caption .tomp3.")
	}
	mediaBytes, err := mctx.Client.Download(ctx, vid)
	if err != nil {
		return store.ReplyText(ctx, mctx, "Gagal download video: "+err.Error())
	}
	if len(mediaBytes) > maxToMediaBytes {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Video %.0f MB melebihi batas %d MB.",
			float64(len(mediaBytes))/(1<<20), maxToMediaBytes/(1<<20)))
	}
	mp3Bytes, err := extractMP3(ctx, mediaBytes)
	if err != nil {
		return store.ReplyText(ctx, mctx, "Gagal ekstrak audio: "+err.Error())
	}
	if err := sendDlBytesAudio(ctx, mctx, mp3Bytes, "tomp3"); err != nil {
		return store.ReplyText(ctx, mctx, "Gagal kirim audio: "+err.Error())
	}
	return nil
}
