package store

import (
	"context"
	"time"
)

// File ini menyimpan JADWAL PANGGILAN (.voicecall <target>|<jam> dan tool AI
// make_call terjadwal) supaya TAHAN RESTART — beda dari implementasi lama
// yang in-memory dan hilang kalau proses mati. Tabel dibuat di InitGroupDB
// (groupdb.go), reuse koneksi group.db.

// Status jadwal panggilan yang dikenal.
const (
	ScheduledCallPending   = "pending"
	ScheduledCallDone      = "done"
	ScheduledCallMissed    = "missed"
	ScheduledCallCancelled = "cancelled"
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

// CancelScheduledCall membatalkan jadwal yang masih pending (jadi cancelled).
// Mengembalikan true kalau memang ada baris pending yang dibatalkan — false
// berarti ID tidak dikenal ATAU sudah bukan pending (done/missed/cancelled).
func (g *GroupDB) CancelScheduledCall(ctx context.Context, id int64) (bool, error) {
	res, err := g.db.ExecContext(ctx, `UPDATE scheduled_calls SET status = 'cancelled' WHERE id = ? AND status = 'pending'`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// CallStatus membaca status jadwal terkini (dipakai timer sebelum menelpon
// supaya jadwal yang keburu di-cancel tidak jalan — lihat armScheduledCall).
func (g *GroupDB) CallStatus(ctx context.Context, id int64) (string, error) {
	var status string
	err := g.db.QueryRowContext(ctx, `SELECT status FROM scheduled_calls WHERE id = ?`, id).Scan(&status)
	if err != nil {
		return "", err
	}
	return status, nil
}

// PruneOldCalls menghapus permanen baris selesai (done/missed/cancelled)
// yang jadwalnya lebih dari 24 jam lewat — cermin PruneOldReminders
// (keputusan owner berlaku untuk call juga). Baris pending tidak tersentuh.
// Dipanggil saat startup (RestoreScheduledCalls) + tiap eksekusi jadwal.
func (g *GroupDB) PruneOldCalls(ctx context.Context) (int64, error) {
	res, err := g.db.ExecContext(ctx, `
		DELETE FROM scheduled_calls WHERE status != 'pending' AND scheduled_at < ?
	`, time.Now().Add(-24*time.Hour).Unix())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}
