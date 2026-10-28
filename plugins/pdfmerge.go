package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"wabot-go/internal/messaging"
	"wabot-go/internal/store"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waCommon"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types"
	"github.com/polymorfa/hypermeow/types/events"
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
//
// Kata `mulai` TIDAK global: ia hanya hidup selama jendela startArmTTL (5
// menit) setelah `.pdfmerge` mengirim menu (lihat startarm.go). Tanpa jendela,
// `mulai` lolos sebagai pesan biasa (AI) tanpa notifikasi apa pun.
func RegisterPDFMerge(r *Registry) {
	r.Register(Command{
		Name:        "pdfmerge",
		Description: "Gabung s.d. 15 PDF (.pdfmerge untuk menu)",
		Category:    CategoryDokumen,
		Handler:     handlePDFMerge,
	})
}

// Batas staging (keputusan owner): 15 file, total 200MB per sesi (tanpa
// batas per file), idle 30 menit. Hasil merge ≤100MB (getMaxFileSize).
// pdfMergeMaxGlobalTotal membatasi TOTAL seluruh sesi aktif supaya banyak
// staging bareng tidak menghabiskan RAM panel.
const (
	pdfMergeMaxFiles       = 15
	pdfMergeMaxTotal       = 200 << 20
	pdfMergeIdleTTL        = 30 * time.Minute
	pdfMergeMaxGlobalTotal = 500 << 20
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

// pdfStageKey adalah kunci staging per sender per chat: sesi di tiap chat
// terpisah, PDF/kata di chat lain tidak ikut tertelan (lihat T1).
func pdfStageKey(sender, chat types.JID) string {
	return sender.ToNonAD().String() + "\x00" + chat.String()
}

func handlePDFMerge(ctx context.Context, mctx *store.MessageContext) error {
	if err := pdfSendMenu(ctx, mctx); err != nil {
		return err
	}
	// Menu terkirim = buka jendela `mulai` 5 menit untuk pengirim+chat ini.
	// Menu gagal kirim tidak meng-arm apa pun (user tak melihat tombolnya).
	startArmSet(startArmKey(mctx.Sender, mctx.Chat), startArmPDF)
	return nil
}

// pdfButton adalah satu tombol quick_reply (id ber-prefix "pdfmerge:").
type pdfButton struct {
	id      string
	display string
}

// pdfMenuButtons adalah tombol menu utama (Mulai/Batal).
func pdfMenuButtons() []pdfButton {
	return []pdfButton{
		{id: "pdfmerge:mulai", display: "Mulai"},
		{id: "pdfmerge:batal", display: "Batal"},
	}
}

// pdfUploadButtons adalah tombol mode upload aktif (Urutan/Batal).
func pdfUploadButtons() []pdfButton {
	return []pdfButton{
		{id: "pdfmerge:urutan", display: "Urutan"},
		{id: "pdfmerge:batal", display: "Batal"},
	}
}

// pdfListButtons adalah tombol daftar urutan (Urutan/Gabung/Nama/Batal).
func pdfListButtons() []pdfButton {
	return []pdfButton{
		{id: "pdfmerge:urutan", display: "Urutan"},
		{id: "pdfmerge:gabung", display: "Gabung"},
		{id: "pdfmerge:nama", display: "Nama"},
		{id: "pdfmerge:batal", display: "Batal"},
	}
}

// pdfSendMenu mengirim bubble menu utama (.pdfmerge).
func pdfSendMenu(ctx context.Context, mctx *store.MessageContext) error {
	return pdfSendButtons(ctx, mctx, "*Tool Merge PDF*",
		"Klik *Mulai* lalu kirim file PDF yang ingin anda gabungkan. Maksimal dokumen yang bisa ditangani adalah 15 dokumen.",
		pdfMenuButtons())
}

// pdfSendButtons mengirim bubble interactive + tombol quick_reply.
// Node `biz` SENGAJA tidak disertakan manual: hypermeow menempelkannya
// otomatis, dan clone ter-patch selalu mengirim name="mixed" (lihat
// btnTestBizExtra di buttontest.go) — node ganda = error 479, nama
// turunan = error 473/405.
func pdfSendButtons(ctx context.Context, mctx *store.MessageContext, title, body string, buttons []pdfButton) error {
	nf := make([]messaging.NFButton, 0, len(buttons))
	for _, b := range buttons {
		nf = append(nf, messaging.QuickReply(b.id, b.display))
	}
	err := messaging.SendNativeFlow(ctx, mctx, messaging.NFText{
		Title:  title,
		Body:   body,
		Footer: "Flora Go • PDFMerge",
	}, "pdfmerge-menu", "pdfmerge: "+title, nf...)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal kirim menu tombol: %v", err))
	}
	return nil
}

// nativeFlowTapID mengekstrak ID tombol dari tap bubble native_flow (dipakai
// pdfmerge, jpgpdf, shazam). Fakta live: tap quick_reply kembali sebagai
// TemplateButtonReplyMessage (selectedID utuh); jalur
// InteractiveResponseMessage/paramsJson dipertahankan sebagai fallback
// (single_select).
func nativeFlowTapID(evt *events.Message) (string, bool) {
	unwrapped := store.UnwrapMessage(evt.Message)
	if unwrapped == nil {
		return "", false
	}
	if tr := unwrapped.GetTemplateButtonReplyMessage(); tr != nil {
		if id := strings.TrimSpace(tr.GetSelectedID()); id != "" {
			return id, true
		}
		return "", false
	}
	resp := unwrapped.GetInteractiveResponseMessage()
	if resp == nil {
		return "", false
	}
	nf := resp.GetNativeFlowResponseMessage()
	if nf == nil {
		return "", false
	}
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(nf.GetParamsJSON()), &p); err != nil || p.ID == "" {
		return "", false
	}
	return p.ID, true
}

// CheckPDFButtonTap mencegat tap tombol pdfmerge (prefix "pdfmerge:") lalu
// menjalankan kata yang sama seperti diketik. true = ditangani (setop,
// jangan ke AI). Jalur teks tetap jalan sebagai fallback (mis. klien yang
// tidak me-render tombol).
func CheckPDFButtonTap(client *whatsmeow.Client, evt *events.Message) bool {
	id, ok := nativeFlowTapID(evt)
	if !ok || !strings.HasPrefix(id, "pdfmerge:") {
		return false
	}
	word := strings.TrimPrefix(id, "pdfmerge:")
	ctx := context.Background()
	mctx := &store.MessageContext{Client: client, Chat: evt.Info.Chat, Sender: evt.Info.Sender, RawEvt: evt}
	key := pdfStageKey(evt.Info.Sender, evt.Info.Chat)
	if word == "nama" {
		_ = store.ReplyText(ctx, mctx, "Format: nama <nama file>, contoh: nama rekap bulanan")
		return true
	}
	canon, hit := pdfWords[word]
	if !hit {
		return false
	}
	pdfStagesMu.Lock()
	st, ok := pdfStages[key]
	pdfStagesMu.Unlock()
	if !ok {
		if canon != "mulai" {
			_ = store.ReplyText(ctx, mctx, "Belum ada sesi upload aktif. Ketuk Mulai dulu.")
			return true
		}
		// Tap Mulai di luar sesi: hanya boleh bila jendela `.pdfmerge` masih
		// hidup. Menu basi (>5 menit) tidak membuka sesi — bot mengirim menu
		// baru + menyegarkan jendela (bukan notifikasi "sesi nonaktif").
		if !startArmTakeOrRearm(startArmKey(evt.Info.Sender, evt.Info.Chat), startArmPDF) {
			_ = pdfSendMenu(ctx, mctx)
			return true
		}
	} else if canon == "mulai" {
		// Niat "mulai" terjawab "masih aktif" oleh pdfStageWord; jendelanya
		// tidak perlu disimpan lagi.
		startArmClear(startArmKey(evt.Info.Sender, evt.Info.Chat))
	}
	return pdfStageWord(ctx, mctx, key, st, ok, canon)
}

// pdfWord adalah kata tunggal + alias Inggrisnya. "urutan" adalah kata
// primer tombol (daftar/list dipertahankan sebagai alias teks).
var pdfWords = map[string]string{
	"mulai": "mulai", "start": "mulai",
	"daftar": "daftar", "list": "daftar", "urutan": "daftar",
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
// "mulai" yang membuka sesi — dan itu pun hanya bila jendela `.pdfmerge`
// (startarm.go) masih hidup. Tanpa sesi + bukan "mulai" (atau jendela basi) =
// pesan biasa, diteruskan ke jalur normal/AI tanpa notifikasi.
func CheckPDFStaging(client *whatsmeow.Client, evt *events.Message) bool {
	key := pdfStageKey(evt.Info.Sender, evt.Info.Chat)
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
			// Gerbang jendela: hanya "mulai" (pembukaan sesi) yang ditahan,
			// kata sesi lain tetap mengikuti keberadaan sesi.
			if canon == "mulai" &&
				!startArmAllows(startArmKey(evt.Info.Sender, evt.Info.Chat), startArmPDF, ok) {
				return false
			}
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
		if pdfGlobalStaged() >= pdfMergeMaxGlobalTotal {
			_ = store.ReplyText(ctx, mctx, "Staging server penuh, coba lagi nanti setelah sesi lain selesai.")
			return true
		}
		pdfStagesMu.Lock()
		pdfStages[key] = &pdfStage{}
		pdfStagesMu.Unlock()
		pdfStageTouch(key)
		_ = pdfSendButtons(ctx, mctx, "*Mode Upload Aktif*",
			"Kirim file PDF anda ke chat ini.\n"+
				"\n"+
				"Untuk melihat urutan dokumen, klik tombol *Urutan*.",
			pdfUploadButtons())
		return true
	case "daftar":
		if !ok {
			return false
		}
		pdfStagesMu.Lock()
		empty := len(st.files) == 0
		list := pdfStageList(st)
		pdfStagesMu.Unlock()
		if empty {
			_ = store.ReplyText(ctx, mctx, "Belum ada file. Kirim PDF dulu.")
			return true
		}
		_ = pdfSendButtons(ctx, mctx, "*Urutan File Merge*", list, pdfListButtons())
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
	name := sanitizeFileName(doc.GetFileName())
	if name == "" {
		name = sanitizeFileName(doc.GetTitle())
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

// pdfStageList menyusun body bubble urutan: daftar bernomor + instruksi
// gabung/reorder/nama sesuai spec owner.
func pdfStageList(st *pdfStage) string {
	var sb strings.Builder
	for i, f := range st.files {
		fmt.Fprintf(&sb, "%d. %s\n", i+1, f.name)
	}
	sb.WriteString("\nKlik *Gabung* untuk gabungkan semua file berdasarkan urutan ini. ")
	sb.WriteString("Untuk mengganti urutan file, reply dokumen dengan nomor urut yang anda inginkan. ")
	sb.WriteString("Anda juga dapat mengubah atau menentukan nama file outputnya dengan ketik nama <nama file>.")
	return sb.String()
}

// pdfStageCancel membuang sesi. announce=false untuk sapu diam.
func pdfStageCancel(ctx context.Context, mctx *store.MessageContext, announce bool) {
	key := pdfStageKey(mctx.Sender, mctx.Chat)
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

// pdfGlobalStaged menjumlahkan byte staging SEMUA sesi aktif.
func pdfGlobalStaged() int64 {
	pdfStagesMu.Lock()
	defer pdfStagesMu.Unlock()
	var total int64
	for _, st := range pdfStages {
		total += st.totalLen
	}
	return total
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
	key := pdfStageKey(mctx.Sender, mctx.Chat)
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
	// Slot dilepas goroutine saat merge SELESAI (bukan saat handler return),
	// supaya batas 2 bersamaan benar-benar menahan beban.
	select {
	case dlSem <- struct{}{}:
		// dilepas goroutine di bawah
	default:
		pdfClearMerging(key)
		return store.ReplyText(ctx, mctx, "Antrean penuh (maks 2 bersamaan), coba lagi sebentar.")
	}

	if _, err := sendDlStatus(ctx, mctx, fmt.Sprintf("Menggabungkan %d PDF...", len(files))); err != nil {
		pdfClearMerging(key)
		func() { <-dlSem }()
		return err
	}
	go func() {
		defer func() { <-dlSem }()
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
