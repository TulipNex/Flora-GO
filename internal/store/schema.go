package store

// schema.go — fondasi skema & versi untuk semua database milik bot sendiri
// (group, ai, finance, crm, absensi, dan group DB per clone). Database auth
// whatsmeow (data/session.db) punya framework migrasinya sendiri; file ini
// hanya untuk DB yang dibuka lewat openSQLite.
//
// Pola:
//   - openSQLite(path): DSN + PRAGMA standar (WAL, busy_timeout, foreign_keys)
//     supaya semua DB memakai setelan identik (dulu diduplikasi di tiap file).
//   - applySchema(...): jalankan skema dasar (idempoten) + migrasi ALTER, lalu
//     sinkronkan PRAGMA user_version ke versi yang diharapkan kode.
//
// user_version adalah penanda versi skema PER FILE (bawaan SQLite). Tujuannya
// bukan menggerbangi migrasi — ALTER di sini tetap idempoten supaya bisa
// menyembuhkan penerapan parsial — melainkan mendeteksi & mencatat drift:
// versi lebih baru (kode di-downgrade) atau migrasi yang gagal tidak lagi
// diam-diam.

import (
	"database/sql"
	"fmt"

	"wabot-go/internal/logfmt"

	_ "modernc.org/sqlite"
)

// Versi skema per file database. Naikkan angka ini saat menambah/mengubah
// skema file terkait (dan tambahkan ALTER-nya di pemanggil applySchema).
const (
	groupSchemaVersion   = 1
	aiSchemaVersion      = 1
	financeSchemaVersion = 1
	crmSchemaVersion     = 1
	absensiSchemaVersion = 1
)

// openSQLite membuka file SQLite dengan PRAGMA standar bot dan memastikan
// koneksinya hidup (Ping). Error dikembalikan mentah; pemanggil membungkusnya
// dengan nama file untuk konteks.
func openSQLite(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// schemaVersion membaca PRAGMA user_version file yang sedang terbuka.
func schemaVersion(db *sql.DB) (int, error) {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}

// ensureSchema menjalankan skema dasar (CREATE ... IF NOT EXISTS, aman
// diulang) lalu seluruh ALTER migrasi. Error "duplicate column" diabaikan
// (kolom sudah ada); error lain DIKEMBALIKAN supaya drift tidak diam-diam.
func ensureSchema(db *sql.DB, dbName, schema string, alters []string) error {
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("skema %s: %w", dbName, err)
	}
	for _, alter := range alters {
		if _, err := db.Exec(alter); err != nil && !isDuplicateColumnError(err) {
			return fmt.Errorf("migrasi %s (%s): %w", dbName, alter, err)
		}
	}
	return nil
}

// syncSchemaVersion menyamakan PRAGMA user_version dengan versi kode.
//   - cur == target: tidak ada yang dilakukan.
//   - cur <  target: tulis target (skema baru dimigrasikan oleh pemanggil).
//   - cur >  target: JANGAN turunkan — kode lebih lama dari DB (downgrade);
//     cukup Warn supaya ketahuan tanpa merusak data.
func syncSchemaVersion(db *sql.DB, dbName string, target int) error {
	cur, err := schemaVersion(db)
	if err != nil {
		logfmt.Warn("%s: gagal baca user_version: %v\n", dbName, err)
		return nil
	}
	switch {
	case cur == target:
		return nil
	case cur > target:
		logfmt.Warn("%s: user_version=%d > versi kode=%d (kode lebih lama? perubahan skema tak diterapkan).\n", dbName, cur, target)
		return nil
	default:
		if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, target)); err != nil {
			return fmt.Errorf("set user_version %s -> %d: %w", dbName, target, err)
		}
		logfmt.Info("%s: skema v%d -> v%d.\n", dbName, cur, target)
		return nil
	}
}

// applySchema = ensureSchema + syncSchemaVersion dalam satu panggilan.
func applySchema(db *sql.DB, dbName, schema string, alters []string, target int) error {
	if err := ensureSchema(db, dbName, schema, alters); err != nil {
		return err
	}
	return syncSchemaVersion(db, dbName, target)
}
