package plugins

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
	_ "modernc.org/sqlite"
)

func TestToExcelOutName(t *testing.T) {
	got := toExcelOutName("group.db")
	if !strings.HasPrefix(got, "group-") || !strings.HasSuffix(got, ".xlsx") {
		t.Errorf("group.db → %q", got)
	}
	if got := toExcelOutName(""); !strings.HasPrefix(got, "database-") {
		t.Errorf("kosong → %q", got)
	}
	if got := toExcelOutName("backup.zip"); !strings.HasPrefix(got, "backup-") {
		t.Errorf("zip → %q", got)
	}
}

func TestToExcelBuildFromDBBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "g.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("buka db: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE t(a INTEGER, b TEXT)`); err != nil {
		t.Fatalf("ddl: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO t VALUES (1,'x')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	db.Close()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	data, fname, err := toExcelBuildFromDBBytes(raw, "g.db")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.HasSuffix(fname, ".xlsx") {
		t.Errorf("nama = %q", fname)
	}
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("buka hasil: %v", err)
	}
	defer f.Close()
	if !containsStr(f.GetSheetList(), "t") {
		t.Fatalf("sheet t tidak ada: %v", f.GetSheetList())
	}
	if v, _ := f.GetCellValue("t", "B2"); v != "x" {
		t.Errorf("t!B2 = %q, mau x", v)
	}
}

func TestToExcelRejectNonSQLite(t *testing.T) {
	if _, _, err := toExcelBuildFromDBBytes([]byte("bukan sqlite"), "x.db"); err == nil {
		t.Error("byte bukan SQLite harus ditolak")
	}
}

func containsStr(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
