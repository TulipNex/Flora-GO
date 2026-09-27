package plugins

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

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
// ikut batas yt-dlp/WhatsApp), tapi antrean + timeout + tameng SSRF
// tetap dipasang supaya panel tidak tumbang.
const (
	dlTimeout   = 10 * time.Minute // bunuh yt-dlp yang macet
	dlMaxPar    = 2                // download bersamaan; sisanya ditolak sopan
	dlVideoFmt  = "bv*[height<=720]+ba/b[height<=720]/b"
	dlStaleDays = 24 * time.Hour // umur temp yatim sebelum disapu startup
	// Album TIDAK dibatasi (keputusan owner): semua item hasil scrape
	// dikirim satu per satu, berapa pun jumlahnya.
)

// dlSem membatasi download bersamaan (yt-dlp + upload boros bandwidth,
// RAM, dan disk). Non-blocking: penuh = tolak langsung, bukan antre diam.
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
// CATATAN: pinterest & soundcloud DITUNDA atas permintaan owner — entrinya
// dihapus dari daftar (bukan sekadar dimatikan) supaya tidak muncul di
// .menu. Balikkan dengan menambah satu baris (universal j2download sudah
// mendukung keduanya, tinggal tambah adapter bila mau rantai khusus).
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
// — yt-dlp fetch server-side, tanpa ini bisa jadi exfil internal).
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

// downloadAndSend adalah engine utama: coba scraper API dulu (cepat,
// tanpa exec, plus dukung foto & album), jatuh ke yt-dlp kalau semua
// gagal (lambat tapi bicara langsung ke platform). Album TANPA batas
// jumlah (keputusan owner). Status "Mengunduh..." SENGAJA dibiarkan dan
// tidak ada pesan penutup — media terakhir adalah penanda selesainya.
func downloadAndSend(ctx context.Context, mctx *store.MessageContext, rawURL string, p *dlPlatform, audioOnly bool, status types.MessageID) error {
	// Jalur 1 — scraper, untuk semua platform KECUALI youtube: yt-dlp
	// juaranya tak tergantikan di sana (termasuk mode --mp3 yang butuh
	// konversi). Spotify WAJIB lewat scraper (yt-dlp buta Spotify).
	if p.command != "youtube" {
		if res, err := fetchScraper(ctx, p, rawURL); err == nil {
			return sendScrapeResult(ctx, mctx, res, status)
		} else {
			fmt.Printf("⚠️ downloader: scraper gagal, fallback yt-dlp (%v)\n", err)
		}
	}
	// Jalur 2 — yt-dlp (fallback universal + utama YouTube/audio).
	return downloadViaYtdlp(ctx, mctx, rawURL, audioOnly, status)
}

// fetchScraper menjalankan rantai adapter per platform + universal
// j2download sebagai jaring pengaman terakhir SEBELUM yt-dlp.
func fetchScraper(ctx context.Context, p *dlPlatform, rawURL string) (*scraper.ScrapeResult, error) {
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
	default: // facebook (+ platform baru lain): langsung universal
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
			serr = sendDlBytesImage(ctx, mctx, data, cap)
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

// downloadViaYtdlp adalah jalur lama (exec yt-dlp → temp → kirim):
// dipakai utama untuk YouTube/audio dan fallback universal saat scraper
// gagal total. Dipertahankan apa adanya karena sudah terbukti di panel.
func downloadViaYtdlp(ctx context.Context, mctx *store.MessageContext, rawURL string, audioOnly bool, status types.MessageID) error {
	dir, err := os.MkdirTemp("", "wabot-dl-*")
	if err != nil {
		return fmt.Errorf("gagal siapkan temp: %v", err)
	}
	defer os.RemoveAll(dir) // umur file = umur job ini saja

	timeoutCtx, cancel := context.WithTimeout(ctx, dlTimeout)
	defer cancel()

	tmpl := filepath.Join(dir, "%(title).80s.%(ext)s")
	args := []string{"--no-playlist", "-o", tmpl}
	if audioOnly {
		args = append(args, "-f", "bestaudio/best", "-x", "--audio-format", "mp3")
	} else {
		// Preferensi codec player-chat WhatsApp (terutama Android): H.264
		// + AAC. Tanpa ini yt-dlp bisa mengambil VP9/AV1 + Opus yang
		// ditolak player dengan "masalah dengan file video". Fallback
		// berlapis: avc1+m4a dulu, baru format umum (dlVideoFmt), lalu
		// best apa adanya — tetap terkirim walau sumbernya tidak punya
		// varian H.264 sama sekali.
		// --postprocessor-args Merger: moov di depan (faststart) supaya
		// bisa streaming saat download penerima belum selesai.
		args = append(args,
			"-f", "bv*[height<=720][vcodec^=avc1]+ba[m4a]/b[height<=720][vcodec^=avc1]/"+dlVideoFmt+"/b",
			"--merge-output-format", "mp4",
			"--postprocessor-args", "Merger+ffmpeg:-movflags +faststart",
		)
	}
	args = append(args, rawURL)

	// Exec TANPA shell (arg list) — URL jahat tidak bisa jadi injeksi.
	cmd := exec.CommandContext(timeoutCtx, "yt-dlp", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		if timeoutCtx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("download melebihi %s, dibatalkan.", dlTimeout)
		}
		return fmt.Errorf("gagal download: %v", firstDlErrorLine(out))
	}

	file, err := pickDlFile(dir)
	if err != nil {
		return err
	}
	caption := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))

	if audioOnly {
		err = sendDlAudio(ctx, mctx, file)
	} else {
		err = sendDlVideo(ctx, mctx, file, caption)
	}
	// Pesan status "Mengunduh..." SENGAJA dibiarkan (keputusan owner) dan
	// tidak ada pesan penutup — media terakhir adalah penanda selesainya.
	if err != nil {
		return fmt.Errorf("gagal kirim hasil: %v", err)
	}
	return nil
}

// firstDlErrorLine mengambil baris ERROR yt-dlp (paling informatif untuk
// user: privat, login-wall, region-block, dsb), fallback ke 300 char awal.
func firstDlErrorLine(out []byte) string {
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "ERROR") {
			return strings.TrimSpace(line)
		}
	}
	runes := []rune(strings.TrimSpace(string(out)))
	if len(runes) > 300 {
		return string(runes[:300]) + "..."
	}
	if len(runes) == 0 {
		return "error tidak diketahui"
	}
	return string(runes)
}

// pickDlFile memilih file hasil unduhan (terbesar, abaikan sisa .part).
func pickDlFile(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		return "", fmt.Errorf("tidak ada file yang terdownload (konten privat/dihapus?)")
	}
	best := ""
	var bestSize int64 = -1
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".part") || strings.HasSuffix(name, ".ytdl") || strings.HasSuffix(name, ".temp") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.IsDir() {
			continue
		}
		if info.Size() > bestSize {
			bestSize = info.Size()
			best = filepath.Join(dir, name)
		}
	}
	if best == "" {
		return "", fmt.Errorf("tidak ada file valid yang terdownload.")
	}
	return best, nil
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

// sendDlVideo mengupload + mengirim file sebagai video dengan caption judul.
func sendDlVideo(ctx context.Context, mctx *store.MessageContext, file, caption string) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	return sendDlBytesVideo(ctx, mctx, data, caption)
}

// sendDlBytesVideo mengirim byte sebagai video (jalur scraper + yt-dlp).
// Thumbnail inline (1 frame via ffmpeg) + dimensi dipasang supaya daftar
// chat menampilkan pratinjau, bukan kartu mini kosong. Dimensi diambil dari
// thumbnail (aspeknya sama dengan video asli).
func sendDlBytesVideo(ctx context.Context, mctx *store.MessageContext, data []byte, caption string) error {
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
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, &waE2E.Message{
		VideoMessage: vm,
	})
	if err != nil {
		return err
	}
	store.LogOutgoing(mctx, "dl-video", caption)
	store.TrackBotMessageID(resp.ID, caption)
	return nil
}

// sendDlAudio mengupload + mengirim file sebagai audio (bukan voice note).
func sendDlAudio(ctx context.Context, mctx *store.MessageContext, file string) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	label := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	return sendDlBytesAudio(ctx, mctx, data, label)
}

// sendDlBytesAudio mengirim byte sebagai audio (jalur scraper + yt-dlp).
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
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, &waE2E.Message{ImageMessage: im})
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

// CleanStaleDownloads menyapu folder temp yatim (wabot-dl-*) yang umurnya
// lebih dari dlStaleDays — pengaman kalau proses mati di tengah download
// sehingga defer RemoveAll tidak sempat jalan. Dipanggil sekali saat startup.
func CleanStaleDownloads() {
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "wabot-dl-") {
			continue
		}
		p := filepath.Join(os.TempDir(), e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.IsDir() && time.Since(info.ModTime()) > dlStaleDays {
			_ = os.RemoveAll(p)
		}
	}
}
