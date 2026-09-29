package scraper

import (
	"context"
	"fmt"
	"html"
	"strings"
)

// FetchFacebook mengambil video/foto Facebook via rantai: chative → nexray.
// Keduanya GET sederhana tanpa auth (terverifikasi live). Foto postingan
// tidak punya video — engine mengembalikan item foto bila ada, kalau tidak
// ada media sama sekali dianggap gagal supaya fallback jalan.
func FetchFacebook(ctx context.Context, pageURL string) (*ScrapeResult, error) {
	if res, err := chativeFetch(ctx, pageURL); err == nil {
		res.Source = "chative"
		return res, nil
	} else {
		err1 := err
		if res, err := nexrayFacebookFetch(ctx, pageURL); err == nil {
			res.Source = "nexray-fb"
			return res, nil
		} else {
			return nil, fmt.Errorf("facebook gagal semua: chative: %v | nexray: %v", err1, err)
		}
	}
}

// chativeGateway adalah API gateway milik chative.io (diambil dari JS bundle
// situsnya). Tanpa auth/key — GET langsung. Bisa berubah saat mereka
// redeploy; kalau 404, engine gagal dan fallback jalan.
const chativeGateway = "https://serverless-tooly-gateway-6n4h522y.ue.gateway.dev"

// chativeFetch memakai endpoint /facebook/video: {success, title,
// videos:{hd:{size,url}, sd:{size,url}}}.
func chativeFetch(ctx context.Context, pageURL string) (*ScrapeResult, error) {
	const engine = "chative"
	var r struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
		Title   string `json:"title"`
		Videos  *struct {
			HD struct {
				Size string `json:"size"`
				URL  string `json:"url"`
			} `json:"hd"`
			SD struct {
				Size string `json:"size"`
				URL  string `json:"url"`
			} `json:"sd"`
		} `json:"videos"`
	}
	api := chativeGateway + "/facebook/video?url=" + urlQueryEscapeGo(pageURL)
	if err := fetchJSON(ctx, engine, api, nil, &r); err != nil {
		return nil, err
	}
	if !r.Success || r.Videos == nil {
		return nil, fmt.Errorf("%s: %s", engine, firstNonEmpty(r.Error, "media tidak ditemukan"))
	}
	hd := strings.TrimSpace(r.Videos.HD.URL)
	sd := strings.TrimSpace(r.Videos.SD.URL)
	if hd == "" && sd == "" {
		return nil, fmt.Errorf("%s: tidak ada URL video (kemungkinan postingan foto)", engine)
	}
	link := firstNonEmpty(hd, sd)
	return &ScrapeResult{
		Title:  strings.TrimSpace(r.Title),
		Items:  []ScrapeItem{{URL: link, Kind: KindVideo}},
		Source: engine,
	}, nil
}

// nexrayFacebookFetch memakai /downloader/facebook (pola sama dengan engine
// spotify yang sudah jalan): {status, result:{title, video_hd, video_sd,
// audio}}. Judul mengandung HTML entities — di-decode.
func nexrayFacebookFetch(ctx context.Context, pageURL string) (*ScrapeResult, error) {
	const engine = "nexray-fb"
	var r struct {
		Status bool `json:"status"`
		Result *struct {
			Title   string `json:"title"`
			VideoHD string `json:"video_hd"`
			VideoSD string `json:"video_sd"`
			Audio   string `json:"audio"`
		} `json:"result"`
	}
	api := "https://api.nexray.web.id/downloader/facebook?url=" + urlQueryEscapeGo(pageURL)
	headers := map[string]string{"Referer": "https://api.nexray.web.id/"}
	if err := fetchJSON(ctx, engine, api, headers, &r); err != nil {
		return nil, err
	}
	if !r.Status || r.Result == nil {
		return nil, fmt.Errorf("%s: respons gagal/kosong", engine)
	}
	hd := strings.TrimSpace(r.Result.VideoHD)
	sd := strings.TrimSpace(r.Result.VideoSD)
	title := html.UnescapeString(strings.TrimSpace(r.Result.Title))
	if hd == "" && sd == "" {
		return nil, fmt.Errorf("%s: tidak ada URL video", engine)
	}
	return &ScrapeResult{
		Title:  title,
		Items:  []ScrapeItem{{URL: firstNonEmpty(hd, sd), Kind: KindVideo}},
		Source: engine,
	}, nil
}
