package deepchat

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	baseURL = "https://uebueqfehjrruiwrbmgs.supabase.co"
	anonKey = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJpc3MiOiJzdXBhYmFzZSIsInJlZiI6InVlYnVlcWZlaGpycnVpd3JibWdzIiwicm9sZSI6ImFub24iLCJpYXQiOjE3NzI4NTI0NDYsImV4cCI6MjA4ODQyODQ0Nn0.pC43b2gMMx44q7_SUfJDM_ceLaYWeMzKI4QvMwz_AxU"

	userAgent = "Ktor client"
)

// Session menyimpan kredensial Supabase aktif untuk DeepChat.
type Session struct {
	AccessToken   string `json:"access_token"`
	AnonUserID    string `json:"anon_user_id"`
	SigningSecret string `json:"signing_secret"`
}

// Client menangani request HTTP dan penandatanganan HMAC ke DeepChat Supabase.
type Client struct {
	httpClient *http.Client
	mu         sync.Mutex
	session    *Session
}

// New membuat instance DeepChat Client baru.
func New() *Client {
	return &Client{
		httpClient: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

func (c *Client) buildSignedHeaders(endpoint string, bodyBytes []byte, sess *Session) http.Header {
	timestampSec := time.Now().Unix()
	nonce := uuid.New().String()
	idempotencyKey := uuid.New().String()

	sum := sha256.Sum256(bodyBytes)
	bodySha256 := hex.EncodeToString(sum[:])

	msg := fmt.Sprintf("POST\n%s\n%d\n%s\n%s", endpoint, timestampSec, nonce, bodySha256)
	secretBytes, _ := hex.DecodeString(sess.SigningSecret)
	mac := hmac.New(sha256.New, secretBytes)
	mac.Write([]byte(msg))
	sig := hex.EncodeToString(mac.Sum(nil))

	h := make(http.Header)
	h.Set("Content-Type", "application/json")
	h.Set("apikey", anonKey)
	h.Set("Authorization", "Bearer "+sess.AccessToken)
	h.Set("X-Request-Timestamp", fmt.Sprintf("%d", timestampSec))
	h.Set("X-Idempotency-Key", idempotencyKey)
	h.Set("X-Request-Signature", sig)
	h.Set("X-Request-Nonce", nonce)
	h.Set("User-Agent", userAgent)
	return h
}

func (c *Client) bootstrap(ctx context.Context) (*Session, error) {
	// 1. Anonymous signup
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/auth/v1/signup", bytes.NewReader([]byte("{}")))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", anonKey)
	req.Header.Set("Authorization", "Bearer "+anonKey)
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("signup network error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("signup failed (HTTP %d): %s", resp.StatusCode, string(body))
	}

	var signupData struct {
		AccessToken string `json:"access_token"`
		User        struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&signupData); err != nil {
		return nil, fmt.Errorf("decode signup resp: %w", err)
	}

	accessToken := signupData.AccessToken
	anonUserID := signupData.User.ID

	// 2. Create User
	randomBytes := make([]byte, 8)
	_, _ = rand.Read(randomBytes)
	randomAndroidID := hex.EncodeToString(randomBytes)
	installSum := sha256.Sum256([]byte(randomAndroidID))
	installIDHash := hex.EncodeToString(installSum[:])

	createUserPayload, _ := json.Marshal(map[string]string{
		"anonUserId":    anonUserID,
		"installIdHash": installIDHash,
	})

	createReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/functions/v1/create-user", bytes.NewReader(createUserPayload))
	if err != nil {
		return nil, err
	}
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("apikey", anonKey)
	createReq.Header.Set("Authorization", "Bearer "+accessToken)
	createReq.Header.Set("User-Agent", userAgent)

	createResp, err := c.httpClient.Do(createReq)
	if err != nil {
		return nil, fmt.Errorf("create-user network error: %w", err)
	}
	defer createResp.Body.Close()

	if createResp.StatusCode < 200 || createResp.StatusCode >= 300 {
		body, _ := io.ReadAll(createResp.Body)
		return nil, fmt.Errorf("create-user failed (HTTP %d): %s", createResp.StatusCode, string(body))
	}

	var createData struct {
		SigningSecret string `json:"signingSecret"`
		Credits       struct {
			Balance int `json:"balance"`
		} `json:"credits"`
	}
	if err := json.NewDecoder(createResp.Body).Decode(&createData); err != nil {
		return nil, fmt.Errorf("decode create-user resp: %w", err)
	}

	sess := &Session{
		AccessToken:   accessToken,
		AnonUserID:    anonUserID,
		SigningSecret: createData.SigningSecret,
	}

	// 3. Claim initial ad-reward if balance < 1
	if createData.Credits.Balance < 1 {
		_ = c.claimRewardWithSession(ctx, sess)
	}

	c.session = sess
	return sess, nil
}

func (c *Client) claimRewardWithSession(ctx context.Context, sess *Session) error {
	body := []byte("{}")
	headers := c.buildSignedHeaders("claim-ad-reward", body, sess)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/functions/v1/claim-ad-reward", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header = headers

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

func (c *Client) requestEndpoint(ctx context.Context, endpoint string, payload any, isRetry bool) ([]byte, error) {
	c.mu.Lock()
	sess := c.session
	if sess == nil {
		var err error
		sess, err = c.bootstrap(ctx)
		if err != nil {
			c.mu.Unlock()
			return nil, err
		}
	}
	c.mu.Unlock()

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}

	headers := c.buildSignedHeaders(endpoint, bodyBytes, sess)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/functions/v1/"+endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	req.Header = headers

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("endpoint %s request failed: %w", endpoint, err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	// Handle 401 Unauthorized / 402 Insufficient credits
	if (resp.StatusCode == 401 || resp.StatusCode == 402) && !isRetry {
		c.mu.Lock()
		// Coba claim ad reward dulu sampai 3 kali
		claimed := false
		for i := 0; i < 3; i++ {
			if err := c.claimRewardWithSession(ctx, sess); err == nil {
				claimed = true
				break
			}
		}
		// Kalau tetap gagal claim, re-bootstrap session baru
		if !claimed {
			_, _ = c.bootstrap(ctx)
		}
		c.mu.Unlock()
		return c.requestEndpoint(ctx, endpoint, payload, true)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("[%s] HTTP %d: %s", endpoint, resp.StatusCode, string(respBytes))
	}

	return respBytes, nil
}

// ConversationTurn merepresentasikan satu pesan dalam riwayat chat DeepChat.
type ConversationTurn struct {
	Role    string `json:"role"` // "user" atau "assistant"
	Content string `json:"content"`
}

// ChatOptions adalah parameter tambahan untuk chat DeepChat.
type ChatOptions struct {
	ProviderID          string             `json:"providerId,omitempty"`
	ModelID             string             `json:"modelId,omitempty"`
	SystemPrompt        string             `json:"systemPrompt,omitempty"`
	ConversationHistory []ConversationTurn `json:"conversationHistory,omitempty"`
	DeepThink           bool               `json:"deepThink,omitempty"`
	DeepResearch        bool               `json:"deepResearch,omitempty"`
	WriteCode           bool               `json:"writeCode,omitempty"`
	ImageBase64         string             `json:"imageBase64,omitempty"`
	ImageMimeType       string             `json:"imageMimeType,omitempty"`
}

// ChatResult adalah balasan dari API send-message.
type ChatResult struct {
	Reply            string `json:"reply"`
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	CreditsUsed      int    `json:"creditsUsed"`
	CreditsRemaining int    `json:"creditsRemaining"`
}

// Chat mengirim pesan ke DeepChat.
func (c *Client) Chat(ctx context.Context, message string, opts *ChatOptions) (*ChatResult, error) {
	if message == "" {
		return nil, errors.New("pesan tidak boleh kosong")
	}

	payload := map[string]any{
		"message":    message,
		"providerId": "chatgpt",
		"modelId":    "gpt-4o-mini",
	}

	if opts != nil {
		if opts.ProviderID != "" {
			payload["providerId"] = opts.ProviderID
		}
		if opts.ModelID != "" {
			payload["modelId"] = opts.ModelID
		}
		if opts.SystemPrompt != "" {
			payload["systemPrompt"] = opts.SystemPrompt
		}
		if len(opts.ConversationHistory) > 0 {
			payload["conversationHistory"] = opts.ConversationHistory
		}
		if opts.DeepThink {
			payload["deepThink"] = true
		}
		if opts.DeepResearch {
			payload["deepResearch"] = true
		}
		if opts.WriteCode {
			payload["writeCode"] = true
		}
		if opts.ImageBase64 != "" {
			payload["imageBase64"] = opts.ImageBase64
			if opts.ImageMimeType != "" {
				payload["imageMimeType"] = opts.ImageMimeType
			} else {
				payload["imageMimeType"] = "image/jpeg"
			}
		}
	}

	dataBytes, err := c.requestEndpoint(ctx, "send-message", payload, false)
	if err != nil {
		return nil, err
	}

	var res ChatResult
	if err := json.Unmarshal(dataBytes, &res); err != nil {
		return nil, fmt.Errorf("decode chat response: %w", err)
	}
	return &res, nil
}

// HumanizeResult hasil dari humanize-text.
type HumanizeResult struct {
	HumanizedText    string `json:"humanizedText"`
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	Intensity        string `json:"intensity"`
	CreditsUsed      int    `json:"creditsUsed"`
	CreditsRemaining int    `json:"creditsRemaining"`
}

// Humanize mengubah teks agar terdengar alami / lolos deteksi AI.
func (c *Client) Humanize(ctx context.Context, text, intensity string) (*HumanizeResult, error) {
	if text == "" {
		return nil, errors.New("teks tidak boleh kosong")
	}
	if intensity == "" {
		intensity = "medium"
	}

	payload := map[string]any{
		"text":       text,
		"providerId": "chatgpt",
		"modelId":    "gpt-4o-mini",
		"intensity":  intensity,
	}

	dataBytes, err := c.requestEndpoint(ctx, "humanize-text", payload, false)
	if err != nil {
		return nil, err
	}

	var res HumanizeResult
	if err := json.Unmarshal(dataBytes, &res); err != nil {
		return nil, fmt.Errorf("decode humanize response: %w", err)
	}
	return &res, nil
}

// OptimizePromptResult hasil dari optimize-prompt.
type OptimizePromptResult struct {
	OptimizedPrompt  string `json:"optimizedPrompt"`
	CreditsUsed      int    `json:"creditsUsed"`
	CreditsRemaining int    `json:"creditsRemaining"`
}

// OptimizePrompt mengoptimasi instruksi / prompt untuk AI.
func (c *Client) OptimizePrompt(ctx context.Context, prompt string) (*OptimizePromptResult, error) {
	if prompt == "" {
		return nil, errors.New("prompt tidak boleh kosong")
	}

	payload := map[string]any{
		"prompt":  prompt,
		"context": "generic",
		"mode":    "optimize",
	}

	dataBytes, err := c.requestEndpoint(ctx, "optimize-prompt", payload, false)
	if err != nil {
		return nil, err
	}

	var res OptimizePromptResult
	if err := json.Unmarshal(dataBytes, &res); err != nil {
		return nil, fmt.Errorf("decode optimize response: %w", err)
	}
	return &res, nil
}

// ImageItem gambar hasil dari generate / edit.
type ImageItem struct {
	URL string `json:"url"`
}

// GenerateImageResult hasil dari generate-image.
type GenerateImageResult struct {
	Images           []ImageItem `json:"images"`
	Provider         string      `json:"provider"`
	Model            string      `json:"model"`
	CreditsUsed      int         `json:"creditsUsed"`
	CreditsRemaining int         `json:"creditsRemaining"`
}

// GenerateImageOptions opsi generate gambar.
type GenerateImageOptions struct {
	ProviderID           string `json:"providerId,omitempty"`
	ModelID              string `json:"modelId,omitempty"`
	Size                 string `json:"size,omitempty"`
	Quality              string `json:"quality,omitempty"`
	N                    int    `json:"n,omitempty"`
	ReferenceImageBase64 string `json:"referenceImageBase64,omitempty"`
}

// GenerateImage membuat gambar dari prompt (default Flux).
func (c *Client) GenerateImage(ctx context.Context, prompt string, opts *GenerateImageOptions) (*GenerateImageResult, error) {
	if prompt == "" {
		return nil, errors.New("prompt tidak boleh kosong")
	}

	payload := map[string]any{
		"prompt":     prompt,
		"providerId": "flux",
		"modelId":    "flux/schnell",
		"size":       "1024x1024",
		"quality":    "standard",
		"n":          1,
	}

	if opts != nil {
		if opts.ProviderID != "" {
			payload["providerId"] = opts.ProviderID
		}
		if opts.ModelID != "" {
			payload["modelId"] = opts.ModelID
		}
		if opts.Size != "" {
			payload["size"] = opts.Size
		}
		if opts.Quality != "" {
			payload["quality"] = opts.Quality
		}
		if opts.N > 0 {
			payload["n"] = opts.N
		}
		if opts.ReferenceImageBase64 != "" {
			payload["referenceImageBase64"] = opts.ReferenceImageBase64
		}
	}

	dataBytes, err := c.requestEndpoint(ctx, "generate-image", payload, false)
	if err != nil {
		return nil, err
	}

	var res GenerateImageResult
	if err := json.Unmarshal(dataBytes, &res); err != nil {
		return nil, fmt.Errorf("decode generate-image response: %w", err)
	}
	return &res, nil
}

// EditImageOptions opsi edit / inpaint gambar.
type EditImageOptions struct {
	MaskBase64 string `json:"maskBase64,omitempty"`
	Size       string `json:"size,omitempty"`
}

// EditImage memodifikasi gambar dengan mask & prompt.
func (c *Client) EditImage(ctx context.Context, prompt, imgBase64 string, opts *EditImageOptions) (*GenerateImageResult, error) {
	if prompt == "" {
		return nil, errors.New("prompt tidak boleh kosong")
	}
	if imgBase64 == "" {
		return nil, errors.New("gambar tidak boleh kosong")
	}

	maskBase64 := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAAAAAA6fptVAAAACklEQVR4nGNiAAAABgADNjd8qAAAAABJRU5ErkJggg=="
	size := "1024x1024"
	if opts != nil {
		if opts.MaskBase64 != "" {
			maskBase64 = opts.MaskBase64
		}
		if opts.Size != "" {
			size = opts.Size
		}
	}

	payload := map[string]any{
		"prompt":       prompt,
		"imageBase64":  imgBase64,
		"maskBase64":   maskBase64,
		"size":         size,
		"n":            1,
		"outputFormat": "jpeg",
	}

	dataBytes, err := c.requestEndpoint(ctx, "edit-image", payload, false)
	if err != nil {
		return nil, err
	}

	var res GenerateImageResult
	if err := json.Unmarshal(dataBytes, &res); err != nil {
		return nil, fmt.Errorf("decode edit-image response: %w", err)
	}
	return &res, nil
}

// FaceSwapResult hasil swap wajah.
type FaceSwapResult struct {
	Original         string `json:"original"`
	Variant          string `json:"variant"`
	CreditsUsed      int    `json:"creditsUsed"`
	CreditsRemaining int    `json:"creditsRemaining"`
}

// FaceSwap menukar wajah dari source image dengan face image.
func (c *Client) FaceSwap(ctx context.Context, sourceBase64, faceBase64 string) (*FaceSwapResult, error) {
	if sourceBase64 == "" || faceBase64 == "" {
		return nil, errors.New("source image dan face image wajib ada")
	}

	payload := map[string]any{
		"sourceImageData": sourceBase64,
		"sourceFormat":    "jpeg",
		"faceImageData":   faceBase64,
		"faceFormat":      "jpeg",
		"generateVariant": false,
		"replaceAll":      false,
		"providerId":      "chatgpt",
		"modelId":         "gpt-image-1",
	}

	dataBytes, err := c.requestEndpoint(ctx, "face-swap", payload, false)
	if err != nil {
		return nil, err
	}

	var res FaceSwapResult
	if err := json.Unmarshal(dataBytes, &res); err != nil {
		return nil, fmt.Errorf("decode face-swap response: %w", err)
	}
	return &res, nil
}
