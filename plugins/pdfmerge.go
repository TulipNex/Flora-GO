package plugins

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"wabot-go/internal/store"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func init() {
	registerPlugin(RegisterPDFMerge)
}

// RegisterPDFMerge mendaftarkan command `.pdfmerge` kategori Alat (semua
// user boleh pakai): gabungkan MAKSIMAL 15 PDF via pdfcpu murni lokal.
//
// Alur: `.pdfmerge` (tampilkan menu kata) → `mulai` → kirim PDF (reaksi ✅
// per file diterima) → `daftar` lihat urutan → reply dokumen + angka untuk
// susun ulang → `nama X` (opsional) → `gabung` → `batal` bila berubah
// pikiran. Kata tunggal Indonesia primer (Inggris alias), cocok persis
// seluruh pesan, hanya berlaku terkait sesi sendiri.
func RegisterPDFMerge(r *Registry) {
	r.Register(Command{
		Name:        "pdfmerge",
		Description: "Gabung s.d. 15 PDF (.pdfmerge untuk menu)",
		Category:    CategoryAlat,
		Handler:     handlePDFMerge,
	})
}

// Batas staging (keputusan owner): 15 file, total 200MB per sender (tanpa
// batas per file), idle 30 menit. Hasil merge ≤100MB (getMaxFileSize).
const (
	pdfMergeMaxFiles = 15
	pdfMergeMaxTotal = 200 << 20
	pdfMergeIdleTTL  = 30 * time.Minute
)

// pdfStageFile adalah satu PDF tersimpan di staging.
type pdfStageFile struct {
	msgID string // stanza ID pesan asli (untuk cocokkan reply reorder)
	name  string // nama unik tampil (doc.pdf, doc(2).pdf, ...)
	data  []byte
}

// pdfStage adalah sesi staging satu sender (global: private+grup digabung).
type pdfStage struct {
	files    []pdfStageFile
	outName  string // nama hasil kustom ("" = default file pertama + " merge")
	merging  bool   // kunci saat merge berjalan (file baru ditolak)
	timer    *time.Timer
	totalLen int64
}

var (
	pdfStagesMu sync.Mutex
	pdfStages   = map[string]*pdfStage{}
)

// pdfStageKey adalah kunci staging global per sender.
func pdfStageKey(sender types.JID) string {
	return sender.ToNonAD().String()
}

func handlePDFMerge(ctx context.Context, mctx *store.MessageContext) error {
	if len(mctx.Args) == 0 {
		return store.ReplyText(ctx, mctx,
			"*TOOL MERGE PDF*\n"+
				"\n"+
				"Daftar perintah tanpa prefix :\n"+
				"*Mulai/Start*\n"+
				"> buka sesi upload\n"+
				"*Daftar/List*\n"+
				"> lihat urutan file\n"+
				"*Nama/Name* _<nama file>_\n"+
				"> nama file output (opsional)\n"+
				"*Gabung/Merge*\n"+
				"> gabungkan sesuai urutan\n"+
				"*Batal/Cancel*\n"+
				"> buang sesi\n"+
				"\n"+
				"Setelah mulai, kirim PDF ke chat ini, maksimal 15 dokumen.")
	}
	return store.ReplyText(ctx, mctx, "Ketik salah satu kata: mulai, daftar, gabung, batal, nama <nama file>.")
}

// pdfWord adalah kata tunggal + alias Inggrisnya.
var pdfWords = map[string]string{
	"mulai": "mulai", "start": "mulai",
	"daftar": "daftar", "list": "daftar",
	"gabung": "gabung", "merge": "gabung",
	"batal": "batal", "cancel": "batal",
}

// pdfWordArg memisahkan kata perintah dari argumen ("nama X" → ("nama","X")).
func pdfWordArg(text string) (word, arg string) {
	t := strings.TrimSpace(text)
	if i := strings.IndexAny(t, " \t"); i >= 0 {
		return strings.ToLower(t[:i]), strings.TrimSpace(t[i+1:])
	}
	return strings.ToLower(t), ""
}

// CheckPDFStaging dipanggil handler untuk tiap pesan non-command: kata
// tunggal sesi, tampung PDF (reaksi ✅/❌), atau reply-angka reorder.
// True = dikonsumsi. Aturan: kata cocok persis; sesi-terkait saja, kecuali
// "mulai" yang membuka sesi; tanpa sesi + bukan "mulai" = pesan biasa.
func CheckPDFStaging(client *whatsmeow.Client, evt *events.Message) bool {
	key := pdfStageKey(evt.Info.Sender)
	pdfStagesMu.Lock()
	st, ok := pdfStages[key]
	pdfStagesMu.Unlock()

	ctx := context.Background()
	mctx := &store.MessageContext{Client: client, Chat: evt.Info.Chat, Sender: evt.Info.Sender, RawEvt: evt}
	text := handlerText(evt)

	// Kata tunggal (persis seluruh pesan). "nama"/"name" membawa argumen;
	// kata lain harus tanpa argumen.
	word, arg := pdfWordArg(text)
	if word == "nama" || word == "name" {
		return pdfStageName(ctx, mctx, key, st, ok, arg)
	}
	if arg == "" {
		if canon, hit := pdfWords[word]; hit {
			return pdfStageWord(ctx, mctx, key, st, ok, canon)
		}
	}

	// Tanpa sesi aktif: bukan urusan staging.
	if !ok || st.merging {
		return false
	}

	// Dokumen PDF (langsung) → tampung + reaksi.
	if doc := store.UnwrapMessage(evt.Message).GetDocumentMessage(); doc != nil {
		return pdfStageAdd(ctx, mctx, key, st, evt, doc)
	}
	// Teks angka + reply ke dokumen staging → reorder.
	if n, isNum := pdfParseOrder(text); isNum {
		if qid := store.ExtractStanzaID(evt); qid != "" {
			if consumed := pdfStageReorder(ctx, mctx, key, st, qid, n); consumed {
				return true
			}
		}
	}
	return false
}

// pdfStageWord mengeksekusi kata tunggal terkait sesi.
func pdfStageWord(ctx context.Context, mctx *store.MessageContext, key string, st *pdfStage, ok bool, canon string) bool {
	switch canon {
	case "mulai":
		if ok {
			_ = store.ReplyText(ctx, mctx, fmt.Sprintf("Sesi upload masih aktif (%d file). Lanjut kirim file, atau batal untuk mulai baru.", len(st.files)))
			return true
		}
		pdfStagesMu.Lock()
		pdfStages[key] = &pdfStage{}
		pdfStagesMu.Unlock()
		pdfStageTouch(key)
		_ = store.ReplyText(ctx, mctx,
			"Mode upload PDF *AKTIF*. Kirim file PDF ke chat ini.\n"+
				"\n"+
				"*Daftar/List*\n"+
				"> lihat urutan file\n"+
				"*Nama/Name* _<nama file>_\n"+
				"> nama file output (opsional)\n"+
				"*Gabung/Merge*\n"+
				"> gabungkan sesuai urutan\n"+
				"*Batal/Cancel*\n"+
				"> buang sesi")
		return true
	case "daftar":
		if !ok {
			return false
		}
		pdfStagesMu.Lock()
		list := pdfStageList(st)
		pdfStagesMu.Unlock()
		_ = store.ReplyText(ctx, mctx, list)
		return true
	case "gabung":
		if !ok {
			return false
		}
		_ = pdfStageFinish(ctx, mctx)
		return true
	case "batal":
		if !ok {
			return false
		}
		pdfStageCancel(ctx, mctx, true)
		return true
	}
	return false
}

// pdfStageName menyimpan nama hasil kustom (tambah .pdf bila perlu).
func pdfStageName(ctx context.Context, mctx *store.MessageContext, key string, st *pdfStage, ok bool, arg string) bool {
	if !ok {
		return false
	}
	name := strings.TrimSpace(arg)
	if name == "" {
		_ = store.ReplyText(ctx, mctx, "Format: nama <nama file>, contoh: nama rekap bulanan")
		return true
	}
	if !strings.HasSuffix(strings.ToLower(name), ".pdf") {
		name += ".pdf"
	}
	pdfStagesMu.Lock()
	st.outName = name
	pdfStagesMu.Unlock()
	_ = store.ReplyText(ctx, mctx, fmt.Sprintf("Nama hasil: %s", name))
	return true
}

// pdfReact menempelkan reaksi emoji ke pesan user (tanda terima ✅ / ❌).
// Fail-soft: gagal kirim reaksi tidak menggagalkan alur utama.
func pdfReact(ctx context.Context, mctx *store.MessageContext, msgID, emoji string) {
	if strings.TrimSpace(msgID) == "" {
		return
	}
	key := &waCommon.MessageKey{
		RemoteJID: proto.String(mctx.Chat.String()),
		FromMe:    proto.Bool(false),
		ID:        proto.String(msgID),
	}
	// Participant wajib untuk reaksi di grup; di private justru merusak.
	if mctx.IsGroup() {
		key.Participant = proto.String(mctx.Sender.String())
	}
	_, _ = mctx.Client.SendMessage(ctx, mctx.Chat, &waE2E.Message{
		ReactionMessage: &waE2E.ReactionMessage{
			Key:  key,
			Text: proto.String(emoji),
		},
	})
}

// pdfStageAdd mengunduh + validasi + simpan PDF, reaksi ✅/❌, tampilkan daftar.
func pdfStageAdd(ctx context.Context, mctx *store.MessageContext, key string, st *pdfStage, evt *events.Message, doc *waE2E.DocumentMessage) bool {
	msgID := evt.Info.ID
	name := strings.TrimSpace(doc.GetFileName())
	if name == "" {
		name = doc.GetTitle()
	}
	if strings.TrimSpace(name) == "" {
		name = "dokumen.pdf"
	}
	pdfStagesMu.Lock()
	full := len(st.files) >= pdfMergeMaxFiles
	overBudget := st.totalLen >= pdfMergeMaxTotal
	pdfStagesMu.Unlock()
	if full {
		pdfReact(ctx, mctx, msgID, "❌")
		_ = store.ReplyText(ctx, mctx, fmt.Sprintf("Maksimal %d file — ketik gabung untuk gabungkan atau batal untuk batalkan.", pdfMergeMaxFiles))
		return true
	}
	if overBudget {
		pdfReact(ctx, mctx, msgID, "❌")
		_ = store.ReplyText(ctx, mctx, "Total file melebihi 200MB — ketik gabung atau batal.")
		return true
	}
	if !isPDFDoc(doc) {
		pdfReact(ctx, mctx, msgID, "❌")
		_ = store.ReplyText(ctx, mctx, fmt.Sprintf("%s bukan PDF — hanya file .pdf yang diterima.", name))
		return true
	}
	data, err := mctx.Client.Download(ctx, doc)
	if err != nil {
		pdfReact(ctx, mctx, msgID, "❌")
		_ = store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal unduh %s: %v", name, err))
		return true
	}
	if err := pdfValidateData(data); err != nil {
		pdfReact(ctx, mctx, msgID, "❌")
		_ = store.ReplyText(ctx, mctx, fmt.Sprintf("%s ditolak: %v", name, err))
		return true
	}
	pdfStagesMu.Lock()
	if st.totalLen+int64(len(data)) > pdfMergeMaxTotal {
		pdfStagesMu.Unlock()
		pdfReact(ctx, mctx, msgID, "❌")
		_ = store.ReplyText(ctx, mctx, "Total file melebihi 200MB — ketik gabung atau batal.")
		return true
	}
	name = pdfUniqueNameLocked(st, name)
	st.files = append(st.files, pdfStageFile{msgID: msgID, name: name, data: data})
	st.totalLen += int64(len(data))
	pdfStagesMu.Unlock()
	pdfStageTouch(key)
	// Keputusan owner: cukup reaksi ✅, tanpa balasan daftar.
	// Daftar hanya tampil bila user meminta (daftar).
	pdfReact(ctx, mctx, msgID, "✅")
	return true
}

// pdfStageReorder memindahkan file yang di-reply ke posisi n (1-based),
// file lain bergeser (swap implisit). Bukan dokumen staging → false.
func pdfStageReorder(ctx context.Context, mctx *store.MessageContext, key string, st *pdfStage, quotedID string, n int) bool {
	pdfStagesMu.Lock()
	idx := -1
	for i, f := range st.files {
		if f.msgID == quotedID {
			idx = i
			break
		}
	}
	if idx < 0 {
		pdfStagesMu.Unlock()
		return false
	}
	if n < 1 || n > len(st.files) {
		count := len(st.files)
		pdfStagesMu.Unlock()
		_ = store.ReplyText(ctx, mctx, fmt.Sprintf("Nomor harus 1..%d.", count))
		return true
	}
	f := st.files[idx]
	st.files = append(st.files[:idx], st.files[idx+1:]...)
	pos := n - 1
	if pos > len(st.files) {
		pos = len(st.files)
	}
	st.files = append(st.files, pdfStageFile{})
	copy(st.files[pos+1:], st.files[pos:])
	st.files[pos] = f
	pdfStagesMu.Unlock()
	pdfStageTouch(key)
	// Keputusan owner: cukup reaksi ✅ ke pesan angka, tanpa balasan daftar.
	pdfReact(ctx, mctx, mctx.RawEvt.Info.ID, "✅")
	return true
}

// pdfStageList menyusun daftar sesuai format owner (verbatim): header,
// bernomor, lalu panduan + perintah. Tanpa hitungan n/15.
func pdfStageList(st *pdfStage) string {
	var sb strings.Builder
	sb.WriteString("*Urutan merger :*\n")
	for i, f := range st.files {
		fmt.Fprintf(&sb, "%d. %s\n", i+1, f.name)
	}
	sb.WriteString("\nReply dokumen dengan angka untuk ubah urutan.\n")
	sb.WriteString("\n*Nama/Name* _<nama file>_\n> nama file output (opsional)\n")
	sb.WriteString("*Gabung/Merge*\n> gabung sesuai urutan\n")
	sb.WriteString("*Batal/Cancel*\n> buang sesi")
	return sb.String()
}

// pdfStageCancel membuang sesi. announce=false untuk sapu diam.
func pdfStageCancel(ctx context.Context, mctx *store.MessageContext, announce bool) {
	key := pdfStageKey(mctx.Sender)
	pdfStagesMu.Lock()
	st, ok := pdfStages[key]
	if ok {
		if st.timer != nil {
			st.timer.Stop()
		}
		delete(pdfStages, key)
	}
	pdfStagesMu.Unlock()
	if !ok {
		if announce {
			_ = store.ReplyText(ctx, mctx, "Tidak ada sesi upload aktif.")
		}
		return
	}
	if announce {
		_ = store.ReplyText(ctx, mctx, "Sesi upload dibatalkan, file dibuang.")
	}
}

// pdfStageTouch me-reset timer idle 30 menit (tiap aktivitas).
func pdfStageTouch(key string) {
	pdfStagesMu.Lock()
	defer pdfStagesMu.Unlock()
	st, ok := pdfStages[key]
	if !ok {
		return
	}
	if st.timer != nil {
		st.timer.Stop()
	}
	k := key
	st.timer = time.AfterFunc(pdfMergeIdleTTL, func() {
		pdfStagesMu.Lock()
		if cur, ok := pdfStages[k]; ok && cur == st {
			delete(pdfStages, k)
		}
		pdfStagesMu.Unlock()
	})
}

// pdfStageFinish mengunci + menggabungkan sesuai urutan via pdfcpu.
func pdfStageFinish(ctx context.Context, mctx *store.MessageContext) error {
	key := pdfStageKey(mctx.Sender)
	pdfStagesMu.Lock()
	st, ok := pdfStages[key]
	if !ok || len(st.files) < 2 {
		pdfStagesMu.Unlock()
		if !ok {
			return store.ReplyText(ctx, mctx, "Tidak ada sesi upload aktif. Ketik mulai untuk membuka sesi.")
		}
		return store.ReplyText(ctx, mctx, "Butuh minimal 2 file untuk digabung.")
	}
	if st.merging {
		pdfStagesMu.Unlock()
		return store.ReplyText(ctx, mctx, "Merge sedang berjalan, tunggu sebentar.")
	}
	st.merging = true
	files := append([]pdfStageFile{}, st.files...)
	outName := pdfOutName(st)
	pdfStagesMu.Unlock()

	// Antrean global downloader (maks 2 bersamaan — semua user boleh pakai,
	// jadi merge berat ikut antrean yang sama, penuh = tolak sopan).
	select {
	case dlSem <- struct{}{}:
		defer func() { <-dlSem }()
	default:
		pdfClearMerging(key)
		return store.ReplyText(ctx, mctx, "Antrean penuh (maks 2 bersamaan), coba lagi sebentar.")
	}

	if _, err := sendDlStatus(ctx, mctx, fmt.Sprintf("Menggabungkan %d PDF...", len(files))); err != nil {
		pdfClearMerging(key)
		return err
	}
	go func() {
		bg := context.Background()
		if rerr := pdfDoMerge(bg, mctx, key, files, outName); rerr != nil {
			pdfClearMerging(key)
			_ = store.ReplyText(bg, mctx, rerr.Error())
		}
	}()
	return nil
}

// pdfOutName menentukan nama hasil: kustom, atau file pertama + " merge".
func pdfOutName(st *pdfStage) string {
	if strings.TrimSpace(st.outName) != "" {
		return strings.TrimSpace(st.outName)
	}
	base := "gabungan"
	if len(st.files) > 0 {
		base = strings.TrimSuffix(st.files[0].name, ".pdf")
	}
	return base + " merge.pdf"
}

// pdfClearMerging membuka kunci merging (jalur gagal).
func pdfClearMerging(key string) {
	pdfStagesMu.Lock()
	if st, ok := pdfStages[key]; ok {
		st.merging = false
	}
	pdfStagesMu.Unlock()
}

// pdfDoMerge menjalankan MergeRaw in-memory + kirim dokumen + bersihkan sesi.
func pdfDoMerge(ctx context.Context, mctx *store.MessageContext, key string, files []pdfStageFile, outName string) error {
	holders := make([]bytes.Reader, len(files))
	seekers := make([]io.ReadSeeker, 0, len(files))
	for i, f := range files {
		holders[i] = *bytes.NewReader(f.data)
		seekers = append(seekers, &holders[i])
	}
	var out bytes.Buffer
	mergeCtx, cancel := context.WithTimeout(ctx, dlTimeout)
	defer cancel()
	if err := api.MergeRaw(mergeCtx, seekers, &out, false, model.NewDefaultConfiguration()); err != nil {
		return fmt.Errorf("gagal gabungkan PDF: %v", err)
	}
	if out.Len() == 0 {
		return fmt.Errorf("hasil gabungan kosong")
	}
	if out.Len() > getMaxFileSize {
		return fmt.Errorf("hasil %.0f MB melebihi batas %d MB.", float64(out.Len())/(1<<20), getMaxFileSize/(1<<20))
	}
	pdfStagesMu.Lock()
	if st, ok := pdfStages[key]; ok {
		if st.timer != nil {
			st.timer.Stop()
		}
		delete(pdfStages, key)
	}
	pdfStagesMu.Unlock()
	return sendGetDocument(ctx, mctx, out.Bytes(), "application/pdf", outName)
}

// pdfValidateData menolak konten bukan-PDF (header %PDF-). Tanpa batas
// ukuran per file (keputusan owner) — batas total 200MB dijaga saat simpan.
func pdfValidateData(data []byte) error {
	if len(data) < 5 || string(data[:5]) != "%PDF-" {
		return fmt.Errorf("bukan file PDF valid")
	}
	return nil
}

// pdfUniqueNameLocked membuat nama unik — dipanggil dengan lock dipegang.
func pdfUniqueNameLocked(st *pdfStage, name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "dokumen.pdf"
	}
	if !strings.HasSuffix(strings.ToLower(name), ".pdf") {
		name += ".pdf"
	}
	taken := map[string]bool{}
	for _, f := range st.files {
		taken[strings.ToLower(f.name)] = true
	}
	if !taken[strings.ToLower(name)] {
		return name
	}
	base := strings.TrimSuffix(name, ".pdf")
	if i := strings.LastIndex(base, "("); i >= 0 {
		if n, err := strconv.Atoi(strings.TrimSuffix(base[i+1:], ")")); err == nil && n > 0 && strings.HasSuffix(base, ")") {
			base = base[:i]
		}
	}
	for n := 2; ; n++ {
		cand := fmt.Sprintf("%s(%d).pdf", base, n)
		if !taken[strings.ToLower(cand)] {
			return cand
		}
	}
}

// isPDFDoc true bila dokumen bertipe/ekstensi PDF.
func isPDFDoc(doc *waE2E.DocumentMessage) bool {
	mime := strings.ToLower(strings.TrimSpace(doc.GetMimetype()))
	if strings.Contains(mime, "pdf") {
		return true
	}
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(doc.GetFileName())), ".pdf")
}

// pdfParseOrder membaca angka 1..N dari teks (murni digit).
func pdfParseOrder(text string) (int, bool) {
	t := strings.TrimSpace(text)
	if t == "" {
		return 0, false
	}
	for _, r := range t {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(t)
	if err != nil || n < 1 || n > pdfMergeMaxFiles {
		return 0, false
	}
	return n, true
}

// handlerText mengambil teks pesan (untuk kata tunggal & angka reorder).
func handlerText(evt *events.Message) string {
	msg := store.UnwrapMessage(evt.Message)
	if msg == nil {
		return ""
	}
	if msg.GetConversation() != "" {
		return msg.GetConversation()
	}
	if ext := msg.GetExtendedTextMessage(); ext != nil {
		return ext.GetText()
	}
	return ""
}
