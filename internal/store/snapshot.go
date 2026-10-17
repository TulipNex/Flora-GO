package store

import (
	"database/sql"
	"fmt"
)

// SnapshotDB menulis salinan KONSISTEN database SQLite srcPath ke dstPath
// memakai "VACUUM INTO": aman terhadap mode WAL (data yang belum
// di-checkpoint tetap ikut terbaca) dan hasilnya satu file .db bersih tanpa
// pendamping -wal/-shm, sekaligus memadatkan halaman kosong.
//
// dstPath TIDAK boleh sudah ada (batasan VACUUM INTO) — pemanggil memakai
// nama unik di folder temp.
func SnapshotDB(srcPath, dstPath string) error {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(10000)", srcPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		return err
	}
	if _, err := db.Exec(`VACUUM INTO ?`, dstPath); err != nil {
		return err
	}
	return nil
}
