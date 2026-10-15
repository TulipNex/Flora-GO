package bridge

// Tool buat_excel: bangun file .xlsx dari data terstruktur (headers +
// rows) lalu kirim sebagai dokumen. Akses owner + trusted user (gerbang
// sama seperti agent Flora sendiri — konsisten: siapa boleh ngobrol,
// boleh pakai tool ini).
//
// Dipakai HANYA bila user eksplisit minta jadikan excel/tabel (JANGAN
// tawarkan duluan). Data berasal dari vision/obrolan yang sudah disepakati
// (termasuk koreksi user di chat) — tool ini TIDAK membaca gambar dan
// TIDAK mengarang baris; hanya merakit + mengirim file.

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"wabot-go/internal/ai"
	"wabot-go/internal/store"
	"wabot-go/internal/timex"
	"wabot-go/plugins"

	"github.com/xuri/excelize/v2"
	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func init() {
	ai.RegisterTool(ai.ToolDefinition{
		Name: "buat_excel",
		Description: "Membuat file Excel (.xlsx) dari data terstruktur lalu mengirimnya ke chat ini. " +
			"Hanya untuk OWNER + user terpercaya (ditolak untuk lainnya). " +
			"PAKAI HANYA bila user eksplisit meminta jadikan excel/tabel/data menjadi file " +
			"(mis. 'jadikan excel', 'buatkan tabelnya', 'ekspor ke excel') — JANGAN tawarkan duluan " +
			"saat menjawab gambar/teks biasa. " +
			"headers: nama kolom sesuai permintaan/kesepakatan (wajib, tak boleh kosong). " +
			"rows: tiap baris WAJIB sepanjang jumlah headers (baris tak seragam = tool menolak + sebut nomor barisnya, " +
			"jangan potong diam-diam). Isi dari hasil vision/obrolan yang sudah disepakati user " +
			"(termasuk koreksinya) — JANGAN karang angka/teks yang tak terbaca; sel tak terbaca = string kosong. " +
			"Batasan: maks 200 baris × 20 kolom. Balasan tool berupa nama file — sampaikan ke user " +
			"beserta kalimat 'cek ulang angka penting sebelum dipakai'.",
		Parameters: ai.ParameterSchema{
			Type: "OBJECT",
			Properties: map[string]ai.PropertySchema{
				"headers": {
					Type:        "STRING",
					Description: "Wajib: nama kolom dipisah koma, mis. 'nama,harga,jumlah'.",
				},
				"rows": {
					Type:        "STRING",
					Description: "Wajib: baris dipisah titik-koma, sel dipisah koma. Mis. 'kopi,20000,2;teh,15000,1'. Sel yang mengandung koma/titik-koma tidak didukung — minta user sederhanakan dulu.",
				},
				"nama_file": {
					Type:        "STRING",
					Description: "SELALU isi nama bermakna dari konteks data (mis. rekap-keuangan-oktober, daftar-harga-pasar, absensi-kelas-a). Kosongkan hanya bila benar-benar tak ada konteks (fallback tabel-YYYYMMDD).",
				},
			},
			Required: []string{"headers", "rows"},
		},
		Execute: executeBuatExcel,
	})
}

// Batas tool (keputusan desain: tabel raksasa = token mahal + risiko
// halusinasi naik; pecah jadi beberapa file bila perlu).
const (
	buatExcelMaxRows = 200
	buatExcelMaxCols = 20
)

// executeBuatExcel memvalidasi ketat lalu membangun + mengirim .xlsx.
func executeBuatExcel(actx *ai.AgentContext, args map[string]interface{}) (string, error) {
	mctx, err := extractMctx(actx)
	if err != nil {
		return "", err
	}
	ctx := actx.Context
	if ctx == nil {
		ctx = context.Background()
	}
	if !plugins.IsOwnerOrTrusted(ctx, mctx) {
		return "", fmt.Errorf("hanya owner dan user terpercaya yang boleh memakai aksi ini")
	}

	headers, err := splitCells(argStr(args, "headers"), ",")
	if err != nil || len(headers) == 0 {
		return "", fmt.Errorf("headers wajib diisi (nama kolom dipisah koma)")
	}
	if len(headers) > buatExcelMaxCols {
		return "", fmt.Errorf("maksimal %d kolom", buatExcelMaxCols)
	}
	rawRows := strings.TrimSpace(argStr(args, "rows"))
	if rawRows == "" {
		return "", fmt.Errorf("rows wajib diisi")
	}
	var rows [][]string
	for i, line := range strings.Split(rawRows, ";") {
		cells, err := splitCells(line, ",")
		if err != nil {
			return "", fmt.Errorf("baris %d kosong", i+1)
		}
		if len(cells) != len(headers) {
			return "", fmt.Errorf("baris %d berisi %d sel, harus %d mengikuti headers", i+1, len(cells), len(headers))
		}
		rows = append(rows, cells)
	}
	if len(rows) > buatExcelMaxRows {
		return "", fmt.Errorf("maksimal %d baris", buatExcelMaxRows)
	}

	fname := sanitizeExcelName(argStr(args, "nama_file"))
	if fname == "" {
		fname = "tabel-" + timex.Now().Format("20060102")
	}
	fname = strings.TrimSuffix(fname, ".xlsx") + ".xlsx"

	xlsx, err := buildGenericXLSX(headers, rows)
	if err != nil {
		return "", err
	}
	if err := sendExcelDocument(mctx, xlsx, fname); err != nil {
		return "", err
	}
	return fmt.Sprintf("Berhasil: file %s (%d baris) terkirim ke chat. Cek ulang angka penting sebelum dipakai.", fname, len(rows)), nil
}

// splitCells memecah teks per separator + trim tiap sel (kosong = "").
func splitCells(s, sep string) ([]string, error) {
	parts := strings.Split(s, sep)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.TrimSpace(p))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("kosong")
	}
	return out, nil
}

// buildGenericXLSX merakit workbook 1 sheet: header + data + angka asli
// bila numerik + border + autofit (pola buildFinanceXLSX).
func buildGenericXLSX(headers []string, rows [][]string) ([]byte, error) {
	f := excelize.NewFile()
	sheet := "Tabel"
	if _, err := f.NewSheet(sheet); err != nil {
		return nil, err
	}
	maxW := make([]int, len(headers))
	for c, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(c+1, 1)
		if err := f.SetCellValue(sheet, cell, h); err != nil {
			return nil, err
		}
		if n := len([]rune(h)); n > maxW[c] {
			maxW[c] = n
		}
	}
	for i, row := range rows {
		rn := i + 2
		for c, s := range row {
			cell, _ := excelize.CoordinatesToCellName(c+1, rn)
			var v interface{} = s
			if n, ok := excelNum(s); ok {
				v = n
			}
			if err := f.SetCellValue(sheet, cell, v); err != nil {
				return nil, err
			}
			if n := len([]rune(s)); n > maxW[c] {
				maxW[c] = n
			}
		}
	}
	borderStyle, err := f.NewStyle(&excelize.Style{
		Border: []excelize.Border{
			{Type: "left", Style: 1},
			{Type: "right", Style: 1},
			{Type: "top", Style: 1},
			{Type: "bottom", Style: 1},
		},
	})
	if err != nil {
		return nil, err
	}
	lastCell, _ := excelize.CoordinatesToCellName(len(headers), len(rows)+1)
	if err := f.SetCellStyle(sheet, "A1", lastCell, borderStyle); err != nil {
		return nil, err
	}
	for c := range headers {
		w := float64(maxW[c] + 2)
		if w < 8 {
			w = 8
		}
		if w > 50 {
			w = 50
		}
		colName, cerr := excelize.ColumnNumberToName(c + 1)
		if cerr != nil {
			return nil, cerr
		}
		if err := f.SetColWidth(sheet, colName, colName, w); err != nil {
			return nil, err
		}
	}
	if err := f.DeleteSheet("Sheet1"); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		return nil, err
	}
	if buf.Len() == 0 {
		return nil, fmt.Errorf("file xlsx kosong")
	}
	return buf.Bytes(), nil
}

// excelNum mengubah string digit bulat jadi float64 (sel teks numerik
// tampil sebagai angka Excel). Non-digit = false (tulis apa adanya).
func excelNum(s string) (float64, bool) {
	t := strings.ReplaceAll(strings.TrimSpace(s), " ", "")
	if t == "" {
		return 0, false
	}
	for _, r := range t {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	if len(t) > 15 {
		return 0, false
	}
	n, err := strconv.ParseFloat(t, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// sanitizeExcelName membersihkan nama file dari model: buang karakter
// path/ilegal (hanya nama tampil WA, tapi tampil aneh bila kotor), maks 80
// rune, "" bila kosong setelah dibersihkan (pemanggil pakai fallback).
func sanitizeExcelName(s string) string {
	var sb strings.Builder
	for _, r := range strings.TrimSpace(s) {
		if strings.ContainsRune(`/\:?*"<>|`, r) {
			continue
		}
		sb.WriteRune(r)
	}
	out := strings.TrimSpace(sb.String())
	if out == "" {
		return ""
	}
	if r := []rune(out); len(r) > 80 {
		out = string(r[:80])
	}
	return out
}

// excelUploadTimeout membatasi upload+kirim dokumen.
const excelUploadTimeout = 2 * time.Minute

// sendExcelDocument mengupload + mengirim byte sebagai dokumen .xlsx.
func sendExcelDocument(mctx *store.MessageContext, data []byte, filename string) error {
	ctx, cancel := context.WithTimeout(context.Background(), excelUploadTimeout)
	defer cancel()
	uploaded, err := mctx.Client.Upload(ctx, data, whatsmeow.MediaDocument)
	if err != nil {
		return fmt.Errorf("gagal upload dokumen: %v", err)
	}
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, &waE2E.Message{
		DocumentMessage: &waE2E.DocumentMessage{
			URL:           proto.String(uploaded.URL),
			DirectPath:    proto.String(uploaded.DirectPath),
			MediaKey:      uploaded.MediaKey,
			Mimetype:      proto.String("application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"),
			FileEncSHA256: uploaded.FileEncSHA256,
			FileSHA256:    uploaded.FileSHA256,
			FileLength:    proto.Uint64(uploaded.FileLength),
			FileName:      proto.String(filename),
		},
	})
	if err != nil {
		return fmt.Errorf("gagal kirim dokumen: %v", err)
	}
	if resp.ID == "" {
		return fmt.Errorf("gagal kirim dokumen: respons kosong")
	}
	return nil
}
