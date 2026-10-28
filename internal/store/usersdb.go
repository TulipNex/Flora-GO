package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"wabot-go/internal/identity"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/types"
)

// UsersDB membungkus koneksi SQLite khusus direktori identitas user
// (data/users.db). Terpisah dari group.db supaya satu user cukup satu baris
// global, walau dia member banyak grup / dilayani banyak clone.
type UsersDB struct {
	db *sql.DB
}

var usersDB *UsersDB

// InitUsersDB membuka (atau membuat) data/users.db + tabel users. Dipanggil
// sekali di main.go. Fail-soft di luar (main yang memutuskan), tapi method
// di sini nil-guard supaya tetap aman kalau init gagal.
func InitUsersDB(path string) error {
	db, err := openSQLite(path)
	if err != nil {
		return fmt.Errorf("gagal membuka users.db: %w", err)
	}
	schema := `
	CREATE TABLE IF NOT EXISTS users (
		id             INTEGER PRIMARY KEY AUTOINCREMENT,
		lid            TEXT UNIQUE,
		pn             TEXT UNIQUE,
		jid            TEXT NOT NULL DEFAULT '',
		pushname       TEXT NOT NULL DEFAULT '',
		full_name      TEXT NOT NULL DEFAULT '',
		first_name     TEXT NOT NULL DEFAULT '',
		username       TEXT NOT NULL DEFAULT '',
		redacted_phone TEXT NOT NULL DEFAULT '',
		pp_status      TEXT NOT NULL DEFAULT '',
		pp_id          TEXT NOT NULL DEFAULT '',
		pp_sha         TEXT NOT NULL DEFAULT '',
		pp_path        TEXT NOT NULL DEFAULT '',
		pp_bytes       INTEGER NOT NULL DEFAULT 0,
		pp_at          INTEGER NOT NULL DEFAULT 0,
		first_seen     INTEGER NOT NULL,
		last_seen      INTEGER NOT NULL,
		seen_count     INTEGER NOT NULL DEFAULT 0,
		created_at     INTEGER NOT NULL,
		updated_at     INTEGER NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_users_pn  ON users(pn);
	CREATE INDEX IF NOT EXISTS idx_users_lid ON users(lid);
	CREATE INDEX IF NOT EXISTS idx_users_jid ON users(jid);
	`
	if err := applySchema(db, "users.db", schema, nil, usersSchemaVersion); err != nil {
		_ = db.Close()
		return err
	}
	usersDB = &UsersDB{db: db}
	return nil
}

// Users mengembalikan instance global, atau nil bila init gagal/belum init.
func Users() *UsersDB {
	return usersDB
}

// UserRecord adalah satu baris users yang sudah di-decode.
type UserRecord struct {
	ID            int64
	LID           string
	PN            string
	JID           string
	PushName      string
	FullName      string
	FirstName     string
	Username      string
	RedactedPhone string
	PPStatus      string
	PPID          string
	PPSha         string
	PPPath        string
	PPBytes       int
	PPAt          int64
	FirstSeen     int64
	LastSeen      int64
	SeenCount     int
	CreatedAt     int64
	UpdatedAt     int64
}

// UserIdentity adalah input minimal untuk upsert: identitas mentah yang
// mungkin belum lengkap (LID/PN salah satu kosong). Upsert akan melengkapi
// pasangan format lewat peta LID↔PN whatsmeow kalau client tersedia.
type UserIdentity struct {
	LID           string
	PN            string
	JID           string
	PushName      string
	FullName      string
	FirstName     string
	Username      string
	RedactedPhone string
	Client        *whatsmeow.Client
	Ctx           context.Context
}

// UpsertUserIdentity menyimpan/memperbarui satu identitas user. Mengembalikan
// id baris. Fail-soft: error dikembalikan, pemanggil yang memutuskan.
func (u *UsersDB) UpsertUserIdentity(ctx context.Context, in UserIdentity) (int64, error) {
	if u == nil || u.db == nil {
		return 0, fmt.Errorf("users.db mati")
	}
	// Lengkapi pasangan LID↔PN lewat peta whatsmeow (fail-closed).
	lid := strings.TrimSpace(in.LID)
	pn := strings.TrimSpace(in.PN)
	if in.Client != nil {
		if lid != "" && pn == "" {
			if j, ok := identity.ResolvePN(ctx, in.Client, types.NewJID(lid, types.HiddenUserServer), types.JID{}); ok {
				pn = j.User
			}
		}
		if pn != "" && lid == "" {
			if j, ok := identity.ResolveLID(ctx, in.Client, types.NewJID(pn, types.DefaultUserServer), types.JID{}); ok {
				lid = j.User
			}
		}
		// Cadangan langsung ke store map (Resolve* sudah mencoba, ini belt-and-braces).
		if lid != "" && pn == "" && in.Client.Store != nil && in.Client.Store.LIDs != nil {
			if alt, err := in.Client.Store.GetAltJID(ctx, types.NewJID(lid, types.HiddenUserServer)); err == nil && !alt.IsEmpty() && alt.Server == types.DefaultUserServer {
				pn = alt.User
			}
		}
		if pn != "" && lid == "" && in.Client.Store != nil && in.Client.Store.LIDs != nil {
			if alt, err := in.Client.Store.GetAltJID(ctx, types.NewJID(pn, types.DefaultUserServer)); err == nil && !alt.IsEmpty() && alt.Server == types.HiddenUserServer {
				lid = alt.User
			}
		}
	}
	jid := strings.TrimSpace(in.JID)
	pushname := strings.TrimSpace(in.PushName)
	fullName := strings.TrimSpace(in.FullName)
	firstName := strings.TrimSpace(in.FirstName)
	username := strings.TrimSpace(in.Username)
	redacted := strings.TrimSpace(in.RedactedPhone)
	now := time.Now().Unix()

	tx, err := u.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	// Cari baris yang cocok (LID atau PN atau JID).
	rows, err := tx.QueryContext(ctx, `
		SELECT id, lid, pn, jid, pushname, full_name, first_name, username, redacted_phone
		FROM users
		WHERE (lid IS NOT NULL AND lid = ?)
		   OR (pn IS NOT NULL AND pn = ?)
		   OR (jid <> '' AND jid = ?)
	`, nullable(lid), nullable(pn), jid)
	if err != nil {
		return 0, err
	}
	type hit struct {
		id                                                              int64
		lid, pn, jid, pushname, fullName, firstName, username, redacted string
	}
	var hits []hit
	for rows.Next() {
		var h hit
		var nl, np sql.NullString
		if err := rows.Scan(&h.id, &nl, &np, &h.jid, &h.pushname, &h.fullName, &h.firstName, &h.username, &h.redacted); err != nil {
			rows.Close()
			return 0, err
		}
		if nl.Valid {
			h.lid = nl.String
		}
		if np.Valid {
			h.pn = np.String
		}
		hits = append(hits, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	// 0 baris → INSERT.
	if len(hits) == 0 {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO users (lid, pn, jid, pushname, full_name, first_name, username, redacted_phone,
				first_seen, last_seen, seen_count, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
		`, nullable(lid), nullable(pn), jid, pushname, fullName, firstName, username, redacted, now, now, now, now)
		if err != nil {
			return 0, err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return 0, err
		}
		if err := tx.Commit(); err != nil {
			return 0, err
		}
		return id, nil
	}

	// Pilih baris yang dipertahankan (id terkecil), merge field dari yang lain.
	keep := hits[0]
	for _, h := range hits[1:] {
		if h.id < keep.id {
			keep = h
		}
	}
	// Isi field kosong dari hit lain.
	for _, h := range hits {
		if keep.lid == "" {
			keep.lid = h.lid
		}
		if keep.pn == "" {
			keep.pn = h.pn
		}
		if keep.jid == "" {
			keep.jid = h.jid
		}
		if keep.pushname == "" {
			keep.pushname = h.pushname
		}
		if keep.fullName == "" {
			keep.fullName = h.fullName
		}
		if keep.firstName == "" {
			keep.firstName = h.firstName
		}
		if keep.username == "" {
			keep.username = h.username
		}
		if keep.redacted == "" {
			keep.redacted = h.redacted
		}
	}
	// Timpa dengan input bila non-kosong.
	if lid != "" {
		keep.lid = lid
	}
	if pn != "" {
		keep.pn = pn
	}
	if jid != "" {
		keep.jid = jid
	}
	if pushname != "" {
		keep.pushname = pushname
	}
	if fullName != "" {
		keep.fullName = fullName
	}
	if firstName != "" {
		keep.firstName = firstName
	}
	if username != "" {
		keep.username = username
	}
	if redacted != "" {
		keep.redacted = redacted
	}

	// Hapus duplikat.
	for _, h := range hits {
		if h.id == keep.id {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, h.id); err != nil {
			return 0, err
		}
	}

	// Update baris yang dipertahankan.
	if _, err := tx.ExecContext(ctx, `
		UPDATE users SET
			lid = ?, pn = ?, jid = ?, pushname = ?, full_name = ?, first_name = ?,
			username = ?, redacted_phone = ?, last_seen = ?, seen_count = seen_count + 1,
			updated_at = ?
		WHERE id = ?
	`, nullable(keep.lid), nullable(keep.pn), keep.jid, keep.pushname, keep.fullName,
		keep.firstName, keep.username, keep.redacted, now, now, keep.id); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return keep.id, nil
}

// GetUser mengambil satu user via id.
func (u *UsersDB) GetUser(ctx context.Context, id int64) (*UserRecord, error) {
	if u == nil || u.db == nil {
		return nil, fmt.Errorf("users.db mati")
	}
	row := u.db.QueryRowContext(ctx, `
		SELECT id, lid, pn, jid, pushname, full_name, first_name, username, redacted_phone,
			pp_status, pp_id, pp_sha, pp_path, pp_bytes, pp_at, first_seen, last_seen,
			seen_count, created_at, updated_at
		FROM users WHERE id = ?`, id)
	return scanUserRow(row)
}

func (u *UsersDB) FindUserByLID(ctx context.Context, lid string) (*UserRecord, error) {
	return u.findUserBy(ctx, "lid", strings.TrimSpace(lid))
}

func (u *UsersDB) FindUserByPN(ctx context.Context, pn string) (*UserRecord, error) {
	return u.findUserBy(ctx, "pn", strings.TrimSpace(pn))
}

func (u *UsersDB) FindUserByJID(ctx context.Context, jid string) (*UserRecord, error) {
	return u.findUserBy(ctx, "jid", strings.TrimSpace(jid))
}

func (u *UsersDB) findUserBy(ctx context.Context, col, val string) (*UserRecord, error) {
	if u == nil || u.db == nil {
		return nil, fmt.Errorf("users.db mati")
	}
	if val == "" {
		return nil, fmt.Errorf("nilai kosong")
	}
	// col sudah terbatas (whitelist) — aman.
	q := fmt.Sprintf(`SELECT id, lid, pn, jid, pushname, full_name, first_name, username, redacted_phone,
		pp_status, pp_id, pp_sha, pp_path, pp_bytes, pp_at, first_seen, last_seen,
		seen_count, created_at, updated_at
		FROM users WHERE %s = ? LIMIT 1`, col)
	row := u.db.QueryRowContext(ctx, q, val)
	return scanUserRow(row)
}

// SearchUsers mencari user berdasarkan nama/nomor/jid (substring). limit/offset.
func (u *UsersDB) SearchUsers(ctx context.Context, q string, limit, offset int) ([]UserRecord, error) {
	if u == nil || u.db == nil {
		return nil, fmt.Errorf("users.db mati")
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}
	pat := "%" + strings.TrimSpace(q) + "%"
	rows, err := u.db.QueryContext(ctx, `
		SELECT id, lid, pn, jid, pushname, full_name, first_name, username, redacted_phone,
			pp_status, pp_id, pp_sha, pp_path, pp_bytes, pp_at, first_seen, last_seen,
			seen_count, created_at, updated_at
		FROM users
		WHERE pushname LIKE ? OR full_name LIKE ? OR first_name LIKE ? OR username LIKE ?
		   OR pn LIKE ? OR lid LIKE ? OR jid LIKE ? OR redacted_phone LIKE ?
		ORDER BY last_seen DESC
		LIMIT ? OFFSET ?
	`, pat, pat, pat, pat, pat, pat, pat, pat, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserRecord
	for rows.Next() {
		r, err := scanUserRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CountUsers mengembalikan total baris users.
func (u *UsersDB) CountUsers(ctx context.Context) (int, error) {
	if u == nil || u.db == nil {
		return 0, fmt.Errorf("users.db mati")
	}
	var n int
	if err := u.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// SetUserPhoto memperbarui metadata foto user.
func (u *UsersDB) SetUserPhoto(ctx context.Context, id int64, status, ppID, sha, path string, bytes int, at int64) error {
	if u == nil || u.db == nil {
		return fmt.Errorf("users.db mati")
	}
	_, err := u.db.ExecContext(ctx, `
		UPDATE users SET pp_status = ?, pp_id = ?, pp_sha = ?, pp_path = ?,
			pp_bytes = ?, pp_at = ?, updated_at = ?
		WHERE id = ?
	`, status, ppID, sha, path, bytes, at, at, id)
	return err
}

// ClearUserPhoto menghapus metadata + (opsional) file ditangani pemanggil.
func (u *UsersDB) ClearUserPhoto(ctx context.Context, id int64) error {
	if u == nil || u.db == nil {
		return fmt.Errorf("users.db mati")
	}
	_, err := u.db.ExecContext(ctx, `
		UPDATE users SET pp_status = '', pp_id = '', pp_sha = '', pp_path = '',
			pp_bytes = 0, pp_at = 0, updated_at = ?
		WHERE id = ?
	`, time.Now().Unix(), id)
	return err
}

// ListUsers mengembalikan semua user (paginasi). limit<=0 → 100000.
func (u *UsersDB) ListUsers(ctx context.Context, limit, offset int) ([]UserRecord, error) {
	if u == nil || u.db == nil {
		return nil, fmt.Errorf("users.db mati")
	}
	if limit <= 0 {
		limit = 100000
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := u.db.QueryContext(ctx, `
		SELECT id, lid, pn, jid, pushname, full_name, first_name, username, redacted_phone,
			pp_status, pp_id, pp_sha, pp_path, pp_bytes, pp_at, first_seen, last_seen,
			seen_count, created_at, updated_at
		FROM users
		ORDER BY last_seen DESC
		LIMIT ? OFFSET ?
	`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserRecord
	for rows.Next() {
		r, err := scanUserRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListUsersByPhotoOlderThan mengembalikan user yang fotonya perlu di-refresh
// (pp_at < cutoff). Dipakai prune/refresh batch.
func (u *UsersDB) ListUsersByPhotoOlderThan(ctx context.Context, cutoff int64, limit int) ([]UserRecord, error) {
	if u == nil || u.db == nil {
		return nil, fmt.Errorf("users.db mati")
	}
	if limit <= 0 {
		limit = 1000
	}
	rows, err := u.db.QueryContext(ctx, `
		SELECT id, lid, pn, jid, pushname, full_name, first_name, username, redacted_phone,
			pp_status, pp_id, pp_sha, pp_path, pp_bytes, pp_at, first_seen, last_seen,
			seen_count, created_at, updated_at
		FROM users
		WHERE pp_path <> '' AND pp_at < ?
		ORDER BY pp_at ASC
		LIMIT ?
	`, cutoff, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserRecord
	for rows.Next() {
		r, err := scanUserRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// --- helpers ---

func nullable(s string) any {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return s
}

func scanUserRow(row *sql.Row) (*UserRecord, error) {
	r := &UserRecord{}
	var nl, np sql.NullString
	err := row.Scan(&r.ID, &nl, &np, &r.JID, &r.PushName, &r.FullName, &r.FirstName, &r.Username,
		&r.RedactedPhone, &r.PPStatus, &r.PPID, &r.PPSha, &r.PPPath, &r.PPBytes, &r.PPAt,
		&r.FirstSeen, &r.LastSeen, &r.SeenCount, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if nl.Valid {
		r.LID = nl.String
	}
	if np.Valid {
		r.PN = np.String
	}
	return r, nil
}

func scanUserRows(rows *sql.Rows) (UserRecord, error) {
	r := UserRecord{}
	var nl, np sql.NullString
	err := rows.Scan(&r.ID, &nl, &np, &r.JID, &r.PushName, &r.FullName, &r.FirstName, &r.Username,
		&r.RedactedPhone, &r.PPStatus, &r.PPID, &r.PPSha, &r.PPPath, &r.PPBytes, &r.PPAt,
		&r.FirstSeen, &r.LastSeen, &r.SeenCount, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return r, err
	}
	if nl.Valid {
		r.LID = nl.String
	}
	if np.Valid {
		r.PN = np.String
	}
	return r, nil
}

// pastikan import identity tetap terpakai (Resolve* dipanggil di UpsertUserIdentity).
var _ = identity.Normalize
