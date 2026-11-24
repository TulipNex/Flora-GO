package plugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBackupUsageFormat(t *testing.T) {
	expected := "*Backup database*\n\n" +
		"`Backup`\n" +
		"Format :\n" +
		"- .backup\n" +
		"- .backup list\n" +
		"- .backup <nomor>\n" +
		"- .backup <nomor> -db\n" +
		"- .backup jadwal <interval/off>\n\n" +
		"```list   : list database\n" +
		"nomor  : ekspor ke xlsx\n" +
		"-db    : unduh SQLite\n" +
		"jadwal : 30m, 6h, 1d```"

	if backupUsage != expected {
		t.Errorf("backupUsage tidak sesuai.\nGot:\n%s\n\nWant:\n%s", backupUsage, expected)
	}
}

func TestBackupListFormat(t *testing.T) {
	tmpDir := t.TempDir()
	p1 := filepath.Join(tmpDir, "group.db")
	p2 := filepath.Join(tmpDir, "ai.db")

	if err := os.WriteFile(p1, make([]byte, 3145728), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p2, make([]byte, 4404019), 0644); err != nil {
		t.Fatal(err)
	}

	got := formatBackupList([]string{p1, p2})

	wantLines := []string{
		"*Daftar Database*",
		"",
		"- *1. group.db* • 3.0 MB",
		"- *2. ai.db* • 4.2 MB",
		"",
		"> Ketik `.backup <no>` untuk Excel, tambah `-db` untuk SQLite.",
	}
	want := strings.Join(wantLines, "\n")

	if got != want {
		t.Errorf("formatBackupList tidak sesuai.\nGot:\n%s\n\nWant:\n%s", got, want)
	}
}

func TestBackupCaptions(t *testing.T) {
	wib := time.FixedZone("WIB", 7*3600)
	fixedTime := time.Date(2026, 11, 18, 23, 50, 0, 0, wib)

	// 1. ZIP caption
	tmpDir := t.TempDir()
	p := filepath.Join(tmpDir, "dummy.db")
	_ = os.WriteFile(p, make([]byte, 16148070), 0644) // ~15.4 MB
	paths := make([]string, 12)
	for i := 0; i < 12; i++ {
		paths[i] = p
	}

	gotZip := backupZipCaptionAt(paths, fixedTime)
	wantZip := "*FULL DATABASE BACKUP*\n\n" +
		"```- Jumlah: 12 file database \n" +
		"- Ukuran: 184.8 MB\n" +
		"- Waktu : 18 Nov 2026\n" +
		"- Jam   : 23:50 WIB```"
	if !strings.HasPrefix(gotZip, "*FULL DATABASE BACKUP*\n\n```- Jumlah: 12 file database \n") ||
		!strings.Contains(gotZip, "- Waktu : 18 Nov 2026\n- Jam   : 23:50 WIB```") {
		t.Errorf("backupZipCaptionAt tidak sesuai:\nGot:\n%s\n\nWant:\n%s", gotZip, wantZip)
	}

	// 2. Excel caption
	gotExcel := backupExcelCaptionAt("group", 5, 1420, 461004, fixedTime)
	wantExcel := "*EXPORT DATABASE EXCEL*\n\n" +
		"```- File  : group.xlsx\n" +
		"- Tabel : 5 sheet\n" +
		"- Baris : 1420 baris\n" +
		"- Ukuran: 450.2 KB\n" +
		"- Waktu : 18 Nov 2026\n" +
		"- Jam   : 23:50 WIB```"
	if gotExcel != wantExcel {
		t.Errorf("backupExcelCaptionAt tidak sesuai:\nGot:\n%s\n\nWant:\n%s", gotExcel, wantExcel)
	}

	// 3. SQLite caption
	gotSQLite := backupSQLiteCaptionAt("group.db", 3250585, fixedTime)
	wantSQLite := "*SNAPSHOT DATABASE SQLITE*\n\n" +
		"```- File  : group.db\n" +
		"- Ukuran: 3.1 MB\n" +
		"- Waktu : 18 Nov 2026\n" +
		"- Jam   : 23:50 WIB```"
	if gotSQLite != wantSQLite {
		t.Errorf("backupSQLiteCaptionAt tidak sesuai:\nGot:\n%s\n\nWant:\n%s", gotSQLite, wantSQLite)
	}
}
