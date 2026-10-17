package store

import (
	"path/filepath"
	"testing"
)

// TestSnapshotDBConsistent memverifikasi SnapshotDB: menghasilkan salinan
// konsisten walau DB sumber TETAP terbuka dalam mode WAL, dan menolak target
// yang sudah ada (batasan VACUUM INTO). DB temporer — tidak menyentuh data/.
func TestSnapshotDBConsistent(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.db")
	db, err := openSQLite(src)
	if err != nil {
		t.Fatalf("openSQLite: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO t (v) VALUES ('halo')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Sumber TETAP terbuka (WAL) — snapshot harus tetap melihat barisnya.
	dst := filepath.Join(dir, "snap.db")
	if err := SnapshotDB(src, dst); err != nil {
		t.Fatalf("SnapshotDB: %v", err)
	}

	snap, err := openSQLite(dst)
	if err != nil {
		t.Fatalf("buka snapshot: %v", err)
	}
	defer snap.Close()
	var n int
	if err := snap.QueryRow(`SELECT COUNT(*) FROM t`).Scan(&n); err != nil {
		t.Fatalf("hitung: %v", err)
	}
	if n != 1 {
		t.Fatalf("baris snapshot = %d, mau 1", n)
	}

	// VACUUM INTO menolak target yang sudah ada.
	if err := SnapshotDB(src, dst); err == nil {
		t.Fatal("SnapshotDB ke target yang sudah ada seharusnya error")
	}
}
