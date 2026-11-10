package plugins

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"wabot-go/internal/identity"
	"wabot-go/internal/logfmt"
	"wabot-go/internal/messaging"
	"wabot-go/internal/store"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types"
	"github.com/polymorfa/hypermeow/types/events"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

func init() {
	registerPlugin(RegisterConvert)
}

// ---- Konstanta ----

const (
	convertAPIBase       = "https://api.freeconvert.com/v1"
	convertSiteOrigin    = "https://www.freeconvert.com"
	convertUserAgent     = "Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/139.0.0.0 Mobile Safari/537.36"
	convertMaxInputBytes = 100 << 20
	convertPendingTTL    = 5 * time.Minute
)

var convertBaseHeaders = map[string]string{
	"accept":     "application/json",
	"origin":     convertSiteOrigin,
	"referer":    convertSiteOrigin + "/",
	"user-agent": convertUserAgent,
}

// convertTransport dipakai bersama semua klien di bawah supaya koneksi bisa
// dipakai ulang antar request (sebelumnya tiap request membuat http.Client
// baru, sehingga TLS handshake selalu diulang).
var convertTransport = &http.Transport{
	MaxIdleConns:        8,
	IdleConnTimeout:     90 * time.Second,
	TLSHandshakeTimeout: 10 * time.Second,
	ForceAttemptHTTP2:   true,
}

// Tiga klien dengan batas waktu berbeda per jenis operasi: token ringan,
// panggilan API JSON, dan unggah/unduh berkas (paling lama).
var (
	convertClientToken  = &http.Client{Timeout: 30 * time.Second, Transport: convertTransport}
	convertClientAPI    = &http.Client{Timeout: 45 * time.Second, Transport: convertTransport}
	convertClientUpload = &http.Client{Timeout: 120 * time.Second, Transport: convertTransport}
)

// Whitelist format umum per bucket.
var convertCommonExts = map[string]map[string]bool{
	"image": {
		"jpg": true, "jpeg": true, "png": true, "webp": true, "svg": true, "ico": true,
	},
	"document": {
		"pdf": true, "docx": true, "xlsx": true, "pptx": true, "txt": true, "csv": true, "html": true,
	},
	"audio": {
		"mp3": true, "wav": true, "ogg": true, "m4a": true, "flac": true,
	},
	"video": {
		"mp4": true, "webm": true, "mkv": true, "mov": true, "3gp": true,
	},
}

// ---- Token cache ----

var (
	convertCachedToken       string
	convertCachedTokenExpiry int64
	convertTokenMu           sync.Mutex
)

func convertTokenExpiry(token string) int64 {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return 0
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return 0
	}
	var payload struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return 0
	}
	return payload.Exp * 1000
}

func convertGetGuestToken(ctx context.Context, force bool) (string, error) {
	convertTokenMu.Lock()
	defer convertTokenMu.Unlock()

	now := time.Now().UnixMilli()
	if !force && convertCachedToken != "" && now < convertCachedTokenExpiry-60_000 {
		return convertCachedToken, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, convertAPIBase+"/account/guest", nil)
	if err != nil {
		return "", err
	}
	for k, v := range convertBaseHeaders {
		req.Header.Set(k, v)
	}
	client := convertClientToken
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gagal mengambil token (%d)", resp.StatusCode)
	}

	raw := strings.TrimSpace(string(body))
	token := raw
	var data map[string]any
	if err := json.Unmarshal([]byte(raw), &data); err == nil {
		if t, ok := data["token"].(string); ok && t != "" {
			token = t
		} else if t, ok := data["access_token"].(string); ok && t != "" {
			token = t
		}
	}
	if token == "" || len(token) < 20 {
		return "", fmt.Errorf("token freeconvert tidak valid")
	}

	convertCachedToken = token
	convertCachedTokenExpiry = convertTokenExpiry(token)
	if convertCachedTokenExpiry == 0 {
		convertCachedTokenExpiry = now + 12*60*60*1000
	}
	return token, nil
}

// ---- HTTP helper ----

func convertAPIRequest(ctx context.Context, path, method string, body any, forceToken bool) (map[string]any, error) {
	token, err := convertGetGuestToken(ctx, forceToken)
	if err != nil {
		return nil, err
	}

	for attempt := 0; attempt < 2; attempt++ {
		var buf io.Reader
		if body != nil {
			b, err := json.Marshal(body)
			if err != nil {
				return nil, err
			}
			buf = bytes.NewReader(b)
		}
		req, err := http.NewRequestWithContext(ctx, method, convertAPIBase+path, buf)
		if err != nil {
			return nil, err
		}
		for k, v := range convertBaseHeaders {
			req.Header.Set(k, v)
		}
		req.Header.Set("authorization", "Bearer "+token)
		if body != nil {
			req.Header.Set("content-type", "application/json")
		}

		client := convertClientAPI
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			token, err = convertGetGuestToken(ctx, true)
			if err != nil {
				return nil, err
			}
			continue
		}

		var data map[string]any
		if err := json.Unmarshal(respBody, &data); err != nil {
			data = map[string]any{"_raw": string(respBody)}
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			detail := fmt.Sprintf("http %d", resp.StatusCode)
			if m, ok := data["errors"].([]any); ok && len(m) > 0 {
				if em, ok := m[0].(map[string]any); ok {
					if msg, ok := em["message"].(string); ok && msg != "" {
						detail = msg
					}
				}
			} else if msg, ok := data["message"].(string); ok && msg != "" {
				detail = msg
			} else if raw, ok := data["_raw"].(string); ok && raw != "" {
				detail = raw
			}
			return nil, fmt.Errorf("%s", detail)
		}
		return data, nil
	}
	return nil, fmt.Errorf("tidak diizinkan oleh server freeconvert")
}

// ---- Ekstensi / MIME ----

var convertMimeExt = map[string]string{
	"image/jpeg":    "jpg",
	"image/jpg":     "jpg",
	"image/png":     "png",
	"image/webp":    "webp",
	"image/gif":     "gif",
	"image/bmp":     "bmp",
	"image/tiff":    "tiff",
	"image/svg+xml": "svg",
	"image/heic":    "heic",
	"image/heif":    "heic",
	"image/avif":    "avif",
	"image/x-icon":  "ico",
	"image/vnd.microsoft.icon": "ico",
	"image/vnd.adobe.photoshop": "psd",
	"application/pdf": "pdf",
	"application/msword": "doc",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": "docx",
	"application/vnd.ms-excel": "xls",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet": "xlsx",
	"application/vnd.ms-powerpoint": "ppt",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": "pptx",
	"application/vnd.oasis.opendocument.text": "odt",
	"application/rtf": "rtf",
	"application/epub+zip": "epub",
	"application/x-mobipocket-ebook": "mobi",
	"application/vnd.amazon.ebook": "azw3",
	"application/zip": "zip",
	"application/x-rar-compressed": "rar",
	"application/vnd.rar": "rar",
	"text/plain": "txt",
	"text/html":  "html",
	"text/csv":   "csv",
	"text/markdown": "md",
	"audio/mpeg": "mp3",
	"audio/mp3":  "mp3",
	"audio/ogg":  "ogg",
	"audio/wav":  "wav",
	"audio/x-wav": "wav",
	"audio/mp4":  "m4a",
	"audio/aac":  "aac",
	"audio/flac": "flac",
	"video/mp4":  "mp4",
	"video/webm": "webm",
	"video/quicktime": "mov",
	"video/x-msvideo": "avi",
	"video/x-matroska": "mkv",
}

var convertExtMime = map[string]string{
	"jpg":  "image/jpeg",
	"jpeg": "image/jpeg",
	"png":  "image/png",
	"webp": "image/webp",
	"gif":  "image/gif",
	"bmp":  "image/bmp",
	"tiff": "image/tiff",
	"svg":  "image/svg+xml",
	"heic": "image/heic",
	"avif": "image/avif",
	"ico":  "image/x-icon",
	"psd":  "image/vnd.adobe.photoshop",
	"pdf":  "application/pdf",
	"doc":  "application/msword",
	"docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	"xls":  "application/vnd.ms-excel",
	"xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	"ppt":  "application/vnd.ms-powerpoint",
	"pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	"odt":  "application/vnd.oasis.opendocument.text",
	"rtf":  "application/rtf",
	"epub": "application/epub+zip",
	"mobi": "application/x-mobipocket-ebook",
	"azw3": "application/vnd.amazon.ebook",
	"zip":  "application/zip",
	"rar":  "application/vnd.rar",
	"txt":  "text/plain",
	"html": "text/html",
	"csv":  "text/csv",
	"md":   "text/markdown",
	"mp3":  "audio/mpeg",
	"ogg":  "audio/ogg",
	"wav":  "audio/wav",
	"m4a":  "audio/mp4",
	"aac":  "audio/aac",
	"flac": "audio/flac",
	"mp4":  "video/mp4",
	"webm": "video/webm",
	"mov":  "video/quicktime",
	"avi":  "video/x-msvideo",
	"mkv":  "video/x-matroska",
}

func convertExtensionFromMime(mime string) string {
	mime = strings.ToLower(strings.TrimSpace(mime))
	if mime == "" {
		return ""
	}
	mime = strings.Split(mime, ";")[0]
	mime = strings.TrimSpace(mime)
	if ext, ok := convertMimeExt[mime]; ok {
		return ext
	}
	return ""
}

// Regex dipakai berulang di setiap request; dikompilasi sekali di level paket
// supaya tidak diulang tiap panggilan.
var (
	convertExtSuffixRe = regexp.MustCompile(`(?i)\.([a-z0-9]{1,10})$`)
	convertStripExtRe  = regexp.MustCompile(`\.[^.]+$`)
	convertSanitizeRe  = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)
)

func convertExtensionFromName(name string) string {
	m := convertExtSuffixRe.FindStringSubmatch(strings.TrimSpace(name))
	if len(m) < 2 {
		return ""
	}
	return strings.ToLower(m[1])
}

func convertMimeFromExtension(ext string) string {
	ext = strings.ToLower(strings.TrimSpace(ext))
	if m, ok := convertExtMime[ext]; ok {
		return m
	}
	return "application/octet-stream"
}

func convertSanitizeBaseName(name string) string {
	s := strings.TrimSpace(name)
	s = convertStripExtRe.ReplaceAllString(s, "")
	s = convertSanitizeRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "convert"
	}
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}

func convertFormatSize(bytes int) string {
	size := bytes
	if size >= 1024*1024 {
		return fmt.Sprintf("%.1f mb", float64(size)/(1024*1024))
	}
	if size >= 1024 {
		return fmt.Sprintf("%.1f kb", float64(size)/1024)
	}
	return fmt.Sprintf("%d b", size)
}

// ---- Media target resolution ----

type convertMediaTarget struct {
	Mime     string
	FileName string
	Download func(context.Context) ([]byte, error)
}

func convertResolveMediaTarget(mctx *store.MessageContext) *convertMediaTarget {
	if mctx == nil || mctx.RawEvt == nil {
		return nil
	}
	q := store.UnwrapMessage(store.ExtractQuotedMessage(mctx.RawEvt))
	if q != nil {
		if img := q.GetImageMessage(); img != nil {
			return &convertMediaTarget{Mime: img.GetMimetype(), FileName: "", Download: func(ctx context.Context) ([]byte, error) { return mctx.Client.Download(ctx, img) }}
		}
		if doc := q.GetDocumentMessage(); doc != nil {
			return &convertMediaTarget{Mime: doc.GetMimetype(), FileName: doc.GetFileName(), Download: func(ctx context.Context) ([]byte, error) { return mctx.Client.Download(ctx, doc) }}
		}
		if vid := q.GetVideoMessage(); vid != nil {
			return &convertMediaTarget{Mime: vid.GetMimetype(), FileName: "", Download: func(ctx context.Context) ([]byte, error) { return mctx.Client.Download(ctx, vid) }}
		}
		if aud := q.GetAudioMessage(); aud != nil {
			return &convertMediaTarget{Mime: aud.GetMimetype(), FileName: "", Download: func(ctx context.Context) ([]byte, error) { return mctx.Client.Download(ctx, aud) }}
		}
	}
	msg := store.UnwrapMessage(mctx.RawEvt.Message)
	if msg != nil {
		if img := msg.GetImageMessage(); img != nil {
			return &convertMediaTarget{Mime: img.GetMimetype(), FileName: "", Download: func(ctx context.Context) ([]byte, error) { return mctx.Client.Download(ctx, img) }}
		}
		if doc := msg.GetDocumentMessage(); doc != nil {
			return &convertMediaTarget{Mime: doc.GetMimetype(), FileName: doc.GetFileName(), Download: func(ctx context.Context) ([]byte, error) { return mctx.Client.Download(ctx, doc) }}
		}
		if vid := msg.GetVideoMessage(); vid != nil {
			return &convertMediaTarget{Mime: vid.GetMimetype(), FileName: "", Download: func(ctx context.Context) ([]byte, error) { return mctx.Client.Download(ctx, vid) }}
		}
		if aud := msg.GetAudioMessage(); aud != nil {
			return &convertMediaTarget{Mime: aud.GetMimetype(), FileName: "", Download: func(ctx context.Context) ([]byte, error) { return mctx.Client.Download(ctx, aud) }}
		}
	}
	return nil
}

// ---- Targets ----

type convertTarget struct {
	Ext  string
	Name string
}

func convertGetTargets(ctx context.Context, inputExt string) (map[string]any, error) {
	q := url.Values{}
	q.Set("include", "targets")
	q.Set("operation", "convert")
	q.Set("input_format", inputExt)
	q.Set("lang", "en")
	data, err := convertAPIRequest(ctx, "/query/view/options?"+q.Encode(), http.MethodGet, nil, false)
	if err != nil {
		return nil, err
	}
	targets, _ := data["targets"].(map[string]any)
	if targets == nil {
		targets = map[string]any{}
	}
	return targets, nil
}

func convertUniqueTargets(rows []any) []convertTarget {
	seen := map[string]bool{}
	var out []convertTarget
	for _, r := range rows {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		ext := strings.ToLower(fmt.Sprint(m["ext"]))
		if ext == "" || ext == "<nil>" || seen[ext] {
			continue
		}
		seen[ext] = true
		name := fmt.Sprint(m["name"])
		if name == "" || name == "<nil>" {
			name = strings.ToUpper(ext)
		}
		out = append(out, convertTarget{Ext: ext, Name: name})
	}
	return out
}

func convertTasks(job map[string]any) []map[string]any {
	raw, ok := job["tasks"]
	if !ok {
		return nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(arr))
	for _, t := range arr {
		if m, ok := t.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func convertTaskError(job map[string]any) string {
	for _, t := range convertTasks(job) {
		if t["status"] != "failed" {
			continue
		}
		if em, ok := t["error"].(map[string]any); ok {
			if msg, ok := em["message"].(string); ok && msg != "" {
				return msg
			}
		}
		if rm, ok := t["result"].(map[string]any); ok {
			if msg, ok := rm["message"].(string); ok && msg != "" {
				return msg
			}
		}
		if om, ok := t["output"].(map[string]any); ok {
			if msg, ok := om["message"].(string); ok && msg != "" {
				return msg
			}
		}
		if msg, ok := t["message"].(string); ok && msg != "" {
			return msg
		}
		op := fmt.Sprint(t["operation"])
		if op == "" || op == "<nil>" {
			op = "konversi"
		}
		return "tugas " + op + " gagal"
	}
	return ""
}

func convertWaitForJob(ctx context.Context, jobID string, timeout time.Duration) (map[string]any, error) {
	started := time.Now()
	for time.Since(started) < timeout {
		job, err := convertAPIRequest(ctx, "/process/jobs/"+jobID, http.MethodGet, nil, false)
		if err != nil {
			return nil, err
		}
		tasks := convertTasks(job)
		var exportTask map[string]any
		for _, t := range tasks {
			if t["operation"] == "export/url" {
				exportTask = t
				break
			}
		}
		if job["status"] == "completed" {
			return job, nil
		}
		if exportTask != nil && exportTask["status"] == "completed" {
			return job, nil
		}
		if errStr := convertTaskError(job); errStr != "" {
			return nil, fmt.Errorf("%s", errStr)
		}
		status := fmt.Sprint(job["status"])
		if status == "failed" || status == "canceled" || status == "deleted" {
			return nil, fmt.Errorf("status pekerjaan %s", status)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(1500 * time.Millisecond):
		}
	}
	return nil, fmt.Errorf("konversi melebihi batas waktu")
}

func convertFindResultUrl(job map[string]any) string {
	tasks := convertTasks(job)
	var exportTask, convertTask map[string]any
	for _, t := range tasks {
		switch t["operation"] {
		case "export/url":
			exportTask = t
		case "convert":
			convertTask = t
		}
	}
	for _, src := range []map[string]any{exportTask, convertTask} {
		if src == nil {
			continue
		}
		for _, key := range []string{"result", "output"} {
			if m, ok := src[key].(map[string]any); ok {
				if u, ok := m["url"].(string); ok && u != "" {
					return u
				}
			}
		}
	}
	return ""
}

func convertUploadImportTask(ctx context.Context, task map[string]any, buffer []byte, fileName string) error {
	form, _ := task["result"].(map[string]any)["form"].(map[string]any)
	if form == nil {
		return fmt.Errorf("tautan unggah tidak tersedia")
	}
	u, _ := form["url"].(string)
	if u == "" {
		return fmt.Errorf("tautan unggah tidak tersedia")
	}
	params, _ := form["parameters"].(map[string]any)

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	for k, v := range params {
		_ = writer.WriteField(k, fmt.Sprint(v))
	}
	part, err := writer.CreateFormFile("file", fileName)
	if err != nil {
		return err
	}
	if _, err := part.Write(buffer); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("User-Agent", convertUserAgent)
	client := convertClientUpload
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unggah gagal (%d): %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

func convertConvertFile(ctx context.Context, buffer []byte, fileName, inputExt, outputExt, outputName string) ([]byte, error) {
	job, err := convertAPIRequest(ctx, "/process/jobs", http.MethodPost, map[string]any{
		"tasks": map[string]any{
			"import": map[string]any{"operation": "import/upload"},
			"convert": map[string]any{
				"operation":     "convert",
				"input":        "import",
				"input_format": inputExt,
				"output_format": outputExt,
			},
			"export": map[string]any{
				"operation": "export/url",
				"input":     "convert",
				"filename":  outputName,
			},
		},
	}, true)
	if err != nil {
		return nil, err
	}
	jobID := fmt.Sprint(job["id"])
	if jobID == "" || jobID == "<nil>" {
		return nil, fmt.Errorf("id pekerjaan tidak tersedia")
	}

	var importTask map[string]any
	for _, t := range convertTasks(job) {
		if t["operation"] == "import/upload" {
			importTask = t
			break
		}
	}
	if importTask == nil {
		return nil, fmt.Errorf("tugas unggah tidak ditemukan")
	}
	if err := convertUploadImportTask(ctx, importTask, buffer, fileName); err != nil {
		return nil, err
	}

	finished, err := convertWaitForJob(ctx, jobID, 150*time.Second)
	if err != nil {
		return nil, err
	}
	u := convertFindResultUrl(finished)
	if u == "" {
		return nil, fmt.Errorf("tautan hasil konversi tidak tersedia")
	}

	dlCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(dlCtx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", convertUserAgent)
	resp, err := convertClientUpload.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("gagal mengunduh hasil (%d)", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("hasil konversi kosong")
	}
	return data, nil
}

// ---- Pending store ----

type convertPending struct {
	ID        string
	Path      string
	FileName  string
	InputExt  string
	Sender    types.JID
	SenderAlt types.JID
	Chat      types.JID
	Created   time.Time
}

var (
	convertPendingMu  sync.Mutex
	convertPendingMap = map[string]convertPending{}
)

func convertCleanupExpiredLocked() {
	now := time.Now()
	for id, p := range convertPendingMap {
		if now.Sub(p.Created) > convertPendingTTL {
			_ = os.Remove(p.Path)
			delete(convertPendingMap, id)
		}
	}
}

func convertStorePending(p convertPending) {
	convertPendingMu.Lock()
	defer convertPendingMu.Unlock()
	convertCleanupExpiredLocked()
	convertPendingMap[p.ID] = p
}

func convertGetPending(id string) (convertPending, bool) {
	convertPendingMu.Lock()
	defer convertPendingMu.Unlock()
	convertCleanupExpiredLocked()
	p, ok := convertPendingMap[id]
	if !ok {
		return convertPending{}, false
	}
	if time.Since(p.Created) > convertPendingTTL {
		_ = os.Remove(p.Path)
		delete(convertPendingMap, id)
		return convertPending{}, false
	}
	return p, true
}

// convertSenderAlt mengembalikan JID alternatif pengirim (PN↔LID) dari event,
// kalau ada. Dipakai supaya tombol konversi tetap cocok walau DM datang
// sebagai @lid sementara pending tersimpan sebagai @pn (atau sebaliknya).
func convertSenderAlt(mctx *store.MessageContext) types.JID {
	if mctx == nil || mctx.RawEvt == nil {
		return types.JID{}
	}
	return mctx.RawEvt.Info.SenderAlt
}

// convertPendingMatchesSender mencocokkan pengirim lewat user-part, bukan
// string JID mentah (aturan identity: jangan bandingkan JID sebagai string).
func convertPendingMatchesSender(p convertPending, sender types.JID) bool {
	c := identity.Candidate{JID: p.Sender, Alt: p.SenderAlt}
	return c.Matches(sender)
}

// CleanStaleConvertPending menyapu sisa berkas "convert-*.bin" di direktori
// temp bot saat startup. Tanpa ini, berkas pending yang belum sempat
// kedaluwarsa saat proses mati akan menumpuk selamanya: cleanup di
// convertGetPending/convertStorePending hanya jalan kalau ada command
// .convert berikutnya. Dipanggil sekali dari main.go (pola sama dengan
// CleanStaleCallTemp/CleanStaleDownloads).
func CleanStaleConvertPending() {
	tmpBase, err := store.BotTempDir()
	if err != nil {
		return
	}
	entries, err := os.ReadDir(tmpBase)
	if err != nil {
		return
	}
	var removed int
	var freed int64
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "convert-") {
			continue
		}
		p := filepath.Join(tmpBase, e.Name())
		if info, ierr := e.Info(); ierr == nil && !info.IsDir() {
			freed += info.Size()
		}
		if rerr := os.Remove(p); rerr != nil {
			logfmt.Warn("convert: gagal hapus sisa temp %s: %v\n", e.Name(), rerr)
			continue
		}
		removed++
	}
	if removed > 0 {
		logfmt.Plain("convert: %d sisa berkas pending dihapus (%d KB).\n", removed, freed/1024)
	}
}

// ---- Command ----

func RegisterConvert(r *Registry) {
	r.Register(Command{
		Name:        "convert",
		Aliases:     []string{"conv"},
		Description: "Konversi media ke format lain via freeconvert (reply media)",
		Category:    CategoryDokumen,
		Handler:     handleConvert,
	})
}

func handleConvert(ctx context.Context, mctx *store.MessageContext) error {
	target := convertResolveMediaTarget(mctx)
	if target == nil {
		return store.ReplyText(ctx, mctx, "`Media Diperlukan`\nkirim atau balas media terlebih dahulu, lalu ketik .convert")
	}

	mime := strings.ToLower(target.Mime)
	if mime != "" {
		ok := strings.HasPrefix(mime, "image/") ||
			strings.HasPrefix(mime, "application/") ||
			strings.HasPrefix(mime, "text/") ||
			strings.HasPrefix(mime, "audio/") ||
			strings.HasPrefix(mime, "video/")
		if !ok {
			return store.ReplyText(ctx, mctx, "`Format Tidak Didukung`\nkirim gambar, dokumen, audio, atau video.")
		}
	}

	buffer, err := target.Download(ctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, "`Unduhan Gagal`\n"+err.Error())
	}
	if len(buffer) == 0 {
		return store.ReplyText(ctx, mctx, "`Unduhan Gagal`\nmedia sumber kosong atau tidak dapat diunduh.")
	}
	if len(buffer) > convertMaxInputBytes {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("File %.0f MB melebihi batas %d MB.", float64(len(buffer))/(1<<20), convertMaxInputBytes/(1<<20)))
	}

	inputExt := convertExtensionFromMime(target.Mime)
	if inputExt == "" {
		inputExt = convertExtensionFromName(target.FileName)
	}
	if inputExt == "" {
		detected := http.DetectContentType(buffer)
		inputExt = convertExtensionFromMime(detected)
	}
	if inputExt == "" {
		return store.ReplyText(ctx, mctx, "`Format Tidak Dikenali`\ngagal membaca format media sumber.")
	}

	avail, err := convertGetTargets(ctx, inputExt)
	if err != nil {
		return store.ReplyText(ctx, mctx, "`Konversi Gagal`\n"+strings.ToLower(err.Error()))
	}

	// Kumpulkan target dari semua bucket, filter whitelist umum, kecualikan input.
	seen := map[string]bool{}
	var targets []convertTarget
	for _, bucket := range []string{"image", "document", "audio", "video"} {
		arr, ok := avail[bucket].([]any)
		if !ok {
			continue
		}
		allowed := convertCommonExts[bucket]
		for _, t := range convertUniqueTargets(arr) {
			if !allowed[t.Ext] {
				continue
			}
			if t.Ext == inputExt {
				continue
			}
			if seen[t.Ext] {
				continue
			}
			seen[t.Ext] = true
			targets = append(targets, t)
		}
	}

	if len(targets) == 0 {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("`Format Tidak Tersedia`\ntidak ada format tujuan umum untuk sumber %s.", inputExt))
	}

	// Simpan pending.
	id := uuid.NewString()
	path := filepath.Join(store.MustBotTempDir(), "convert-"+id+".bin")
	if err := os.WriteFile(path, buffer, 0600); err != nil {
		return store.ReplyText(ctx, mctx, "`Konversi Gagal`\n"+err.Error())
	}
	fileName := target.FileName
	if fileName == "" {
		fileName = "source." + inputExt
	}
	convertStorePending(convertPending{
		ID:        id,
		Path:      path,
		FileName:  fileName,
		InputExt:  inputExt,
		Sender:    mctx.Sender,
		SenderAlt: convertSenderAlt(mctx),
		Chat:      mctx.Chat,
		Created:   time.Now(),
	})

	// Kirim tombol.
	nf := make([]messaging.NFButton, 0, len(targets))
	for _, t := range targets {
		nf = append(nf, messaging.QuickReply("convert:"+id+":"+t.Ext, t.Ext))
	}
	title := "Convert " + strings.ToUpper(inputExt)
	body := fmt.Sprintf("Pilih format tujuan dari tombol di bawah. Tersedia %d pilihan.", len(targets))
	if err := messaging.SendNativeFlow(ctx, mctx, messaging.NFText{
		Title:  title,
		Body:   body,
		Footer: "Flora Go • Convert",
	}, "convert-menu", "convert: "+inputExt, nf...); err != nil {
		_ = os.Remove(path)
		convertPendingMu.Lock()
		delete(convertPendingMap, id)
		convertPendingMu.Unlock()
		return store.ReplyText(ctx, mctx, "Gagal kirim menu tombol: "+err.Error())
	}
	return nil
}

// ---- Helper kirim dokumen + caption dalam satu bubble ----

func convertSendDocument(ctx context.Context, mctx *store.MessageContext, data []byte, mime, filename, caption string) error {
	uploaded, err := mctx.Client.Upload(ctx, data, whatsmeow.MediaDocument)
	if err != nil {
		return fmt.Errorf("gagal upload dokumen: %v", err)
	}
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, &waE2E.Message{
		DocumentMessage: &waE2E.DocumentMessage{
			URL:           proto.String(uploaded.URL),
			DirectPath:    proto.String(uploaded.DirectPath),
			MediaKey:      uploaded.MediaKey,
			Mimetype:      proto.String(mime),
			FileEncSHA256: uploaded.FileEncSHA256,
			FileSHA256:    uploaded.FileSHA256,
			FileLength:    proto.Uint64(uploaded.FileLength),
			FileName:      proto.String(filename),
			Caption:       proto.String(caption),
		},
	})
	if err != nil {
		return fmt.Errorf("gagal kirim dokumen: %v", err)
	}
	store.LogOutgoing(mctx, "convert-document", filename)
	store.TrackBotMessageID(resp.ID, filename)
	return nil
}

// ---- Button tap handler ----

func CheckConvertButtonTap(client *whatsmeow.Client, evt *events.Message) bool {
	id, ok := nativeFlowTapID(evt)
	if !ok || !strings.HasPrefix(id, "convert:") {
		return false
	}
	rest := strings.TrimPrefix(id, "convert:")
	parts := strings.SplitN(rest, ":", 2)
	if len(parts) != 2 {
		return false
	}
	pendingID := parts[0]
	outputExt := strings.ToLower(strings.TrimSpace(parts[1]))

	pending, ok := convertGetPending(pendingID)
	if !ok {
		ctx := context.Background()
		mctx := &store.MessageContext{Client: client, Chat: evt.Info.Chat, Sender: evt.Info.Sender, RawEvt: evt}
		_ = store.ReplyText(ctx, mctx, "Sesi konversi kedaluwarsa. Ulangi .convert.")
		return true
	}
	if !convertPendingMatchesSender(pending, evt.Info.Sender) || !identity.SameUser(pending.Chat, evt.Info.Chat) {
		return false
	}

	// Konversi bisa memakan waktu (tunggu job maks 150 dtk + unduh hasil),
	// jadi context diberi batas longgar — jangan context.Background() telanjang.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	mctx := &store.MessageContext{Client: client, Chat: evt.Info.Chat, Sender: evt.Info.Sender, RawEvt: evt}

	select {
	case dlSem <- struct{}{}:
	default:
		_ = store.ReplyText(ctx, mctx, "Antrean penuh (maks 2 bersamaan), coba lagi sebentar.")
		return true
	}
	defer func() { <-dlSem }()

	buffer, err := os.ReadFile(pending.Path)
	if err != nil {
		_ = store.ReplyText(ctx, mctx, "`Konversi Gagal`\n"+err.Error())
		return true
	}
	if len(buffer) == 0 {
		_ = store.ReplyText(ctx, mctx, "`Konversi Gagal`\nmedia sumber kosong.")
		return true
	}

	fileName := pending.FileName
	if fileName == "" {
		fileName = "source." + pending.InputExt
	}
	outputName := convertSanitizeBaseName(pending.FileName) + "-convert." + outputExt
	if pending.FileName == "" {
		outputName = "source-convert." + outputExt
	}

	result, err := convertConvertFile(ctx, buffer, fileName, pending.InputExt, outputExt, outputName)
	if err != nil {
		_ = store.ReplyText(ctx, mctx, "`Konversi Gagal`\n"+strings.ToLower(err.Error()))
		return true
	}

	caption := strings.Join([]string{
		"`Konversi Berhasil`",
		fmt.Sprintf("• Sumber : %s", pending.InputExt),
		fmt.Sprintf("• Format : %s", outputExt),
		fmt.Sprintf("• Ukuran : %s", convertFormatSize(len(result))),
	}, "\n")

	mime := convertMimeFromExtension(outputExt)
	if err := convertSendDocument(ctx, mctx, result, mime, outputName, caption); err != nil {
		_ = store.ReplyText(ctx, mctx, "Gagal kirim dokumen: "+err.Error())
		return true
	}
	return true
}
