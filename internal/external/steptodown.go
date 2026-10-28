package external

// Helper umum untuk endpoint "steptodown.com": sejumlah tool download stok
// (Shutterstock, Getty, iStock, dst.) berbagi backend yang sama — form POST
// ke <tool>/get.php berisi parameter "url", lalu HTML hasil memuat link
// gambar relatif "images/steptodown.comNNN.jpg" yang bisa diunduh langsung.
//
// Alur (dua HTTP biasa):
//
//	POST https://steptodown.com/<tool>/get.php   form url=<url halaman>
//	     -> HTML berisi href="images/steptodown.comNNN.jpg"
//	GET  https://steptodown.com/<tool>/images/steptodown.comNNN.jpg -> byte gambar
//
// URL/ID yang tak dikenali dibalas HTTP 500 TANPA link -> dianggap tidak
// ditemukan. Nama file hasil acak tiap request (isinya deterministik per ID).

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// steptodownHost adalah origin steptodown.com.
const steptodownHost = "https://steptodown.com/"

// reSteptodownLink mencocokkan href gambar hasil pada HTML get.php.
var reSteptodownLink = regexp.MustCompile(`href="(images/steptodown\.com\d+\.(?:jpg|jpeg|png|webp))"`)

// SteptodownImage mengunduh byte gambar bersih dari sebuah tool steptodown
// (mis. "shutterstock-downloader", "getty-images-downloader") untuk input
// berupa URL halaman (atau ID, tergantung tool). Mengembalikan error bila
// tool tidak menemukan gambar atau jaringan gagal.
func SteptodownImage(ctx context.Context, tool, input string) ([]byte, error) {
	tool = strings.Trim(strings.TrimSpace(tool), "/")
	input = strings.TrimSpace(input)
	if tool == "" {
		return nil, fmt.Errorf("tool kosong")
	}
	if input == "" {
		return nil, fmt.Errorf("URL/ID kosong")
	}

	base := steptodownHost + tool + "/"
	form := url.Values{}
	form.Set("url", input)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"get.php", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", scraperUA)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	rel := parseSteptodownImageLink(body)
	if rel == "" {
		return nil, fmt.Errorf("gambar tidak ditemukan (URL/ID salah atau tidak tersedia)")
	}
	return DownloadBytes(ctx, base+rel)
}

// parseSteptodownImageLink mengambil path relatif gambar dari HTML get.php.
// Mengembalikan "" bila tidak ada (mis. URL/ID tak valid -> HTTP 500 tanpa link).
func parseSteptodownImageLink(html []byte) string {
	m := reSteptodownLink.FindSubmatch(html)
	if m == nil {
		return ""
	}
	return string(m[1])
}
