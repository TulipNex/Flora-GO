package store

import (
	"context"
	"time"
)

// File ini menyimpan PENGINGAT Flora (AI tool reminder) supaya TAHAN
// RESTART — pola persis scheduledcalls.go (call terjadwal): tabel dibuat di
// InitGroupDB (groupdb.go, CREATE TABLE IF NOT EXISTS sehingga deployment
// lama otomatis dapat tabel baru tanpa migrasi khusus), reuse koneksi
// group.db.
//
// Bedanya dengan call: reminder terikat PEMBUAT (creator_jid, multi-owner)
// dan dikirim ke DM pembuat, bukan chat asal.

// Status pengingat yang dikenal.
const (
	ReminderPending   = "pending"
	ReminderDone      = "done"
	ReminderMissed    = "missed"
	ReminderCancelled = "cancelled"
)

// Reminder adalah satu baris pengingat.
type Reminder struct {
	ID           int64
	ChatJID      string // chat asal permintaan (konteks, tampil di list)
	CreatorJID   string // JID owner pembuat (kunci DM tujuan + filter milik)
	CreatorPhone string // nomor HP ter-resolve saat set (untuk eskalasi telepon; "" bila tak terpetakan)
	Message      string // isi inti pengingat (dibuat natural saat fire)
	TriggerAt    int64  // unix timestamp zona absolut
	CallAfter    bool
	Status       string
}

// AddReminder menyimpan pengingat baru, mengembalikan ID-nya.
func (g *GroupDB) AddReminder(ctx context.Context, chatJID, creatorJID, creatorPhone, message string, triggerAt int64, callAfter bool) (int64, error) {
	callInt := 0
	if callAfter {
		callInt = 1
	}
	res, err := g.db.ExecContext(ctx, `
		INSERT INTO reminders (chat_jid, creator_jid, creator_phone, message, trigger_at, call_after, status)
		VALUES (?, ?, ?, ?, ?, ?, 'pending')
	`, chatJID, creatorJID, creatorPhone, message, triggerAt, callInt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListPendingReminders mengembalikan semua pengingat pending, urut waktu
// terdekat dulu. creatorJID kosong = semua kreator; terisi = filter milik
// pembuat itu saja (untuk list/cancel/edit per-owner).
func (g *GroupDB) ListPendingReminders(ctx context.Context, creatorJID string) ([]Reminder, error) {
	query := `
		SELECT id, chat_jid, creator_jid, creator_phone, message, trigger_at, call_after, status FROM reminders
		WHERE status = 'pending'
	`
	args := []interface{}{}
	if creatorJID != "" {
		query += ` AND creator_jid = ?`
		args = append(args, creatorJID)
	}
	query += ` ORDER BY trigger_at ASC`
	rows, err := g.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Reminder
	for rows.Next() {
		var r Reminder
		var callInt int
		if err := rows.Scan(&r.ID, &r.ChatJID, &r.CreatorJID, &r.CreatorPhone, &r.Message, &r.TriggerAt, &callInt, &r.Status); err != nil {
			return nil, err
		}
		r.CallAfter = callInt != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// CountPendingReminders menghitung pengingat pending di satu chat (untuk
// batas anti-spam per chat).
func (g *GroupDB) CountPendingReminders(ctx context.Context, chatJID string) (int, error) {
	var n int
	err := g.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM reminders WHERE status = 'pending' AND chat_jid = ?
	`, chatJID).Scan(&n)
	return n, err
}

// MarkReminderStatus mengubah status pengingat HANYA bila masih pending,
// mengembalikan true bila baris benar berubah. Guard AND status='pending'
// WAJIB (audit H1): tanpa ini, cancel/edit yang masuk di milidetik antara
// baca-baris dan tandai-done tidak menghalangi fire — pengingat yang
// dibatalkan tetap terkirim (+eskalasi telepon). Pemanggil yang dapat false
// harus ABORT diam-diam (timer basi / kalah balapan).
func (g *GroupDB) MarkReminderStatus(ctx context.Context, id int64, status string) (bool, error) {
	res, err := g.db.ExecContext(ctx, `UPDATE reminders SET status = ? WHERE id = ? AND status = 'pending'`, status, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// CancelReminder membatalkan pengingat milik creator yang masih pending.
// Mengembalikan true kalau memang ada baris yang dibatalkan.
func (g *GroupDB) CancelReminder(ctx context.Context, id int64, creatorJID string) (bool, error) {
	res, err := g.db.ExecContext(ctx, `
		UPDATE reminders SET status = 'cancelled' WHERE id = ? AND creator_jid = ? AND status = 'pending'
	`, id, creatorJID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// UpdateReminder mengubah pesan/waktu/call pengingat milik creator yang
// masih pending. Field kosong/nil = tidak diubah (untuk waktu, 0 = tidak
// diubah; untuk call, gunakan callSet sebagai penanda eksplisit).
func (g *GroupDB) UpdateReminder(ctx context.Context, id int64, creatorJID, message string, triggerAt int64, callAfter bool, callSet bool) (bool, error) {
	setParts := []string{}
	args := []interface{}{}
	if message != "" {
		setParts = append(setParts, "message = ?")
		args = append(args, message)
	}
	if triggerAt > 0 {
		setParts = append(setParts, "trigger_at = ?")
		args = append(args, triggerAt)
	}
	if callSet {
		callInt := 0
		if callAfter {
			callInt = 1
		}
		setParts = append(setParts, "call_after = ?")
		args = append(args, callInt)
	}
	if len(setParts) == 0 {
		return false, nil
	}
	query := "UPDATE reminders SET " + joinComma(setParts) + " WHERE id = ? AND creator_jid = ? AND status = 'pending'"
	args = append(args, id, creatorJID)
	res, err := g.db.ExecContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ReminderStatus membaca status terkini (dipakai timer sebelum fire supaya
// pengingat yang keburu di-cancel/diubah tidak jalan dengan data basi).
func (g *GroupDB) ReminderStatus(ctx context.Context, id int64) (string, error) {
	var status string
	err := g.db.QueryRowContext(ctx, `SELECT status FROM reminders WHERE id = ?`, id).Scan(&status)
	return status, err
}

// GetReminder mengambil satu pengingat milik creator (untuk validasi edit).
func (g *GroupDB) GetReminder(ctx context.Context, id int64, creatorJID string) (*Reminder, error) {
	var r Reminder
	var callInt int
	err := g.db.QueryRowContext(ctx, `
		SELECT id, chat_jid, creator_jid, creator_phone, message, trigger_at, call_after, status FROM reminders
		WHERE id = ? AND creator_jid = ?
	`, id, creatorJID).Scan(&r.ID, &r.ChatJID, &r.CreatorJID, &r.CreatorPhone, &r.Message, &r.TriggerAt, &callInt, &r.Status)
	if err != nil {
		return nil, err
	}
	r.CallAfter = callInt != 0
	return &r, nil
}

// GetReminderByID mengambil satu pengingat tanpa filter creator — khusus
// dipakai timer/scheduler internal (fire selalu baca ulang baris supaya
// edit yang terjadi setelah timer dipasang tidak memakai data basi).
func (g *GroupDB) GetReminderByID(ctx context.Context, id int64) (*Reminder, error) {
	var r Reminder
	var callInt int
	err := g.db.QueryRowContext(ctx, `
		SELECT id, chat_jid, creator_jid, creator_phone, message, trigger_at, call_after, status FROM reminders
		WHERE id = ?
	`, id).Scan(&r.ID, &r.ChatJID, &r.CreatorJID, &r.CreatorPhone, &r.Message, &r.TriggerAt, &callInt, &r.Status)
	if err != nil {
		return nil, err
	}
	r.CallAfter = callInt != 0
	return &r, nil
}

// joinComma menggabungkan bagian SET tanpa import strings (file ini
// disengaja minim dependensi, cerminan gaya scheduledcalls.go).
func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

// pruneCutoff adalah batas umur baris selesai: jadwal lebih tua dari ini
// dan status sudah bukan pending → dihapus permanen (keputusan owner:
// tidak ada toleransi simpan riwayat).
const pruneCutoff = 24 * time.Hour

// PruneOldReminders menghapus permanen baris selesai (done/missed/cancelled)
// yang jadwalnya lebih dari 24 jam lewat. Baris pending TIDAK PERNAH
// tersentuh walau jadwalnya aneh. Mengembalikan jumlah baris terhapus.
// Dipanggil saat startup (RestoreReminders) + tiap fire sukses supaya bot
// yang jalan berminggu-minggu tanpa restart tetap bersih.
func (g *GroupDB) PruneOldReminders(ctx context.Context) (int64, error) {
	res, err := g.db.ExecContext(ctx, `
		DELETE FROM reminders WHERE status != 'pending' AND trigger_at < ?
	`, time.Now().Add(-pruneCutoff).Unix())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}
