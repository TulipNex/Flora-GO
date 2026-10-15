package store

import (
	"context"
	"strings"
)

// File ini berisi penyimpanan moderasi grup: saklar antilink per grup dan
// hitungan warn per user per grup. Tabel dibuat di InitGroupDB (groupdb.go)
// dan reuse koneksi group.db — tidak ada file database baru.

// IsAntilinkOn true kalau antilink aktif untuk grup ini (default mati).
func (g *GroupDB) IsAntilinkOn(ctx context.Context, chatJID string) (bool, error) {
	var val int
	err := g.db.QueryRowContext(ctx, `SELECT antilink FROM group_moderation WHERE chat_jid = ?`, chatJID).Scan(&val)
	if err != nil {
		// Baris belum ada = belum pernah di-set = mati, bukan error.
		if isNoRowsError(err) {
			return false, nil
		}
		return false, err
	}
	return val == 1, nil
}

// SetAntilink menyalakan/mematikan antilink untuk grup ini.
func (g *GroupDB) SetAntilink(ctx context.Context, chatJID string, on bool) error {
	val := 0
	if on {
		val = 1
	}
	_, err := g.db.ExecContext(ctx, `
		INSERT INTO group_moderation (chat_jid, antilink) VALUES (?, ?)
		ON CONFLICT(chat_jid) DO UPDATE SET antilink = excluded.antilink, updated_at = strftime('%s','now')
	`, chatJID, val)
	return err
}

// IsAntistatusOn true kalau auto-hapus status grup orang aktif untuk grup
// ini (default mati). Kolom antistatus dibuat via migrasi di groupdb.go
// (database lama tidak punya kolom ini — SELECT gagal = anggap mati,
// JANGAN error supaya bot lama tetap jalan).
func (g *GroupDB) IsAntistatusOn(ctx context.Context, chatJID string) (bool, error) {
	var val int
	err := g.db.QueryRowContext(ctx, `SELECT antistatus FROM group_moderation WHERE chat_jid = ?`, chatJID).Scan(&val)
	if err != nil {
		if isNoRowsError(err) {
			return false, nil
		}
		if isNoSuchColumnError(err) {
			return false, nil
		}
		return false, err
	}
	return val == 1, nil
}

// SetAntistatus menyalakan/mematikan auto-hapus status grup untuk grup ini.
func (g *GroupDB) SetAntistatus(ctx context.Context, chatJID string, on bool) error {
	val := 0
	if on {
		val = 1
	}
	_, err := g.db.ExecContext(ctx, `
		INSERT INTO group_moderation (chat_jid, antistatus) VALUES (?, ?)
		ON CONFLICT(chat_jid) DO UPDATE SET antistatus = excluded.antistatus, updated_at = strftime('%s','now')
	`, chatJID, val)
	return err
}

// AddWarn menambah 1 warn untuk user di grup ini, mengembalikan total
// sekarang. Warn dari deteksi antilink otomatis dan command .warn manual
// memakai fungsi yang SAMA — makanya keduanya sinkron.
func (g *GroupDB) AddWarn(ctx context.Context, chatJID, userJID string) (int, error) {
	if _, err := g.db.ExecContext(ctx, `
		INSERT INTO user_warnings (chat_jid, user_jid, warn_count) VALUES (?, ?, 0)
		ON CONFLICT(chat_jid, user_jid) DO NOTHING
	`, chatJID, userJID); err != nil {
		return 0, err
	}
	if _, err := g.db.ExecContext(ctx, `
		UPDATE user_warnings SET warn_count = warn_count + 1, updated_at = strftime('%s','now')
		WHERE chat_jid = ? AND user_jid = ?
	`, chatJID, userJID); err != nil {
		return 0, err
	}
	var count int
	err := g.db.QueryRowContext(ctx, `
		SELECT warn_count FROM user_warnings WHERE chat_jid = ? AND user_jid = ?
	`, chatJID, userJID).Scan(&count)
	return count, err
}

// GetWarnCount membaca jumlah warn user di grup ini (0 kalau tidak ada).
func (g *GroupDB) GetWarnCount(ctx context.Context, chatJID, userJID string) (int, error) {
	var count int
	err := g.db.QueryRowContext(ctx, `
		SELECT warn_count FROM user_warnings WHERE chat_jid = ? AND user_jid = ?
	`, chatJID, userJID).Scan(&count)
	if err != nil {
		if isNoRowsError(err) {
			return 0, nil
		}
		return 0, err
	}
	return count, nil
}

// ResetWarn menghapus catatan warn user di grup ini (dipakai .unwarn dan
// setelah user di-kick karena mencapai batas maksimal).
func (g *GroupDB) ResetWarn(ctx context.Context, chatJID, userJID string) error {
	_, err := g.db.ExecContext(ctx, `DELETE FROM user_warnings WHERE chat_jid = ? AND user_jid = ?`, chatJID, userJID)
	return err
}

// WarnEntry adalah satu baris daftar warn di grup.
type WarnEntry struct {
	UserJID string
	Count   int
}

// ListWarns mengembalikan semua user yang punya warn di grup ini.
func (g *GroupDB) ListWarns(ctx context.Context, chatJID string) ([]WarnEntry, error) {
	rows, err := g.db.QueryContext(ctx, `
		SELECT user_jid, warn_count FROM user_warnings WHERE chat_jid = ? ORDER BY warn_count DESC, user_jid ASC
	`, chatJID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WarnEntry
	for rows.Next() {
		var e WarnEntry
		if err := rows.Scan(&e.UserJID, &e.Count); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// IsWelcomeOn true kalau sambutan anggota baru aktif untuk grup ini.
// Default AKTIF (true) untuk grup yang belum pernah di-set — sambutan
// bersifat non-destruktif, jadi opt-out (.welcome off), bukan opt-in.
func (g *GroupDB) IsWelcomeOn(ctx context.Context, chatJID string) (bool, error) {
	var val int
	err := g.db.QueryRowContext(ctx, `SELECT welcome FROM group_greetings WHERE chat_jid = ?`, chatJID).Scan(&val)
	if err != nil {
		if isNoRowsError(err) {
			return true, nil
		}
		return false, err
	}
	return val == 1, nil
}

// SetWelcome menyalakan/mematikan sambutan anggota baru untuk grup ini.
func (g *GroupDB) SetWelcome(ctx context.Context, chatJID string, on bool) error {
	val := 0
	if on {
		val = 1
	}
	_, err := g.db.ExecContext(ctx, `
		INSERT INTO group_greetings (chat_jid, welcome) VALUES (?, ?)
		ON CONFLICT(chat_jid) DO UPDATE SET welcome = excluded.welcome, updated_at = strftime('%s','now')
	`, chatJID, val)
	return err
}

// Greeting adalah snapshot pengaturan sambutan & perpisahan satu grup.
// Msg kosong berarti belum dikustom — pemanggil memakai template default.
type Greeting struct {
	WelcomeOn  bool
	ByeOn      bool
	WelcomeMsg string
	ByeMsg     string
}

// GetGreeting membaca pengaturan sambutan/perpisahan grup. Grup yang belum
// pernah di-set mendapat default: welcome OFF, bye OFF (opt-in — admin
// menyalakan via .welcome on/.bye on). Template kosong artinya pakai
// default di plugins/welcome.go.
func (g *GroupDB) GetGreeting(ctx context.Context, chatJID string) (Greeting, error) {
	gr := Greeting{WelcomeOn: false, ByeOn: false}
	var welcome, bye int
	err := g.db.QueryRowContext(ctx, `
		SELECT welcome, bye, welcome_msg, bye_msg FROM group_greetings WHERE chat_jid = ?
	`, chatJID).Scan(&welcome, &bye, &gr.WelcomeMsg, &gr.ByeMsg)
	if err != nil {
		if isNoRowsError(err) {
			return gr, nil
		}
		return Greeting{}, err
	}
	gr.WelcomeOn = welcome == 1
	gr.ByeOn = bye == 1
	return gr, nil
}

// SetBye menyalakan/mematikan perpisahan anggota keluar untuk grup ini.
func (g *GroupDB) SetBye(ctx context.Context, chatJID string, on bool) error {
	val := 0
	if on {
		val = 1
	}
	_, err := g.db.ExecContext(ctx, `
		INSERT INTO group_greetings (chat_jid, bye) VALUES (?, ?)
		ON CONFLICT(chat_jid) DO UPDATE SET bye = excluded.bye, updated_at = strftime('%s','now')
	`, chatJID, val)
	return err
}

// SetWelcomeMsg menyimpan template sambutan kustom (string kosong = kembali
// ke default). Template boleh mengandung variabel @user/@group/@desc.
func (g *GroupDB) SetWelcomeMsg(ctx context.Context, chatJID, tmpl string) error {
	_, err := g.db.ExecContext(ctx, `
		INSERT INTO group_greetings (chat_jid, welcome_msg) VALUES (?, ?)
		ON CONFLICT(chat_jid) DO UPDATE SET welcome_msg = excluded.welcome_msg, updated_at = strftime('%s','now')
	`, chatJID, tmpl)
	return err
}

// SetByeMsg menyimpan template perpisahan kustom (string kosong = default).
func (g *GroupDB) SetByeMsg(ctx context.Context, chatJID, tmpl string) error {
	_, err := g.db.ExecContext(ctx, `
		INSERT INTO group_greetings (chat_jid, bye_msg) VALUES (?, ?)
		ON CONFLICT(chat_jid) DO UPDATE SET bye_msg = excluded.bye_msg, updated_at = strftime('%s','now')
	`, chatJID, tmpl)
	return err
}

// GetInviteCode membaca kode invite grup sendiri yang tersimpan (hasil
// cache dari GetGroupInviteLink). String kosong = belum pernah disimpan.
// Database lama tanpa kolom (migrasi belum jalan) dianggap kosong, bukan
// error — pemanggil akan fetch live seperti sebelum fitur cache ada.
func (g *GroupDB) GetInviteCode(ctx context.Context, chatJID string) (string, error) {
	var code string
	err := g.db.QueryRowContext(ctx, `SELECT invite_code FROM group_moderation WHERE chat_jid = ?`, chatJID).Scan(&code)
	if err != nil {
		if isNoRowsError(err) || isNoSuchColumnError(err) {
			return "", nil
		}
		return "", err
	}
	return code, nil
}

// SetInviteCode menyimpan kode invite grup sendiri (hasil fetch
// GetGroupInviteLink yang diekstrak kodenya). Dipanggil saat cache pertama
// kali diisi dan saat terdeteksi link grup di-reset (lihat
// foreignInviteCodes di plugins/moderation.go).
func (g *GroupDB) SetInviteCode(ctx context.Context, chatJID, code string) error {
	_, err := g.db.ExecContext(ctx, `
		INSERT INTO group_moderation (chat_jid, invite_code) VALUES (?, ?)
		ON CONFLICT(chat_jid) DO UPDATE SET invite_code = excluded.invite_code, updated_at = strftime('%s','now')
	`, chatJID, code)
	if err != nil && isNoSuchColumnError(err) {
		return nil
	}
	return err
}

// isNoSuchColumnError mengecek error "no such column" (database lama yang
// migrasinya belum jalan / gagal sebagian) — pemanggil menganggap fitur
// mati, bukan error fatal.
func isNoSuchColumnError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "no such column")
}

// isNoRowsError mengecek error "no rows in result set" dari database/sql
// tanpa import errors — pola yang sama dipakai di ownerdb.go.
func isNoRowsError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for i := 0; i+len("no rows") <= len(msg); i++ {
		if msg[i:i+len("no rows")] == "no rows" {
			return true
		}
	}
	return false
}
