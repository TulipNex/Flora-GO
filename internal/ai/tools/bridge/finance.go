package bridge

// Tools bridge keuangan Flora — database SQLite LOKAL (store.Finance).
// Tanpa Google Sheet, tanpa foto bukti (keputusan owner: kolom bukti
// dihapus), tanpa nomor tersimpan (enumerasi 1..N saat export).
//
// catat_keuangan: validasi ketat + INSERT langsung (req_id deterministik
// anti-ganda). export_keuangan: SELECT + .xlsx 7 kolom + kirim dokumen.

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"wabot-go/internal/ai"
	native "wabot-go/internal/ai/tools/native"
	"wabot-go/internal/store"
	"wabot-go/internal/timex"

	"github.com/google/uuid"
	"github.com/xuri/excelize/v2"
	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func init() {
	ai.RegisterTool(ai.ToolDefinition{
		Name: "catat_keuangan",
		Description: "Mencatat transaksi keuangan OWNER ke database lokal (privat per nomor owner). " +
			"Hanya untuk OWNER (ditolak untuk non-owner). " +
			"ATURN PENTING: JANGAN panggil sebelum info lengkap — kalau tanggal/tipe/kategori/nominal/sumber " +
			"belum jelas dari permintaan user, TANYAKAN dulu (kapan? pemasukan/pengeluaran? kategori apa? berapa? " +
			"dari kas atau rekening?). " +
			"sumber WAJIB kas/rekening (alias cash/tunai/bank/transfer dipetakan otomatis; " +
			"di luar itu DITOLAK tool — tanyakan bila tak disebut, e-wallet/QRIS juga tanyakan maksudnya) — " +
			"tidak ada default diam-diam. " +
			"tanggal format YYYY-MM-DD (default hari ini bila tak disebut); tipe wajib masuk/keluar; " +
			"nominal integer rupiah >0 (urai '50 ribu'→50000, '1,5 juta'→1500000). " +
			"Balasan tool berupa ID baris — sampaikan ringkas ke user.",
		Parameters: ai.ParameterSchema{
			Type: "OBJECT",
			Properties: map[string]ai.PropertySchema{
				"tanggal": {
					Type:        "STRING",
					Description: "Tanggal YYYY-MM-DD (default hari ini).",
				},
				"tipe": {
					Type:        "STRING",
					Description: "Wajib: masuk atau keluar.",
				},
				"kategori": {
					Type:        "STRING",
					Description: "Kategori, mis. makan, transport, gaji (opsional bila keterangan sudah jelas).",
				},
				"nominal": {
					Type:        "NUMBER",
					Description: "Wajib: nominal rupiah integer >0.",
				},
				"sumber": {
					Type:        "STRING",
					Description: "Wajib: kas atau rekening (tanyakan bila tak jelas; e-wallet/QRIS tanyakan maksudnya).",
				},
				"keterangan": {
					Type:        "STRING",
					Description: "Keterangan bebas (opsional bila kategori sudah menjelaskan).",
				},
			},
			Required: []string{"tipe", "nominal", "sumber"},
		},
		Execute: executeCatatKeuangan,
	})

	ai.RegisterTool(ai.ToolDefinition{
		Name: "export_keuangan",
		Description: "Mengekspor catatan keuangan OWNER ke file Excel (.xlsx) 7 kolom " +
			"(Nomor,Id,Transaksi,Masuk,Keluar,Sumber,Tanggal — Nomor enumerasi 1..N terlama→terbaru) lalu mengirimnya ke chat ini. " +
			"Hanya untuk OWNER. Parameter tanggal format YYYY-MM-DD; kosongkan untuk 30 hari terakhir. " +
			"Tabel ber-border + lebar kolom autofit.",
		Parameters: ai.ParameterSchema{
			Type: "OBJECT",
			Properties: map[string]ai.PropertySchema{
				"dari": {
					Type:        "STRING",
					Description: "Tanggal mulai YYYY-MM-DD (opsional).",
				},
				"sampai": {
					Type:        "STRING",
					Description: "Tanggal akhir YYYY-MM-DD (opsional).",
				},
				"sumber": {
					Type:        "STRING",
					Description: "Filter kas/rekening (opsional).",
				},
				"kategori": {
					Type:        "STRING",
					Description: "Filter teks transaksi (opsional).",
				},
			},
		},
		Execute: executeExportKeuangan,
	})
}

// executeCatatKeuangan memvalidasi ketat lalu INSERT langsung ke SQLite.
func executeCatatKeuangan(actx *ai.AgentContext, args map[string]interface{}) (string, error) {
	mctx, err := extractMctx(actx)
	if err != nil {
		return "", err
	}
	if err := requireOwnerTool(mctx); err != nil {
		return "", err
	}
	owner, err := native.OwnerPhone(actx)
	if err != nil {
		return "", err
	}

	tipe := strings.ToLower(strings.TrimSpace(argStr(args, "tipe")))
	if tipe != "masuk" && tipe != "keluar" {
		return "", fmt.Errorf("tipe harus masuk/keluar — tanyakan dulu bila belum jelas")
	}
	nominal, ok := argNum(args, "nominal")
	if !ok || nominal <= 0 || nominal != float64(int64(nominal)) {
		return "", fmt.Errorf("nominal harus integer rupiah >0")
	}
	if nominal > native.MaxNominal {
		return "", fmt.Errorf("nominal terlalu besar (maks 15 digit)")
	}
	tanggal := argStr(args, "tanggal")
	if tanggal == "" {
		// Default zona aktif timex (bukan jam server): panel bisa UTC.
		tanggal = timex.Today()
	}
	if !validTanggalStr(tanggal) {
		return "", fmt.Errorf("tanggal harus YYYY-MM-DD")
	}
	kategori := argStr(args, "kategori")
	keterangan := argStr(args, "keterangan")
	if kategori == "" && keterangan == "" {
		return "", fmt.Errorf("kategori/keterangan kosong — tanyakan dulu transaksinya apa")
	}
	sumber := native.NormSumber(argStr(args, "sumber"))
	if sumber == "" {
		return "", fmt.Errorf("sumber harus kas/rekening — tanyakan dulu dari kas atau rekening")
	}

	transaksi := kategori
	if keterangan != "" {
		if transaksi != "" {
			transaksi += ": "
		}
		transaksi += keterangan
	}
	var masuk, keluar *int64
	n := int64(nominal)
	if tipe == "masuk" {
		masuk = &n
	} else {
		keluar = &n
	}
	id := strings.ReplaceAll(uuid.NewString(), "-", "")
	isDup, err := store.Finance().InsertKeuangan(actx.Context, id, owner, transaksi, masuk, keluar, sumber, tanggal,
		financeReqID(mctx, tanggal, tipe, n, sumber, kategori, keterangan))
	if err != nil {
		return "", fmt.Errorf("gagal mencatat: %v", err)
	}
	if isDup {
		return fmt.Sprintf("Sudah tercatat sebelumnya (duplikat ditolak, tidak catat ulang)."), nil
	}
	return fmt.Sprintf("Berhasil: tercatat (%s %d %s, %s).", tipe, n, transaksi, sumber), nil
}

// executeExportKeuangan membaca baris lalu membangun .xlsx 7 kolom
// (terlama→terbaru, Nomor enumerasi) dan mengirimnya sebagai dokumen.
func executeExportKeuangan(actx *ai.AgentContext, args map[string]interface{}) (string, error) {
	mctx, err := extractMctx(actx)
	if err != nil {
		return "", err
	}
	if err := requireOwnerTool(mctx); err != nil {
		return "", err
	}
	owner, err := native.OwnerPhone(actx)
	if err != nil {
		return "", err
	}
	if err := native.CheckFilterDates(argStr(args, "dari"), argStr(args, "sampai")); err != nil {
		return "", err
	}
	if err := native.CheckFilterSumber(argStr(args, "sumber")); err != nil {
		return "", err
	}
	dari, sampai := exportRange(argStr(args, "dari"), argStr(args, "sampai"))
	rows, truncated, err := store.Finance().SearchKeuangan(actx.Context, store.FinanceFilter{
		Owner:  owner,
		Teks:   argStr(args, "kategori"),
		Dari:   dari,
		Sampai: sampai,
		Sumber: native.NormSumber(argStr(args, "sumber")),
		Limit:  500,
	})
	if err != nil {
		return "", fmt.Errorf("gagal baca keuangan: %v", err)
	}
	if len(rows) == 0 {
		return "Tidak ada data pada periode itu — file tidak dibuat.", nil
	}
	// Search mengembalikan terbaru-dulu; file disusun terlama→terbaru
	// supaya Nomor 1..N natural untuk laporan.
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}

	xlsx, fname, err := buildFinanceXLSX(rows)
	if err != nil {
		return "", err
	}
	if err := sendFinanceDocument(mctx, xlsx, fname); err != nil {
		return "", err
	}
	msg := fmt.Sprintf("Berhasil: file %s (%d baris) terkirim ke chat.", fname, len(rows))
	if truncated {
		msg += " PERINGATAN: data terpotong limit 500 — masih ada baris lebih tua. Persempit tanggal untuk sisanya."
	}
	return msg, nil
}

// buildFinanceXLSX merakit workbook 1 sheet dengan header kapital + 7 kolom
// (Nomor enumerasi 1..N,Id,Transaksi,Masuk,Keluar,Sumber,Tanggal). Angka
// ditulis sebagai angka Excel asli (SUM/sort benar). Border tipis + autofit.
func buildFinanceXLSX(rows []store.FinanceRow) ([]byte, string, error) {
	f := excelize.NewFile()
	sheet := "Keuangan"
	if _, err := f.NewSheet(sheet); err != nil {
		return nil, "", err
	}
	headers := []string{"Nomor", "Id", "Transaksi", "Masuk", "Keluar", "Sumber", "Tanggal"}
	// maxW melacak lebar konten per kolom (rune) untuk autofit.
	maxW := make([]int, len(headers))
	cellStr := make([][]string, 0, len(rows)+1)
	cellVals := make([][]interface{}, 0, len(rows)+1)
	headRow := append([]string{}, headers...)
	cellStr = append(cellStr, headRow)
	headVals := make([]interface{}, len(headers))
	for c, h := range headers {
		headVals[c] = h
		if len([]rune(h)) > maxW[c] {
			maxW[c] = len([]rune(h))
		}
	}
	cellVals = append(cellVals, headVals)
	for i, r := range rows {
		row := []string{
			strconv.Itoa(i + 1),
			r.ID,
			r.Transaksi,
			financeNumStr(r.Masuk),
			financeNumStr(r.Keluar),
			r.Sumber,
			r.Tanggal,
		}
		cellStr = append(cellStr, row)
		for c, s := range row {
			if n := len([]rune(s)); n > maxW[c] {
				maxW[c] = n
			}
		}
		cellVals = append(cellVals, []interface{}{
			i + 1,
			r.ID,
			r.Transaksi,
			financeNumValPtr(r.Masuk),
			financeNumValPtr(r.Keluar),
			r.Sumber,
			r.Tanggal,
		})
	}
	// Tulis sel.
	for i, row := range cellVals {
		rn := i + 1
		for c, v := range row {
			cell, _ := excelize.CoordinatesToCellName(c+1, rn)
			if err := f.SetCellValue(sheet, cell, v); err != nil {
				return nil, "", err
			}
		}
	}
	// Border tipis seluruh area tabel.
	borderStyle, err := f.NewStyle(&excelize.Style{
		Border: []excelize.Border{
			{Type: "left", Style: 1},
			{Type: "right", Style: 1},
			{Type: "top", Style: 1},
			{Type: "bottom", Style: 1},
		},
	})
	if err != nil {
		return nil, "", err
	}
	lastCell, _ := excelize.CoordinatesToCellName(len(headers), len(cellStr))
	if err := f.SetCellStyle(sheet, "A1", lastCell, borderStyle); err != nil {
		return nil, "", err
	}
	// Autofit: max konten + padding, dibatasi per kolom.
	caps := []float64{12, 40, 40, 15, 15, 12, 13}
	for c := range headers {
		w := float64(maxW[c] + 2)
		if w < 8 {
			w = 8
		}
		if c < len(caps) && w > caps[c] {
			w = caps[c]
		}
		colName, cerr := excelize.ColumnNumberToName(c + 1)
		if cerr != nil {
			return nil, "", cerr
		}
		if err := f.SetColWidth(sheet, colName, colName, w); err != nil {
			return nil, "", err
		}
	}
	if err := f.DeleteSheet("Sheet1"); err != nil {
		return nil, "", err
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		return nil, "", err
	}
	if buf.Len() == 0 {
		return nil, "", fmt.Errorf("file xlsx kosong")
	}
	fname := "keuangan-" + timex.Now().Format("20060102") + ".xlsx"
	return buf.Bytes(), fname, nil
}

// financeNumStr memformat pointer nominal int64 untuk ukur lebar (""
// bila nil).
func financeNumStr(v *int64) string {
	if v == nil {
		return ""
	}
	return strconv.FormatInt(*v, 10)
}

// financeNumValPtr varian pointer nominal (nil → sel kosong "").
func financeNumValPtr(v *int64) interface{} {
	if v == nil {
		return ""
	}
	return *v
}

// financeNumPtr mengubah nilai campuran JSON jadi pointer nominal int64:
// float64/string digit ("50000") atau nil. Dipertahankan untuk kompatibilitas
// pemanggil lama selama migrasi.
func financeNumPtr(v interface{}) *int64 {
	switch t := v.(type) {
	case float64:
		if t == float64(int64(t)) {
			out := int64(t)
			return &out
		}
	case string:
		s := strings.ReplaceAll(strings.TrimSpace(t), " ", "")
		if s == "" {
			return nil
		}
		if financeDigitsRe.MatchString(s) {
			if n, err := strconv.ParseInt(s, 10, 64); err == nil {
				return &n
			}
		}
	}
	return nil
}

// financeDigitsRe mengenali string digit bulat.
var financeDigitsRe = regexp.MustCompile(`^\d{1,15}$`)

// financeCellStr memformat nilai campuran jadi teks (ukur lebar / fallback).
func financeCellStr(v interface{}) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// exportRange mengisi tanggal kosong dengan jendela 30 hari terakhir zona
// aktif — cermin defaultRange30 sisi native untuk baca: default hanya bila
// KEDUANYA kosong; bila satu sisi diisi, sisi lain terbuka.
func exportRange(dari, sampai string) (string, string) {
	if strings.TrimSpace(dari) == "" && strings.TrimSpace(sampai) == "" {
		today := timex.Now()
		return today.AddDate(0, 0, -30).Format("2006-01-02"), today.Format("2006-01-02")
	}
	return dari, sampai
}

// financeUploadTimeout membatasi upload+kirim dokumen export.
const financeUploadTimeout = 2 * time.Minute

// sendFinanceDocument mengupload + mengirim byte sebagai dokumen (khusus
// hasil export — pola sendDlDocument downloader, ditulis lokal karena helper
// itu unexported dan milik jalur downloader). Timeout sendiri: upload file
// 500-baris stall tidak boleh gantung agent-loop selamanya.
func sendFinanceDocument(mctx *store.MessageContext, data []byte, filename string) error {
	ctx, cancel := context.WithTimeout(context.Background(), financeUploadTimeout)
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

// financeReqID membuat idempotency key DETERMINISTIK dari pesan pemicu +
// isi transaksi (bukan UUID acak per panggilan). Retry sah menghasilkan key
// yang SAMA sehingga UNIQUE constraint menolak duplikat. Tanpa stanza ID,
// fallback ke UUID acak seperti sebelumnya.
func financeReqID(mctx *store.MessageContext, tanggal, tipe string, nominal int64, sumber, kategori, keterangan string) string {
	stanza := ""
	if mctx != nil && mctx.RawEvt != nil {
		stanza = mctx.RawEvt.Info.ID
	}
	if strings.TrimSpace(stanza) == "" {
		return reqID()
	}
	sum := sha1.Sum([]byte(strings.Join([]string{
		stanza, tanggal, tipe, strconv.FormatInt(nominal, 10), sumber, kategori, keterangan,
	}, "\x00")))
	return hex.EncodeToString(sum[:])
}

// reqID membuat idempotency key unik per panggilan tool (fallback bila
// tidak ada identitas pesan pemicu).
func reqID() string {
	return strings.ReplaceAll(uuid.NewString(), "-", "")
}

// argStr/argNum helper kecil baca argumen tool (toleran int/float/string).
func argStr(args map[string]interface{}, key string) string {
	s, _ := args[key].(string)
	return strings.TrimSpace(s)
}

// argNum membaca angka argumen (cermin argNumberOk native): float64/int/
// int64 langsung; string digit (spasi/koma di-strip). Nilai yang lolos di
// sini tetap divalidasi ketat oleh pemanggil (integer, >0, cap).
func argNum(args map[string]interface{}, key string) (float64, bool) {
	switch v := args[key].(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case string:
		s := strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(v), " ", ""), ",", "")
		if n, err := strconv.ParseFloat(s, 64); err == nil {
			return n, true
		}
		return 0, false
	default:
		return 0, false
	}
}

// validTanggalStr true bila string tanggal format YYYY-MM-DD valid kalender.
func validTanggalStr(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) != 10 || s[4] != '-' || s[7] != '-' {
		return false
	}
	for i, c := range s {
		if i == 4 || i == 7 {
			continue
		}
		if c < '0' || c > '9' {
			return false
		}
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return false
	}
	return t.Format("2006-01-02") == s
}
