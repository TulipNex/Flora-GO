package plugins

import (
	"context"
	"fmt"
	"strings"
	"time"

	"wabot-go/internal/external"
	"wabot-go/internal/logfmt"
	"wabot-go/internal/messaging"
	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterDouyin)
}

// RegisterDouyin mendaftarkan command `.douyin` (alias `.dy`): TikTok via
// rantai tikwm → siputzx, cermin perilaku down-tiktok.js anya-MD-v3
// (link/keyword → foto loop | video + caption | audio mp3). Terpisah dari
// `.tiktok` (rantai tikwm → tiktokdl → musically + album + normalisasi)
// supaya keduanya bisa dibandingkan live. Kategori Unduh.
func RegisterDouyin(r *Registry) {
	r.Register(Command{
		Name:        "douyin",
		Aliases:     []string{"dy"},
		Description: "Download TikTok gaya Baileys (.douyin <url/judul>)",
		Category:    CategoryUnduh,
		Handler:     handleDouyin,
	})
}

func douyinUsage() string {
	return "Contoh:\n.douyin https://vt.tiktok.com/xxxx\n.douyin elaina edit"
}

func handleDouyin(ctx context.Context, mctx *store.MessageContext) error {
	input := strings.TrimSpace(mctx.RawArgs)
	if input == "" && mctx.RawEvt != nil {
		if q := store.UnwrapMessage(store.ExtractQuotedMessage(mctx.RawEvt)).GetConversation(); strings.TrimSpace(q) != "" {
			input = strings.TrimSpace(q)
		} else if ext := store.UnwrapMessage(store.ExtractQuotedMessage(mctx.RawEvt)).GetExtendedTextMessage(); ext != nil {
			input = strings.TrimSpace(ext.GetText())
		}
	}
	if input == "" {
		return store.ReplyText(ctx, mctx, douyinUsage())
	}

	select {
	case dlSem <- struct{}{}:
		defer func() { <-dlSem }()
	default:
		return store.ReplyText(ctx, mctx, QueueFullMsg())
	}
	if _, err := sendDlStatus(ctx, mctx, "Mengunduh TikTok..."); err != nil {
		return err
	}
	go func() {
		bg := context.Background()
		if rerr := runDouyin(bg, mctx, input); rerr != nil {
			logfmt.Warn("douyin gagal: %v\n", rerr)
			_ = store.ReplyText(bg, mctx, rerr.Error())
		}
	}()
	return nil
}

// runDouyin mengeksekusi alur cermin down-tiktok.js: link langsung atau
// keyword search, lalu foto loop / video + caption / audio mp3.
func runDouyin(ctx context.Context, mctx *store.MessageContext, input string) error {
	var res *external.DouyinResult
	if url := external.DouyinFindURL(input); url != "" {
		var err error
		res, err = external.FetchDouyin(ctx, url)
		if err != nil {
			return fmt.Errorf("❌ Semua API TikTok gagal.")
		}
	} else {
		var err error
		res, err = external.SearchDouyin(ctx, input)
		if err != nil {
			return fmt.Errorf("❌ Hasil tidak ditemukan untuk %q", input)
		}
	}

	// Foto: kirim satu per satu, caption hanya di pertama, jeda 3 detik.
	if len(res.Images) > 0 {
		for i, imgURL := range res.Images {
			caption := ""
			if i == 0 {
				caption = fmt.Sprintf("🖼️ *TIKTOK PHOTO*\n\n> Judul : %s\n> Uploader : %s\n> Total Foto : %d\n> Views : %s",
					douyinDash(res.Title), douyinDash(res.Author), len(res.Images), external.FormatDouyinCount(res.PlayCount))
			}
			if err := messaging.SendURLAsImage(ctx, mctx, imgURL, caption); err != nil {
				return fmt.Errorf("❌ Gagal kirim foto %d: %v", i+1, err)
			}
			if i < len(res.Images)-1 {
				time.Sleep(3 * time.Second)
			}
		}
		return nil
	}

	// Video + caption. Cover API dijadikan thumbnail preview (tanpa ffmpeg);
	// gagal unduh cover = kirim tanpa preview (fail-soft di helper).
	if strings.TrimSpace(res.VideoURL) != "" {
		caption := fmt.Sprintf("🎬 *TIKTOK VIDEO*\n\n> Judul : %s\n> Uploader : %s\n> Durasi : %s\n> Views : %s",
			douyinDash(res.Title), douyinDash(res.Author), external.FormatDouyinDuration(res.Duration), external.FormatDouyinCount(res.PlayCount))
		var cover []byte
		if strings.TrimSpace(res.Cover) != "" {
			if cb, _, cerr := messaging.FetchBytes(ctx, res.Cover, 2<<20); cerr == nil {
				cover = cb
			} else {
				logfmt.Warn("douyin cover gagal (lanjut tanpa preview): %v\n", cerr)
			}
		}
		if err := messaging.SendURLAsVideo(ctx, mctx, res.VideoURL, caption, "tiktok.mp4", cover); err != nil {
			return fmt.Errorf("❌ Gagal kirim video: %v", err)
		}
	}

	// Audio menyusul sebagai mp3 (boleh tidak ada).
	if strings.TrimSpace(res.MusicURL) != "" {
		title := strings.TrimSpace(res.Title)
		if title == "" {
			title = "tiktok"
		}
		if err := messaging.SendURLAsAudio(ctx, mctx, res.MusicURL, title+".mp3"); err != nil {
			logfmt.Warn("douyin audio gagal (video tetap terkirim): %v\n", err)
		}
	}
	return nil
}

func douyinDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}
