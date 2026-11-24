package store

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"wabot-go/internal/identity"

	"github.com/polymorfa/hypermeow/types"
)

// File ini berisi penyimpanan untuk fitur khusus owner: daftar user yang
// di-ban, daftar grup yang di-ban, dan mode bot (self/public). Semua tabel
// dibuat di InitGroupDB (groupdb.go) dan reuse koneksi group.db yang sama —
// tidak ada file database baru, supaya urutan init di main.go tidak berubah.

// Mode bot yang didukung: "self" (hanya owner yang dilayani) atau
// "public" (semua orang dilayani, perilaku default).
const (
	BotModeSelf   = "self"
	BotModePublic = "public"
)

// ensureCacheLoaded memuat data status bot_mode, banned_users, trusted_users,
// dan banned_groups ke memori jika belum dimuat, agar pengecekan di hot-path
// pesan masuk tidak membebani query disk SQLite.
func (g *GroupDB) ensureCacheLoaded(ctx context.Context) error {
	if g == nil || g.db == nil {
		return fmt.Errorf("group.db mati")
	}
	g.cacheMu.RLock()
	if g.cacheLoaded {
		g.cacheMu.RUnlock()
		return nil
	}
	g.cacheMu.RUnlock()

	g.cacheMu.Lock()
	defer g.cacheMu.Unlock()
	if g.cacheLoaded {
		return nil
	}

	// 1. Load bot mode
	var mode string
	err := g.db.QueryRowContext(ctx, `SELECT value FROM bot_settings WHERE key = 'mode'`).Scan(&mode)
	if err != nil && !isNoRowsError(err) {
		return err
	}
	if mode != BotModeSelf && mode != BotModePublic {
		mode = BotModePublic
	}
	g.botMode = mode

	// 2. Load banned users
	banned := make(map[string]string)
	bRows, err := g.db.QueryContext(ctx, `SELECT user_jid, user_part FROM banned_users`)
	if err != nil {
		return err
	}
	for bRows.Next() {
		var jid, part string
		if err := bRows.Scan(&jid, &part); err == nil {
			banned[jid] = part
		}
	}
	_ = bRows.Close()
	g.bannedUsers = banned

	// 3. Load trusted users
	trusted := make(map[string]string)
	tRows, err := g.db.QueryContext(ctx, `SELECT user_jid, user_part FROM trusted_users`)
	if err != nil {
		return err
	}
	for tRows.Next() {
		var jid, part string
		if err := tRows.Scan(&jid, &part); err == nil {
			trusted[jid] = part
		}
	}
	_ = tRows.Close()
	g.trustedUsers = trusted

	// 4. Load banned groups
	bGroups := make(map[string]struct{})
	bgRows, err := g.db.QueryContext(ctx, `SELECT chat_jid FROM banned_groups`)
	if err != nil {
		return err
	}
	for bgRows.Next() {
		var cjid string
		if err := bgRows.Scan(&cjid); err == nil {
			bGroups[cjid] = struct{}{}
		}
	}
	_ = bgRows.Close()
	g.bannedGroups = bGroups

	g.cacheLoaded = true
	return nil
}

// BanUser menambahkan JID (sudah dinormalisasi ToNonAD oleh pemanggil) ke
// daftar ban. user_part disimpan terpisah supaya pencocokan tetap jalan walau
// pengirim yang sama kadang datang sebagai @s.whatsapp.net dan kadang
// sebagai @lid (duplikasi identitas WhatsApp) — lihat IsUserBanned.
func (g *GroupDB) BanUser(ctx context.Context, bareJID types.JID) error {
	_, err := g.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO banned_users (user_jid, user_part) VALUES (?, ?)
	`, bareJID.String(), bareJID.User)
	if err != nil {
		return err
	}
	g.cacheMu.Lock()
	if g.bannedUsers != nil {
		g.bannedUsers[bareJID.String()] = bareJID.User
	}
	g.cacheMu.Unlock()
	return nil
}

// UnbanUser menghapus JID dari daftar ban. Mengembalikan true kalau memang
// ada baris yang terhapus (dipakai untuk pesan "tidak ada di daftar").
func (g *GroupDB) UnbanUser(ctx context.Context, bareJID types.JID) (bool, error) {
	res, err := g.db.ExecContext(ctx, `DELETE FROM banned_users WHERE user_jid = ?`, bareJID.String())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		g.cacheMu.Lock()
		if g.bannedUsers != nil {
			delete(g.bannedUsers, bareJID.String())
		}
		g.cacheMu.Unlock()
	}
	return n > 0, nil
}

// IsUserBanned mengecek apakah pengirim masuk daftar ban. sender dan
// senderAlt (keduanya dari evt.Info) dibandingkan lewat identity: cocok kalau
// JID bare-nya sama ATAU user_part-nya sama dengan salah satu baris. Fallback
// user_part inilah yang membuat ban tetap mempan walau identitas pengirim
// flip-flop antara PN dan LID.
func (g *GroupDB) IsUserBanned(ctx context.Context, sender, senderAlt types.JID) (bool, error) {
	return g.userListMatches(ctx, "banned_users", sender, senderAlt)
}

// IsUserTrusted mengecek apakah pengirim masuk daftar trust, dengan aturan
// pencocokan sama seperti IsUserBanned.
func (g *GroupDB) IsUserTrusted(ctx context.Context, sender, senderAlt types.JID) (bool, error) {
	return g.userListMatches(ctx, "trusted_users", sender, senderAlt)
}

// userListMatches mencocokkan identitas pengirim (sender + senderAlt, sudah
// dinormalisasi lewat identity) terhadap cache in-memory daftar user
// (banned_users / trusted_users). table HANYA boleh konstanta internal.
func (g *GroupDB) userListMatches(ctx context.Context, table string, sender, senderAlt types.JID) (bool, error) {
	full := make(map[string]bool, 2)
	for _, s := range identity.Strings(ctx, nil, sender, senderAlt) {
		full[s] = true
	}
	part := make(map[string]bool, 2)
	for _, s := range identity.Keys(ctx, nil, sender, senderAlt) {
		part[s] = true
	}
	if len(full) == 0 && len(part) == 0 {
		return false, nil
	}
	if err := g.ensureCacheLoaded(ctx); err != nil {
		return false, err
	}

	g.cacheMu.RLock()
	defer g.cacheMu.RUnlock()

	var target map[string]string
	switch table {
	case "banned_users":
		target = g.bannedUsers
	case "trusted_users":
		target = g.trustedUsers
	default:
		return false, fmt.Errorf("tabel user tidak dikenal: %s", table)
	}

	for jid, p := range target {
		if jid != "" && full[jid] {
			return true, nil
		}
		if p != "" && part[p] {
			return true, nil
		}
	}
	return false, nil
}

// ListBannedUsers mengembalikan semua JID yang di-ban, di-sort alfabet.
func (g *GroupDB) ListBannedUsers(ctx context.Context) ([]string, error) {
	rows, err := g.db.QueryContext(ctx, `SELECT user_jid FROM banned_users ORDER BY user_jid ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var jid string
		if err := rows.Scan(&jid); err != nil {
			return nil, err
		}
		out = append(out, jid)
	}
	return out, rows.Err()
}

// BanGroup menambahkan chat JID grup ke daftar ban.
func (g *GroupDB) BanGroup(ctx context.Context, chatJID string) error {
	_, err := g.db.ExecContext(ctx, `INSERT OR REPLACE INTO banned_groups (chat_jid) VALUES (?)`, chatJID)
	if err != nil {
		return err
	}
	g.cacheMu.Lock()
	if g.bannedGroups != nil {
		g.bannedGroups[chatJID] = struct{}{}
	}
	g.cacheMu.Unlock()
	return nil
}

// UnbanGroup menghapus grup dari daftar ban, true kalau memang ada barisnya.
func (g *GroupDB) UnbanGroup(ctx context.Context, chatJID string) (bool, error) {
	res, err := g.db.ExecContext(ctx, `DELETE FROM banned_groups WHERE chat_jid = ?`, chatJID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		g.cacheMu.Lock()
		if g.bannedGroups != nil {
			delete(g.bannedGroups, chatJID)
		}
		g.cacheMu.Unlock()
	}
	return n > 0, nil
}

// IsGroupBanned mengecek apakah chat JID masuk daftar ban grup.
func (g *GroupDB) IsGroupBanned(ctx context.Context, chatJID string) (bool, error) {
	if err := g.ensureCacheLoaded(ctx); err != nil {
		return false, err
	}
	g.cacheMu.RLock()
	defer g.cacheMu.RUnlock()
	_, ok := g.bannedGroups[chatJID]
	return ok, nil
}

// ListBannedGroups mengembalikan semua chat JID grup yang di-ban.
func (g *GroupDB) ListBannedGroups(ctx context.Context) ([]string, error) {
	rows, err := g.db.QueryContext(ctx, `SELECT chat_jid FROM banned_groups ORDER BY chat_jid ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var jid string
		if err := rows.Scan(&jid); err != nil {
			return nil, err
		}
		out = append(out, jid)
	}
	return out, rows.Err()
}

// GetBotMode membaca mode bot, default "public" kalau belum pernah di-set.
func (g *GroupDB) GetBotMode(ctx context.Context) (string, error) {
	if err := g.ensureCacheLoaded(ctx); err != nil {
		return BotModePublic, err
	}
	g.cacheMu.RLock()
	defer g.cacheMu.RUnlock()
	if g.botMode == "" {
		return BotModePublic, nil
	}
	return g.botMode, nil
}

// IsSelfMode true kalau bot sedang mode self (hanya owner yang dilayani).
func (g *GroupDB) IsSelfMode(ctx context.Context) (bool, error) {
	mode, err := g.GetBotMode(ctx)
	if err != nil {
		return false, err
	}
	return mode == BotModeSelf, nil
}

// SetBotMode menyimpan mode bot, hanya menerima "self"/"public".
func (g *GroupDB) SetBotMode(ctx context.Context, mode string) error {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != BotModeSelf && mode != BotModePublic {
		return errInvalidMode(mode)
	}
	_, err := g.db.ExecContext(ctx, `INSERT OR REPLACE INTO bot_settings (key, value) VALUES ('mode', ?)`, mode)
	if err != nil {
		return err
	}
	g.cacheMu.Lock()
	g.botMode = mode
	g.cacheMu.Unlock()
	return nil
}

// IsAILocked true kalau trigger natural Flora sedang di-lock global
// (mati di semua chat). Default false kalau belum pernah di-set.
func (g *GroupDB) IsAILocked(ctx context.Context) (bool, error) {
	var val string
	err := g.db.QueryRowContext(ctx, `SELECT value FROM bot_settings WHERE key = 'ai_lock'`).Scan(&val)
	if err != nil {
		if isNoRowsError(err) {
			return false, nil
		}
		return false, err
	}
	return val == "1", nil
}

// SetAILock menyalakan/mematikan lock trigger natural Flora (global).
func (g *GroupDB) SetAILock(ctx context.Context, locked bool) error {
	val := "0"
	if locked {
		val = "1"
	}
	_, err := g.db.ExecContext(ctx, `INSERT OR REPLACE INTO bot_settings (key, value) VALUES ('ai_lock', ?)`, val)
	return err
}

// IsSelfCommandEnabled true kalau saklar .selfcmd sedang ON: bot boleh
// mengeksekusi command yang dikirim dari akunnya sendiri. Default false
// (OFF) kalau belum pernah di-set — fail-closed mengikuti pola ai_lock.
func (g *GroupDB) IsSelfCommandEnabled(ctx context.Context) (bool, error) {
	var val string
	err := g.db.QueryRowContext(ctx, `SELECT value FROM bot_settings WHERE key = 'selfcmd'`).Scan(&val)
	if err != nil {
		if isNoRowsError(err) {
			return false, nil
		}
		return false, err
	}
	return val == "1", nil
}

// SetSelfCommand menyalakan/mematikan saklar .selfcmd (global).
func (g *GroupDB) SetSelfCommand(ctx context.Context, on bool) error {
	val := "0"
	if on {
		val = "1"
	}
	_, err := g.db.ExecContext(ctx, `INSERT OR REPLACE INTO bot_settings (key, value) VALUES ('selfcmd', ?)`, val)
	return err
}

// Kunci bot_settings untuk tampilan kartu .menu (di-set via .thumbnail /
// .favicon, prioritas di atas .env — lihat menu.go).
//
// Ada dua bentuk nilai per aset: URL (teks) dan blob (gambar base64).
// Blob menang atas URL; pemasangan lewat command selalu menghapus bentuk
// yang lain supaya sumber yang aktif tidak ambigu.
const (
	menuThumbKey       = "menu_thumb_url"
	menuThumbBlobKey   = "menu_thumb_blob"
	menuFaviconKey     = "menu_favicon_url"
	menuFaviconBlobKey = "menu_favicon_blob"
)

// GetMenuThumbURL membaca thumbnail kustom kartu .menu ("" = belum di-set,
// pakai .env). Fail-soft seperti GetBotMode: DB error selain no-rows
// diteruskan, no-rows = string kosong.
func (g *GroupDB) GetMenuThumbURL(ctx context.Context) (string, error) {
	var v string
	err := g.db.QueryRowContext(ctx, `SELECT value FROM bot_settings WHERE key = ?`, menuThumbKey).Scan(&v)
	if err != nil {
		if isNoRowsError(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(v), nil
}

// SetMenuThumbURL menyimpan thumbnail kustom (string kosong = reset ke .env).
func (g *GroupDB) SetMenuThumbURL(ctx context.Context, url string) error {
	url = strings.TrimSpace(url)
	if url == "" {
		_, err := g.db.ExecContext(ctx, `DELETE FROM bot_settings WHERE key = ?`, menuThumbKey)
		return err
	}
	_, err := g.db.ExecContext(ctx, `INSERT OR REPLACE INTO bot_settings (key, value) VALUES (?, ?)`, menuThumbKey, url)
	return err
}

// GetMenuFaviconURL membaca favicon kustom kartu .menu ("" = pakai .env).
func (g *GroupDB) GetMenuFaviconURL(ctx context.Context) (string, error) {
	var v string
	err := g.db.QueryRowContext(ctx, `SELECT value FROM bot_settings WHERE key = ?`, menuFaviconKey).Scan(&v)
	if err != nil {
		if isNoRowsError(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(v), nil
}

// SetMenuFaviconURL menyimpan favicon kustom (string kosong = reset ke .env).
func (g *GroupDB) SetMenuFaviconURL(ctx context.Context, url string) error {
	url = strings.TrimSpace(url)
	if url == "" {
		_, err := g.db.ExecContext(ctx, `DELETE FROM bot_settings WHERE key = ?`, menuFaviconKey)
		return err
	}
	_, err := g.db.ExecContext(ctx, `INSERT OR REPLACE INTO bot_settings (key, value) VALUES (?, ?)`, menuFaviconKey, url)
	return err
}

// GetMenuThumbBlob membaca thumbnail tersimpan (gambar yang dikirim owner
// via .thumbnail). Nil = belum ada. Disimpan base64 di bot_settings supaya
// tidak perlu file tambahan dan ikut terbawa .backup (SnapshotDB).
func (g *GroupDB) GetMenuThumbBlob(ctx context.Context) ([]byte, error) {
	return g.getSettingBlob(ctx, menuThumbBlobKey)
}

// SetMenuThumbBlob menyimpan byte gambar thumbnail (nil/kosong = hapus).
func (g *GroupDB) SetMenuThumbBlob(ctx context.Context, data []byte) error {
	return g.setSettingBlob(ctx, menuThumbBlobKey, data)
}

// GetMenuFaviconBlob / SetMenuFaviconBlob pola sama untuk favicon.
func (g *GroupDB) GetMenuFaviconBlob(ctx context.Context) ([]byte, error) {
	return g.getSettingBlob(ctx, menuFaviconBlobKey)
}

func (g *GroupDB) SetMenuFaviconBlob(ctx context.Context, data []byte) error {
	return g.setSettingBlob(ctx, menuFaviconBlobKey, data)
}

// getSettingBlob membaca nilai bot_settings yang berisi base64. No-rows =
// nil (belum di-set). Base64 rusak juga nil: lebih baik kartu tanpa gambar
// daripada fitur .menu gagal total.
func (g *GroupDB) getSettingBlob(ctx context.Context, key string) ([]byte, error) {
	var v string
	err := g.db.QueryRowContext(ctx, `SELECT value FROM bot_settings WHERE key = ?`, key).Scan(&v)
	if err != nil {
		if isNoRowsError(err) {
			return nil, nil
		}
		return nil, err
	}
	raw, decErr := base64.StdEncoding.DecodeString(strings.TrimSpace(v))
	if decErr != nil || len(raw) == 0 {
		return nil, nil
	}
	return raw, nil
}

// setSettingBlob menulis base64 ke bot_settings; data kosong = hapus baris.
func (g *GroupDB) setSettingBlob(ctx context.Context, key string, data []byte) error {
	if len(data) == 0 {
		_, err := g.db.ExecContext(ctx, `DELETE FROM bot_settings WHERE key = ?`, key)
		return err
	}
	_, err := g.db.ExecContext(ctx, `INSERT OR REPLACE INTO bot_settings (key, value) VALUES (?, ?)`,
		key, base64.StdEncoding.EncodeToString(data))
	return err
}

// Kunci bot_settings untuk backup database otomatis (lihat plugins/backup.go).
const (
	backupIntervalKey = "backup_interval_min"
	backupLastRunKey  = "backup_last_run"
)

// GetBackupSchedule mengembalikan interval backup otomatis dalam MENIT
// (0 = mati). Tahan restart karena disimpan di bot_settings.
func (g *GroupDB) GetBackupSchedule(ctx context.Context) (int, error) {
	var v string
	err := g.db.QueryRowContext(ctx, `SELECT value FROM bot_settings WHERE key = ?`, backupIntervalKey).Scan(&v)
	if err != nil {
		if isNoRowsError(err) {
			return 0, nil
		}
		return 0, err
	}
	n, _ := strconv.Atoi(strings.TrimSpace(v))
	return n, nil
}

// SetBackupSchedule menyimpan interval backup (menit); <= 0 = matikan
// (baris dihapus).
func (g *GroupDB) SetBackupSchedule(ctx context.Context, minutes int) error {
	if minutes <= 0 {
		_, err := g.db.ExecContext(ctx, `DELETE FROM bot_settings WHERE key = ?`, backupIntervalKey)
		return err
	}
	_, err := g.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO bot_settings (key, value) VALUES (?, ?)`,
		backupIntervalKey, strconv.Itoa(minutes))
	return err
}

// GetBackupLastRun mengembalikan waktu backup otomatis terakhir (zero time
// bila belum pernah / tak ada catatan).
func (g *GroupDB) GetBackupLastRun(ctx context.Context) (time.Time, error) {
	var v string
	err := g.db.QueryRowContext(ctx, `SELECT value FROM bot_settings WHERE key = ?`, backupLastRunKey).Scan(&v)
	if err != nil {
		if isNoRowsError(err) {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if n <= 0 {
		return time.Time{}, nil
	}
	return time.Unix(n, 0), nil
}

// SetBackupLastRun mencatat waktu backup otomatis terakhir.
func (g *GroupDB) SetBackupLastRun(ctx context.Context, t time.Time) error {
	_, err := g.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO bot_settings (key, value) VALUES (?, ?)`,
		backupLastRunKey, strconv.FormatInt(t.Unix(), 10))
	return err
}

// TrustUser menambahkan JID (sudah dinormalisasi ToNonAD oleh pemanggil)
// ke daftar user yang dipercaya mengakses Flora. Pola penyimpanan identik
// dengan ban (lihat BanUser/IsUserBanned): user_part disimpan terpisah
// supaya pencocokan tetap jalan walau pengirim flip-flop antara PN dan LID.
func (g *GroupDB) TrustUser(ctx context.Context, bareJID types.JID) error {
	_, err := g.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO trusted_users (user_jid, user_part) VALUES (?, ?)
	`, bareJID.String(), bareJID.User)
	if err != nil {
		return err
	}
	g.cacheMu.Lock()
	if g.trustedUsers != nil {
		g.trustedUsers[bareJID.String()] = bareJID.User
	}
	g.cacheMu.Unlock()
	return nil
}

// UntrustUser menghapus JID dari daftar trust. Mengembalikan true kalau
// memang ada baris yang terhapus (dipakai untuk pesan "tidak ada di daftar").
func (g *GroupDB) UntrustUser(ctx context.Context, bareJID types.JID) (bool, error) {
	res, err := g.db.ExecContext(ctx, `DELETE FROM trusted_users WHERE user_jid = ?`, bareJID.String())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		g.cacheMu.Lock()
		if g.trustedUsers != nil {
			delete(g.trustedUsers, bareJID.String())
		}
		g.cacheMu.Unlock()
	}
	return n > 0, nil
}

// TrustedEntry adalah satu baris daftar trust (untuk .agent trustlist).
type TrustedEntry struct {
	JID       string
	Part      string
	CreatedAt int64
}

// ListTrustedUsers mengembalikan seluruh daftar trust terurut waktu
// ditambahkan. Kosong (tanpa error) kalau belum ada yang dipercaya.
func (g *GroupDB) ListTrustedUsers(ctx context.Context) ([]TrustedEntry, error) {
	rows, err := g.db.QueryContext(ctx, `SELECT user_jid, user_part, created_at FROM trusted_users ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TrustedEntry
	for rows.Next() {
		var e TrustedEntry
		if err := rows.Scan(&e.JID, &e.Part, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

const geminiSystemKey = "gemini_system_prompt"

// GetGeminiSystemPrompt membaca prompt sistem Gemini dari bot_settings.
// Mengembalikan string kosong jika belum pernah disetel.
func (g *GroupDB) GetGeminiSystemPrompt(ctx context.Context) (string, error) {
	var v string
	err := g.db.QueryRowContext(ctx, `SELECT value FROM bot_settings WHERE key = ?`, geminiSystemKey).Scan(&v)
	if err != nil {
		if isNoRowsError(err) {
			return "", nil
		}
		return "", err
	}
	return v, nil
}

// SetGeminiSystemPrompt menyimpan prompt sistem Gemini ke bot_settings.
// Teks kosong (setelah trim) menghapus baris dari bot_settings.
func (g *GroupDB) SetGeminiSystemPrompt(ctx context.Context, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		_, err := g.db.ExecContext(ctx, `DELETE FROM bot_settings WHERE key = ?`, geminiSystemKey)
		return err
	}
	_, err := g.db.ExecContext(ctx, `INSERT OR REPLACE INTO bot_settings (key, value) VALUES (?, ?)`, geminiSystemKey, text)
	return err
}

// GetGeminiState membaca state percakapan Gemini (JSON) untuk satu kunci sesi.
// Dipakai `.gemini` untuk menyimpan riwayat giliran (bukan kredensial). String
// kosong = belum ada.
func (g *GroupDB) GetGeminiState(ctx context.Context, key string) (string, error) {
	var v string
	err := g.db.QueryRowContext(ctx, `SELECT value FROM bot_settings WHERE key = ?`, key).Scan(&v)
	if err != nil {
		if isNoRowsError(err) {
			return "", nil
		}
		return "", err
	}
	return v, nil
}

// SetGeminiState menyimpan state percakapan Gemini. Nilai kosong menghapus
// baris (kembali ke keadaan belum ada state).
func (g *GroupDB) SetGeminiState(ctx context.Context, key, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return g.DeleteGeminiState(ctx, key)
	}
	_, err := g.db.ExecContext(ctx, `INSERT OR REPLACE INTO bot_settings (key, value) VALUES (?, ?)`, key, value)
	return err
}

// DeleteGeminiState membuang state percakapan Gemini untuk satu kunci.
func (g *GroupDB) DeleteGeminiState(ctx context.Context, key string) error {
	_, err := g.db.ExecContext(ctx, `DELETE FROM bot_settings WHERE key = ?`, key)
	return err
}

// errInvalidMode dipisah supaya pesan error konsisten dipakai ulang.
func errInvalidMode(mode string) error {
	return &modeError{mode: mode}
}

type modeError struct {
	mode string
}

func (e *modeError) Error() string {
	return "mode tidak dikenal: " + e.mode + " (pilih: self / public)"
}
