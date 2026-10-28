package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// GroupMember adalah satu baris roster grup (snapshot identitas).
type GroupMember struct {
	ChatJID      string
	UserID       int64
	JID          string
	LID          string
	PN           string
	PushName     string
	IsAdmin      bool
	IsSuperAdmin bool
	Present      bool
	JoinedAt     int64
	LeftAt       int64
	FirstSeen    int64
	LastSeen     int64
	UpdatedAt    int64
}

// UpsertGroupMember menyimpan/memperbarui anggota grup.
func (g *GroupDB) UpsertGroupMember(ctx context.Context, m GroupMember) error {
	if g == nil || g.db == nil {
		return fmt.Errorf("group.db mati")
	}
	now := time.Now().Unix()
	if m.FirstSeen == 0 {
		m.FirstSeen = now
	}
	if m.JoinedAt == 0 && m.Present {
		m.JoinedAt = now
	}
	if m.LastSeen == 0 {
		m.LastSeen = now
	}
	present := 0
	if m.Present {
		present = 1
	}
	admin := 0
	if m.IsAdmin {
		admin = 1
	}
	super := 0
	if m.IsSuperAdmin {
		super = 1
	}
	_, err := g.db.ExecContext(ctx, `
		INSERT INTO group_members
			(chat_jid, user_id, jid, lid, pn, pushname, is_admin, is_superadmin,
			 present, joined_at, left_at, first_seen, last_seen, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(chat_jid, user_id) DO UPDATE SET
			jid = excluded.jid,
			lid = excluded.lid,
			pn = excluded.pn,
			pushname = excluded.pushname,
			is_admin = excluded.is_admin,
			is_superadmin = excluded.is_superadmin,
			present = excluded.present,
			joined_at = CASE WHEN group_members.joined_at = 0 THEN excluded.joined_at ELSE group_members.joined_at END,
			left_at = excluded.left_at,
			last_seen = excluded.last_seen,
			updated_at = excluded.updated_at
	`, m.ChatJID, m.UserID, m.JID, m.LID, m.PN, m.PushName, admin, super,
		present, m.JoinedAt, m.LeftAt, m.FirstSeen, m.LastSeen, now)
	return err
}

// MarkGroupMembersAbsent menandai anggota yang tidak ada di daftar terbaru
// sebagai keluar (present=0). presentIDs = daftar user_id yang MASIH ada.
// Jika presentIDs kosong, tidak melakukan apa-apa (jangan hapus semua).
func (g *GroupDB) MarkGroupMembersAbsent(ctx context.Context, chatJID string, presentIDs []int64, now int64) error {
	if g == nil || g.db == nil || chatJID == "" || len(presentIDs) == 0 {
		return nil
	}
	if now == 0 {
		now = time.Now().Unix()
	}
	// Bangun placeholder.
	ph := make([]string, len(presentIDs))
	args := make([]any, 0, len(presentIDs)+3)
	args = append(args, now, now, chatJID)
	for i, id := range presentIDs {
		ph[i] = "?"
		args = append(args, id)
	}
	q := fmt.Sprintf(`
		UPDATE group_members
		SET present = 0, left_at = ?, updated_at = ?
		WHERE chat_jid = ? AND user_id NOT IN (%s) AND present = 1
	`, strings.Join(ph, ","))
	_, err := g.db.ExecContext(ctx, q, args...)
	return err
}

// ListGroupMembers mengambil anggota satu grup.
func (g *GroupDB) ListGroupMembers(ctx context.Context, chatJID string, onlyPresent bool, limit, offset int) ([]GroupMember, error) {
	if g == nil || g.db == nil {
		return nil, fmt.Errorf("group.db mati")
	}
	if limit <= 0 {
		limit = 100000
	}
	if offset < 0 {
		offset = 0
	}
	q := `SELECT chat_jid, user_id, jid, lid, pn, pushname, is_admin, is_superadmin,
		present, joined_at, left_at, first_seen, last_seen, updated_at
		FROM group_members WHERE chat_jid = ?`
	args := []any{chatJID}
	if onlyPresent {
		q += ` AND present = 1`
	}
	q += ` ORDER BY is_superadmin DESC, is_admin DESC, pn ASC, lid ASC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := g.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GroupMember
	for rows.Next() {
		var m GroupMember
		var admin, super, present int
		if err := rows.Scan(&m.ChatJID, &m.UserID, &m.JID, &m.LID, &m.PN, &m.PushName,
			&admin, &super, &present, &m.JoinedAt, &m.LeftAt, &m.FirstSeen, &m.LastSeen, &m.UpdatedAt); err != nil {
			return nil, err
		}
		m.IsAdmin = admin == 1
		m.IsSuperAdmin = super == 1
		m.Present = present == 1
		out = append(out, m)
	}
	return out, rows.Err()
}

// FindGroupMember mencari anggota grup berdasarkan token (pn/lid/jid/user_id).
func (g *GroupDB) FindGroupMember(ctx context.Context, chatJID, token string) (*GroupMember, error) {
	if g == nil || g.db == nil {
		return nil, fmt.Errorf("group.db mati")
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, fmt.Errorf("token kosong")
	}
	row := g.db.QueryRowContext(ctx, `
		SELECT chat_jid, user_id, jid, lid, pn, pushname, is_admin, is_superadmin,
			present, joined_at, left_at, first_seen, last_seen, updated_at
		FROM group_members
		WHERE chat_jid = ? AND (pn = ? OR lid = ? OR jid = ? OR CAST(user_id AS TEXT) = ?)
		LIMIT 1
	`, chatJID, token, token, token, token)
	var m GroupMember
	var admin, super, present int
	err := row.Scan(&m.ChatJID, &m.UserID, &m.JID, &m.LID, &m.PN, &m.PushName,
		&admin, &super, &present, &m.JoinedAt, &m.LeftAt, &m.FirstSeen, &m.LastSeen, &m.UpdatedAt)
	if err != nil {
		return nil, err
	}
	m.IsAdmin = admin == 1
	m.IsSuperAdmin = super == 1
	m.Present = present == 1
	return &m, nil
}

// CountGroupMembers menghitung anggota grup.
func (g *GroupDB) CountGroupMembers(ctx context.Context, chatJID string, onlyPresent bool) (int, error) {
	if g == nil || g.db == nil {
		return 0, fmt.Errorf("group.db mati")
	}
	q := `SELECT COUNT(*) FROM group_members WHERE chat_jid = ?`
	if onlyPresent {
		q += ` AND present = 1`
	}
	var n int
	err := g.db.QueryRowContext(ctx, q, chatJID).Scan(&n)
	return n, err
}
