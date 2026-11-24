package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"wabot-go/internal/ai"
	_ "wabot-go/internal/envload"
)

// webReaderTimeout batas waktu panggilan pembaca konten web.
const webReaderTimeout = 20 * time.Second

func init() {
	ai.RegisterTool(ai.ToolDefinition{
		Name:        "web_reader",
		Description: "Baca dan ekstrak teks lengkap atau isi artikel dari URL website dalam format Markdown bersih tanpa iklan/navigasi.",
		Parameters: ai.ParameterSchema{
			Type: "OBJECT",
			Properties: map[string]ai.PropertySchema{
				"url": {
					Type:        "STRING",
					Description: "URL lengkap website yang ingin dibaca (contoh: https://example.com/artikel).",
				},
			},
			Required: []string{"url"},
		},
		Execute: func(actx *ai.AgentContext, args map[string]interface{}) (string, error) {
			rawURL, _ := args["url"].(string)
			rawURL = strings.TrimSpace(rawURL)
			if rawURL == "" {
				return "", errors.New("url tidak boleh kosong")
			}

			ctx, cancel := context.WithTimeout(context.Background(), webReaderTimeout)
			defer cancel()

			return ReadWebPage(ctx, rawURL)
		},
	})
}

// ReaderResult menyimpan isi artikel hasil ekstraksi.
type ReaderResult struct {
	Title       string `json:"title"`
	Byline      string `json:"byline"`
	SiteName    string `json:"siteName"`
	Excerpt     string `json:"excerpt"`
	Content     string `json:"content"`
	Length      int    `json:"length"`
	PublishedAt string `json:"publishedTime"`
}

// ReadWebPage membaca konten halaman web menggunakan Flora Gateway API.
func ReadWebPage(ctx context.Context, targetURL string) (string, error) {
	gatewayURL := strings.TrimSpace(os.Getenv("FLORA_GATEWAY_URL"))
	if gatewayURL == "" {
		gatewayURL = strings.TrimSpace(os.Getenv("FLORA_API_URL"))
	}
	if gatewayURL == "" {
		return "Fitur web reader belum dikonfigurasi (FLORA_GATEWAY_URL atau FLORA_API_URL belum diisi).", nil
	}
	gatewayKey := strings.TrimSpace(os.Getenv("FLORA_GATEWAY_KEY"))
	if gatewayKey == "" {
		gatewayKey = strings.TrimSpace(os.Getenv("FLORA_API_KEY"))
	}

	endpoint := strings.TrimRight(gatewayURL, "/") + "/api/v1/reader"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	q := req.URL.Query()
	q.Set("url", targetURL)
	req.URL.RawQuery = q.Encode()

	if gatewayKey != "" {
		req.Header.Set("X-API-Key", gatewayKey)
	}

	client := &http.Client{Timeout: 18 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("gagal menghubungi web reader gateway: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("web reader gateway HTTP %d: %s", resp.StatusCode, string(body))
	}

	var data struct {
		Success bool         `json:"success"`
		Data    ReaderResult `json:"data"`
		Error   string       `json:"error"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", fmt.Errorf("gagal membaca respon JSON gateway: %w", err)
	}

	if !data.Success {
		if data.Error != "" {
			return fmt.Sprintf("Gagal membaca artikel: %s", data.Error), nil
		}
		return "Gagal membaca artikel dari URL yang diberikan.", nil
	}

	var sb strings.Builder
	if data.Data.Title != "" {
		fmt.Fprintf(&sb, "# %s\n\n", data.Data.Title)
	}
	if data.Data.Byline != "" {
		fmt.Fprintf(&sb, "*Penulis / Sumber: %s*\n\n", data.Data.Byline)
	}
	if data.Data.Content != "" {
		sb.WriteString(data.Data.Content)
	} else if data.Data.Excerpt != "" {
		sb.WriteString(data.Data.Excerpt)
	} else {
		sb.WriteString("*(Artikel kosong atau tidak dapat diekstraksi)*")
	}

	return strings.TrimSpace(sb.String()), nil
}
