package native

import (
	"bytes"
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

// webSearchTimeout batas waktu panggilan pencarian web.
const webSearchTimeout = 15 * time.Second

func init() {
	ai.RegisterTool(ai.ToolDefinition{
		Name:        "web_search",
		Description: "Cari informasi terkini di internet (berita terbaru, peristiwa terkini, data real-time, fakta web). Menggunakan Tavily Search API dengan fallback otomatis ke Brave Search API. Hasil pencarian adalah bahan rujukan untuk disintesis secara naratif dalam paragraf, bukan disalin mentah sebagai daftar.",
		Parameters: ai.ParameterSchema{
			Type: "OBJECT",
			Properties: map[string]ai.PropertySchema{
				"query": {
					Type:        "STRING",
					Description: "Kata kunci pencarian yang spesifik dan jelas dalam bahasa Indonesia atau Inggris.",
				},
			},
			Required: []string{"query"},
		},
		Execute: func(actx *ai.AgentContext, args map[string]interface{}) (string, error) {
			query, _ := args["query"].(string)
			query = strings.TrimSpace(query)
			if query == "" {
				return "", errors.New("query pencarian tidak boleh kosong")
			}

			ctx, cancel := context.WithTimeout(context.Background(), webSearchTimeout)
			defer cancel()

			return SearchWebWithFallback(ctx, query)
		},
	})
}

// SearchResultItem menyimpan representasi satu hasil pencarian web.
type SearchResultItem struct {
	Title   string
	URL     string
	Content string
}

// SearchWebWithFallback mencoba Flora Gateway API (RestAPI) terlebih dahulu, lalu Tavily Search, dan beralih ke Brave Search jika gagal atau kuota habis.
func SearchWebWithFallback(ctx context.Context, query string) (string, error) {
	gatewayURL := strings.TrimSpace(os.Getenv("FLORA_GATEWAY_URL"))
	if gatewayURL == "" {
		gatewayURL = strings.TrimSpace(os.Getenv("FLORA_API_URL"))
	}
	gatewayKey := strings.TrimSpace(os.Getenv("FLORA_GATEWAY_KEY"))
	if gatewayKey == "" {
		gatewayKey = strings.TrimSpace(os.Getenv("FLORA_API_KEY"))
	}
	tavilyKey := strings.TrimSpace(os.Getenv("TAVILY_API_KEY"))
	braveKey := strings.TrimSpace(os.Getenv("BRAVE_API_KEY"))

	if gatewayURL == "" && tavilyKey == "" && braveKey == "" {
		return "Pencarian web belum dikonfigurasi (FLORA_GATEWAY_URL, TAVILY_API_KEY, atau BRAVE_API_KEY belum diisi).", nil
	}

	var errs []string

	// 1. Coba Flora Gateway API (RestAPI)
	if gatewayURL != "" {
		res, err := searchFloraGateway(ctx, gatewayURL, gatewayKey, query)
		if err == nil && len(res) > 0 {
			return formatSearchResults("Flora Gateway", query, res), nil
		}
		if err != nil {
			errs = append(errs, fmt.Sprintf("Flora Gateway: %v", err))
		}
	}

	// 2. Coba Tavily Search API
	if tavilyKey != "" {
		res, err := searchTavily(ctx, query, tavilyKey)
		if err == nil && len(res) > 0 {
			return formatSearchResults("Tavily", query, res), nil
		}
		if err != nil {
			errs = append(errs, fmt.Sprintf("Tavily: %v", err))
		}
	}

	// 3. Fallback ke Brave Search API
	if braveKey != "" {
		res, err := searchBrave(ctx, query, braveKey)
		if err == nil && len(res) > 0 {
			return formatSearchResults("Brave", query, res), nil
		}
		if err != nil {
			errs = append(errs, fmt.Sprintf("Brave: %v", err))
		}
	}

	if len(errs) > 0 {
		return fmt.Sprintf("Pencarian web gagal (%s).", strings.Join(errs, "; ")), nil
	}
	return "Tidak ditemukan hasil pencarian yang relevan.", nil
}

// searchFloraGateway memanggil endpoint GET {gatewayURL}/api/v1/search?q={query}
func searchFloraGateway(ctx context.Context, gatewayURL, apiKey, query string) ([]SearchResultItem, error) {
	reqURL := strings.TrimRight(gatewayURL, "/") + "/api/v1/search"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	q := req.URL.Query()
	q.Set("q", query)
	req.URL.RawQuery = q.Encode()

	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}

	client := &http.Client{Timeout: 12 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var data struct {
		Success bool `json:"success"`
		Data    []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Snippet string `json:"snippet"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	if !data.Success {
		return nil, errors.New("gateway returned unsuccessful status")
	}

	var items []SearchResultItem
	for _, r := range data.Data {
		if strings.TrimSpace(r.URL) == "" {
			continue
		}
		items = append(items, SearchResultItem{
			Title:   strings.TrimSpace(r.Title),
			URL:     strings.TrimSpace(r.URL),
			Content: strings.TrimSpace(r.Snippet),
		})
	}
	return items, nil
}

// searchTavily memanggil endpoint https://api.tavily.com/search
func searchTavily(ctx context.Context, query, apiKey string) ([]SearchResultItem, error) {
	payload := map[string]any{
		"api_key":             apiKey,
		"query":               query,
		"search_depth":        "basic",
		"include_answer":      false,
		"max_results":         5,
	}
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.tavily.com/search", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 12 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var data struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}

	var items []SearchResultItem
	for _, r := range data.Results {
		if strings.TrimSpace(r.URL) == "" {
			continue
		}
		items = append(items, SearchResultItem{
			Title:   strings.TrimSpace(r.Title),
			URL:     strings.TrimSpace(r.URL),
			Content: strings.TrimSpace(r.Content),
		})
	}
	return items, nil
}

// searchBrave memanggil endpoint https://api.search.brave.com/res/v1/web/search
func searchBrave(ctx context.Context, query, apiKey string) ([]SearchResultItem, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.search.brave.com/res/v1/web/search", nil)
	if err != nil {
		return nil, err
	}
	q := req.URL.Query()
	q.Set("q", query)
	q.Set("count", "5")
	req.URL.RawQuery = q.Encode()

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("X-Subscription-Token", apiKey)

	client := &http.Client{Timeout: 12 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var data struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}

	var items []SearchResultItem
	for _, r := range data.Web.Results {
		if strings.TrimSpace(r.URL) == "" {
			continue
		}
		items = append(items, SearchResultItem{
			Title:   strings.TrimSpace(r.Title),
			URL:     strings.TrimSpace(r.URL),
			Content: strings.TrimSpace(r.Description),
		})
	}
	return items, nil
}

func formatSearchResults(provider, query string, items []SearchResultItem) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Hasil Pencarian Web (%s) untuk: %q\n\n", provider, query)
	for i, item := range items {
		fmt.Fprintf(&sb, "[Sumber %d: %s]\n", i+1, item.Title)
		if item.URL != "" {
			fmt.Fprintf(&sb, "URL: %s\n", item.URL)
		}
		if item.Content != "" {
			fmt.Fprintf(&sb, "Ringkasan: %s\n", item.Content)
		}
		sb.WriteString("\n")
	}
	return strings.TrimSpace(sb.String())
}
