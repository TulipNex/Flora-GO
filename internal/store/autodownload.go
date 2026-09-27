package store

import (
	"context"
)

// File ini menyimpan saklar autodownload per chat (grup maupun private).
// Tabel dibuat di InitGroupDB (groupdb.go), reuse koneksi group.db.
// Default MATI untuk chat yang belum pernah di-set.

// IsAutodownloadOn true kalau autodownload aktif untuk chat ini.
func (g *GroupDB) IsAutodownloadOn(ctx context.Context, chatJID string) (bool, error) {
	var val int
	err := g.db.QueryRowContext(ctx, `SELECT enabled FROM autodownload WHERE chat_jid = ?`, chatJID).Scan(&val)
	if err != nil {
		if isNoRowsError(err) {
			return false, nil
		}
		return false, err
	}
	return val == 1, nil
}

// SetAutodownload menyalakan/mematikan autodownload untuk chat ini.
func (g *GroupDB) SetAutodownload(ctx context.Context, chatJID string, on bool) error {
	val := 0
	if on {
		val = 1
	}
	_, err := g.db.ExecContext(ctx, `
		INSERT INTO autodownload (chat_jid, enabled) VALUES (?, ?)
		ON CONFLICT(chat_jid) DO UPDATE SET enabled = excluded.enabled, updated_at = strftime('%s','now')
	`, chatJID, val)
	return err
}
