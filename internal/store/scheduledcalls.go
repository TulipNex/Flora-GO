package store

import (
	"context"
)

// File ini menyimpan JADWAL PANGGILAN (.call <target>|<jam> dan tool AI
// make_call terjadwal) supaya TAHAN RESTART — beda dari implementasi lama
// yang in-memory dan hilang kalau proses mati. Tabel dibuat di InitGroupDB
// (groupdb.go), reuse koneksi group.db.

// Status jadwal panggilan yang dikenal.
const (
	ScheduledCallPending = "pending"
	ScheduledCallDone    = "done"
	ScheduledCallMissed  = "missed"
)

// ScheduledCall adalah satu baris jadwal panggilan.
type ScheduledCall struct {
	ID          int64
	TargetPhone string
	ChatJID     string
	ScheduledAt int64 // unix timestamp (zona absolut, tidak tergantung TZ)
	Status      string
}

// AddScheduledCall menyimpan jadwal baru, mengembalikan ID-nya.
func (g *GroupDB) AddScheduledCall(ctx context.Context, phone, chatJID string, atUnix int64) (int64, error) {
	res, err := g.db.ExecContext(ctx, `
		INSERT INTO scheduled_calls (target_phone, chat_jid, scheduled_at, status)
		VALUES (?, ?, ?, 'pending')
	`, phone, chatJID, atUnix)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListPendingCalls mengembalikan semua jadwal yang masih pending, urut
// waktu terdekat dulu. Dipakai RestoreScheduledCalls saat startup.
func (g *GroupDB) ListPendingCalls(ctx context.Context) ([]ScheduledCall, error) {
	rows, err := g.db.QueryContext(ctx, `
		SELECT id, target_phone, chat_jid, scheduled_at, status FROM scheduled_calls
		WHERE status = 'pending' ORDER BY scheduled_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScheduledCall
	for rows.Next() {
		var c ScheduledCall
		if err := rows.Scan(&c.ID, &c.TargetPhone, &c.ChatJID, &c.ScheduledAt, &c.Status); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// MarkCallStatus mengubah status jadwal (done/missed). Status ditulis SEBELUM
// panggilan dijalankan (bukan sesudah) supaya jadwal yang sama tidak pernah
// jalan dobel — mis. kalau proses crash tepat di tengah panggilan.
func (g *GroupDB) MarkCallStatus(ctx context.Context, id int64, status string) error {
	_, err := g.db.ExecContext(ctx, `UPDATE scheduled_calls SET status = ? WHERE id = ?`, status, id)
	return err
}
