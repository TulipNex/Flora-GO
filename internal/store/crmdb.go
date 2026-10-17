package store

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/polymorfa/hypermeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

// Arsip pesan ala FIORA/CRMStore (diilhami struktur tabel + retensinya,
// ditulis ulang penuh dalam Go — tanpa menyalin kode aslinya): setiap pesan
// masuk (dan teks keluar bot) disimpan sebagai byte proto + metadata
// (id/chat/pengirim/waktu) supaya bisa dilihat lagi via .lastchat/.crm/
// .insp. BUKAN arsip media (byte gambar/video tidak disimpan, hanya pesan
// protobuf-nya yang kecil).
//
// Batasan sadar: tabel messages SAJA (tanpa tabel nodes mentah WS —
// whatsmeow tidak mengekspos node mentah ke handler, jadi tidak ada yang
// bisa disimpan), retensi 200 baris GLOBAL terbaru (keputusan owner),
// dan kiriman media langsung bot tidak ikut terarsip (hanya helper teks
// terpusat yang menulis lewat antrian arsip async).
//
// Penulisan dipusatkan: pesan masuk & keluar sama-sama masuk SATU antrian
// yang dilayani satu worker (lihat startArchiveWorker), bukan goroutine baru
// per pesan — jalur panas tak pernah menunggu I/O dan tak menumbuhkan
// goroutine tanpa batas saat pesan banjir.
//
// Fail-soft total: Init gagal = arsip mati, bot tetap jalan (CRM() nil,
// semua method nil-guard). Arsip forensik tidak boleh membunuh bot.

// crmMaxRows adalah cap global isi tabel (keputusan owner: 200 terbaru).
const crmMaxRows = 200

// CRMDB membungkus koneksi SQLite khusus arsip pesan (data/crm.db).
// Terpisah dari session/group/ai/finance supaya tidak saling mengganggu.
type CRMDB struct {
	db *sql.DB
}

// crmDB adalah instance global, diisi sekali lewat InitCRMDB di main.go.
// Boleh tetap nil (init gagal) — semua method nil-guard.
var crmDB *CRMDB

// InitCRMDB membuka (atau membuat) data/crm.db + tabel messages. Dipanggil
// sekali di main.go. Error dikembalikan ke main yang memutuskan fail-soft
// (Warn + lanjut tanpa arsip), TIDAK panic seperti DB operasional.
func InitCRMDB(path string) error {
	db, err := openSQLite(path)
	if err != nil {
		return fmt.Errorf("gagal membuka crm.db: %w", err)
	}
	schema := `
	CREATE TABLE IF NOT EXISTS crm_messages (
		rowid INTEGER PRIMARY KEY AUTOINCREMENT,
		id TEXT NOT NULL,
		chat TEXT NOT NULL,
		sender TEXT NOT NULL DEFAULT '',
		ts INTEGER NOT NULL DEFAULT 0,
		data BLOB NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_crm_chat_ts ON crm_messages(chat, ts);
	CREATE INDEX IF NOT EXISTS idx_crm_id ON crm_messages(id);
	`
	if err := applySchema(db, "crm.db", schema, nil, crmSchemaVersion); err != nil {
		return err
	}
	crmDB = &CRMDB{db: db}
	// Sapu startup: pastikan tidak melebihi cap sejak awal (mis. DB lama).
	if err := crmDB.Prune(); err != nil {
		return fmt.Errorf("gagal prune awal crm.db: %w", err)
	}
	// Nyalakan satu worker arsip yang melayani seluruh penulisan berikutnya.
	startArchiveWorker(crmDB)
	return nil
}

// CRM mengembalikan instance arsip global, atau nil bila arsip mati
// (init gagal / belum init). Pemanggil WAJIB tahan terhadap nil.
func CRM() *CRMDB {
	return crmDB
}

// CRMRow adalah satu baris arsip. Data adalah byte proto waE2E.Message
// (gunakan .Message() untuk decode).
type CRMRow struct {
	Rowid  int64
	ID     string
	Chat   string
	Sender string
	TS     int64
	Data   []byte
}

// Message men-decode Data menjadi pesan protobuf.
func (r *CRMRow) Message() (*waE2E.Message, error) {
	var msg waE2E.Message
	if err := proto.Unmarshal(r.Data, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

// saveRaw menyimpan satu pesan (byte proto yang SUDAH di-marshal) + memangkas
// ke cap. Hanya dipanggil worker arsip (lihat startArchiveWorker).
func (c *CRMDB) saveRaw(chat, id, sender string, ts int64, raw []byte) error {
	if c == nil || c.db == nil || len(raw) == 0 || id == "" || chat == "" {
		return nil
	}
	if _, err := c.db.ExecContext(context.Background(),
		`INSERT INTO crm_messages (id, chat, sender, ts, data) VALUES (?, ?, ?, ?, ?)`,
		id, chat, sender, ts, raw); err != nil {
		return err
	}
	return c.Prune()
}

// save menyimpan satu pesan secara SINKRON (marshal + saveRaw). Dipakai jalur
// non-panas / pengujian; jalur pesan biasa memakai antrian (enqueueArchive).
func (c *CRMDB) save(chat, id, sender string, ts int64, msg *waE2E.Message) error {
	if c == nil || c.db == nil || msg == nil || id == "" || chat == "" {
		return nil
	}
	raw, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	return c.saveRaw(chat, id, sender, ts, raw)
}

// --- Penulisan arsip terpusat (antrian + satu worker) ---

// archiveQueueSize adalah kapasitas antrian arsip. Cukup untuk melancarkan
// lonjakan sesaat; lebih dari ini arsip sengaja ketinggalan (best-effort).
const archiveQueueSize = 256

// archiveJob menyimpan byte proto yang SUDAH di-marshal supaya worker tidak
// memegang pointer protobuf milik evt/pesan yang bisa dipakai ulang whatsmeow.
type archiveJob struct {
	chat, id, sender string
	ts               int64
	raw              []byte
}

var (
	archiveCh   chan archiveJob
	archiveOnce sync.Once
)

// startArchiveWorker menyalakan worker singleton arsip sekali saja. Satu
// worker = penulisan terserialisasi (tak ada rebutan write-lock WAL antar
// pesan) dan jumlah goroutine tetap, apa pun laju pesan.
func startArchiveWorker(c *CRMDB) {
	archiveOnce.Do(func() {
		archiveCh = make(chan archiveJob, archiveQueueSize)
		go func() {
			for job := range archiveCh {
				_ = c.saveRaw(job.chat, job.id, job.sender, job.ts, job.raw)
			}
		}()
	})
}

// enqueueArchive memasukkan satu job tanpa memblokir. Marshal gagal atau
// antrian penuh = job dibuang (arsip best-effort, tak pernah fatal).
func enqueueArchive(chat, id, sender string, ts int64, msg *waE2E.Message) {
	if crmDB == nil || archiveCh == nil || msg == nil || id == "" || chat == "" {
		return
	}
	raw, err := proto.Marshal(msg)
	if err != nil {
		return
	}
	select {
	case archiveCh <- archiveJob{chat: chat, id: id, sender: sender, ts: ts, raw: raw}:
	default:
	}
}

// ArchiveIncomingMessage mengarsipkan satu pesan MASUK (sudah di-unwrap
// pemanggil) lewat antrian — non-blocking, aman di jalur handler.
func ArchiveIncomingMessage(chat, id, sender string, ts int64, msg *waE2E.Message) {
	enqueueArchive(chat, id, sender, ts, msg)
}

// ArchiveOutgoingMessage mengarsipkan satu pesan TEKS KELUAR bot lewat
// antrian — non-blocking, jadi pemanggil TIDAK perlu lagi membungkusnya
// dengan `go`. Pengirim dicatat literal "bot".
func ArchiveOutgoingMessage(chatJID, msgID string, msg *waE2E.Message) {
	enqueueArchive(chatJID, msgID, "bot", nowUnix(), msg)
}

// Prune memangkas tabel ke crmMaxRows terbaru (global, semua chat).
func (c *CRMDB) Prune() error {
	if c == nil || c.db == nil {
		return nil
	}
	_, err := c.db.Exec(
		`DELETE FROM crm_messages WHERE rowid NOT IN (
			SELECT rowid FROM crm_messages ORDER BY rowid DESC LIMIT ?
		)`, crmMaxRows)
	return err
}

// Count mengembalikan jumlah baris arsip (untuk baris statistik usage).
func (c *CRMDB) Count() int64 {
	if c == nil || c.db == nil {
		return 0
	}
	var n int64
	if err := c.db.QueryRow(`SELECT COUNT(*) FROM crm_messages`).Scan(&n); err != nil {
		return 0
	}
	return n
}

// scanCRMRows memindai baris SELECT berkolom (rowid, id, chat, sender, ts,
// data) — dipakai semua loader di bawah.
func scanCRMRows(rows *sql.Rows) ([]CRMRow, error) {
	defer rows.Close()
	var out []CRMRow
	for rows.Next() {
		var r CRMRow
		if err := rows.Scan(&r.Rowid, &r.ID, &r.Chat, &r.Sender, &r.TS, &r.Data); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LoadByRowid mengambil satu baris via rowid (untuk `.crm #123`).
func (c *CRMDB) LoadByRowid(rowid int64) (*CRMRow, error) {
	if c == nil || c.db == nil {
		return nil, fmt.Errorf("arsip mati")
	}
	rows, err := c.db.Query(
		`SELECT rowid, id, chat, sender, ts, data FROM crm_messages WHERE rowid = ? LIMIT 1`, rowid)
	if err != nil {
		return nil, err
	}
	out, err := scanCRMRows(rows)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("tidak ditemukan")
	}
	return &out[0], nil
}

// LoadByID mengambil pesan terbaru dengan ID di chat tertentu (untuk reply
// ke bubble: StanzaID + chat room).
func (c *CRMDB) LoadByID(id, chat string) (*CRMRow, error) {
	if c == nil || c.db == nil || id == "" || chat == "" {
		return nil, fmt.Errorf("arsip mati")
	}
	rows, err := c.db.Query(
		`SELECT rowid, id, chat, sender, ts, data FROM crm_messages
		 WHERE id = ? AND chat = ? ORDER BY rowid DESC LIMIT 1`, id, chat)
	if err != nil {
		return nil, err
	}
	out, err := scanCRMRows(rows)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("tidak ditemukan")
	}
	return &out[0], nil
}

// RecentByChat mengambil N pesan terbaru satu chat (terbaru dulu) untuk
// `.lastchat`. limit dijepit 1..100 oleh pemanggil.
func (c *CRMDB) RecentByChat(chat string, limit int) ([]CRMRow, error) {
	if c == nil || c.db == nil || chat == "" {
		return nil, fmt.Errorf("arsip mati")
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 100 {
		limit = 100
	}
	rows, err := c.db.Query(
		`SELECT rowid, id, chat, sender, ts, data FROM crm_messages
		 WHERE chat = ? ORDER BY rowid DESC LIMIT ?`, chat, limit)
	if err != nil {
		return nil, err
	}
	return scanCRMRows(rows)
}

// nowUnix mengembalikan epoch detik sekarang (dipakai stamp arsip keluar).
func nowUnix() int64 {
	return time.Now().Unix()
}
