package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"wabot-go/internal/config"
	"wabot-go/internal/external"
	"wabot-go/internal/logfmt"
	"wabot-go/internal/messaging"
	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterBrat)
}

const (
	// bratFaaBase adalah endpoint generator brat faa (primer): me-return
	// bytes media LANGSUNG (PNG untuk /brat, GIF animasi untuk /bratvid)
	// — tanpa perantara JSON seperti Sawit. Terverifikasi: teks biasa +
	// emoji ke-render (emoji full color), background putih solid.
	bratFaaBase = "https://api-faa.my.id/faa"

	// bratAPIBase adalah endpoint generator brat Sawit API (fallback).
	// Skemanya sama untuk image dan video, yang membedakan hanya query
	// type=nya; respons JSON berisi URL media yang lalu diunduh.
	bratAPIBase = "https://api.sawit.biz.id/api/maker/brat"

	// bratMaxText membatasi panjang teks supaya URL query tetap wajar
	// dan beban generator di sisi API tidak meledak — teks brat memang
	// dirancang pendek (meme satu-dua baris).
	bratMaxText = 200

	// bratTimeout membatasi seluruh tahap jaringan (request generator +
	// unduh media hasil) supaya request yang menggantung tidak menahan
	// goroutine selamanya.
	bratTimeout = 60 * time.Second

	// bratMaxResponse membatasi respons JSON API — kalau server balas
	// blob bukan JSON, decode akan gagal jelas daripada menelan memori.
	bratMaxResponse = 1 << 20 // 1MB
)

// bratVariant mendeskripsikan satu command brat: jenis hasil dari API
// (image/video) dan perlakuan akhirnya di WhatsApp.
type bratVariant struct {
	command string
	aliases []string
	kind    string // nilai query type= dan ekspektasi result.type dari API
	desc    string
	title   string // judul balasan standar (Title-Case), mis. "Brat"
	isVideo bool   // true = hasil di-convert jadi stiker animasi
}

// bratVariants adalah SEMUA varian brat — dipakai pendaftaran command
// sekaligus sebagai sumber kebenaran kind API, supaya query type= dan
// ekspektasi hasil tidak pernah terpisah.
var bratVariants = []bratVariant{
	{
		command: "brat",
		aliases: []string{"br"},
		kind:    "image",
		desc:    "Bikin stiker brat dari teks (.brat <teks> atau reply pesan)",
		title:   "Brat",
		isVideo: false,
	},
	{
		command: "bratvideo",
		aliases: []string{"bratvid"},
		kind:    "video",
		desc:    "Bikin stiker brat video (.bratvideo <teks> atau reply pesan)",
		title:   "Brat Video",
		isVideo: true,
	},
}

// RegisterBrat mendaftarkan semua varian brat kategori Media. Satu engine
// dipakai bersama — tiap command hanya membawa kind + perlakuan konversi.
func RegisterBrat(r *Registry) {
	for _, v := range bratVariants {
		v := v
		r.Register(Command{
			Name:        v.command,
			Aliases:     v.aliases,
			Description: v.desc,
			Category:    CategoryMedia,
			Handler: func(ctx context.Context, mctx *store.MessageContext) error {
				return handleBrat(ctx, mctx, v)
			},
		})
	}
}

// handleBrat memvalidasi teks input lalu memproses brat di goroutine
// terpisah (pola sama dengan engine downloader): tahap jaringan bisa
// memakan beberapa detik, jadi event loop hypermeow tidak boleh diblokir.
func handleBrat(ctx context.Context, mctx *store.MessageContext, v bratVariant) error {
	text := bratInputText(mctx)
	if text == "" {
		return store.ReplyText(ctx, mctx, Block(v.title,
			Row("Cara", fmt.Sprintf(".%s <teks> / reply pesan", v.command)),
			Row("Contoh", fmt.Sprintf(".%s apa kabar", v.command)),
		))
	}
	if utf8.RuneCountInString(text) > bratMaxText {
		return store.ReplyText(ctx, mctx, ErrMsg(v.title,
			fmt.Sprintf("Teks kepanjangan (maks %d karakter).", bratMaxText)))
	}

	go func() {
		bg := context.Background()
		if err := bratGenerate(bg, mctx, text, v); err != nil {
			logfmt.Warn("brat(%s): %v\n", v.kind, err)
			_ = store.ReplyText(bg, mctx, ErrMsg(v.title,
				fmt.Sprintf("Gagal (%v, coba lagi sebentar.)", err)))
		}
	}()
	return nil
}

// bratInputText mengambil teks brat dari argumen command; kalau kosong,
// jatuh ke pesan yang di-reply (teks maupun caption media). Pesan di-unwrap
// dulu lewat ExtractQuotedMessage+UnwrapMessage supaya tetap jalan di grup
// ber-Pesan-Sementara / pesan sekali-lihat (jalur ContextInfo bersama).
func bratInputText(mctx *store.MessageContext) string {
	if text := strings.TrimSpace(mctx.RawArgs); text != "" {
		return text
	}

	quoted := store.UnwrapMessage(store.ExtractQuotedMessage(mctx.RawEvt))
	if quoted == nil {
		return ""
	}
	// Getter protobuf aman dipanggil pada receiver nil, jadi tiap tipe
	// dicek berurutan tanpa perlu if bertingkat. Pengecekan dipisah (bukan
	// konkatenasi) supaya dua field yang kebetulan terisi tidak menempel
	// jadi satu baris tanpa pemisah.
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

// bratGenerate menjalankan seluruh pipeline brat: panggil API → unduh
// hasil → validasi byte → convert ke stiker → pasang EXIF → kirim.
// error yang keluar berupa pesan ramah siap-balas.
func bratGenerate(ctx context.Context, mctx *store.MessageContext, text string, v bratVariant) error {
	ctx, cancel := context.WithTimeout(ctx, bratTimeout)
	defer cancel()

	media, err := fetchBratMedia(ctx, text, v.kind)
	if err != nil {
		return err
	}
	if err := messaging.CheckMediaBytes(media); err != nil {
		return err
	}

	// Pipeline convert stiker yang SAMA dengan .sticker (ffmpeg pad
	// 512x512 transparan + EXIF pack/author) — reuse, bukan duplikasi,
	// supaya perilaku stiker tidak pernah menyimpang antar command.
	webp, err := convertToStickerWebp(media, v.isVideo)
	if err != nil {
		return fmt.Errorf("gagal convert ke stiker: %v", err)
	}
	webp, err = store.WriteStickerExif(webp, config.StickerPackname, config.StickerAuthor)
	if err != nil {
		return fmt.Errorf("gagal set metadata stiker: %v", err)
	}
	return sendStickerWebp(ctx, mctx, webp, "brat", v.kind)
}

// bratAPIResponse adalah bentuk respons JSON Sawit API untuk
// /api/maker/brat — sama untuk type=image maupun type=video.
type bratAPIResponse struct {
	Status bool `json:"status"`
	Result struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	} `json:"result"`
}

// fetchBratMedia mengambil media brat via faa (primer, bytes langsung),
// fallback Sawit (JSON → URL → unduh) bila faa gagal. Keduanya support
// emoji; error gabungan menyebut keduanya supaya diagnosis jelas.
func fetchBratMedia(ctx context.Context, text, kind string) ([]byte, error) {
	if data, err := fetchBratFaa(ctx, text, kind); err == nil {
		return data, nil
	} else {
		errFaa := err
		data, serr := fetchBratSawit(ctx, text, kind)
		if serr != nil {
			return nil, fmt.Errorf("faa: %v; sawit: %v", errFaa, serr)
		}
		return data, nil
	}
}

// fetchBratFaa mengunduh bytes brat langsung dari faa: /brat (image/PNG)
// atau /bratvid (video/GIF animasi). Unduh lewat external.DownloadBytes
// supaya kena tameng SSRF + cap 100MB yang sama dengan media API lain.
func fetchBratFaa(ctx context.Context, text, kind string) ([]byte, error) {
	ep := "brat"
	if kind == "video" {
		ep = "bratvid"
	}
	query := url.Values{}
	query.Set("text", text)
	return external.DownloadBytes(ctx, bratFaaBase+"/"+ep+"?"+query.Encode())
}

// fetchBratSawit memanggil API generator Sawit lalu mengunduh media
// hasilnya. Unduh lewat external.DownloadBytes supaya kena tameng SSRF +
// cap 100MB yang sama dengan media dari API pihak ketiga lain.
func fetchBratSawit(ctx context.Context, text, kind string) ([]byte, error) {
	query := url.Values{}
	query.Set("text", text)
	query.Set("type", kind)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, bratAPIBase+"?"+query.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("gagal siapkan request: %v", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API tidak merespons: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("API menjawab HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, bratMaxResponse+1))
	if err != nil {
		return nil, fmt.Errorf("gagal baca respons API: %v", err)
	}
	if len(body) > bratMaxResponse {
		return nil, fmt.Errorf("respons API terlalu besar")
	}

	var parsed bratAPIResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("respons API bukan JSON valid")
	}
	if !parsed.Status || parsed.Result.URL == "" {
		return nil, fmt.Errorf("API tidak menghasilkan media (kemungkinan teks ditolak)")
	}
	// Ekspektasi tipe diverifikasi — kalau API diam-diam mengganti
	// perilaku (mis. tetap balas image saat diminta video), jangan
	// dipaksakan masuk jalur konversi yang salah.
	if parsed.Result.Type != kind {
		return nil, fmt.Errorf("API mengembalikan tipe %q, bukan %q", parsed.Result.Type, kind)
	}

	data, err := external.DownloadBytes(ctx, parsed.Result.URL)
	if err != nil {
		return nil, fmt.Errorf("gagal unduh hasil: %v", err)
	}
	return data, nil
}
