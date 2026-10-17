package sqlxlsx

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

// makeDB membuat file SQLite temp berisi satu tabel "users" + data uji.
func makeDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("buka db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE users(id INTEGER, name TEXT, score REAL, blob BLOB)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users VALUES (1,'andi',9.5,x'00FF'),(2,NULL,NULL,NULL)`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	return path
}

func TestConvertBasic(t *testing.T) {
	path := makeDB(t)
	f := NewWorkbook()
	rep, err := ConvertInto(f, path, "", Options{IncludeInfo: true})
	if err != nil {
		t.Fatalf("ConvertInto: %v", err)
	}
	if len(rep.Tables) != 1 || rep.Tables[0].Name != "users" || rep.Tables[0].Rows != 2 {
		t.Fatalf("report tak sesuai: %+v", rep)
	}
	if err := Finalize(f); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	data, err := Bytes(f)
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	out, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("buka hasil: %v", err)
	}
	defer out.Close()

	sheets := out.GetSheetList()
	if !contains(sheets, "users") || !contains(sheets, "info") {
		t.Fatalf("sheet tak lengkap: %v", sheets)
	}
	for _, tc := range []struct{ cell, want string }{
		{"A1", "id"}, {"B1", "name"}, {"C1", "score"}, {"D1", "blob"},
		{"A2", "1"}, {"B2", "andi"}, {"C2", "9.5"}, {"D2", "(blob 2 bytes)"},
		{"A3", "2"}, {"B3", ""},
	} {
		if got, _ := out.GetCellValue("users", tc.cell); got != tc.want {
			t.Errorf("users!%s = %q, mau %q", tc.cell, got, tc.want)
		}
	}
	// Sheet1 default harus sudah dibuang.
	for _, s := range sheets {
		if s == "Sheet1" {
			t.Errorf("Sheet1 default masih ada")
		}
	}
}

func TestTruncateRows(t *testing.T) {
	path := makeDB(t)
	// tambah baris ketiga
	db, _ := sql.Open("sqlite", path)
	if _, err := db.Exec(`INSERT INTO users VALUES (3,'c',1.0,NULL)`); err != nil {
		t.Fatalf("insert3: %v", err)
	}
	db.Close()

	rep, err := ConvertInto(NewWorkbook(), path, "", Options{MaxRowsPerTable: 2})
	if err != nil {
		t.Fatalf("ConvertInto: %v", err)
	}
	tr := rep.Tables[0]
	if tr.Rows != 2 || !tr.Truncated {
		t.Errorf("rows=%d truncated=%v, mau 2/true", tr.Rows, tr.Truncated)
	}
}

func TestMaxCols(t *testing.T) {
	path := makeDB(t) // 4 kolom
	rep, err := ConvertInto(NewWorkbook(), path, "", Options{MaxCols: 2})
	if err != nil {
		t.Fatalf("ConvertInto: %v", err)
	}
	if tr := rep.Tables[0]; tr.Cols != 2 || !tr.Truncated {
		t.Errorf("cols=%d truncated=%v, mau 2/true", tr.Cols, tr.Truncated)
	}
}

func TestSheetName(t *testing.T) {
	used := map[string]bool{}
	if got := sheetName(used, "", "a/b:c?"); got != "abc" {
		t.Errorf("sanitasi = %q, mau abc", got)
	}
	if got := sheetName(used, "", "a/b:c?"); got != "abc~2" {
		t.Errorf("dedup = %q, mau abc~2", got)
	}
	long := strings.Repeat("x", 40)
	if got := sheetName(used, "", long); len([]rune(got)) != 31 {
		t.Errorf("panjang = %d, mau 31", len([]rune(got)))
	}
	if got := sheetName(used, "group", "users"); got != "group.users" {
		t.Errorf("prefix = %q, mau group.users", got)
	}
}

func TestFinalizeKeepsSingleSheet(t *testing.T) {
	f := NewWorkbook()
	if err := Finalize(f); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if len(f.GetSheetList()) != 1 {
		t.Errorf("mau 1 sheet, dapat %v", f.GetSheetList())
	}
}

func TestNonSQLiteRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bukan.db")
	if err := os.WriteFile(path, []byte("bukan sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ConvertInto(NewWorkbook(), path, "", Options{}); err == nil {
		t.Error("file bukan SQLite harus ditolak")
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
