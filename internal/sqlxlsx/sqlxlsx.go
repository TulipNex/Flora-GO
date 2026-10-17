// Package sqlxlsx mengubah file database SQLite menjadi workbook Excel
// (.xlsx): setiap tabel jadi satu sheet, header diambil dari nama kolom, dan
// sebuah sheet "Info" merangkum isi. Dipakai plugin .toexcel untuk mengubah
// file backup .db agar enak dibaca/difilter di Excel.
//
// Paket ini MURNI (tanpa ketergantungan WhatsApp) supaya bisa diuji sendiri.
// Prinsip:
//   - Fail-closed & nil-safe: file yang bukan SQLite ditolak jelas.
//   - Read-only: DB dibuka dengan `PRAGMA query_only`, tidak pernah ditulis.
//   - Terbatas: jumlah tabel/baris/kolom dibatasi supaya RAM tidak jebol tabel
//     raksasa; kelebihan ditandai Truncated dan dicatat di sheet Info.
package sqlxlsx

import (
	"bytes"
	"database/sql"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"wabot-go/internal/timex"

	"github.com/xuri/excelize/v2"
	_ "modernc.org/sqlite"
)

// Batas bawaan (bisa ditimpa lewat Options). Nilai sengaja konservatif:
// tabel bot yang wajar jauh di bawah ini, sementara DB yang membengkak tetap
// aman diekspor sebagian.
const (
	DefaultMaxRowsPerTable = 50000
	DefaultMaxTables       = 200
	DefaultMaxCols         = 256

	// borderCellLimit: border per sel hanya dipasang untuk tabel kecil
	// (baris×kolom ≤ nilai ini) — border pada ratusan ribu sel lambat & boros.
	borderCellLimit = 30000
	maxColWidth     = 50
	minColWidth     = 8
	maxSheetName    = 31 // batas nama sheet Excel
	infoSheetName   = "info"
)

// Options menyalakan/mengatur batas konversi. Nilai ≤ 0 memakai default.
type Options struct {
	MaxRowsPerTable int  // maks baris yang ditulis per tabel
	MaxTables       int  // maks tabel yang diekspor
	MaxCols         int  // maks kolom yang ditulis per tabel
	IncludeInfo     bool // sertakan sheet "Info" untuk DB ini
}

func (o Options) norm() Options {
	if o.MaxRowsPerTable <= 0 {
		o.MaxRowsPerTable = DefaultMaxRowsPerTable
	}
	if o.MaxTables <= 0 {
		o.MaxTables = DefaultMaxTables
	}
	if o.MaxCols <= 0 {
		o.MaxCols = DefaultMaxCols
	}
	return o
}

// TableReport ringkasan satu tabel yang diekspor.
type TableReport struct {
	Name      string
	Rows      int
	Cols      int
	Truncated bool // baris dan/atau kolom dipotong
}

// Report ringkasan satu pemanggilan ConvertInto.
type Report struct {
	Tables    []TableReport
	TotalRows int
}

// NewWorkbook membuat workbook kosong (masih berisi sheet default "Sheet1"
// yang dibuang oleh Finalize bila sudah ada sheet lain).
func NewWorkbook() *excelize.File {
	return excelize.NewFile()
}

// ConvertInto menambahkan sheet untuk setiap tabel di srcPath ke workbook f.
// prefix, bila tidak kosong, ditempel di depan nama sheet ("<prefix>.<tabel>")
// — dipakai saat menggabungkan beberapa DB ke satu workbook. Mengembalikan
// ringkasan isi yang diekspor.
func ConvertInto(f *excelize.File, srcPath, prefix string, opt Options) (Report, error) {
	opt = opt.norm()
	var rep Report
	db, err := openReadOnly(srcPath)
	if err != nil {
		return rep, fmt.Errorf("buka %s: %w", filepath.Base(srcPath), err)
	}
	defer db.Close()

	tables, err := listTables(db)
	if err != nil {
		return rep, err
	}
	used := sheetSet(f)
	tablesTruncated := false
	if len(tables) > opt.MaxTables {
		tables = tables[:opt.MaxTables]
		tablesTruncated = true
	}
	for _, t := range tables {
		sheet := sheetName(used, prefix, t)
		tr, werr := writeTable(f, db, t, sheet, opt.MaxRowsPerTable, opt.MaxCols)
		if werr != nil {
			return rep, fmt.Errorf("tabel %s: %w", t, werr)
		}
		rep.Tables = append(rep.Tables, tr)
		rep.TotalRows += tr.Rows
	}
	if opt.IncludeInfo {
		if ierr := writeInfoSheet(f, used, prefix, filepath.Base(srcPath), rep, tablesTruncated); ierr != nil {
			return rep, ierr
		}
	}
	return rep, nil
}

// AddInfoSheet menambah sheet ringkasan bebas (dipakai plugin untuk merangkum
// beberapa DB dalam satu workbook ZIP). title jadi awalan nama sheet.
func AddInfoSheet(f *excelize.File, title string, header []string, rows [][]string) error {
	used := sheetSet(f)
	sheet := sheetName(used, "", title)
	if _, err := f.NewSheet(sheet); err != nil {
		return err
	}
	return writeGrid(f, sheet, header, rows)
}

// Finalize menyempurnakan workbook: memastikan minimal satu sheet dan membuang
// sheet default "Sheet1" bila sudah ada sheet lain. Harus dipanggil sebelum
// Bytes.
func Finalize(f *excelize.File) error {
	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		if _, err := f.NewSheet("Kosong"); err != nil {
			return err
		}
		return nil
	}
	if len(sheets) > 1 {
		for _, s := range sheets {
			if s == "Sheet1" {
				if err := f.DeleteSheet("Sheet1"); err != nil {
					return err
				}
				break
			}
		}
	}
	return nil
}

// Render menyelesaikan workbook (Finalize) lalu mengembalikan byte .xlsx.
// Pembungkus praktis supaya pemanggil cukup satu langkah.
func Render(f *excelize.File) ([]byte, error) {
	if err := Finalize(f); err != nil {
		return nil, err
	}
	return Bytes(f)
}

// Bytes menulis workbook ke byte .xlsx.
func Bytes(f *excelize.File) ([]byte, error) {
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		return nil, err
	}
	if buf.Len() == 0 {
		return nil, fmt.Errorf("file xlsx kosong")
	}
	return buf.Bytes(), nil
}

// openReadOnly membuka SQLite tanpa hak tulis (query_only) supaya tidak
// pernah mengubah/membuat side-file WAL pada file sumber.
func openReadOnly(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(10000)&_pragma=query_only(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// listTables mengembalikan nama tabel user (tanpa tabel internal sqlite_*).
func listTables(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// quoteIdent mengutip identifier SQLite (nama tabel) dengan aman.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// writeTable menulis satu tabel ke sheet: baris 1 header (tebal), sisanya data.
func writeTable(f *excelize.File, db *sql.DB, table, sheet string, maxRows, maxCols int) (TableReport, error) {
	tr := TableReport{Name: table}
	if _, err := f.NewSheet(sheet); err != nil {
		return tr, err
	}
	rows, err := db.Query(`SELECT * FROM ` + quoteIdent(table))
	if err != nil {
		return tr, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return tr, err
	}
	n := len(cols)
	if n == 0 {
		return tr, nil
	}
	exportCols := n
	if maxCols > 0 && exportCols > maxCols {
		exportCols = maxCols
		tr.Truncated = true
	}

	// Header tebal.
	headStyle, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	for c := 0; c < exportCols; c++ {
		cell, _ := excelize.CoordinatesToCellName(c+1, 1)
		if err := f.SetCellValue(sheet, cell, cols[c]); err != nil {
			return tr, err
		}
	}
	if headStyle != 0 {
		last, _ := excelize.CoordinatesToCellName(exportCols, 1)
		_ = f.SetCellStyle(sheet, "A1", last, headStyle)
	}

	// Scan menampung SEMUA kolom asli (walau hanya sebagian ditulis) supaya
	// baris tetap terpakai penuh; kolom lebih dari batas cukup dibuang.
	vals := make([]any, n)
	ptrs := make([]any, n)
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	widths := make([]int, exportCols)
	for c := 0; c < exportCols; c++ {
		widths[c] = utf8.RuneCountInString(cols[c])
	}

	rn := 1
	trunc := false
	for rows.Next() {
		if tr.Rows >= maxRows {
			trunc = true
			break
		}
		if err := rows.Scan(ptrs...); err != nil {
			return tr, err
		}
		rn++
		for c := 0; c < exportCols; c++ {
			v := cellValue(vals[c])
			cell, _ := excelize.CoordinatesToCellName(c+1, rn)
			if err := f.SetCellValue(sheet, cell, v); err != nil {
				return tr, err
			}
			if w := utf8.RuneCountInString(fmt.Sprintf("%v", v)); w > widths[c] {
				widths[c] = w
			}
		}
		tr.Rows++
	}
	if err := rows.Err(); err != nil {
		return tr, err
	}
	if trunc {
		tr.Truncated = true
	}
	tr.Cols = exportCols

	applyWidths(f, sheet, widths)

	// Border hanya untuk tabel kecil agar tidak boros.
	if exportCols*tr.Rows <= borderCellLimit && tr.Rows > 0 {
		if b, berr := f.NewStyle(&excelize.Style{Border: []excelize.Border{
			{Type: "left", Style: 1}, {Type: "right", Style: 1},
			{Type: "top", Style: 1}, {Type: "bottom", Style: 1},
		}}); berr == nil {
			last, _ := excelize.CoordinatesToCellName(exportCols, tr.Rows+1)
			_ = f.SetCellStyle(sheet, "A2", last, b)
		}
	}
	return tr, nil
}

// applyWidths memasang lebar kolom (min 8, maks 50) dari lebar konten maksimum.
func applyWidths(f *excelize.File, sheet string, widths []int) {
	for c, w := range widths {
		name, err := excelize.ColumnNumberToName(c + 1)
		if err != nil {
			continue
		}
		wf := float64(w + 2)
		if wf < minColWidth {
			wf = minColWidth
		}
		if wf > maxColWidth {
			wf = maxColWidth
		}
		_ = f.SetColWidth(sheet, name, name, wf)
	}
}

// cellValue menyeragamkan nilai hasil Scan jadi tipe yang ramah Excel. BLOB
// tidak ditulis mentah (bisa raksasa/binari) — cukup keterangan ukurannya.
func cellValue(v any) any {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case int64:
		return t
	case float64:
		return t
	case bool:
		return t
	case []byte:
		return fmt.Sprintf("(blob %d bytes)", len(t))
	case time.Time:
		return t.Format("2006-01-02 15:04:05")
	default:
		return fmt.Sprintf("%v", t)
	}
}

// writeInfoSheet menulis sheet ringkasan: metadata + daftar tabel.
func writeInfoSheet(f *excelize.File, used map[string]bool, prefix, srcName string, rep Report, tablesTruncated bool) error {
	sheet := sheetName(used, prefix, infoSheetName)
	if _, err := f.NewSheet(sheet); err != nil {
		return err
	}
	headStyle, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})

	meta := [][]string{
		{"File", srcName},
		{"Diekspor", timex.Now().Format("02 Jan 2006 15:04:05")},
		{"Jumlah tabel", strconv.Itoa(len(rep.Tables))},
		{"Total baris", strconv.Itoa(rep.TotalRows)},
	}
	if tablesTruncated {
		meta = append(meta, []string{"Catatan", fmt.Sprintf("dibatasi %d tabel pertama", DefaultMaxTables)})
	}
	rn := 1
	for _, kv := range meta {
		for c, v := range kv {
			cell, _ := excelize.CoordinatesToCellName(c+1, rn)
			if err := f.SetCellValue(sheet, cell, v); err != nil {
				return err
			}
		}
		rn++
	}
	if headStyle != 0 {
		_ = f.SetCellStyle(sheet, "A1", "A1", headStyle)
	}

	rn++ // baris kosong pemisah
	header := []string{"Tabel", "Baris", "Kolom", "Dipangkas"}
	for c, h := range header {
		cell, _ := excelize.CoordinatesToCellName(c+1, rn)
		if err := f.SetCellValue(sheet, cell, h); err != nil {
			return err
		}
	}
	if headStyle != 0 {
		last, _ := excelize.CoordinatesToCellName(len(header), rn)
		_ = f.SetCellStyle(sheet, fmt.Sprintf("A%d", rn), last, headStyle)
	}
	for _, t := range rep.Tables {
		rn++
		row := []any{t.Name, t.Rows, t.Cols, t.Truncated}
		for c, v := range row {
			cell, _ := excelize.CoordinatesToCellName(c+1, rn)
			if err := f.SetCellValue(sheet, cell, v); err != nil {
				return err
			}
		}
	}
	_ = f.SetColWidth(sheet, "A", "A", 28)
	_ = f.SetColWidth(sheet, "B", "D", 12)
	return nil
}

// writeGrid menulis header + baris sederhana (dipakai AddInfoSheet).
func writeGrid(f *excelize.File, sheet string, header []string, rows [][]string) error {
	headStyle, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	for c, h := range header {
		cell, _ := excelize.CoordinatesToCellName(c+1, 1)
		if err := f.SetCellValue(sheet, cell, h); err != nil {
			return err
		}
	}
	if headStyle != 0 && len(header) > 0 {
		last, _ := excelize.CoordinatesToCellName(len(header), 1)
		_ = f.SetCellStyle(sheet, "A1", last, headStyle)
	}
	for i, row := range rows {
		for c, v := range row {
			cell, _ := excelize.CoordinatesToCellName(c+1, i+2)
			if err := f.SetCellValue(sheet, cell, v); err != nil {
				return err
			}
		}
	}
	return nil
}

// sheetSet mengumpulkan nama sheet yang sudah ada (lowercase) untuk dedup.
func sheetSet(f *excelize.File) map[string]bool {
	m := make(map[string]bool)
	for _, s := range f.GetSheetList() {
		m[strings.ToLower(s)] = true
	}
	return m
}

// sheetName membangun nama sheet unik & valid: buang karakter terlarang
// Excel, batasi 31 rune, dan tambahkan sufiks "~n" bila bentrok.
func sheetName(used map[string]bool, prefix, table string) string {
	base := table
	if prefix != "" {
		base = prefix + "." + table
	}
	var sb strings.Builder
	for _, r := range base {
		if r < 0x20 || strings.ContainsRune(`[]:*?/\`, r) {
			continue
		}
		sb.WriteRune(r)
	}
	s := strings.TrimSpace(sb.String())
	if s == "" {
		s = "Sheet"
	}
	s = truncRunes(s, maxSheetName)
	name := s
	for i := 2; used[strings.ToLower(name)]; i++ {
		suffix := "~" + strconv.Itoa(i)
		name = truncRunes(s, maxSheetName-utf8.RuneCountInString(suffix)) + suffix
	}
	used[strings.ToLower(name)] = true
	return name
}

// truncRunes memotong string ke paling banyak n rune (n<1 → "").
func truncRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
