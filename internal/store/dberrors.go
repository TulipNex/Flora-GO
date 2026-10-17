package store

// dberrors.go — predikat error SQLite yang dipakai berulang di seluruh paket
// store, dikumpulkan di satu tempat supaya konsisten:
//   - deteksi "no rows" memakai errors.Is (tahan pembungkusan/alias driver),
//     BUKAN perbandingan string seperti `err.Error() == "..."`;
//   - cek pesan driver ("duplicate column", "no such column") memakai
//     strings.Contains yang sama di semua pemanggil.

import (
	"database/sql"
	"errors"
	"strings"
)

// isNoRowsError true kalau err adalah sql.ErrNoRows (langsung atau dibungkus).
func isNoRowsError(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}

// isDuplicateColumnError true kalau error ALTER TABLE ADD COLUMN disebabkan
// kolom yang sudah ada (aman diabaikan), bukan error lain.
func isDuplicateColumnError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "duplicate column") || strings.Contains(msg, "already exists")
}

// isNoSuchColumnError true kalau error "no such column" (database lama yang
// migrasinya belum jalan / gagal sebagian) — pemanggil menganggap fitur mati,
// bukan error fatal.
func isNoSuchColumnError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "no such column")
}
