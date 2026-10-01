package ai

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"wabot-go/internal/config"
	"wabot-go/internal/store"
)

// AvailableProviders adalah daftar nama provider yang valid untuk command
// ".agent provider <nama>". Ditulis eksplisit (bukan diturunkan dari map lain)
// supaya urutan tampil di pesan bantuan selalu konsisten.
var AvailableProviders = []string{"google", "openrouter", "nexarouter", "ciora"}

// AvailableModels adalah daftar model yang bisa dipilih per provider lewat
// ".agent model <model>", HARDCODE (bukan fetch live dari API) sesuai
// keputusan desain — gampang dipastikan valid, tidak nambah panggilan API
// hanya untuk menampilkan daftar.
//
// Model default (dipakai kalau user belum pernah set lewat command) tetap
// config.AIModelDefault untuk Google, dan entri pertama di sini untuk
// OpenRouter/NexaRouter/Ciora.
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
	"ciora": {
		"deepseek/deepseek-v4.1-flash",
		"poolside/laguna-s-2.1-free",
		"z-ai/glm-5.3-flash",
		"qwen/qwen3.8-27b",
		"dots-studio/dots-3-note-preview",
		"qwen/qwen3.8-flash",
		"ciora-ai-free",
		"cohere/north-mini-code-free",
		"minimax/minimax-m3",
		"ciora-vision-free",
		"ciora-coding-free",
		"ciora/gemini-3.6-flash",
		"go/gemini-3.8-flash",
		"ciora/gemini-3.7-flash",
		"koboillm/gemini-2.5-flash-lite",
		"op/gpt-5.6-luna",
		"go/gemini-3.1-pro",
		"ciora/gemini-3.1-pro",
		"op/gpt-5.6-terra",
		"an/claude-sonnet-4-6",
		"an/claude-sonnet-5",
		"an/claude-opus-5",
		"op/gpt-6-astra",
		"koboillm/gemini-3.6-flash",
		"koboillm/gemini-3.8-flash",
		"an/claude-opus-4-8",
		"anthropic/claude-sonnet-4.6",
	},
}

// defaultModelFor mengembalikan model default untuk sebuah provider kalau
// belum pernah di-set lewat command ".agent model" (kolom masih string kosong
// di SQLite).
func defaultModelFor(provider string) string {
	switch provider {
	case "google":
		return config.AIModelDefault
	case "openrouter", "nexarouter", "ciora":
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
// sekali saat pertama diakses, diperbarui tiap kali command ".agent provider"
// / ".agent model" berhasil menyimpan ke DB.
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
	case "ciora":
		model = s.ModelCiora
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

	// API key di-TrimSpace dulu karena nilai dari environment sering membawa
	// whitespace tak terlihat (spasi akhir, newline, atau \r dari file .env
	// berformat CRLF Windows) — karakter itu ILEGAL di HTTP header dan membuat
	// request gagal dengan "net/http: invalid header field value" sebelum
	// sempat terkirim ke provider.
	switch provider {
	case "google":
		if strings.TrimSpace(config.AIApiKey) == "" {
			return nil, fmt.Errorf("provider google aktif tapi AI_API_KEY kosong di .env")
		}
		return NewGeminiAdapter(strings.TrimSpace(config.AIApiKey), model), nil
	case "openrouter":
		if strings.TrimSpace(config.OpenRouterAPIKey) == "" {
			return nil, fmt.Errorf("provider openrouter aktif tapi OPENROUTER_API_KEY kosong di .env")
		}
		return NewOpenRouterAdapter(strings.TrimSpace(config.OpenRouterAPIKey), model), nil
	case "nexarouter":
		if strings.TrimSpace(config.NexaRouterAPIKey) == "" {
			return nil, fmt.Errorf("provider nexarouter aktif tapi NEXAROUTER_API_KEY kosong di .env")
		}
		return NewNexaRouterAdapter(strings.TrimSpace(config.NexaRouterAPIKey), model), nil
	case "ciora":
		if strings.TrimSpace(config.CioraAPIKey) == "" {
			return nil, fmt.Errorf("provider ciora aktif tapi CIORA_API_KEY kosong di .env")
		}
		return NewCioraAdapter(strings.TrimSpace(config.CioraAPIKey), model), nil
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

// IsVoiceReplyOn mengembalikan status mode suara Flora (command
// `.agent voice on/off`), dibaca dari cache/SQLite. Fail-soft ke false
// (teks) kalau DB bermasalah — supaya chat tetap jalan normal, bukan
// gagal total gara-gara setting tidak terbaca.
func IsVoiceReplyOn(ctx context.Context) bool {
	s, err := getSettingsCached(ctx)
	if err != nil {
		return false
	}
	return s.VoiceReply
}

// SetVoiceReply menyimpan mode suara Flora, persist ke SQLite, dan
// memperbarui cache — pola sama seperti SetActiveProvider.
func SetVoiceReply(ctx context.Context, on bool) error {
	if err := store.AI().SetVoiceReply(ctx, on); err != nil {
		return err
	}
	invalidateSettingsCache()
	return nil
}

// FormatProviderList merangkai daftar provider tersedia + penanda provider
// aktif saat ini, dipakai saat user ketik ".agent provider" tanpa argumen.
// Bernomor urut (1-based mengikuti AvailableProviders) supaya user bisa
// memilih pakai nama maupun nomor (".agent provider 2").
func FormatProviderList(ctx context.Context) string {
	active := GetActiveProviderName(ctx)
	var sb strings.Builder
	sb.WriteString("Provider tersedia:\n")
	for i, p := range AvailableProviders {
		marker := ""
		if p == active {
			marker = " (aktif)"
		}
		sb.WriteString(fmt.Sprintf("%d. %s%s\n", i+1, p, marker))
	}
	sb.WriteString("\nGanti pakai: .agent provider <nama/nomor>")
	return sb.String()
}

// FormatModelList merangkai daftar model tersedia UNTUK PROVIDER AKTIF
// saat ini + penanda model aktif, dipakai saat user ketik ".agent model"
// tanpa argumen. Bernomor urut supaya user bisa memilih pakai nama maupun
// nomor (".agent model 4").
func FormatModelList(ctx context.Context) string {
	provider := GetActiveProviderName(ctx)
	activeModel := GetActiveModelName(ctx)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Model tersedia untuk provider %s:\n", provider))
	for i, m := range AvailableModels[provider] {
		marker := ""
		if m == activeModel {
			marker = " (aktif)"
		}
		sb.WriteString(fmt.Sprintf("%d. %s%s\n", i+1, m, marker))
	}
	sb.WriteString("\nGanti pakai: .agent model <nama/nomor>")
	return sb.String()
}

// ResolveProviderRef mengubah referensi user (nama atau nomor urut 1-based
// sesuai FormatProviderList) jadi nama provider. Nomor di luar rentang
// dikembalikan sebagai nama apa adanya supaya error validasinya tetap
// informatif di SetActiveProvider.
func ResolveProviderRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if n, err := strconv.Atoi(ref); err == nil && n >= 1 && n <= len(AvailableProviders) {
		return AvailableProviders[n-1]
	}
	return ref
}

// ResolveModelRef mengubah referensi user (nama atau nomor urut 1-based
// sesuai FormatModelList untuk provider tersebut) jadi nama model.
func ResolveModelRef(provider, ref string) string {
	ref = strings.TrimSpace(ref)
	if models := AvailableModels[provider]; len(models) > 0 {
		if n, err := strconv.Atoi(ref); err == nil && n >= 1 && n <= len(models) {
			return models[n-1]
		}
	}
	return ref
}
