package store

import (
	"context"
	"fmt"
	"strings"
	"sync"

	whatsmeow "github.com/polymorfa/hypermeow"
)

// clones.go — fondasi multi-bot (bot clone / jadibot).
//
// Bot utama + tiap clone punya *whatsmeow.Client masing-masing. Token adalah
// nomor HP clone (digit saja, mis. "62812xxx"); bot utama bertoken "".
// Pemetaan client -> token memungkinkan lapisan store memilih database yang
// tepat per pesan (lihat GroupsFor), dan router memagari fitur main-only
// (lihat plugins.Command.CloneBlocked).

var cloneMu sync.RWMutex

// mainClient adalah client bot utama, diisi sekali via SetMainClient di main.go.
var mainClient *whatsmeow.Client

// cloneClients memetakan client clone -> token (nomor HP-nya).
var cloneClients = make(map[*whatsmeow.Client]string)

// cloneGroupDBs adalah cache handle GroupDB per token clone. Tiap clone
// punya file data/group-<token>.db sendiri supaya setting grup, ban, warn,
// autodownload, dsb tidak bocor campur dengan bot utama maupun clone lain
// (kunci chat JID bisa sama bila dua bot ada di grup yang sama).
var cloneGroupDBs = make(map[string]*GroupDB)

// SetMainClient mencatat client bot utama. Dipanggil sekali di main.go
// setelah NewClient, sebelum Connect/RestoreClones.
func SetMainClient(c *whatsmeow.Client) {
	cloneMu.Lock()
	defer cloneMu.Unlock()
	mainClient = c
}

// RegisterCloneClient mendaftarkan client clone yang sudah connect/login.
func RegisterCloneClient(token string, c *whatsmeow.Client) {
	cloneMu.Lock()
	defer cloneMu.Unlock()
	cloneClients[c] = token
}

// UnregisterCloneClient melepas pendaftaran client clone (saat .jadibot stop).
func UnregisterCloneClient(c *whatsmeow.Client) {
	cloneMu.Lock()
	defer cloneMu.Unlock()
	delete(cloneClients, c)
}

// TokenFor mengembalikan token pemilik client: "" untuk bot utama,
// nomor HP untuk clone, "" juga bila client tak dikenal/nil (fail ke main).
func TokenFor(c *whatsmeow.Client) string {
	cloneMu.RLock()
	defer cloneMu.RUnlock()
	if c == nil {
		return ""
	}
	if c == mainClient {
		return ""
	}
	return cloneClients[c]
}

// IsMainClient true bila pesan datang lewat client bot utama.
func IsMainClient(c *whatsmeow.Client) bool {
	return TokenFor(c) == ""
}

// DisconnectAllClones memutus semua client clone yang terdaftar. Dipanggil
// dari main.go saat SIGTERM/SIGINT (sebelum disconnect utama) supaya
// shutdown bersih dan cepat — tanpa ini, koneksi clone yang menggantung bisa
// menyeret proses melewati stop timeout panel (SIGKILL = terbaca crash =
// panel me-restart sendiri). Registrasi dipertahankan (bukan unregister)
// karena proses segera exit; restore saat start baca dari tabel clones.
func DisconnectAllClones() {
	cloneMu.RLock()
	clients := make([]*whatsmeow.Client, 0, len(cloneClients))
	for c := range cloneClients {
		clients = append(clients, c)
	}
	cloneMu.RUnlock()
	for _, c := range clients {
		c.Disconnect()
	}
}

// CloneClient mengembalikan client clone bertoken phone, atau nil bila
// tidak terdaftar (belum direstore / sudah di-stop).
func CloneClient(phone string) *whatsmeow.Client {
	cloneMu.RLock()
	defer cloneMu.RUnlock()
	for c, tok := range cloneClients {
		if tok == phone {
			return c
		}
	}
	return nil
}

// GroupsFor mengembalikan GroupDB milik pemilik client: database utama
// untuk bot utama, data/group-<token>.db (dibuka malas, fail-soft panic
// seperti Groups bila gagal) untuk clone.
func GroupsFor(c *whatsmeow.Client) *GroupDB {
	token := TokenFor(c)
	if token == "" {
		return Groups()
	}
	cloneMu.RLock()
	g, ok := cloneGroupDBs[token]
	cloneMu.RUnlock()
	if ok {
		return g
	}
	cloneMu.Lock()
	defer cloneMu.Unlock()
	if g, ok := cloneGroupDBs[token]; ok {
		return g
	}
	db, err := openGroupDBFile("data/group-" + token + ".db")
	if err != nil {
		panic(fmt.Sprintf("gagal membuka group DB clone %s: %v", token, err))
	}
	g = &GroupDB{db: db}
	cloneGroupDBs[token] = g
	return g
}

// SanitizePhone menormalisasi nomor HP jadi digit saja untuk token/nama file.
func SanitizePhone(raw string) string {
	var sb strings.Builder
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// CloneSessionPath mengembalikan path file session SQLite untuk token clone.
func CloneSessionPath(token string) string {
	return "data/session-" + token + ".db"
}

// --- Registry clone (persisten, di database UTAMA) ---

// Status clone: active (jalan/direstore saat boot) atau stopped (dimatikan
// via .jadibot stop, session tersimpan, bisa .jadibot start lagi).
const (
	CloneActive  = "active"
	CloneStopped = "stopped"
)

// CloneEntry adalah satu baris registry clone.
type CloneEntry struct {
	Phone  string
	Status string
}

// ListCloneEntries mengembalikan semua clone terdaftar + statusnya.
func (g *GroupDB) ListCloneEntries(ctx context.Context) ([]CloneEntry, error) {
	rows, err := g.db.QueryContext(ctx, `SELECT phone, status FROM clones ORDER BY phone`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CloneEntry
	for rows.Next() {
		var e CloneEntry
		if err := rows.Scan(&e.Phone, &e.Status); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListClones mengembalikan nomor HP semua clone terdaftar (urut abjad).
func (g *GroupDB) ListClones(ctx context.Context) ([]string, error) {
	entries, err := g.ListCloneEntries(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Phone)
	}
	return out, nil
}

// AddClone mendaftarkan nomor HP clone baru. Pendaftaran ulang nomor yang
// pernah di-stop mengaktifkannya kembali (status -> active).
func (g *GroupDB) AddClone(ctx context.Context, phone string) error {
	_, err := g.db.ExecContext(ctx, `
		INSERT INTO clones (phone, status) VALUES (?, 'active')
		ON CONFLICT(phone) DO UPDATE SET status='active'
	`, phone)
	return err
}

// SetCloneStatus mengubah status clone (active/stopped).
func (g *GroupDB) SetCloneStatus(ctx context.Context, phone, status string) error {
	_, err := g.db.ExecContext(ctx, `UPDATE clones SET status = ? WHERE phone = ?`, status, phone)
	return err
}

// RemoveClone menghapus pendaftaran nomor HP clone.
func (g *GroupDB) RemoveClone(ctx context.Context, phone string) error {
	_, err := g.db.ExecContext(ctx, `DELETE FROM clones WHERE phone = ?`, phone)
	return err
}

// CloseCloneDB menutup + melupakan handle group DB clone (dipakai sebelum
// file-nya dihapus oleh .jadibot hapus). Aman dipanggil walau tak ada handle.
func CloseCloneDB(token string) {
	cloneMu.Lock()
	defer cloneMu.Unlock()
	if g, ok := cloneGroupDBs[token]; ok {
		_ = g.db.Close()
		delete(cloneGroupDBs, token)
	}
}
