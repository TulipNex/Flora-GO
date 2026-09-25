package plugins

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"wabot-go/internal/ai"
	"wabot-go/internal/store"

	"github.com/ledongthuc/pdf"
	"github.com/xuri/excelize/v2"
)

// Batasan ekstraksi dokumen. Dokumen bisa SANGAT besar sementara token AI
// dibayar per karakter (apalagi provider pay-per-token) — jadi unduhan dan
// hasil dibatasi, selebihnya dipotong dengan catatan jujur ke AI.
const (
	maxDocBytes = 8 << 20 // 8MB file mentah
	// maxDocChars: dokumen sampai sepanjang ini ditempel UTUH ke pesan.
	// Lebih panjang dari itu dibaca bertahap (summarizeLongDocument).
	maxDocChars = 12000 // dihitung sebagai rune, bukan byte
	// docChunkChars: ukuran tiap potongan saat baca bertahap — cukup kecil
	// supaya tiap request ringkas muat nyaman di context model apapun.
	docChunkChars = 8000
	// maxDocChunks: maksimal potongan yang diproses (~64 ribu karakter).
	// Selebihnya dilewati dengan catatan (waktu + token tidak tak terbatas).
	maxDocChunks  = 8
	maxPDFPages   = 30
	maxXLSXSheets = 5
	maxXLSXRows   = 300
	maxXLSXCols   = 30
)

// readTriggerDocument mengambil dokumen dari pesan pemicu AI (lampiran
// langsung atau yang di-reply) dan mengekstrak teksnya SECARA PENUH
// (tanpa potong karakter — batas halaman/baris parse tetap berlaku).
// Kembalian: name (nama file), text (hasil ekstraksi, kosong kalau
// gagal/tidak ada), note (catatan jujur kalau dokumen ada tapi tidak bisa
// dibaca; kosong kalau sukses atau memang tidak ada dokumen).
// Semua kegagalan fail-soft supaya chat tetap jalan dan AI tidak mengarang
// isi dokumen.
func readTriggerDocument(ctx context.Context, mctx *store.MessageContext) (name, text, note string) {
	// Getter protobuf aman pada pesan nil, seperti di extractTriggerImage.
	doc := store.UnwrapMessage(mctx.RawEvt.Message).GetDocumentMessage()
	if doc == nil {
		doc = store.UnwrapMessage(store.ExtractQuotedMessage(mctx.RawEvt)).GetDocumentMessage()
	}
	if doc == nil {
		return "", "", ""
	}

	name = doc.GetFileName()
	if name == "" {
		name = "dokumen"
	}

	data, err := mctx.Client.Download(ctx, doc)
	if err != nil {
		fmt.Printf("⚠️ docread: gagal download dokumen %s: %v\n", name, err)
		return name, "", fmt.Sprintf("[Catatan: ada dokumen %q terlampir tapi tidak bisa diunduh.]", name)
	}
	if len(data) > maxDocBytes {
		fmt.Printf("⚠️ docread: dokumen %s terlalu besar (%d byte), dilewati.\n", name, len(data))
		return name, "", fmt.Sprintf("[Catatan: dokumen %q terlalu besar untuk dibaca (%.1f MB).]", name, float64(len(data))/(1<<20))
	}

	kind := docKind(doc.GetMimetype(), name)
	switch kind {
	case "pdf":
		text, err = extractPDFText(data)
	case "docx":
		text, err = extractDOCXText(data)
	case "xlsx":
		text, err = extractXLSXText(data)
	case "txt":
		text, err = extractPlainText(data)
	default:
		return name, "", fmt.Sprintf("[Catatan: dokumen %q bertipe %s yang tidak didukung. Format yang bisa dibaca: PDF, DOCX, XLSX, dan teks polos (TXT/CSV/Markdown).]", name, kindLabel(doc.GetMimetype(), name))
	}
	if err != nil {
		fmt.Printf("⚠️ docread: gagal ekstrak %s: %v\n", name, err)
		return name, "", fmt.Sprintf("[Catatan: dokumen %q gagal dibaca (%v).]", name, err)
	}
	if strings.TrimSpace(text) == "" {
		return name, "", fmt.Sprintf("[Catatan: dokumen %q tidak mengandung teks yang bisa dibaca (mungkin hasil scan/foto).]", name)
	}
	return name, text, ""
}

// formatDocBlock membungkus teks dokumen jadi blok siap tempel ke pesan
// user. truncated=true menambah penanda jujur bahwa ini hanya potongan.
func formatDocBlock(name, text string, truncated bool) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[Isi dokumen %q:\n%s", name, text))
	if truncated {
		sb.WriteString(fmt.Sprintf("\n(Dokumen dipotong: hanya %d karakter pertama yang dibaca.)]", maxDocChars))
	} else {
		sb.WriteString("]")
	}
	return sb.String()
}

// summarizeLongDocument membaca dokumen PANJANG secara bertahap
// (map-reduce): teks dipecah jadi potongan-potongan, tiap potongan
// diringkas satu request, lalu ringkasan gabungannya dikembalikan sebagai
// SATU blok konteks. Alur chat utama (persona, tools, mood) tetap jalan
// normal di atas blok ini — fungsi ini hanya menyiapkan konteksnya.
//
// Tiap request ringkas murah (teks saja, tanpa tools/persona). Potongan
// diproses paralel (maks 3 bersamaan) supaya tidak lama; yang gagal
// diringkas diberi penanda dan dilewati, bukan menggagalkan semuanya.
// Kalau SEMUA potongan gagal, fallback ke tempelan langsung yang dipotong
// (perilaku lama) supaya user tetap dapat sesuatu.
func summarizeLongDocument(ctx context.Context, provider ai.ProviderAdapter, name, text string) string {
	chunks := splitDocChunks(text)
	fmt.Printf("📄 docread: dokumen %q panjang (%d karakter), dibaca bertahap %d bagian.\n", name, len([]rune(text)), len(chunks))

	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	summaries := make([]string, len(chunks))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 3)
	for i, chunk := range chunks {
		wg.Add(1)
		go func(idx int, part string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			prompt := fmt.Sprintf("Ringkas bagian %d/%d dari dokumen %q secara padat. Pertahankan fakta, angka, nama, dan poin penting. Jangan menjawab pertanyaan apapun, cukup ringkas bagian ini:\n\n%s",
				idx+1, len(chunks), name, part)
			resp, err := provider.Generate(timeoutCtx, ai.GenerateParams{
				History: []ai.Content{{Role: "user", Parts: []ai.Part{{Text: prompt}}}},
			})
			if err != nil || strings.TrimSpace(resp.Text) == "" {
				fmt.Printf("⚠️ docread: ringkas bagian %d/%d gagal: %v\n", idx+1, len(chunks), err)
				summaries[idx] = "[bagian ini gagal diringkas]"
				return
			}
			summaries[idx] = strings.TrimSpace(resp.Text)
		}(i, chunk)
	}
	wg.Wait()

	ok := 0
	for _, s := range summaries {
		if s != "[bagian ini gagal diringkas]" {
			ok++
		}
	}
	if ok == 0 {
		fmt.Printf("⚠️ docread: semua ringkasan %q gagal, fallback tempel langsung.\n", name)
		trimmed, _ := truncateRunes(text, maxDocChars)
		return formatDocBlock(name, trimmed, true)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[Ringkasan dokumen %q (dibaca bertahap dari %d bagian):\n", name, len(chunks)))
	for i, s := range summaries {
		fmt.Fprintf(&sb, "\n-- Bagian %d --\n%s\n", i+1, s)
	}
	if len([]rune(text)) > maxDocChunks*docChunkChars {
		sb.WriteString("\n(Dokumen sangat panjang: hanya bagian awal yang dibaca.)")
	}
	sb.WriteString("]")
	return sb.String()
}

// splitDocChunks memecah teks jadi potongan-potongan sekitar docChunkChars,
// diusahakan putus di batas baris (paragraf utuh). Paragraf tunggal yang
// lebih panjang dari chunk dipotong keras. Total dibatasi maxDocChunks.
func splitDocChunks(text string) []string {
	var chunks []string
	var cur strings.Builder
	curLen := 0
	flush := func() {
		if curLen > 0 {
			chunks = append(chunks, cur.String())
			cur.Reset()
			curLen = 0
		}
	}
	for _, para := range strings.Split(text, "\n") {
		pr := []rune(para)
		// Paragraf raksasa: potong keras per chunk.
		for len(pr) > docChunkChars {
			flush()
			chunks = append(chunks, string(pr[:docChunkChars]))
			pr = pr[docChunkChars:]
		}
		if curLen > 0 && curLen+1+len(pr) > docChunkChars {
			flush()
		}
		if curLen > 0 {
			cur.WriteString("\n")
			curLen++
		}
		cur.WriteString(string(pr))
		curLen += len(pr)
		if len(chunks) >= maxDocChunks {
			break
		}
	}
	flush()
	if len(chunks) > maxDocChunks {
		chunks = chunks[:maxDocChunks]
	}
	return chunks
}

// docKind menentukan jenis dokumen dari mimetype + ekstensi nama file
// (ekstensi sebagai fallback karena mimetype WA kadang generik seperti
// application/octet-stream).
func docKind(mime, name string) string {
	mime = strings.ToLower(strings.TrimSpace(mime))
	ext := strings.ToLower(docExt(name))
	switch {
	case strings.Contains(mime, "pdf") || ext == ".pdf":
		return "pdf"
	case strings.Contains(mime, "wordprocessingml") || ext == ".docx":
		return "docx"
	case strings.Contains(mime, "spreadsheetml") || ext == ".xlsx":
		return "xlsx"
	case strings.HasPrefix(mime, "text/") || mime == "application/json" ||
		ext == ".txt" || ext == ".csv" || ext == ".md" || ext == ".markdown" ||
		ext == ".json" || ext == ".log":
		return "txt"
	default:
		return "other"
	}
}

// kindLabel dipakai untuk pesan "tidak didukung" — tampilkan info paling
// berguna yang ada (mimetype atau ekstensi).
func kindLabel(mime, name string) string {
	if mime != "" {
		return mime
	}
	if ext := docExt(name); ext != "" {
		return "ekstensi " + ext
	}
	return "tidak dikenal"
}

func docExt(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i:]
	}
	return ""
}

// truncateRunes memotong teks ke max rune (aman untuk multi-byte) dan
// melaporkan apakah pemotongan terjadi.
func truncateRunes(text string, max int) (string, bool) {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= max {
		return string(runes), false
	}
	return string(runes[:max]), true
}

// extractPDFText membaca teks PDF per baris visual (GetTextByRow) supaya
// urutannya masuk akal. PDF hasil scan (tanpa layer teks) wajar kosong.
func extractPDFText(data []byte) (string, error) {
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	n := r.NumPage()
	if n > maxPDFPages {
		n = maxPDFPages
	}
	var sb strings.Builder
	for i := 1; i <= n; i++ {
		rows, err := r.Page(i).GetTextByRow()
		if err != nil {
			continue // satu halaman rusak jangan gagalkan seluruh dokumen
		}
		if n > 1 {
			fmt.Fprintf(&sb, "\n--- Halaman %d ---\n", i)
		}
		for _, row := range rows {
			for _, w := range row.Content {
				sb.WriteString(w.S)
			}
			sb.WriteString("\n")
		}
	}
	return sb.String(), nil
}

// extractDOCXText membaca word/document.xml di dalam ZIP DOCX memakai
// stdlib saja (tanpa dependensi tambahan): teks isi <w:t>, ganti paragraf
// <w:p> jadi baris baru. Run (<w:r>) yang terpecah karena formatting
// digabung apa adanya karena memang satu kata yang sama.
func extractDOCXText(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	var docXML io.Reader
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			rc, err := f.Open()
			if err != nil {
				return "", err
			}
			defer rc.Close()
			docXML = rc
			break
		}
	}
	if docXML == nil {
		return "", fmt.Errorf("bukan file DOCX valid (word/document.xml tidak ada)")
	}

	var sb strings.Builder
	dec := xml.NewDecoder(docXML)
	inText := false
	for {
		tok, err := dec.Token()
		if err != nil {
			break // io.EOF = selesai, error lain = berhenti dengan hasil sejauh ini
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "t" {
				inText = true
			}
		case xml.CharData:
			if inText {
				sb.Write([]byte(t))
			}
		case xml.EndElement:
			if t.Name.Local == "t" {
				inText = false
			} else if t.Name.Local == "p" {
				sb.WriteString("\n")
			}
		}
	}
	return sb.String(), nil
}

// extractXLSXText membaca XLSX via excelize: tiap sheet jadi blok
// "== Sheet: nama ==" berisi baris-baris sel yang digabung " | ".
// Sel kosong di ujung baris dan baris yang sepenuhnya kosong dilewati
// supaya tidak boros token.
func extractXLSXText(data []byte) (string, error) {
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	defer f.Close()

	sheets := f.GetSheetList()
	if len(sheets) > maxXLSXSheets {
		sheets = sheets[:maxXLSXSheets]
	}
	var sb strings.Builder
	for _, sheet := range sheets {
		rows, err := f.GetRows(sheet)
		if err != nil {
			continue
		}
		if len(rows) > maxXLSXRows {
			rows = rows[:maxXLSXRows]
		}
		fmt.Fprintf(&sb, "\n== Sheet: %s ==\n", sheet)
		for _, row := range rows {
			if len(row) > maxXLSXCols {
				row = row[:maxXLSXCols]
			}
			end := len(row)
			for end > 0 && strings.TrimSpace(row[end-1]) == "" {
				end--
			}
			if end == 0 {
				continue
			}
			sb.WriteString(strings.Join(row[:end], " | "))
			sb.WriteString("\n")
		}
	}
	return sb.String(), nil
}

// extractPlainText untuk TXT/CSV/Markdown/JSON — teruskan apa adanya.
// Ditolak kalau bukan UTF-8 valid (kemungkinan file biner yang salah
// ekstensi) supaya tidak mengirim sampah ke AI.
func extractPlainText(data []byte) (string, error) {
	if !utf8.Valid(data) {
		return "", fmt.Errorf("bukan teks valid (kemungkinan file biner)")
	}
	return string(data), nil
}
