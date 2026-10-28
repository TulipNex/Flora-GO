package external

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// fbVideoURLRe hanya PETUNJUK jenis media, bukan gerbang pemilihan mesin:
// respons faa tidak menyebut foto/video, jadi URL yang cocok dianggap video
// dan sisanya foto. Tebakan ini dikoreksi dari byte hasil unduhan oleh
// resolveMediaKind (plugins/downloader.go), jadi salah tebak tidak
// berujung kirim video sebagai foto (atau sebaliknya).
//
// Termasuk bentuk share baru /share/r/ (reel) dan /video.php — dulu dua
// bentuk ini tidak cocok dan URL-nya terlempar ke jalur foto.
var fbVideoURLRe = regexp.MustCompile(`(?i)(/share/r/|/share/reel/|/share/v/|/reel/|/watch|/videos?/|/video\.php|fb\.watch/)`)

// FetchFacebook mengambil video/foto Facebook via faa — SATU-SATUNYA mesin
// (keputusan owner: omnify dihapus dari rantai, lihat catatan di bawah).
// Kegagalan faa langsung jadi error; tidak ada fallback lain.
//
// omnify SUDAH DIHAPUS (bukan dimatikan): sejak Okt 2026 endpoint
// api-aio.omnifylabs.sbs menjawab 401 "Akses ditolak: API Key atau Ticket
// Challenge diperlukan" untuk semua request tanpa key, jadi selama ini ia
// hanya menambah ~20 detik dan pesan error dobel. Jangan dihidupkan lagi
// tanpa API key yang sah.
func FetchFacebook(ctx context.Context, pageURL string) (*ScrapeResult, error) {
	return scrapeCache.GetOrLoad(ctx, pageURL, func(ctx context.Context) (*ScrapeResult, error) {
		res, err := faaFetch(ctx, pageURL)
		if err != nil {
			return nil, fmt.Errorf("facebook gagal: faa: %v", err)
		}
		if len(res.Items) > 0 && fbKindHint(pageURL) == KindPhoto {
			res.Items[0].Kind = KindPhoto
		}
		res.Source = "faa"
		return res, nil
	})
}

// fbKindHint menebak jenis media dari bentuk URL (fungsi murni, bisa dites
// tanpa jaringan).
func fbKindHint(pageURL string) MediaKind {
	if fbVideoURLRe.MatchString(pageURL) {
		return KindVideo
	}
	return KindPhoto
}

// faaFetch memakai api-faa.my.id/faa/fbdownload (tanpa auth/key):
// {status, result:{thumbnail, media}}. Media adalah URL proxy siap unduh
// (terverifikasi HEAD 200). Error/429 apa pun = gagal-cepat (tidak ada
// fallback lain, jadi kegagalan diteruskan apa adanya).
func faaFetch(ctx context.Context, pageURL string) (*ScrapeResult, error) {
	const engine = "faa"
	var r struct {
		Status bool `json:"status"`
		Result *struct {
			Thumbnail string `json:"thumbnail"`
			Media     string `json:"media"`
		} `json:"result"`
	}
	api := "https://api-faa.my.id/faa/fbdownload?url=" + urlQueryEscapeGo(pageURL)
	if err := fetchJSON(ctx, engine, api, nil, &r); err != nil {
		return nil, err
	}
	if !r.Status || r.Result == nil || strings.TrimSpace(r.Result.Media) == "" {
		return nil, fmt.Errorf("%s: media tidak ditemukan", engine)
	}
	return &ScrapeResult{
		Items:  []ScrapeItem{{URL: strings.TrimSpace(r.Result.Media), Thumb: strings.TrimSpace(r.Result.Thumbnail), Kind: KindVideo}},
		Source: engine,
	}, nil
}
