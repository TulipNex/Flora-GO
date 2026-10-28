package geminiapi

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// uploadEndpoint bisa ditimpa tes; produksi selalu memakai uploadURL.
var uploadEndpoint = uploadURL

// Upload mengunggah satu lampiran (gambar/PDF/dokumen) ke storage Gemini dan
// mengembalikan id yang bisa dipakai sebagai File pada Request.
//
// Alurnya mengikuti apa yang dilakukan halaman web: OPTIONS → POST "start"
// (mendapat X-Goog-Upload-Url) → OPTIONS → POST "upload, finalize". Tidak ada
// API resmi untuk ini, jadi bentuknya bisa berubah.
//
// mime wajib benar (kosong = dideteksi dari isi berkas) dan dikirim sebagai
// X-Goog-Upload-Header-Content-Type. Tanpa deklarasi tipe, server menerima
// unggahan tapi bisa menjawab kosong saat berkasnya dipakai di percakapan.
func (c *Client) Upload(ctx context.Context, name, mime string, data []byte) (File, error) {
	if len(data) == 0 {
		return File{}, fmt.Errorf("%w: berkas kosong", ErrUpload)
	}
	if err := c.ensureCookies(); err != nil {
		return File{}, err
	}
	cs, _, _, _ := c.snapshot()
	if cs == nil {
		return File{}, fmt.Errorf("%w: cookie kosong", ErrNoCookie)
	}
	if strings.TrimSpace(name) == "" {
		name = "lampiran"
	}
	if strings.TrimSpace(mime) == "" {
		mime = http.DetectContentType(data)
	}

	headers := map[string]string{
		"Accept":                              "*/*",
		"Accept-Language":                     "en-US,en;q=0.7",
		"Authorization":                       uploadBasic,
		"Content-Type":                        "application/x-www-form-urlencoded;charset=UTF-8",
		"Origin":                              baseURL,
		"Push-Id":                             c.pushIDValue(),
		"Referer":                             baseURL + "/",
		"X-Goog-Upload-Command":               "start",
		"X-Goog-Upload-Header-Content-Length": strconv.Itoa(len(data)),
		"X-Goog-Upload-Header-Content-Type":   mime,
		"X-Goog-Upload-Protocol":              "resumable",
		"X-Tenant-Id":                         tenantID,
		"User-Agent":                          defaultUA,
		"Cookie":                              cs.Header(),
	}

	if err := c.uploadRequest(ctx, http.MethodOptions, uploadEndpoint, headers, nil); err != nil {
		return File{}, err
	}

	startHeaders := cloneHeaders(headers)
	startHeaders["Size"] = strconv.Itoa(len(data))
	var startBody io.Reader
	if strings.TrimSpace(name) != "" {
		startBody = strings.NewReader("File name: " + name)
	}
	resp, err := c.uploadDo(ctx, http.MethodPost, uploadEndpoint, startHeaders, startBody)
	if err != nil {
		return File{}, err
	}
	uploadTarget := resp.Header.Get("X-Goog-Upload-Url")
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	resp.Body.Close()
	if resp.StatusCode >= 400 || uploadTarget == "" {
		return File{}, fmt.Errorf("%w: start %d (url unggah %s)", ErrUpload, resp.StatusCode, emptyDash(uploadTarget))
	}

	if err := c.uploadRequest(ctx, http.MethodOptions, uploadTarget, headers, nil); err != nil {
		return File{}, err
	}

	finalHeaders := cloneHeaders(headers)
	// Finalize mengirim byte mentah, bukan form: tipe isinya harus tipe berkas
	// yang sebenarnya.
	finalHeaders["Content-Type"] = mime
	finalHeaders["X-Goog-Upload-Command"] = "upload, finalize"
	finalHeaders["X-Goog-Upload-Offset"] = "0"
	finalResp, err := c.uploadDo(ctx, http.MethodPost, uploadTarget, finalHeaders, bytes.NewReader(data))
	if err != nil {
		return File{}, err
	}
	defer finalResp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(finalResp.Body, 64<<10))
	if finalResp.StatusCode >= 400 {
		return File{}, fmt.Errorf("%w: finalize %d: %s", ErrUpload, finalResp.StatusCode, snippet(body))
	}
	id := strings.TrimSpace(string(body))
	if id == "" || strings.ContainsAny(id, " \n\t") {
		return File{}, fmt.Errorf("%w: id lampiran tidak dikenali: %s", ErrUpload, snippet(body))
	}
	return File{ID: id, Name: name}, nil
}

func (c *Client) uploadRequest(ctx context.Context, method, target string, headers map[string]string, body io.Reader) error {
	resp, err := c.uploadDo(ctx, method, target, headers, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%w: %s %d", ErrUpload, strings.ToLower(method), resp.StatusCode)
	}
	return nil
}

func (c *Client) uploadDo(ctx context.Context, method, target string, headers map[string]string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUpload, err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUpload, err)
	}
	return resp, nil
}

func cloneHeaders(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func snippet(b []byte) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
