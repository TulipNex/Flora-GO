package plugins

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"wabot-go/internal/logfmt"
	"wabot-go/internal/sqlxlsx"
	"wabot-go/internal/store"
	"wabot-go/internal/timex"

	"github.com/polymorfa/hypermeow/proto/waE2E"
)

func init() {
	registerPlugin(RegisterToExcel)
}

// toexcel.go — ubah file database SQLite (.db) jadi Excel (.xlsx), owner-only
// dan hanya via DM (data DB sensitif: nomor HP, CRM, dsb).
//
//   - Reply file .db (mis. hasil .backup get) dengan .getexcel.
//   - .getexcel <nama.db> — ambil langsung dari data/ (snapshot dulu).
//   - Reply ZIP hasil .backup dengan .getexcel — semua DB jadi satu workbook.
//
// Setiap tabel jadi satu sheet; sheet "Info" merangkum. Konversi dijalankan di
// latar dengan antrean dlSem supaya tidak menahan pipeline pesan; temp selalu
// di store.MustBotTempDir() dan dihapus setelah selesai.

const (
	// toExcelMaxInputBytes membatasi file yang diunduh/diproses (ZIP maupun .db).
	toExcelMaxInputBytes = 50 << 20 // 50MB

	xlsxMIME    = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	sqliteMagic = "SQLite format 3\x00"
)

// Opsi konversi: satu DB → sertakan sheet Info per DB; ZIP → ringkasan gabungan.
var (
	toExcelOpts    = sqlxlsx.Options{IncludeInfo: true}
	toExcelZipOpts = sqlxlsx.Options{IncludeInfo: false}
)

func RegisterToExcel(r *Registry) {
	r.Register(Command{
		Name:        "getexcel",
		Description: "Ubah file database (.db) jadi Excel — owner, via DM",
		Category:    CategoryOwner,
		Handler:     handleToExcel,
	})
}

const toExcelUsage = `*Database → Excel*
Cara pakai:
1. Reply file .db (mis. hasil .backup get) dengan .getexcel
2. .getexcel <nama.db> — ambil langsung dari server
3. Reply file ZIP hasil .backup dengan .getexcel

Setiap tabel jadi satu sheet. Lihat DB: .backup list`

func handleToExcel(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	if mctx.IsGroup() {
		return store.ReplyText(ctx, mctx, "Command ini hanya bisa dipakai di chat pribadi (DM) owner.")
	}
	if len(mctx.Args) > 0 {
		name := filepath.Base(strings.TrimSpace(strings.Join(mctx.Args, " ")))
		return toExcelFromServerDB(ctx, mctx, name)
	}
	if doc := toExcelTargetDoc(mctx); doc != nil {
		return toExcelFromDocument(ctx, mctx, doc)
	}
	return store.ReplyText(ctx, mctx, toExcelUsage)
}

// toExcelTargetDoc mengambil dokumen dari pesan pemicu (lampiran langsung atau
// yang di-reply). nil bila tidak ada.
func toExcelTargetDoc(mctx *store.MessageContext) *waE2E.DocumentMessage {
	if mctx.RawEvt == nil {
		return nil
	}
	if d := store.UnwrapMessage(mctx.RawEvt.Message).GetDocumentMessage(); d != nil {
		return d
	}
	if d := store.UnwrapMessage(store.ExtractQuotedMessage(mctx.RawEvt)).GetDocumentMessage(); d != nil {
		return d
	}
	return nil
}

// toExcelFromServerDB mengonversi DB di data/ sesuai nama (snapshot dulu).
func toExcelFromServerDB(ctx context.Context, mctx *store.MessageContext, name string) error {
	if !strings.HasSuffix(strings.ToLower(name), ".db") {
		return store.ReplyText(ctx, mctx, "Nama harus file .db, contoh: .getexcel group.db\nLihat daftar: .backup list")
	}
	paths, err := listDBFiles()
	if err != nil {
		return store.ReplyText(ctx, mctx, "Gagal membaca daftar database: "+err.Error())
	}
	src := ""
	for _, p := range paths {
		if filepath.Base(p) == name {
			src = p
			break
		}
	}
	if src == "" {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("File %q tidak ditemukan.\nKetik .backup list untuk melihat daftar.", name))
	}
	return toExcelQueue(ctx, mctx, name, func(bg context.Context) ([]byte, string, error) {
		workDir, err := os.MkdirTemp(store.MustBotTempDir(), "toexcel-*")
		if err != nil {
			return nil, "", err
		}
		defer os.RemoveAll(workDir)
		snap := filepath.Join(workDir, filepath.Base(src))
		if err := store.SnapshotDB(src, snap); err != nil {
			return nil, "", fmt.Errorf("snapshot %s: %w", filepath.Base(src), err)
		}
		f := sqlxlsx.NewWorkbook()
		if _, err := sqlxlsx.ConvertInto(f, snap, "", toExcelOpts); err != nil {
			return nil, "", err
		}
		data, err := sqlxlsx.Render(f)
		if err != nil {
			return nil, "", err
		}
		return data, toExcelOutName(name), nil
	})
}

// toExcelFromDocument mengunduh dokumen yang di-reply lalu mengonversinya
// (DB tunggal atau seluruh isi ZIP).
func toExcelFromDocument(ctx context.Context, mctx *store.MessageContext, doc *waE2E.DocumentMessage) error {
	name := sanitizeFileName(doc.GetFileName())
	if name == "" {
		name = "database.db"
	}
	return toExcelQueue(ctx, mctx, name, func(bg context.Context) ([]byte, string, error) {
		data, err := mctx.Client.Download(bg, doc)
		if err != nil {
			return nil, "", fmt.Errorf("gagal unduh %s: %w", name, err)
		}
		if len(data) > toExcelMaxInputBytes {
			return nil, "", fmt.Errorf("file terlalu besar (maks %dMB)", toExcelMaxInputBytes>>20)
		}
		if bytes.HasPrefix(data, []byte("PK\x03\x04")) || strings.HasSuffix(strings.ToLower(name), ".zip") {
			return toExcelBuildFromZip(bg, data, name)
		}
		return toExcelBuildFromDBBytes(data, name)
	})
}

// toExcelBuildFromDBBytes mengonversi byte file SQLite tunggal.
func toExcelBuildFromDBBytes(data []byte, label string) ([]byte, string, error) {
	if !bytes.HasPrefix(data, []byte(sqliteMagic)) {
		return nil, "", fmt.Errorf("%s bukan file SQLite (.db) valid", label)
	}
	workDir, err := os.MkdirTemp(store.MustBotTempDir(), "toexcel-*")
	if err != nil {
		return nil, "", err
	}
	defer os.RemoveAll(workDir)
	path := filepath.Join(workDir, "input.db")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return nil, "", err
	}
	f := sqlxlsx.NewWorkbook()
	if _, err := sqlxlsx.ConvertInto(f, path, "", toExcelOpts); err != nil {
		return nil, "", err
	}
	out, err := sqlxlsx.Render(f)
	if err != nil {
		return nil, "", err
	}
	return out, toExcelOutName(label), nil
}

// toExcelBuildFromZip mengekstrak semua member .db dalam ZIP menjadi satu
// workbook (sheet berprefix <db>.<tabel>) + sheet ringkasan.
func toExcelBuildFromZip(ctx context.Context, data []byte, zipLabel string) ([]byte, string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, "", fmt.Errorf("ZIP tidak valid: %w", err)
	}
	workDir, err := os.MkdirTemp(store.MustBotTempDir(), "toexcel-*")
	if err != nil {
		return nil, "", err
	}
	defer os.RemoveAll(workDir)

	f := sqlxlsx.NewWorkbook()
	var summary [][]string
	added := 0
	for i, zf := range zr.File {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		if zf.FileInfo().IsDir() {
			continue
		}
		base := filepath.Base(zf.Name)
		if !strings.HasSuffix(strings.ToLower(base), ".db") {
			continue
		}
		out := filepath.Join(workDir, fmt.Sprintf("%d-%s", i, base))
		if err := toExcelExtractZipMember(zf, out); err != nil {
			logfmt.Warn("toexcel: lewati %s (%v)\n", base, err)
			summary = append(summary, []string{base, "gagal", err.Error()})
			continue
		}
		prefix := strings.TrimSuffix(base, filepath.Ext(base))
		rep, cerr := sqlxlsx.ConvertInto(f, out, prefix, toExcelZipOpts)
		if cerr != nil {
			logfmt.Warn("toexcel: konversi %s gagal: %v\n", base, cerr)
			summary = append(summary, []string{base, "gagal", cerr.Error()})
			continue
		}
		summary = append(summary, []string{base, strconv.Itoa(len(rep.Tables)), strconv.Itoa(rep.TotalRows)})
		added++
	}
	if added == 0 {
		return nil, "", fmt.Errorf("tidak ada file .db yang bisa dikonversi di dalam ZIP")
	}
	if err := sqlxlsx.AddInfoSheet(f, "Info", []string{"File DB", "Tabel", "Baris"}, summary); err != nil {
		return nil, "", err
	}
	out, err := sqlxlsx.Render(f)
	if err != nil {
		return nil, "", err
	}
	return out, toExcelOutName(zipLabel), nil
}

// toExcelExtractZipMember menulis satu member ZIP ke path (dibatasi ukuran).
func toExcelExtractZipMember(zf *zip.File, path string) error {
	rc, err := zf.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	dst, err := os.Create(path)
	if err != nil {
		return err
	}
	defer dst.Close()
	n, err := io.Copy(dst, io.LimitReader(rc, toExcelMaxInputBytes+1))
	if err != nil {
		return err
	}
	if n > toExcelMaxInputBytes {
		return fmt.Errorf("member terlalu besar (>%dMB)", toExcelMaxInputBytes>>20)
	}
	return nil
}

// toExcelQueue mengirim status lalu menjalankan build di latar (antrean dlSem).
func toExcelQueue(ctx context.Context, mctx *store.MessageContext, label string, build func(context.Context) ([]byte, string, error)) error {
	select {
	case dlSem <- struct{}{}:
	default:
		return store.ReplyText(ctx, mctx, "Antrean penuh (maks 2 bersamaan), coba lagi sebentar.")
	}
	if _, err := sendDlStatus(ctx, mctx, "Mengubah "+label+" ke Excel…"); err != nil {
		<-dlSem
		return err
	}
	go func() {
		defer func() { <-dlSem }()
		bg := context.Background()
		data, fname, err := build(bg)
		if err != nil {
			_ = store.ReplyText(bg, mctx, "Gagal: "+err.Error())
			return
		}
		if err := sendGetDocument(bg, mctx, data, xlsxMIME, fname); err != nil {
			_ = store.ReplyText(bg, mctx, "Gagal kirim Excel: "+err.Error())
		}
	}()
	return nil
}

// toExcelOutName membangun nama file hasil: <base>-<stempel>.xlsx.
func toExcelOutName(label string) string {
	base := sanitizeFileName(label)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	if base == "" {
		base = "database"
	}
	return base + "-" + timex.Now().Format("20060102-150405") + ".xlsx"
}
