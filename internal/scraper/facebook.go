package scraper

import (
	"context"
	"fmt"
	"strings"
)

// FetchFacebook mengambil video/foto Facebook via rantai: faa → (omnify
// via universal di level engine). chative/nexray/fdown.vn SUDAH DIHAPUS
// (keputusan owner — ramping; faa + omnify terverifikasi menutup foto
// maupun video). Postingan foto tanpa video dilayani omnify (1 foto
// sampul); tidak ada sumber foto satuan (terbukti lintas 7+ layanan).
func FetchFacebook(ctx context.Context, pageURL string) (*ScrapeResult, error) {
	if res, err := faaFetch(ctx, pageURL); err == nil {
		res.Source = "faa"
		return res, nil
	} else {
		return nil, fmt.Errorf("facebook gagal: faa: %v", err)
	}
}

// faaFetch memakai api-faa.my.id/faa/fbdownload (tanpa auth/key):
// {status, result:{thumbnail, media}}. Media adalah URL proxy siap unduh
// (terverifikasi HEAD 200). Error/429 apa pun = gagal-cepat ke fallback.
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
