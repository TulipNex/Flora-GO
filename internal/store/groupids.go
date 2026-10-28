package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// GroupIDEntry adalah satu baris indeks grup permanen.
type GroupIDEntry struct {
	Seq     int
	ChatJID string
	Name    string
	Present bool
}

// UpsertGroupID menyimpan/memperbarui satu grup di indeks, mengembalikan seq.
func (g *GroupDB) UpsertGroupID(ctx context.Context, chatJID, name string) (int, error) {
	if g == nil || g.db == nil {
		return 0, fmt.Errorf("group.db mati")
	}
	now := time.Now().Unix()
	_, err := g.db.ExecContext(ctx, `
		INSERT INTO group_ids (chat_jid, name, present, updated_at)
		VALUES (?, ?, 1, ?)
		ON CONFLICT(chat_jid) DO UPDATE SET
			name = excluded.name,
			present = 1,
			updated_at = excluded.updated_at
	`, chatJID, name, now)
	if err != nil {
		return 0, err
	}
	var seq int
	if err := g.db.QueryRowContext(ctx, `SELECT seq FROM group_ids WHERE chat_jid = ?`, chatJID).Scan(&seq); err != nil {
		return 0, err
	}
	return seq, nil
}

// MarkGroupIDsAbsent menandai grup yang tidak ada di daftar present sebagai keluar.
func (g *GroupDB) MarkGroupIDsAbsent(ctx context.Context, present []string) error {
	if g == nil || g.db == nil || len(present) == 0 {
		return nil
	}
	ph := make([]string, len(present))
	args := []any{}
	for i, jid := range present {
		ph[i] = "?"
		args = append(args, jid)
	}
	q := fmt.Sprintf(`UPDATE group_ids SET present = 0, updated_at = ? WHERE chat_jid NOT IN (%s)`, strings.Join(ph, ","))
	args = append([]any{time.Now().Unix()}, args...)
	_, err := g.db.ExecContext(ctx, q, args...)
	return err
}

// ListGroupIDs mengembalikan indeks grup (urut seq).
func (g *GroupDB) ListGroupIDs(ctx context.Context, onlyPresent bool) ([]GroupIDEntry, error) {
	if g == nil || g.db == nil {
		return nil, fmt.Errorf("group.db mati")
	}
	q := `SELECT seq, chat_jid, name, present FROM group_ids`
	if onlyPresent {
		q += ` WHERE present = 1`
	}
	q += ` ORDER BY seq ASC`
	rows, err := g.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GroupIDEntry
	for rows.Next() {
		var e GroupIDEntry
		var present int
		if err := rows.Scan(&e.Seq, &e.ChatJID, &e.Name, &present); err != nil {
			return nil, err
		}
		e.Present = present == 1
		out = append(out, e)
	}
	return out, rows.Err()
}

// GroupIDBySeq mencari grup berdasarkan nomor ID.
func (g *GroupDB) GroupIDBySeq(ctx context.Context, seq int) (*GroupIDEntry, error) {
	if g == nil || g.db == nil {
		return nil, fmt.Errorf("group.db mati")
	}
	row := g.db.QueryRowContext(ctx, `SELECT seq, chat_jid, name, present FROM group_ids WHERE seq = ?`, seq)
	var e GroupIDEntry
	var present int
	if err := row.Scan(&e.Seq, &e.ChatJID, &e.Name, &present); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	e.Present = present == 1
	return &e, nil
}

// CountGroupIDs menghitung jumlah grup terindeks.
func (g *GroupDB) CountGroupIDs(ctx context.Context) (int, error) {
	if g == nil || g.db == nil {
		return 0, fmt.Errorf("group.db mati")
	}
	var n int
	err := g.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM group_ids`).Scan(&n)
	return n, err
}
