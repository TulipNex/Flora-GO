package store

import (
	"context"
	"strings"

	"go.mau.fi/whatsmeow/types"
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

// BanUser menambahkan JID (sudah dinormalisasi ToNonAD oleh pemanggil) ke
// daftar ban. user_part disimpan terpisah supaya pencocokan tetap jalan walau
// pengirim yang sama kadang datang sebagai @s.whatsapp.net dan kadang
// sebagai @lid (duplikasi identitas WhatsApp) — lihat IsUserBanned.
func (g *GroupDB) BanUser(ctx context.Context, bareJID types.JID) error {
	_, err := g.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO banned_users (user_jid, user_part) VALUES (?, ?)
	`, bareJID.String(), bareJID.User)
	return err
}

// UnbanUser menghapus JID dari daftar ban. Mengembalikan true kalau memang
// ada baris yang terhapus (dipakai untuk pesan "tidak ada di daftar").
func (g *GroupDB) UnbanUser(ctx context.Context, bareJID types.JID) (bool, error) {
	res, err := g.db.ExecContext(ctx, `DELETE FROM banned_users WHERE user_jid = ?`, bareJID.String())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// IsUserBanned mengecek apakah pengirim masuk daftar ban. sender dan
// senderAlt (keduanya dari evt.Info) dibandingkan dalam bentuk bare
// (ToNonAD) — cocok kalau JID full-nya sama persis ATAU user_part-nya sama
// dengan salah satu baris. Fallback user_part ini yang membuat ban tetap
// mempan walau identitas pengirim flip-flop antara PN dan LID.
func (g *GroupDB) IsUserBanned(ctx context.Context, sender, senderAlt types.JID) (bool, error) {
	sender = sender.ToNonAD()
	senderAlt = senderAlt.ToNonAD()
	rows, err := g.db.QueryContext(ctx, `SELECT user_jid, user_part FROM banned_users`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var jid, part string
		if err := rows.Scan(&jid, &part); err != nil {
			return false, err
		}
		if jid != "" && (jid == sender.String() || (!senderAlt.IsEmpty() && jid == senderAlt.String())) {
			return true, nil
		}
		if part != "" && (part == sender.User || (!senderAlt.IsEmpty() && part == senderAlt.User)) {
			return true, nil
		}
	}
	return false, rows.Err()
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
	return err
}

// UnbanGroup menghapus grup dari daftar ban, true kalau memang ada barisnya.
func (g *GroupDB) UnbanGroup(ctx context.Context, chatJID string) (bool, error) {
	res, err := g.db.ExecContext(ctx, `DELETE FROM banned_groups WHERE chat_jid = ?`, chatJID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// IsGroupBanned mengecek apakah chat JID masuk daftar ban grup.
func (g *GroupDB) IsGroupBanned(ctx context.Context, chatJID string) (bool, error) {
	var jid string
	err := g.db.QueryRowContext(ctx, `SELECT chat_jid FROM banned_groups WHERE chat_jid = ?`, chatJID).Scan(&jid)
	if err != nil {
		// Baris tidak ada = grup tidak di-ban, bukan error.
		if strings.Contains(err.Error(), "no rows") {
			return false, nil
		}
		return false, err
	}
	return true, nil
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
	var mode string
	err := g.db.QueryRowContext(ctx, `SELECT value FROM bot_settings WHERE key = 'mode'`).Scan(&mode)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return BotModePublic, nil
		}
		return "", err
	}
	if mode != BotModeSelf && mode != BotModePublic {
		return BotModePublic, nil
	}
	return mode, nil
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
	return err
}

// IsAILocked true kalau trigger natural Flora sedang di-lock global
// (mati di semua chat). Default false kalau belum pernah di-set.
func (g *GroupDB) IsAILocked(ctx context.Context) (bool, error) {
	var val string
	err := g.db.QueryRowContext(ctx, `SELECT value FROM bot_settings WHERE key = 'ai_lock'`).Scan(&val)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
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

// TrustUser menambahkan JID (sudah dinormalisasi ToNonAD oleh pemanggil)
// ke daftar user yang dipercaya mengakses Flora. Pola penyimpanan identik
// dengan ban (lihat BanUser/IsUserBanned): user_part disimpan terpisah
// supaya pencocokan tetap jalan walau pengirim flip-flop antara PN dan LID.
func (g *GroupDB) TrustUser(ctx context.Context, bareJID types.JID) error {
	_, err := g.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO trusted_users (user_jid, user_part) VALUES (?, ?)
	`, bareJID.String(), bareJID.User)
	return err
}

// UntrustUser menghapus JID dari daftar trust. Mengembalikan true kalau
// memang ada baris yang terhapus (dipakai untuk pesan "tidak ada di daftar").
func (g *GroupDB) UntrustUser(ctx context.Context, bareJID types.JID) (bool, error) {
	res, err := g.db.ExecContext(ctx, `DELETE FROM trusted_users WHERE user_jid = ?`, bareJID.String())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// IsUserTrusted mengecek apakah pengirim masuk daftar trust. sender dan
// senderAlt dibandingkan seperti IsUserBanned (full JID ATAU user_part)
// supaya trust tidak jebol saat identitas pengirim flip-flop PN↔LID.
func (g *GroupDB) IsUserTrusted(ctx context.Context, sender, senderAlt types.JID) (bool, error) {
	sender = sender.ToNonAD()
	senderAlt = senderAlt.ToNonAD()
	rows, err := g.db.QueryContext(ctx, `SELECT user_jid, user_part FROM trusted_users`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var jid, part string
		if err := rows.Scan(&jid, &part); err != nil {
			return false, err
		}
		if jid != "" && (jid == sender.String() || (!senderAlt.IsEmpty() && jid == senderAlt.String())) {
			return true, nil
		}
		if part != "" && (part == sender.User || (!senderAlt.IsEmpty() && part == senderAlt.User)) {
			return true, nil
		}
	}
	return false, rows.Err()
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
