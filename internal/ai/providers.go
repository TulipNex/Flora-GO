package ai

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"wabot-go/internal/config"
	"wabot-go/internal/store"
)

// AvailableProviders adalah daftar nama provider yang valid untuk command
// ".ai provider <nama>". Ditulis eksplisit (bukan diturunkan dari map lain)
// supaya urutan tampil di pesan bantuan selalu konsisten.
var AvailableProviders = []string{"google", "openrouter", "nexarouter"}

// AvailableModels adalah daftar model yang bisa dipilih per provider lewat
// ".ai model <model>", HARDCODE (bukan fetch live dari API) sesuai
// keputusan desain — gampang dipastikan valid, tidak nambah panggilan API
// hanya untuk menampilkan daftar.
//
// Model default (dipakai kalau user belum pernah set lewat command) tetap
// config.AIModelDefault untuk Google, dan entri pertama di sini untuk
// OpenRouter/NexaRouter.
var AvailableModels = map[string][]string{
	"google": {
		"gemini-3.6-flash",
		"gemini-3.6-pro",
		"gemini-2.5-flash",
	},
	"openrouter": {
		"openai/gpt-4o-mini",
		"openai/gpt-4o",
		"anthropic/claude-3.5-sonnet",
		"deepseek/deepseek-chat",
		"meta-llama/llama-3.3-70b-instruct",
	},
	"nexarouter": {
		"deepseek-v4-flash",
		"deepseek-v4-flash-vision-exp",
		"deepseek-v4-pro",
		"deepseek-v4.1-flash",
		"glm-5.1",
		"glm-5.2",
		"glm-5.3",
		"glm-5.3-flash",
		"gpt-5.6-luna",
		"gpt-5.6-sol",
		"gpt-5.6-terra",
		"hy3",
		"kimi-k2.7-code",
		"kimi-k3",
		"mimo-v2.5",
		"mimo-v2.5-pro",
		"minimax-m3",
		"qwen3.8-27b",
		"qwen3.8-flash",
		"claude-opus-5",
	},
}

// defaultModelFor mengembalikan model default untuk sebuah provider kalau
// belum pernah di-set lewat command ".ai model" (kolom masih string kosong
// di SQLite).
func defaultModelFor(provider string) string {
	switch provider {
	case "google":
		return config.AIModelDefault
	case "openrouter", "nexarouter":
		if models := AvailableModels[provider]; len(models) > 0 {
			return models[0]
		}
	}
	return ""
}

// IsValidProvider mengecek apakah nama provider ada di AvailableProviders.
func IsValidProvider(provider string) bool {
	for _, p := range AvailableProviders {
		if p == provider {
			return true
		}
	}
	return false
}

// IsValidModel mengecek apakah nama model ada di daftar hardcode untuk
// provider tersebut.
func IsValidModel(provider, model string) bool {
	for _, m := range AvailableModels[provider] {
		if m == model {
			return true
		}
	}
	return false
}

// providerSettingsCache adalah cache in-memory dari ai_settings (SQLite),
// supaya tiap giliran chat TIDAK perlu query DB hanya untuk tahu provider
// aktif — sama pola cache-first seperti SessionStore (session.go). Diisi
// sekali saat pertama diakses, diperbarui tiap kali command ".ai provider"
// / ".ai model" berhasil menyimpan ke DB.
var (
	settingsCacheMu sync.Mutex
	settingsCache   *store.AISettings
)

// getSettingsCached mengembalikan cache setting, mengisi dari DB dulu
// kalau cache masih kosong (pertama kali dipanggil sejak bot start).
func getSettingsCached(ctx context.Context) (store.AISettings, error) {
	settingsCacheMu.Lock()
	defer settingsCacheMu.Unlock()

	if settingsCache != nil {
		return *settingsCache, nil
	}

	s, err := store.AI().GetAISettings(ctx)
	if err != nil {
		return store.AISettings{}, err
	}
	settingsCache = &s
	return s, nil
}

// invalidateSettingsCache membuang cache in-memory supaya baca berikutnya
// mengambil ulang dari DB. Dipanggil setelah SetActiveProvider/
// SetModelForProvider berhasil.
func invalidateSettingsCache() {
	settingsCacheMu.Lock()
	settingsCache = nil
	settingsCacheMu.Unlock()
}

// GetActiveProviderName mengembalikan nama provider aktif saat ini
// ("google" / "openrouter"), dibaca dari cache/SQLite. Fail-soft ke
// "google" kalau DB bermasalah — supaya chat tetap bisa jalan dengan
// provider default daripada bot berhenti total gara-gara setting gagal
// dibaca.
func GetActiveProviderName(ctx context.Context) string {
	s, err := getSettingsCached(ctx)
	if err != nil || s.ActiveProvider == "" {
		return "google"
	}
	return s.ActiveProvider
}

// GetActiveModelName mengembalikan nama model aktif untuk provider yang
// SEDANG aktif saat ini. Kalau belum pernah di-set (kolom masih kosong di
// DB), fallback ke defaultModelFor(provider).
func GetActiveModelName(ctx context.Context) string {
	s, err := getSettingsCached(ctx)
	provider := "google"
	if err == nil && s.ActiveProvider != "" {
		provider = s.ActiveProvider
	}

	var model string
	switch provider {
	case "google":
		model = s.ModelGoogle
	case "openrouter":
		model = s.ModelOpenRouter
	case "nexarouter":
		model = s.ModelNexaRouter
	}
	if model == "" {
		model = defaultModelFor(provider)
	}
	return model
}

// GetActiveProvider membangun ProviderAdapter yang SESUAI dengan provider
// & model aktif saat ini (dibaca dari setting global tersimpan). Dipanggil
// oleh plugins/ai.go SETIAP giliran chat, menggantikan variabel
// geminiAdapter global yang statis pada desain sebelum fitur multi-provider.
//
// API key tiap provider tetap dari config (AIApiKey untuk Google,
// OpenRouterAPIKey untuk OpenRouter) — hanya PILIHAN provider & model yang
// dinamis/tersimpan di DB, kredensialnya tetap dari .env seperti biasa.
func GetActiveProvider(ctx context.Context) (ProviderAdapter, error) {
	provider := GetActiveProviderName(ctx)
	model := GetActiveModelName(ctx)

	switch provider {
	case "google":
		if config.AIApiKey == "" {
			return nil, fmt.Errorf("provider google aktif tapi AI_API_KEY kosong di .env")
		}
		return NewGeminiAdapter(config.AIApiKey, model), nil
	case "openrouter":
		if config.OpenRouterAPIKey == "" {
			return nil, fmt.Errorf("provider openrouter aktif tapi OPENROUTER_API_KEY kosong di .env")
		}
		return NewOpenRouterAdapter(config.OpenRouterAPIKey, model), nil
	case "nexarouter":
		if config.NexaRouterAPIKey == "" {
			return nil, fmt.Errorf("provider nexarouter aktif tapi NEXAROUTER_API_KEY kosong di .env")
		}
		return NewNexaRouterAdapter(config.NexaRouterAPIKey, model), nil
	default:
		return nil, fmt.Errorf("provider tidak dikenal: %s", provider)
	}
}

// SetActiveProvider mengganti provider aktif, persist ke SQLite, dan
// memperbarui cache. Validasi nama provider dilakukan di sini supaya
// pemanggil (command handler) tidak perlu duplikasi pengecekan.
func SetActiveProvider(ctx context.Context, provider string) error {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if !IsValidProvider(provider) {
		return fmt.Errorf("provider tidak dikenal: %s", provider)
	}
	if err := store.AI().SetActiveProvider(ctx, provider); err != nil {
		return err
	}
	invalidateSettingsCache()
	return nil
}

// SetActiveModel menyimpan model pilihan untuk provider yang SEDANG aktif
// saat ini, persist ke SQLite, dan memperbarui cache.
func SetActiveModel(ctx context.Context, model string) error {
	provider := GetActiveProviderName(ctx)
	model = strings.TrimSpace(model)
	if !IsValidModel(provider, model) {
		return fmt.Errorf("model %q tidak dikenal untuk provider %s", model, provider)
	}
	if err := store.AI().SetModelForProvider(ctx, provider, model); err != nil {
		return err
	}
	invalidateSettingsCache()
	return nil
}

// FormatProviderList merangkai daftar provider tersedia + penanda provider
// aktif saat ini, dipakai saat user ketik ".ai provider" tanpa argumen.
func FormatProviderList(ctx context.Context) string {
	active := GetActiveProviderName(ctx)
	var sb strings.Builder
	sb.WriteString("Provider tersedia:\n")
	for _, p := range AvailableProviders {
		marker := ""
		if p == active {
			marker = " (aktif)"
		}
		sb.WriteString(fmt.Sprintf("- %s%s\n", p, marker))
	}
	sb.WriteString("\nGanti pakai: .ai provider <nama>")
	return sb.String()
}

// FormatModelList merangkai daftar model tersedia UNTUK PROVIDER AKTIF
// saat ini + penanda model aktif, dipakai saat user ketik ".ai model"
// tanpa argumen.
func FormatModelList(ctx context.Context) string {
	provider := GetActiveProviderName(ctx)
	activeModel := GetActiveModelName(ctx)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Model tersedia untuk provider %s:\n", provider))
	for _, m := range AvailableModels[provider] {
		marker := ""
		if m == activeModel {
			marker = " (aktif)"
		}
		sb.WriteString(fmt.Sprintf("- %s%s\n", m, marker))
	}
	sb.WriteString("\nGanti pakai: .ai model <nama>")
	return sb.String()
}
