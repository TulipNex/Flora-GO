package ai

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"wabot-go/internal/store"
)

// ProviderInfo adalah satu entri registry provider runtime (nama +
// endpoint chat-completions penuh). Semua provider wajib OpenAI-compatible.
type ProviderInfo struct {
	Name     string
	Endpoint string
}

// seedProviders/seedModels adalah data awal registry (di-seed ke SQLite
// saat InitAISettingsDB; pilihan owner via command tidak pernah ditimpa).
// Dipakai juga sebagai fallback bila DB tak terbaca — bot tetap bisa chat
// dengan provider utama daripada mati total.
//
// Urutan = urutan tampil + default = entri pertama tiap daftar.
var seedProviders = []ProviderInfo{
	{Name: "ciora", Endpoint: "https://ciora.id/v1/chat/completions"},
	{Name: "openagentic", Endpoint: "https://aimurah.my.id/api/v1/chat/completions"},
}

var seedModels = map[string][]string{
	"ciora": {
		"ciora-ai-free",
		"ciora-coding-free",
		"ciora-vision-free",
		"cohere/north-mini-code-free",
		"poolside/laguna-s-2.1-free",
		"ciora/gemini-3.8-flash",
	},
	"openagentic": {
		"claude-haiku-5.5-free",
		"space-bunny-free",
		"mimo-v2.6-flash",
	},
}

// providerNameRe memvalidasi nama provider/model custom: huruf kecil, angka,
// strip. Nama dipakai di command, nomor urut, dan nama env key.
var providerNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// reservedNames adalah kata yang dipakai subcommand (".agent provider add")
// sehingga tak bisa dipakai sebagai nama provider/model.
var reservedNames = map[string]bool{"add": true, "del": true}

func validRegistryName(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if reservedNames[name] {
		return false
	}
	return providerNameRe.MatchString(name)
}

// ListProviders mengembalikan registry provider runtime terurut. DB gagal =
// fallback seed (fail-soft: daftar tampil, chat tetap jalan).
func ListProviders(ctx context.Context) []ProviderInfo {
	if ps, err := store.AI().ListProviders(ctx); err == nil && len(ps) > 0 {
		out := make([]ProviderInfo, 0, len(ps))
		for _, p := range ps {
			out = append(out, ProviderInfo{Name: p.Name, Endpoint: p.Endpoint})
		}
		return out
	}
	return append([]ProviderInfo(nil), seedProviders...)
}

// providerEndpoint mengembalikan endpoint satu provider ("" = tak dikenal).
func providerEndpoint(ctx context.Context, name string) string {
	for _, p := range ListProviders(ctx) {
		if p.Name == name {
			return p.Endpoint
		}
	}
	return ""
}

// ListModels mengembalikan daftar model satu provider terurut. Provider tak
// dikenal / DB gagal = fallback seed (bisa kosong).
func ListModels(ctx context.Context, provider string) []string {
	if ms, err := store.AI().ListModels(ctx, provider); err == nil && len(ms) > 0 {
		return ms
	}
	return append([]string(nil), seedModels[provider]...)
}

// defaultModelFor mengembalikan model default untuk sebuah provider kalau
// belum pernah di-set (entri pertama daftar).
func defaultModelFor(ctx context.Context, provider string) string {
	if models := ListModels(ctx, provider); len(models) > 0 {
		return models[0]
	}
	return ""
}

// IsValidProvider mengecek apakah nama provider ada di registry.
func IsValidProvider(provider string) bool {
	for _, p := range ListProviders(context.Background()) {
		if p.Name == provider {
			return true
		}
	}
	return false
}

// IsValidModel mengecek apakah nama model ada di daftar untuk provider
// tersebut.
func IsValidModel(provider, model string) bool {
	for _, m := range ListModels(context.Background(), provider) {
		if m == model {
			return true
		}
	}
	return false
}

// providerAPIKey membaca API key provider dari environment dengan aturan
// seragam: <NAMA>_API_KEY (strip jadi underscore, huruf besar). Contoh:
// ciora -> CIORA_API_KEY, openagentic -> OPENAGENTIC_API_KEY. Secret tetap
// di env (tidak masuk DB/chat) supaya tak bocor ke log/arsip.
func providerAPIKey(name string) string {
	key := strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "_API_KEY"
	return strings.TrimSpace(os.Getenv(key))
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
// mengambil ulang dari DB. Dipanggil setelah setiap mutasi setting berhasil.
func invalidateSettingsCache() {
	settingsCacheMu.Lock()
	settingsCache = nil
	settingsCacheMu.Unlock()
}

// GetActiveProviderName mengembalikan nama provider aktif saat ini, dibaca
// dari cache/SQLite. Fail-soft ke "ciora" kalau DB bermasalah atau nilainya
// tak dikenal — supaya chat tetap bisa jalan dengan provider utama daripada
// bot berhenti total gara-gara setting gagal dibaca.
func GetActiveProviderName(ctx context.Context) string {
	s, err := getSettingsCached(ctx)
	if err != nil || s.ActiveProvider == "" {
		return "ciora"
	}
	if !IsValidProvider(s.ActiveProvider) {
		return "ciora"
	}
	return s.ActiveProvider
}

// GetActiveModelName mengembalikan nama model aktif untuk provider yang
// SEDANG aktif saat ini. Kalau belum pernah di-set, fallback ke model
// default provider. Self-heal: model tersimpan yang tak lagi ada di daftar
// ikut fallback — supaya chat tidak mati mengarah ke ID yang sudah mati.
func GetActiveModelName(ctx context.Context) string {
	provider := GetActiveProviderName(ctx)
	s, err := getSettingsCached(ctx)
	if err != nil {
		return defaultModelFor(ctx, provider)
	}
	if s.ActiveModel == "" || !IsValidModel(provider, s.ActiveModel) {
		return defaultModelFor(ctx, provider)
	}
	return s.ActiveModel
}

// GetActiveProvider membangun ProviderAdapter yang SESUAI dengan provider &
// model aktif saat ini (dibaca dari setting global tersimpan). Dipanggil
// oleh plugins/ai.go SETIAP giliran chat.
//
// API key tiap provider dari environment (<NAMA>_API_KEY) — hanya PILIHAN
// provider, endpoint & model yang dinamis/tersimpan di DB, kredensialnya
// tetap dari .env seperti biasa.
func GetActiveProvider(ctx context.Context) (ProviderAdapter, error) {
	provider := GetActiveProviderName(ctx)
	model := GetActiveModelName(ctx)

	endpoint := providerEndpoint(ctx, provider)
	if endpoint == "" {
		return nil, fmt.Errorf("provider %s tidak punya endpoint terdaftar", provider)
	}
	key := providerAPIKey(provider)
	if key == "" {
		envName := strings.ToUpper(strings.ReplaceAll(provider, "-", "_")) + "_API_KEY"
		return nil, fmt.Errorf("provider %s aktif tapi %s kosong di .env", provider, envName)
	}
	return NewGenericOpenAIAdapter(provider, endpoint, key, model), nil
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
	if err := store.AI().SetActiveModel(ctx, model); err != nil {
		return err
	}
	invalidateSettingsCache()
	return nil
}

// AddProvider menambah provider OpenAI-compatible baru (nama + endpoint
// chat-completions penuh). Key dibaca dari env <NAMA>_API_KEY saat dipakai.
func AddProvider(ctx context.Context, name, endpoint string) error {
	name = strings.ToLower(strings.TrimSpace(name))
	endpoint = strings.TrimSpace(endpoint)
	if !validRegistryName(name) {
		return fmt.Errorf("nama provider tidak valid (huruf kecil/angka/strip, bukan add/del): %q", name)
	}
	if endpoint == "" || (!strings.HasPrefix(endpoint, "https://") && !strings.HasPrefix(endpoint, "http://")) {
		return fmt.Errorf("endpoint harus URL http(s) chat-completions penuh")
	}
	return store.AI().AddProvider(ctx, name, endpoint)
}

// DelProvider menghapus provider beserta daftar modelnya. Provider aktif dan
// provider terakhir tidak boleh dihapus (ganti/dulu tambah pengganti).
func DelProvider(ctx context.Context, name string) error {
	name = strings.ToLower(strings.TrimSpace(name))
	providers := ListProviders(ctx)
	if len(providers) <= 1 {
		return fmt.Errorf("tidak bisa hapus: %s satu-satunya provider", name)
	}
	found := false
	for _, p := range providers {
		if p.Name == name {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("provider tidak dikenal: %s", name)
	}
	if name == GetActiveProviderName(ctx) {
		return fmt.Errorf("provider %s sedang aktif — ganti dulu baru hapus", name)
	}
	return store.AI().DelProvider(ctx, name)
}

// AddModel menambah model ke provider yang SEDANG aktif.
func AddModel(ctx context.Context, model string) error {
	provider := GetActiveProviderName(ctx)
	model = strings.TrimSpace(model)
	if model == "" || strings.ContainsAny(model, " \t\n") || reservedNames[strings.ToLower(model)] {
		return fmt.Errorf("nama model tidak valid: %q", model)
	}
	return store.AI().AddModel(ctx, provider, model)
}

// DelModel menghapus model dari provider yang SEDANG aktif. Model terakhir
// dan model yang sedang aktif tidak boleh dihapus.
func DelModel(ctx context.Context, model string) error {
	provider := GetActiveProviderName(ctx)
	model = strings.TrimSpace(model)
	models := ListModels(ctx, provider)
	found := false
	for _, m := range models {
		if m == model {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("model %q tidak ada untuk provider %s", model, provider)
	}
	if len(models) <= 1 {
		return fmt.Errorf("tidak bisa hapus: %s satu-satunya model %s", model, provider)
	}
	if model == GetActiveModelName(ctx) {
		return fmt.Errorf("model %s sedang aktif — ganti dulu baru hapus", model)
	}
	return store.AI().DelModel(ctx, provider, model)
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
// Bernomor urut (1-based) supaya user bisa memilih pakai nama maupun nomor
// (".agent provider 2"). Endpoint ditampilkan untuk verifikasi provider custom.
func FormatProviderList(ctx context.Context) string {
	active := GetActiveProviderName(ctx)
	var sb strings.Builder
	sb.WriteString("Provider tersedia:\n")
	for i, p := range ListProviders(ctx) {
		marker := ""
		if p.Name == active {
			marker = " (aktif)"
		}
		sb.WriteString(fmt.Sprintf("%d. %s%s\n   %s\n", i+1, p.Name, marker, p.Endpoint))
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
	for i, m := range ListModels(ctx, provider) {
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
func ResolveProviderRef(ctx context.Context, ref string) string {
	ref = strings.TrimSpace(ref)
	if providers := ListProviders(ctx); len(providers) > 0 {
		if n, err := strconv.Atoi(ref); err == nil && n >= 1 && n <= len(providers) {
			return providers[n-1].Name
		}
	}
	return ref
}

// ResolveModelRef mengubah referensi user (nama atau nomor urut 1-based
// sesuai FormatModelList untuk provider tersebut) jadi nama model.
func ResolveModelRef(ctx context.Context, provider, ref string) string {
	ref = strings.TrimSpace(ref)
	if models := ListModels(ctx, provider); len(models) > 0 {
		if n, err := strconv.Atoi(ref); err == nil && n >= 1 && n <= len(models) {
			return models[n-1]
		}
	}
	return ref
}
