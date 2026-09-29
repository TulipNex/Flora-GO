package plugins

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"wabot-go/internal/config"
	"wabot-go/internal/messaging"
	"wabot-go/internal/scraper"
	"wabot-go/internal/store"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func init() {
	registerPlugin(RegisterDownloader)
}

// Batasan downloader medsos. Tanpa cap ukuran file (keputusan owner —
// ikut batas WhatsApp), tapi antrean + tameng SSRF tetap dipasang supaya
// panel tidak tumbang.
const (
	dlMaxPar = 2 // download bersamaan; sisanya ditolak sopan
	// Album TIDAK dibatasi (keputusan owner): semua item hasil scrape
	// dikirim satu per satu, berapa pun jumlahnya.
)

// dlSem membatasi download bersamaan (unduh + upload boros bandwidth, RAM,
// dan disk). Non-blocking: penuh = tolak langsung, bukan antre diam.
var dlSem = make(chan struct{}, dlMaxPar)

// dlPlatform mendeskripsikan satu command downloader: domain yang diterima
// + apakah default-nya audio saja.
type dlPlatform struct {
	command string
	aliases []string
	desc    string
	hosts   []string
	audio   bool // true = unduh audio saja (soundcloud)
}

// dlPlatforms adalah daftar SEMUA platform yang didukung — dipakai command
// manual (.tt/.yt/...) DAN deteksi autodownload, supaya keduanya selalu
// sinkron (tambah platform cukup di satu tempat).
// CATATAN: soundcloud DITUNDA atas permintaan owner — entrinya dihapus dari
// daftar (bukan sekadar dimatikan) supaya tidak muncul di .menu.
//
// Nama kanonis adalah yang FULL (.tiktok bukan .tt) supaya .menu tidak
// menampilkan singkatan — alias pendek tetap jalan seperti biasa.
var dlPlatforms = []dlPlatform{
	{command: "tiktok", aliases: []string{"tt"}, desc: "Download video TikTok (.tiktok <url>)", hosts: []string{"tiktok.com", "vm.tiktok.com", "vt.tiktok.com"}},
	{command: "youtube", aliases: []string{"yt"}, desc: "Download video YouTube (.youtube <url> [--mp3])", hosts: []string{"youtube.com", "youtu.be", "music.youtube.com"}},
	{command: "youtubemp3", aliases: []string{"ytmp3"}, desc: "Download audio YouTube (.youtubemp3 <url>)", hosts: []string{"youtube.com", "youtu.be", "music.youtube.com"}, audio: true},
	{command: "instagram", aliases: []string{"ig"}, desc: "Download Reel/post Instagram (.instagram <url>)", hosts: []string{"instagram.com"}},
	{command: "twitter", aliases: []string{"x", "tw"}, desc: "Download video X/Twitter (.twitter <url>)", hosts: []string{"x.com", "twitter.com"}},
	{command: "facebook", aliases: []string{"fb"}, desc: "Download video Facebook (.facebook <url>)", hosts: []string{"facebook.com", "fb.watch"}},
	{command: "pinterest", aliases: []string{"pin"}, desc: "Download pin Pinterest (.pinterest <url>)", hosts: []string{"pinterest.com", "pin.it"}},
	{command: "spotify", aliases: []string{"sp"}, desc: "Download audio Spotify (.spotify <url>)", hosts: []string{"open.spotify.com", "spotify.link"}, audio: true},
}

// matchDlPlatform mencocokkan host (sudah lowercase, tanpa port) ke salah
// satu platform. Subdomain ikut cocok ("m.tiktok.com" cocok "tiktok.com").
func matchDlPlatform(host string) *dlPlatform {
	for i := range dlPlatforms {
		for _, h := range dlPlatforms[i].hosts {
			if host == h || strings.HasSuffix(host, "."+h) {
				return &dlPlatforms[i]
			}
		}
	}
	return nil
}

// RegisterDownloader mendaftarkan 5 command downloader kategori Alat,
// semuanya boleh dipakai semua user. Satu engine dipakai berlima — tiap
// command hanya validasi domain + registrasi.
func RegisterDownloader(r *Registry) {
	for _, p := range dlPlatforms {
		p := p
		r.Register(Command{
			Name:        p.command,
			Aliases:     p.aliases,
			Description: p.desc,
			Category:    CategoryMedia,
			Handler: func(ctx context.Context, mctx *store.MessageContext) error {
				return handleDownload(ctx, mctx, p)
			},
		})
	}
	r.Register(Command{
		Name:        "autodownload",
		Description: "Download otomatis link medsos di chat ini (.autodownload on/off)",
		Category:    CategoryUmum,
		Handler:     handleAutodownload,
	})
}

// handleAutodownload mengatur saklar autodownload per chat. Di grup hanya
// admin/owner yang boleh (pakai gate moderasi yang sama dengan antilink);
// di private chat pengetiknya = pemilik chat, jadi selalu lolos.
func handleAutodownload(ctx context.Context, mctx *store.MessageContext) error {
	if mctx.IsGroup() {
		if msg := requireModGate(ctx, mctx); msg != "" {
			return store.ReplyText(ctx, mctx, msg)
		}
	}
	arg := ""
	if len(mctx.Args) > 0 {
		arg = strings.ToLower(strings.TrimSpace(mctx.Args[0]))
	}
	switch arg {
	case "on", "aktif", "enable":
		if err := store.Groups().SetAutodownload(ctx, mctx.Chat.String(), true); err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal menyalakan autodownload: %v", err))
		}
		return store.ReplyText(ctx, mctx, "Autodownload *aktif* di chat ini — link medsos yang didukung otomatis didownload + dikirim.")
	case "off", "mati", "disable":
		if err := store.Groups().SetAutodownload(ctx, mctx.Chat.String(), false); err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal mematikan autodownload: %v", err))
		}
		return store.ReplyText(ctx, mctx, "Autodownload *mati* di chat ini.")
	default:
		on, err := store.Groups().IsAutodownloadOn(ctx, mctx.Chat.String())
		if err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal baca status autodownload: %v", err))
		}
		status := "mati"
		if on {
			status = "aktif"
		}
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Autodownload chat ini: *%s*\n\nPakai `.autodownload on` / `.autodownload off` untuk mengubah.", status))
	}
}

func handleDownload(ctx context.Context, mctx *store.MessageContext, p dlPlatform) error {
	if len(mctx.Args) == 0 {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Sertakan URL. Contoh: .%s https://... (%s)", p.command, strings.Join(p.hosts, ", ")))
	}
	rawURL := mctx.Args[0]
	if err := validateDlURL(ctx, rawURL, p.hosts); err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("%v Contoh: .%s https://...", err, p.command))
	}

	// Varian audio YouTube: `.yt <url> --mp3`.
	audioOnly := p.audio
	for _, a := range mctx.Args[1:] {
		if strings.EqualFold(strings.TrimSpace(a), "--mp3") || strings.EqualFold(strings.TrimSpace(a), "mp3") {
			audioOnly = true
		}
	}

	// Antrean penuh = tolak langsung (non-blocking).
	select {
	case dlSem <- struct{}{}:
		defer func() { <-dlSem }()
	default:
		return store.ReplyText(ctx, mctx, "Antrean download penuh (maks 2 bersamaan), coba lagi sebentar.")
	}

	// Async: download bisa menit-menit — balasan dikirim saat selesai,
	// event loop tidak terblokir.
	status, err := sendDlStatus(ctx, mctx, "Mengunduh, tunggu sebentar...")
	if err != nil {
		return err
	}
	go func() {
		bg := context.Background()
		if rerr := downloadAndSend(bg, mctx, rawURL, &p, audioOnly, status); rerr != nil {
			_ = store.ReplyText(bg, mctx, rerr.Error())
		}
	}()
	return nil
}

// validateDlURL memastikan URL ber-skema http/https, host-nya cocok dengan
// platform command, DAN lolos tameng SSRF (reuse isPublicGetURL milik .get
// — scraper fetch server-side, tanpa ini bisa jadi exfil internal).
func validateDlURL(ctx context.Context, rawURL string, hosts []string) error {
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		rawURL = "https://" + rawURL
	}
	host := dlURLHost(rawURL)
	matched := false
	for _, h := range hosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			matched = true
			break
		}
	}
	if !matched {
		return fmt.Errorf("URL bukan %s.", strings.Join(hosts, "/"))
	}
	if err := isPublicGetURL(ctx, rawURL); err != nil {
		return err
	}
	return nil
}

// dlURLHost mengambil host lowercase dari URL (tanpa port).
func dlURLHost(rawURL string) string {
	rest := strings.TrimPrefix(strings.TrimPrefix(rawURL, "https://"), "http://")
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	if h, _, err := net.SplitHostPort(rest); err == nil {
		rest = h
	}
	return strings.ToLower(strings.Trim(rest, "[]"))
}

// downloadAndSend adalah engine utama: rantai scraper API per platform
// (cepat, tanpa exec, plus dukung foto & album). yt-dlp SUDAH DIHAPUS
// total (keputusan owner — sering error); kalau semua scraper gagal,
// error gabungan dibalas ke user oleh pemanggil. Album TANPA batas jumlah
// (keputusan owner). Status "Mengunduh..." SENGAJA dibiarkan dan tidak ada
// pesan penutup — media terakhir adalah penanda selesainya.
func downloadAndSend(ctx context.Context, mctx *store.MessageContext, rawURL string, p *dlPlatform, audioOnly bool, status types.MessageID) error {
	res, err := fetchScraper(ctx, p, rawURL, audioOnly)
	if err != nil {
		fmt.Printf("⚠️ downloader: semua scraper gagal (%v)\n", err)
		return err
	}
	return sendScrapeResult(ctx, mctx, res, status)
}

// fetchScraper menjalankan rantai adapter per platform + omnify universal
// sebagai jaring pengaman terakhir (lihat withUniversal).
func fetchScraper(ctx context.Context, p *dlPlatform, rawURL string, audioOnly bool) (*scraper.ScrapeResult, error) {
	// withUniversal menjalankan fetch utama, jatuh ke universal bila gagal,
	// dan merangkai error gabungan yang informatif bila keduanya gagal.
	withUniversal := func(name string, fetch func(context.Context, string) (*scraper.ScrapeResult, error)) (*scraper.ScrapeResult, error) {
		if res, err := fetch(ctx, rawURL); err == nil {
			return res, nil
		} else {
			firstErr := err
			if res, err := scraper.FetchUniversal(ctx, rawURL); err == nil {
				return res, nil
			} else {
				return nil, fmt.Errorf("%s: %v | universal: %v", name, firstErr, err)
			}
		}
	}
	switch p.command {
	case "tiktok":
		return withUniversal("tiktok", scraper.FetchTikTok)
	case "instagram":
		return withUniversal("instagram", scraper.FetchInstagram)
	case "twitter":
		return withUniversal("twitter", scraper.FetchTwitter)
	case "spotify":
		return withUniversal("spotify", scraper.FetchSpotify)
	case "facebook":
		return withUniversal("facebook", scraper.FetchFacebook)
	case "pinterest":
		return withUniversal("pinterest", scraper.FetchPinterest)
	case "youtube", "youtubemp3":
		// YouTube lewat scraper (savetube-direct → omnify), flag audio
		// diteruskan untuk mode --mp3/.youtubemp3. Kegagalan primary DICATAT
		// ke console (bukan ditelan) supaya diagnosa panel tidak buta bila
		// rantai jatuh ke universal.
		if res, err := scraper.FetchYouTube(ctx, rawURL, audioOnly); err == nil {
			return res, nil
		} else {
			firstErr := err
			fmt.Printf("⚠️ downloader: savetube gagal (%v), fallback omnify.\n", firstErr)
			if res, err := scraper.FetchUniversal(ctx, rawURL); err == nil {
				return res, nil
			} else {
				return nil, fmt.Errorf("youtube: %v | universal: %v", firstErr, err)
			}
		}
	default:
		return scraper.FetchUniversal(ctx, rawURL)
	}
}

// sendScrapeResult mengirim SEMUA item hasil scrape sesuai jenisnya
// (video/audio/foto) — tanpa batas jumlah (keputusan owner). Pesan status
// "Mengunduh..." SENGAJA dibiarkan (tidak di-revoke) dan tidak ada pesan
// penutup; media terakhir adalah penanda selesainya. Item yang gagal
// diunduh/dikirim dilewati (log console); kalau SEMUA gagal baru error
// ke user (keputusan owner: kegagalan tetap dibalas agar user tidak
// menunggu tanpa kabar).
func sendScrapeResult(ctx context.Context, mctx *store.MessageContext, res *scraper.ScrapeResult, status types.MessageID) error {
	items := res.Items
	if len(items) == 0 {
		return fmt.Errorf("scraper %s tidak mengembalikan media", res.Source)
	}

	caption := strings.TrimSpace(res.Title)

	// Album: hasil multi foto/video (mis. carousel TikTok) digabung jadi
	// satu bubble album, bukan N bubble terpisah — lihat messaging/album.go.
	if albumEligible(items) {
		return sendScrapeAlbum(ctx, mctx, items, caption)
	}

	sent, failed := 0, 0
	for i, item := range items {
		data, err := scraper.DownloadBytes(ctx, item.URL)
		if err != nil {
			fmt.Printf("⚠️ downloader: unduh item %d gagal (%v)\n", i+1, err)
			failed++
			continue
		}
		cap := ""
		if i == 0 {
			cap = caption
		}
		var serr error
		switch item.Kind {
		case scraper.KindAudio:
			serr = sendDlBytesAudio(ctx, mctx, data, labelFromURL(item.URL))
		case scraper.KindPhoto:
			serr = sendDlPhotoSmart(ctx, mctx, data, cap)
		default:
			serr = sendDlBytesVideo(ctx, mctx, data, cap)
		}
		if serr != nil {
			fmt.Printf("⚠️ downloader: kirim item %d gagal (%v)\n", i+1, serr)
			failed++
			continue
		}
		sent++
	}

	if sent == 0 {
		return fmt.Errorf("semua %d item gagal diproses (%s)", failed, res.Source)
	}
	return nil
}

// albumEligible true kalau ada ≥2 item foto/video (syarat minimal album).
// Audio tidak dihitung — tidak ikut album.
func albumEligible(items []scraper.ScrapeItem) bool {
	n := 0
	for _, item := range items {
		if item.Kind == scraper.KindPhoto || item.Kind == scraper.KindVideo {
			n++
		}
	}
	return n >= 2
}

// sendScrapeAlbum mengunduh semua item lalu mengirim foto/video sebagai satu
// album (induk counts dulu, baru anak-anak dengan association). Audio tetap
// dikirim satuan seperti biasa. Item yang gagal diunduh dilewati; kalau
// media yang lolos tinggal <2, fallback ke kirim satuan (bukan album
// dengan counts yang meleset).
// albumMediaItem adalah satu media (foto/video) yang sudah diunduh dan siap
// dikirim sebagai anak album.
type albumMediaItem struct {
	video bool
	data  []byte
}

func sendScrapeAlbum(ctx context.Context, mctx *store.MessageContext, items []scraper.ScrapeItem, caption string) error {
	var media []albumMediaItem
	sent, failed := 0, 0
	for i, item := range items {
		data, err := scraper.DownloadBytes(ctx, item.URL)
		if err != nil {
			fmt.Printf("⚠️ downloader: unduh item %d gagal (%v)\n", i+1, err)
			failed++
			continue
		}
		if item.Kind == scraper.KindAudio {
			if serr := sendDlBytesAudio(ctx, mctx, data, labelFromURL(item.URL)); serr != nil {
				fmt.Printf("⚠️ downloader: kirim item %d gagal (%v)\n", i+1, serr)
				failed++
				continue
			}
			sent++
			continue
		}
		if item.Kind == scraper.KindVideo {
			media = append(media, albumMediaItem{video: true, data: data})
			continue
		}
		// Foto: normalisasi WebP dulu (statis→JPEG, animasi→MP4, gagal
		// konversi→stiker satuan) supaya album hanya berisi byte yang pasti
		// ke-render klien. Caption album tetap di anak pertama.
		out, isVideo, asSticker := normalizePhotoBytes(ctx, data)
		if asSticker {
			label := caption
			if label == "" {
				label = labelFromURL(item.URL)
			}
			if serr := sendWebPAsSticker(ctx, mctx, out, label); serr != nil {
				fmt.Printf("⚠️ downloader: kirim stiker item %d gagal (%v)\n", i+1, serr)
				failed++
				continue
			}
			sent++
			continue
		}
		media = append(media, albumMediaItem{video: isVideo, data: out})
	}
	if len(media) >= 2 {
		s, f := sendAlbumMedia(ctx, mctx, media, caption)
		sent += s
		failed += f
	} else {
		// Fallback single: caption di media pertama (cermin perilaku i==0
		// di jalur non-album — audio tidak membawa caption).
		s, f := sendAlbumSingles(ctx, mctx, media, caption)
		sent += s
		failed += f
	}
	if sent == 0 {
		return fmt.Errorf("semua %d item gagal diproses (album)", failed)
	}
	return nil
}

// sendAlbumMedia mengirim slice media yang sudah diunduh sebagai satu album:
// induk counts dulu, lalu tiap anak dengan association + ID sendiri.
// Caption di anak pertama. Anak yang gagal tidak mengurangi counts induk
// (keterbatasan v1 — counts dihitung dari yang berhasil diunduh, bukan yang
// berhasil terkirim).
func sendAlbumMedia(ctx context.Context, mctx *store.MessageContext, media []albumMediaItem, caption string) (sent, failed int) {
	nImg, nVid := 0, 0
	for _, m := range media {
		if m.video {
			nVid++
		} else {
			nImg++
		}
	}
	parentID := whatsmeow.GenerateMessageID()
	parentKey := messaging.AlbumParentKey(mctx.Chat, parentID)
	if err := messaging.SendAlbumParent(ctx, mctx, parentID, nImg, nVid); err != nil {
		fmt.Printf("⚠️ downloader: induk album gagal (%v), fallback satuan.\n", err)
		return sendAlbumSingles(ctx, mctx, media, caption)
	}
	assoc := messaging.AlbumAssociation(parentKey)
	for i, m := range media {
		cap := ""
		if i == 0 {
			cap = caption
		}
		childID := whatsmeow.GenerateMessageID()
		var serr error
		if m.video {
			serr = sendDlBytesVideoAlbum(ctx, mctx, m.data, cap, childID, assoc)
		} else {
			serr = sendDlBytesImageAlbum(ctx, mctx, m.data, cap, childID, assoc)
		}
		if serr != nil {
			fmt.Printf("⚠️ downloader: kirim anak album %d gagal (%v)\n", i+1, serr)
			failed++
			continue
		}
		sent++
	}
	return sent, failed
}

// sendAlbumSingles mengirim slice media satu per satu (fallback kalau induk
// album gagal, atau media lolos <2). Caption di media pertama.
func sendAlbumSingles(ctx context.Context, mctx *store.MessageContext, media []albumMediaItem, caption string) (sent, failed int) {
	for i, m := range media {
		cap := ""
		if i == 0 {
			cap = caption
		}
		var serr error
		if m.video {
			serr = sendDlBytesVideo(ctx, mctx, m.data, cap)
		} else {
			serr = sendDlBytesImage(ctx, mctx, m.data, cap)
		}
		if serr != nil {
			fmt.Printf("⚠️ downloader: kirim media gagal (%v)\n", serr)
			failed++
			continue
		}
		sent++
	}
	return sent, failed
}

// labelFromURL mengambil nama file untuk log dari URL media.
func labelFromURL(rawURL string) string {
	rest := rawURL
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[i+1:]
	}
	if i := strings.LastIndex(rest, "/"); i >= 0 {
		rest = rest[i+1:]
	}
	if rest == "" {
		return "media"
	}
	if len(rest) > 60 {
		rest = rest[:60]
	}
	return rest
}

// sendDlStatus mengirim pesan status + mengembalikan ID-nya untuk dihapus
// nanti setelah media terkirim.
func sendDlStatus(ctx context.Context, mctx *store.MessageContext, text string) (types.MessageID, error) {
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String(text)},
	})
	if err != nil {
		return "", err
	}
	store.LogOutgoing(mctx, "dl-status", text)
	return resp.ID, nil
}

// sendDlBytesVideo mengirim byte sebagai video (jalur scraper).
// Thumbnail inline (1 frame via ffmpeg) + dimensi dipasang supaya daftar
// chat menampilkan pratinjau, bukan kartu mini kosong. Dimensi diambil dari
// thumbnail (aspeknya sama dengan video asli).
func sendDlBytesVideo(ctx context.Context, mctx *store.MessageContext, data []byte, caption string) error {
	return sendDlBytesVideoAlbum(ctx, mctx, data, caption, "", nil)
}

// sendDlBytesVideoAlbum adalah varian album dari sendDlBytesVideo: msgID
// memaksa ID pesan (untuk anak album, ID-nya di-generate duluan) dan assoc
// menempelkan MessageContextInfo association MEDIA_ALBUM. Keduanya kosong =
// perilaku single seperti sebelumnya.
func sendDlBytesVideoAlbum(ctx context.Context, mctx *store.MessageContext, data []byte, caption string, msgID types.MessageID, assoc *waE2E.MessageAssociation) error {
	// Normalisasi DULU (tolak kosong/HTML, transcode kontainer asing),
	// baru upload + thumb dari hasilnya — supaya tak terkirim bubble hantu.
	data, err := messaging.PrepareVideoBytes(ctx, data)
	if err != nil {
		return err
	}
	uploaded, err := mctx.Client.Upload(ctx, data, whatsmeow.MediaVideo)
	if err != nil {
		return err
	}
	thumb := messaging.InlineThumbForVideo(ctx, data)
	vm := &waE2E.VideoMessage{
		URL:               proto.String(uploaded.URL),
		DirectPath:        proto.String(uploaded.DirectPath),
		MediaKey:          uploaded.MediaKey,
		MediaKeyTimestamp: proto.Int64(time.Now().Unix()),
		Mimetype:          proto.String("video/mp4"),
		FileEncSHA256:     uploaded.FileEncSHA256,
		FileSHA256:        uploaded.FileSHA256,
		FileLength:        proto.Uint64(uploaded.FileLength),
		Caption:           proto.String(caption),
		JPEGThumbnail:     thumb,
	}
	if len(thumb) > 0 {
		if cfg, _, cerr := image.DecodeConfig(bytes.NewReader(thumb)); cerr == nil && cfg.Width > 0 {
			vm.Width = proto.Uint32(uint32(cfg.Width))
			vm.Height = proto.Uint32(uint32(cfg.Height))
		}
	}
	msg := &waE2E.Message{VideoMessage: vm}
	if assoc != nil {
		msg.MessageContextInfo = &waE2E.MessageContextInfo{MessageAssociation: assoc}
	}
	var resp whatsmeow.SendResponse
	if msgID != "" {
		resp, err = mctx.Client.SendMessage(ctx, mctx.Chat, msg, whatsmeow.SendRequestExtra{ID: msgID})
	} else {
		resp, err = mctx.Client.SendMessage(ctx, mctx.Chat, msg)
	}
	if err != nil {
		return err
	}
	store.LogOutgoing(mctx, "dl-video", caption)
	store.TrackBotMessageID(resp.ID, caption)
	return nil
}

// sendDlBytesAudio mengirim byte sebagai audio (jalur scraper).
func sendDlBytesAudio(ctx context.Context, mctx *store.MessageContext, data []byte, label string) error {
	uploaded, err := mctx.Client.Upload(ctx, data, whatsmeow.MediaAudio)
	if err != nil {
		return err
	}
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, &waE2E.Message{
		AudioMessage: &waE2E.AudioMessage{
			URL:           proto.String(uploaded.URL),
			DirectPath:    proto.String(uploaded.DirectPath),
			MediaKey:      uploaded.MediaKey,
			Mimetype:      proto.String("audio/mpeg"),
			FileEncSHA256: uploaded.FileEncSHA256,
			FileSHA256:    uploaded.FileSHA256,
			FileLength:    proto.Uint64(uploaded.FileLength),
			PTT:           proto.Bool(false),
		},
	})
	if err != nil {
		return err
	}
	store.LogOutgoing(mctx, "dl-audio", label)
	store.TrackBotMessageID(resp.ID, label)
	return nil
}

// sendDlBytesImage mengupload + mengirim byte sebagai FOTO (bukan video).
// Menutup lubang lama: postingan foto IG/Pin gambar yang sebelumnya dikirim
// paksa sebagai video. Dimensi diisi kalau terbaca (decoder jpeg/png sudah
// terdaftar di package ini via sticker.go) — gagal baca = kirim tanpa
// dimensi, bukan gagal total.
func sendDlBytesImage(ctx context.Context, mctx *store.MessageContext, data []byte, caption string) error {
	return sendDlBytesImageAlbum(ctx, mctx, data, caption, "", nil)
}

// sendDlBytesImageAlbum adalah varian album dari sendDlBytesImage (lihat
// sendDlBytesVideoAlbum untuk arti msgID/assoc).
func sendDlBytesImageAlbum(ctx context.Context, mctx *store.MessageContext, data []byte, caption string, msgID types.MessageID, assoc *waE2E.MessageAssociation) error {
	if err := messaging.CheckMediaBytes(data); err != nil {
		return err
	}
	uploaded, err := mctx.Client.Upload(ctx, data, whatsmeow.MediaImage)
	if err != nil {
		return err
	}
	thumb := messaging.InlineThumbForImage(data)
	im := &waE2E.ImageMessage{
		URL:               proto.String(uploaded.URL),
		DirectPath:        proto.String(uploaded.DirectPath),
		MediaKey:          uploaded.MediaKey,
		MediaKeyTimestamp: proto.Int64(time.Now().Unix()),
		Mimetype:          proto.String(detectDlImageMIME(data)),
		FileEncSHA256:     uploaded.FileEncSHA256,
		FileSHA256:        uploaded.FileSHA256,
		FileLength:        proto.Uint64(uploaded.FileLength),
		Caption:           proto.String(caption),
		JPEGThumbnail:     thumb,
	}
	if cfg, _, cerr := image.DecodeConfig(bytes.NewReader(data)); cerr == nil && cfg.Width > 0 {
		im.Width = proto.Uint32(uint32(cfg.Width))
		im.Height = proto.Uint32(uint32(cfg.Height))
	}
	msg := &waE2E.Message{ImageMessage: im}
	if assoc != nil {
		msg.MessageContextInfo = &waE2E.MessageContextInfo{MessageAssociation: assoc}
	}
	var resp whatsmeow.SendResponse
	if msgID != "" {
		resp, err = mctx.Client.SendMessage(ctx, mctx.Chat, msg, whatsmeow.SendRequestExtra{ID: msgID})
	} else {
		resp, err = mctx.Client.SendMessage(ctx, mctx.Chat, msg)
	}
	if err != nil {
		return err
	}
	store.LogOutgoing(mctx, "dl-image", caption)
	store.TrackBotMessageID(resp.ID, caption)
	return nil
}

// detectDlImageMIME menebak mime dari magic bytes (tanpa percaya ekstensi).
func detectDlImageMIME(data []byte) string {
	switch {
	case len(data) > 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return "image/jpeg"
	case len(data) > 8 && string(data[1:4]) == "PNG":
		return "image/png"
	case len(data) > 12 && string(data[8:12]) == "WEBP":
		return "image/webp"
	case len(data) > 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a"):
		return "image/gif"
	default:
		return "image/jpeg"
	}
}

// isWebPBytes true kalau byte ber-magic RIFF....WEBP — tanpa percaya
// ekstensi URL (server sering menyajikan webp di balik URL .jpg).
func isWebPBytes(data []byte) bool {
	return len(data) > 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP"
}

// isAnimatedWebP menelusuri chunk container WebP dan true kalau ada chunk
// ANMF (frame animasi). Walk manual supaya tanpa dependensi decoder webp —
// bytes.Contains mentah rawan false-positive dari payload gambar.
// Payload ganjil di-pad 1 byte (aturan RIFF).
func isAnimatedWebP(data []byte) bool {
	if !isWebPBytes(data) {
		return false
	}
	off := 12 // lewati header RIFF
	for off+8 <= len(data) {
		fourCC := string(data[off : off+4])
		size := int(binary.LittleEndian.Uint32(data[off+4 : off+8]))
		if fourCC == "ANMF" {
			return true
		}
		if size < 0 || off+8+size > len(data) {
			break
		}
		off += 8 + size + (size & 1)
	}
	return false
}

// webpConvertTimeout membatasi sekali konversi ffmpeg webp (file carousel
// kecil, hitungan detik) supaya webp korup tidak menggantung pengiriman.
const webpConvertTimeout = 60 * time.Second

// runFfmpegConvert menjalankan ffmpeg (input byte → file output) lalu
// membaca hasilnya kembali. Pola temp+stderr sama dengan
// convertToStickerWebp di sticker.go.
func runFfmpegConvert(ctx context.Context, data []byte, inExt, outName string, args ...string) ([]byte, error) {
	tmpDir, err := os.MkdirTemp("", "dl-webp-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)
	inPath := filepath.Join(tmpDir, "input"+inExt)
	outPath := filepath.Join(tmpDir, outName)
	if err := os.WriteFile(inPath, data, 0600); err != nil {
		return nil, err
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, webpConvertTimeout)
	defer cancel()
	full := append([]string{"-y", "-i", inPath}, args...)
	full = append(full, outPath)
	cmd := exec.CommandContext(timeoutCtx, "ffmpeg", full...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %v (%s)", err, stderr.String())
	}
	return os.ReadFile(outPath)
}

// convertWebPToJPEG mengubah webp STATIS jadi JPEG (dimensi asli
// dipertahankan — bukan kanvas stiker 512x512, karena tujuannya anak
// album/gambar biasa).
func convertWebPToJPEG(ctx context.Context, data []byte) ([]byte, error) {
	return runFfmpegConvert(ctx, data, ".webp", "out.jpg", "-frames:v", "1", "-q:v", "3")
}

// convertWebPToMP4 mengubah webp ANIMASI jadi MP4 h264 (durasi animasi asli
// terbawa) supaya bisa menjadi anak VIDEO album.
func convertWebPToMP4(ctx context.Context, data []byte) ([]byte, error) {
	return runFfmpegConvert(ctx, data, ".webp", "out.mp4",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-movflags", "+faststart")
}

// normalizePhotoBytes menyiapkan byte foto untuk dikirim: WebP statis →
// JPEG, WebP animasi → MP4 (isVideo true), non-WebP → apa adanya (JPEG/PNG
// tidak pernah di-re-encode). Konversi gagal = asSticker true (kirim mentah
// sebagai stiker — animasi/statis tetap hidup, lebih baik daripada bubble
// kosong yang tidak ke-render).
func normalizePhotoBytes(ctx context.Context, data []byte) (out []byte, isVideo, asSticker bool) {
	if !isWebPBytes(data) {
		return data, false, false
	}
	if isAnimatedWebP(data) {
		if mp4, err := convertWebPToMP4(ctx, data); err == nil && len(mp4) > 0 {
			return mp4, true, false
		}
		fmt.Printf("⚠️ downloader: webp animasi gagal jadi mp4, fallback stiker.\n")
		return data, false, true
	}
	if jpg, err := convertWebPToJPEG(ctx, data); err == nil && len(jpg) > 0 {
		return jpg, false, false
	}
	fmt.Printf("⚠️ downloader: webp gagal jadi jpeg, fallback stiker.\n")
	return data, false, true
}

// sendDlPhotoSmart mengirim byte foto dengan penanganan WebP: statis → JPEG
// (jalur gambar), animasi → MP4 (jalur video + thumb), gagal konversi →
// stiker mentah. Dipakai loop single downloader.
func sendDlPhotoSmart(ctx context.Context, mctx *store.MessageContext, data []byte, caption string) error {
	out, isVideo, asSticker := normalizePhotoBytes(ctx, data)
	if asSticker {
		label := caption
		if label == "" {
			label = "webp"
		}
		return sendWebPAsSticker(ctx, mctx, out, label)
	}
	if isVideo {
		return sendDlBytesVideo(ctx, mctx, out, caption)
	}
	return sendDlBytesImage(ctx, mctx, out, caption)
}

// sendWebPAsSticker mengirim byte webp mentah sebagai stiker (fallback bila
// konversi gagal). EXIF pack/author standar; gagal EXIF = kirim mentah
// (stiker tanpa metadata tetap tampil — lebih baik daripada dilewati).
func sendWebPAsSticker(ctx context.Context, mctx *store.MessageContext, data []byte, label string) error {
	webp := data
	if ex, err := store.WriteStickerExif(data, config.StickerPackname, config.StickerAuthor); err == nil {
		webp = ex
	} else {
		fmt.Printf("⚠️ downloader: EXIF stiker gagal (%v), kirim mentah.\n", err)
	}
	return sendStickerWebp(ctx, mctx, webp, "dl-sticker", label)
}

// urlRe mendeteksi URL http/https di teks bebas untuk autodownload.
var urlRe = regexp.MustCompile(`(?i)https?://[^\s]+`)

// firstSupportedURL mengambil SATU URL medsos pertama yang didukung dari
// teks (link pertama saja, sesuai keputusan — anti boros). Mengembalikan
// URL mentah + platformnya, atau kosong kalau tidak ada yang cocok.
func firstSupportedURL(text string) (string, *dlPlatform) {
	for _, raw := range urlRe.FindAllString(text, -1) {
		cleaned := strings.TrimRight(raw, ".,;:!?)'\"")
		host := dlURLHost(cleaned)
		if p := matchDlPlatform(host); p != nil {
			return cleaned, p
		}
	}
	return "", nil
}

// CheckAutodownload dipanggil handler untuk tiap pesan NON-command. Kalau
// autodownload aktif di chat ini dan teks mengandung link medsos yang
// didukung, link pertama didownload + dikirim — lalu pemrosesan pesan
// DILANJUTKAN normal (AI trigger tetap jalan, tidak seperti antilink yang
// menyetop). Seluruh kerja berat async di goroutine; fungsi ini kembali
// seketika. Kegagalan DIBALAS ke chat (keputusan owner: jangan bikin user
// menunggu tanpa kabar).
func CheckAutodownload(client *whatsmeow.Client, evt *events.Message, text string) {
	ctx := context.Background()
	chatJID := evt.Info.Chat.String()
	on, err := store.Groups().IsAutodownloadOn(ctx, chatJID)
	if err != nil {
		fmt.Printf("⚠️ Gagal cek autodownload %s: %v\n", chatJID, err)
		return
	}
	if !on {
		return
	}
	rawURL, p := firstSupportedURL(text)
	if rawURL == "" {
		return
	}

	mctx := &store.MessageContext{
		Client: client,
		Chat:   evt.Info.Chat,
		Sender: evt.Info.Sender,
		RawEvt: evt,
	}

	go func() {
		bg := context.Background()
		// Validasi penuh (domain + tameng SSRF) di dalam goroutine supaya
		// resolve DNS tidak menahan event loop.
		if verr := validateDlURL(bg, rawURL, p.hosts); verr != nil {
			_ = store.ReplyText(bg, mctx, fmt.Sprintf("Gagal autodownload: %v", verr))
			return
		}
		// Antrean global ikut berlaku (bagi pakai dengan command manual).
		select {
		case dlSem <- struct{}{}:
			defer func() { <-dlSem }()
		default:
			_ = store.ReplyText(bg, mctx, "Antrean download penuh (maks 2 bersamaan), link dilewati.")
			return
		}
		status, serr := sendDlStatus(bg, mctx, "Mengunduh, tunggu sebentar...")
		if serr != nil {
			fmt.Printf("⚠️ Autodownload: gagal kirim status ke %s: %v\n", chatJID, serr)
			return
		}
		if rerr := downloadAndSend(bg, mctx, rawURL, p, p.audio, status); rerr != nil {
			_ = store.ReplyText(bg, mctx, fmt.Sprintf("Gagal autodownload: %v", rerr))
		}
	}()
}
