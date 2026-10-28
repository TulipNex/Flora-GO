package plugins

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"wabot-go/internal/external"
	"wabot-go/internal/logfmt"
	"wabot-go/internal/messaging"
	"wabot-go/internal/metrics"
	"wabot-go/internal/store"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/types"
)

func init() {
	registerPlugin(RegisterDownloader)
	if dlSem == nil {
		n := dlMaxPar
		if raw := strings.TrimSpace(os.Getenv("DL_MAX_PAR")); raw != "" {
			if v, err := strconv.Atoi(raw); err == nil && v >= 1 && v <= 8 {
				n = v
			}
		}
		dlSem = make(chan struct{}, n)
	}
}

// Batasan downloader medsos. Tanpa cap ukuran file (keputusan owner —
// ikut batas WhatsApp), tapi antrean + tameng SSRF tetap dipasang supaya
// panel tidak tumbang.
const (
	dlTimeout   = 4 * time.Minute // yt-dlp primer: gagal-cepat lalu fallback scraper
	dlMaxPar    = 2               // download bersamaan; sisanya ditolak sopan (override via env DL_MAX_PAR)
	dlVideoFmt  = "bv*[height<=720]+ba/b[height<=720]/b"
	dlStaleDays = 24 * time.Hour // umur temp yatim sebelum disapu startup
	// Album TIDAK dibatasi (keputusan owner): semua item hasil scrape
	// dikirim satu per satu, berapa pun jumlahnya.
	// Batas ukuran & durasi YouTube (keputusan owner): media ≤100MB kirim
	// normal (playable), 100-200MB kirim sebagai dokumen (batas WA 2GB,
	// tapi RAM panel 1,46GB tidak muat lebih), >200MB ditolak. Durasi
	// maks 35 menit divalidasi SEBELUM unduh via --match-filter.
	maxDlMediaBytes = 100 << 20
	maxDlDocBytes   = 200 << 20
	maxDlSeconds    = 2100 // 35 menit
)

// dlSem membatasi download bersamaan (unduh + upload boros bandwidth, RAM,
// dan disk). Non-blocking: penuh = tolak langsung, bukan antre diam.
// Ukuran bisa di-override via env DL_MAX_PAR (default 2, cap 8).
var dlSem chan struct{}

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
	{command: "spotify", aliases: []string{"sp"}, desc: "Download audio Spotify (.spotify <url/judul>)", hosts: []string{"open.spotify.com", "spotify.link"}, audio: true},
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
			Category:    CategoryUnduh,
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
		if err := store.GroupsFor(mctx.Client).SetAutodownload(ctx, mctx.Chat.String(), true); err != nil {
			return store.ReplyText(ctx, mctx, ErrMsg("Autodownload", fmt.Sprintf("Gagal menyalakan: %v", err)))
		}
		return store.ReplyText(ctx, mctx, Block("Autodownload", Row("Status", "aktif"), "Link medsos yang didukung otomatis didownload + dikirim."))
	case "off", "mati", "disable":
		if err := store.GroupsFor(mctx.Client).SetAutodownload(ctx, mctx.Chat.String(), false); err != nil {
			return store.ReplyText(ctx, mctx, ErrMsg("Autodownload", fmt.Sprintf("Gagal mematikan: %v", err)))
		}
		return store.ReplyText(ctx, mctx, Block("Autodownload", Row("Status", "mati")))
	default:
		on, err := store.GroupsFor(mctx.Client).IsAutodownloadOn(ctx, mctx.Chat.String())
		if err != nil {
			return store.ReplyText(ctx, mctx, ErrMsg("Autodownload", fmt.Sprintf("Gagal baca status: %v", err)))
		}
		status := "mati"
		if on {
			status = "aktif"
		}
		return store.ReplyText(ctx, mctx, Block("Autodownload", Row("Status", status), "Ubah: `.autodownload on` / `.autodownload off`."))
	}
}

func handleDownload(ctx context.Context, mctx *store.MessageContext, p dlPlatform) error {
	if len(mctx.Args) == 0 {
		if p.command == "spotify" {
			return store.ReplyText(ctx, mctx, Block("Spotify", "Sertakan URL atau judul lagu.", "Contoh: .sp https://open.spotify.com/... atau .sp about you"))
		}
		return store.ReplyText(ctx, mctx, Block("Downloader", Row("Cara", "."+p.command+" <url>"), Row("Sumber", strings.Join(p.hosts, ", "))))
	}
	// Spotify: tiga jalur — pilih angka pending, URL langsung, atau pencarian judul.
	if p.command == "spotify" {
		if len(mctx.Args) == 1 {
			if handled, msg := trySpotifyNumberPick(mctx, mctx.Args[0]); handled {
				if msg != "" {
					return store.ReplyText(ctx, mctx, msg)
				}
				return nil
			}
		}
		if !isSpotifyURLArg(mctx.Args[0]) {
			query := strings.TrimSpace(mctx.RawArgs)
			if query == "" {
				query = strings.Join(mctx.Args, " ")
			}
			return handleSpotifySearch(ctx, mctx, query)
		}
	}
	rawURL := mctx.Args[0]
	if err := validateDlURL(ctx, rawURL, p.hosts); err != nil {
		return store.ReplyText(ctx, mctx, Block("Downloader", "Status: "+err.Error(), "Contoh: ."+p.command+" https://..."))
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
		return store.ReplyText(ctx, mctx, QueueFullMsg())
	}

	// Async: download bisa menit-menit — balasan dikirim saat selesai,
	// event loop tidak terblokir.
	status, err := sendDlStatus(ctx, mctx, "Mengunduh, tunggu sebentar...")
	if err != nil {
		return err
	}
	go func() {
		bg := context.Background()
		metrics.IncDownload()
		if rerr := downloadAndSend(bg, mctx, rawURL, &p, audioOnly, status); rerr != nil {
			metrics.IncDownloadError()
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

// downloadAndSend adalah engine utama. YouTube: yt-dlp PRIMER (pilih
// format compliant sejak unduh — tanpa transcode, tanpa polling), gagal-
// cepat 4 menit lalu jatuh ke rantai scraper. Platform lain: langsung
// rantai scraper. Kalau semua gagal, error gabungan dibalas ke user oleh
// pemanggil. Album TANPA batas jumlah (keputusan owner). Status
// "Mengunduh..." SENGAJA dibiarkan dan tidak ada pesan penutup — media
// terakhir adalah penanda selesainya.
func downloadAndSend(ctx context.Context, mctx *store.MessageContext, rawURL string, p *dlPlatform, audioOnly bool, status types.MessageID) error {
	if p.command == "youtube" || p.command == "youtubemp3" {
		// YouTube HANYA via yt-dlp (keputusan owner — tanpa fallback
		// scraper). Gagal = error langsung dibalas ke user oleh pemanggil.
		return downloadViaYtdlp(ctx, mctx, rawURL, audioOnly, status)
	}
	if p.command == "spotify" {
		// Spotify: cover art + caption DULU, audio KEMUDIAN (lihat
		// downloadSpotifyAndSend di spotify_search.go). hint nil = meta
		// resolve dari URL; gagal meta/cover = audio-only diam-diam.
		return downloadSpotifyAndSend(ctx, mctx, rawURL, p, audioOnly, status, nil)
	}
	res, err := fetchScraper(ctx, p, rawURL, audioOnly)
	if err != nil {
		logfmt.Warn("downloader: semua scraper gagal (%v)\n", err)
		return err
	}
	return sendScrapeResult(ctx, mctx, res, status)
}

// fetchScraper menjalankan adapter platform yang bersangkutan. TIDAK ada lagi
// fallback universal (omnify) di sini: endpoint-nya menuntut API key sejak
// Okt 2026 (401 untuk semua request), jadi dulu ia hanya menambah ~20 detik
// dan pesan error dobel. Rantai cadangan sekarang murni milik tiap adapter
// (mis. tiktok: faa → tikwm → tiktokdl → musically; instagram: igexport →
// savereels; facebook: faa saja).
func fetchScraper(ctx context.Context, p *dlPlatform, rawURL string, audioOnly bool) (*external.ScrapeResult, error) {
	switch p.command {
	case "tiktok":
		return external.FetchTikTok(ctx, rawURL)
	case "instagram":
		return external.FetchInstagram(ctx, rawURL)
	case "twitter":
		return external.FetchTwitter(ctx, rawURL)
	case "spotify":
		return external.FetchSpotify(ctx, rawURL)
	case "facebook":
		return external.FetchFacebook(ctx, rawURL)
	case "pinterest":
		return external.FetchPinterest(ctx, rawURL)
	default:
		return nil, fmt.Errorf("platform %s belum punya adapter", p.command)
	}
}

// sendScrapeResult mengirim SEMUA item hasil scrape sesuai jenisnya
// (video/audio/foto) — tanpa batas jumlah (keputusan owner). Pesan status
// "Mengunduh..." SENGAJA dibiarkan (tidak di-revoke) dan tidak ada pesan
// penutup; media terakhir adalah penanda selesainya. Item yang gagal
// diunduh/dikirim dilewati (log console); kalau SEMUA gagal baru error
// ke user (keputusan owner: kegagalan tetap dibalas agar user tidak
// menunggu tanpa kabar).
func sendScrapeResult(ctx context.Context, mctx *store.MessageContext, res *external.ScrapeResult, status types.MessageID) error {
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
		data, err := external.DownloadBytes(ctx, item.URL)
		if err != nil {
			logfmt.Warn("downloader: unduh item %d gagal (%v)\n", i+1, err)
			failed++
			continue
		}
		cap := ""
		if i == 0 {
			cap = caption
		}
		var serr error
		// Sniff byte: tebakan Kind dari adapter bisa salah (faa tidak
		// membedakan foto/video).
		switch resolveMediaKind(data, item.Kind) {
		case external.KindAudio:
			serr = sendDlBytesAudio(ctx, mctx, data, labelFromURL(item.URL))
		case external.KindPhoto:
			serr = sendDlPhotoSmart(ctx, mctx, data, cap)
		default:
			serr = sendDlBytesVideo(ctx, mctx, data, cap)
		}
		if serr != nil {
			logfmt.Warn("downloader: kirim item %d gagal (%v)\n", i+1, serr)
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

// resolveMediaKind mengoreksi Kind hasil scraper dengan sniff 512 byte awal
// hasil unduhan. Perlu karena sebagian engine (faa) tidak membedakan foto dan
// video — adapter hanya menebak dari bentuk URL. Hasil sniff yang jelas
// (image/* atau video/*) menang atas tebakan adapter; selain itu tebakan
// dipertahankan. Audio tidak pernah diubah (tidak di-sniff).
func resolveMediaKind(data []byte, kind external.MediaKind) external.MediaKind {
	if kind == external.KindAudio || len(data) == 0 {
		return kind
	}
	head := data
	if len(head) > 512 {
		head = head[:512]
	}
	ct := http.DetectContentType(head)
	switch {
	case strings.HasPrefix(ct, "video/"):
		return external.KindVideo
	case strings.HasPrefix(ct, "image/"):
		return external.KindPhoto
	}
	return kind
}

// albumEligible true kalau ada ≥2 item foto/video (syarat minimal album).
// Audio tidak dihitung — tidak ikut album.
func albumEligible(items []external.ScrapeItem) bool {
	n := 0
	for _, item := range items {
		if item.Kind == external.KindPhoto || item.Kind == external.KindVideo {
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

func sendScrapeAlbum(ctx context.Context, mctx *store.MessageContext, items []external.ScrapeItem, caption string) error {
	var media []albumMediaItem
	sent, failed := 0, 0
	for i, item := range items {
		data, err := external.DownloadBytes(ctx, item.URL)
		if err != nil {
			logfmt.Warn("downloader: unduh item %d gagal (%v)\n", i+1, err)
			failed++
			continue
		}
		if item.Kind == external.KindAudio {
			if serr := sendDlBytesAudio(ctx, mctx, data, labelFromURL(item.URL)); serr != nil {
				logfmt.Warn("downloader: kirim item %d gagal (%v)\n", i+1, serr)
				failed++
				continue
			}
			sent++
			continue
		}
		// Sniff byte: Kind dari adapter hanya tebakan (lihat resolveMediaKind).
		switch resolveMediaKind(data, item.Kind) {
		case external.KindVideo:
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
				logfmt.Warn("downloader: kirim stiker item %d gagal (%v)\n", i+1, serr)
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
		logfmt.Warn("downloader: induk album gagal (%v), fallback satuan.\n", err)
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
			logfmt.Warn("downloader: kirim anak album %d gagal (%v)\n", i+1, serr)
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
			logfmt.Warn("downloader: kirim media gagal (%v)\n", serr)
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
