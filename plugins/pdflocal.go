package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"wabot-go/internal/logfmt"
	"wabot-go/internal/store"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waCommon"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types/events"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
	"google.golang.org/protobuf/proto"
)

func init() {
	registerPlugin(RegisterPDFLocal)
}

// Batas input PDF lokal (keputusan owner): pdfcpu bekerja in-memory,
// jadi dibatasi 50MB agar RAM panel tidak jebol. Tanpa batas network
// (file tidak keluar server).
const pdfLocalMaxBytes = 50 << 20

var pdfRangeRe = regexp.MustCompile(`^[0-9,\-\s]+$`)

// RegisterPDFLocal mendaftarkan alat PDF LOKAL via pdfcpu (murni offline):
// .compress, .split, .jpgpdf, .watermark, .protect, .unlock.
// Semua CategoryAlat (boleh di clone), in-memory, antrean dlSem maks 2.
func RegisterPDFLocal(r *Registry) {
	r.Register(Command{
		Name:        "compress",
		Description: "Kecilkan PDF (lokal, .compress + reply PDF)",
		Category:    CategoryDokumen,
		Handler:     handleCompress,
	})
	r.Register(Command{
		Name:        "split",
		Description: "Pecah PDF per rentang (mis. .split 1-3 + reply PDF)",
		Category:    CategoryDokumen,
		Handler:     handleSplit,
	})
	r.Register(Command{
		Name:        "jpgpdf",
		Description: "Foto jadi PDF (.jpgpdf mulai/kirim/gabung/daftar/nama/batal)",
		Category:    CategoryDokumen,
		Handler:     handleJpgPDF,
	})
	r.Register(Command{
		Name:        "watermark",
		Description: "Cap teks di PDF (mis. .watermark DRAF + reply PDF)",
		Category:    CategoryDokumen,
		Handler:     handleWatermark,
	})
	r.Register(Command{
		Name:        "protect",
		Description: "Kunci PDF pakai password (mis. .protect rahasia + reply PDF)",
		Category:    CategoryDokumen,
		Handler:     handleProtect,
	})
	r.Register(Command{
		Name:        "unlock",
		Description: "Buka kunci PDF (.unlock [password] + reply PDF)",
		Category:    CategoryDokumen,
		Handler:     handleUnlock,
	})
}

// pdfLocalInput mengambil 1 dokumen PDF dari pesan langsung atau reply +
// validasi header %PDF + batas ukuran. Kembalian: nama + bytes.
func pdfLocalInput(ctx context.Context, mctx *store.MessageContext, what string) (string, []byte, error) {
	doc := store.UnwrapMessage(mctx.RawEvt.Message).GetDocumentMessage()
	if doc == nil {
		doc = store.UnwrapMessage(store.ExtractQuotedMessage(mctx.RawEvt)).GetDocumentMessage()
	}
	if doc == nil {
		return "", nil, fmt.Errorf("kirim/reply file PDF untuk .%s", what)
	}
	name := sanitizeFileName(doc.GetFileName())
	if name == "" {
		name = "dokumen.pdf"
	}
	data, err := mctx.Client.Download(ctx, doc)
	if err != nil || len(data) == 0 {
		return "", nil, fmt.Errorf("gagal unduh %s: %v", name, err)
	}
	if len(data) > pdfLocalMaxBytes {
		return "", nil, fmt.Errorf("%s melebihi 50MB", name)
	}
	if !strings.HasSuffix(strings.ToLower(name), ".pdf") || !bytes.HasPrefix(data, []byte("%PDF")) {
		return "", nil, fmt.Errorf("%s bukan PDF", name)
	}
	return name, data, nil
}

// pdfTitle memetakan label internal ke judul tampilan standar.
func pdfTitle(label string) string {
	switch label {
	case "compress":
		return "Compress PDF"
	case "split":
		return "Split PDF"
	case "jpgpdf":
		return "JPG ke PDF"
	case "watermark":
		return "Watermark PDF"
	case "protect":
		return "Protect PDF"
	case "unlock":
		return "Unlock PDF"
	default:
		return "PDF"
	}
}

// pdfLocalRun menjalankan proses PDF di goroutine via antrean dlSem
// (pola pdfmerge): status dulu, hasil dokumen kemudian. fn mengembalikan
// caption hasil (format Title/Row, tanpa nama file).
func pdfLocalRun(ctx context.Context, mctx *store.MessageContext, label string, fn func(bg context.Context) (data []byte, name, caption string, err error)) error {
	select {
	case dlSem <- struct{}{}:
		// dilepas goroutine di bawah
	default:
		return store.ReplyText(ctx, mctx, QueueFullMsg())
	}
	if _, err := sendDlStatus(ctx, mctx, "Memproses "+label+"..."); err != nil {
		func() { <-dlSem }()
		return err
	}
	go func() {
		defer func() { <-dlSem }()
		bg := context.Background()
		data, name, caption, err := fn(bg)
		if err != nil {
			logfmt.Warn("pdf %s gagal: %v\n", label, err)
			_ = store.ReplyText(bg, mctx, ErrMsg(pdfTitle(label), err.Error()))
			return
		}
		if rerr := sendPDFLocalResult(bg, mctx, data, name, caption); rerr != nil {
			logfmt.Warn("pdf %s kirim gagal: %v\n", label, rerr)
			_ = store.ReplyText(bg, mctx, ErrMsg(pdfTitle(label), "Hasil jadi tapi gagal terkirim: "+rerr.Error()))
		}
	}()
	return nil
}

// sendPDFLocalResult mengirim bytes PDF sebagai dokumen + log + track.
// Caption sudah format standar (tanpa nama file — nama tampil sebagai nama
// dokumen di WA).
func sendPDFLocalResult(ctx context.Context, mctx *store.MessageContext, data []byte, name, caption string) error {
	name = sanitizeFileName(name)
	if name == "" {
		name = "hasil.pdf"
	}
	if len(data) > 100<<20 {
		return fmt.Errorf("hasil %.1fMB melebihi batas kirim 100MB", float64(len(data))/(1<<20))
	}
	uploaded, err := mctx.Client.Upload(ctx, data, whatsmeow.MediaDocument)
	if err != nil {
		return err
	}
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, &waE2E.Message{
		DocumentMessage: &waE2E.DocumentMessage{
			URL:           proto.String(uploaded.URL),
			DirectPath:    proto.String(uploaded.DirectPath),
			MediaKey:      uploaded.MediaKey,
			Mimetype:      proto.String("application/pdf"),
			FileEncSHA256: uploaded.FileEncSHA256,
			FileSHA256:    uploaded.FileSHA256,
			FileLength:    proto.Uint64(uploaded.FileLength),
			FileName:      proto.String(name),
			Caption:       proto.String(caption),
		},
	})
	if err != nil {
		return err
	}
	store.LogOutgoing(mctx, "pdf-document", name)
	store.TrackBotMessageID(resp.ID, name)
	return nil
}

// handleCompress: .compress + reply/kirim PDF -> Optimize lokal.
// Caption hasil menyertakan susut ukuran (bukti kerja, mis. 8.2MB -> 3.1MB).
func handleCompress(ctx context.Context, mctx *store.MessageContext) error {
	name, data, err := pdfLocalInput(ctx, mctx, "compress")
	if err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Compress PDF", err.Error()))
	}
	before := float64(len(data)) / (1 << 20)
	return pdfLocalRun(ctx, mctx, "compress", func(bg context.Context) ([]byte, string, string, error) {
		var out bytes.Buffer
		if err := api.Optimize(bg, bytes.NewReader(data), &out, model.NewDefaultConfiguration(), nil); err != nil {
			return nil, "", "", err
		}
		after := float64(out.Len()) / (1 << 20)
		logfmt.Plain("compress %s: %.1fMB -> %.1fMB\n", name, before, after)
		caption := Block("Compress PDF", Row("Ukuran", fmt.Sprintf("%.1fMB → %.1fMB", before, after)))
		return out.Bytes(), strings.TrimSuffix(name, ".pdf") + " compress.pdf", caption, nil
	})
}

// handleSplit: .split 1-3 + reply/kirim PDF -> Trim halaman.
func handleSplit(ctx context.Context, mctx *store.MessageContext) error {
	if len(mctx.Args) < 1 || !pdfRangeRe.MatchString(mctx.Args[0]) {
		return store.ReplyText(ctx, mctx, Block("Split PDF", Row("Cara", ".split <rentang> + reply PDF"), Row("Contoh", ".split 1-3")))
	}
	ranges := strings.TrimSpace(mctx.Args[0])
	name, data, err := pdfLocalInput(ctx, mctx, "split")
	if err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Split PDF", err.Error()))
	}
	return pdfLocalRun(ctx, mctx, "split", func(bg context.Context) ([]byte, string, string, error) {
		var pages []string
		for _, p := range strings.Split(ranges, ",") {
			if p = strings.TrimSpace(p); p != "" {
				pages = append(pages, p)
			}
		}
		var out bytes.Buffer
		if err := api.Trim(bg, bytes.NewReader(data), &out, pages, model.NewDefaultConfiguration()); err != nil {
			return nil, "", "", fmt.Errorf("rentang %q ditolak: %w", ranges, err)
		}
		clean := strings.ReplaceAll(ranges, " ", "")
		caption := Block("Split PDF", Row("Halaman", clean))
		return out.Bytes(), strings.TrimSuffix(name, ".pdf") + " p" + clean + ".pdf", caption, nil
	})
}

// handleWatermark: .watermark <teks> + reply/kirim PDF -> cap teks diagonal
// 45° semua halaman.
func handleWatermark(ctx context.Context, mctx *store.MessageContext) error {
	text := strings.TrimSpace(mctx.RawArgs)
	if text == "" {
		return store.ReplyText(ctx, mctx, Block("Watermark PDF", Row("Cara", ".watermark <teks> + reply PDF"), Row("Contoh", ".watermark DRAF")))
	}
	name, data, err := pdfLocalInput(ctx, mctx, "watermark")
	if err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Watermark PDF", err.Error()))
	}
	return pdfLocalRun(ctx, mctx, "watermark", func(bg context.Context) ([]byte, string, string, error) {
		conf := model.NewDefaultConfiguration()
		wm, err := api.TextWatermark(bg, text, "rot:45, op:.35, pos:c", true, false, types.POINTS, conf)
		if err != nil {
			return nil, "", "", fmt.Errorf("watermark ditolak: %w", err)
		}
		count, err := api.PageCount(bg, bytes.NewReader(data), conf)
		if err != nil || count < 1 {
			return nil, "", "", fmt.Errorf("gagal baca halaman: %v", err)
		}
		pages := make([]string, 0, count)
		for i := 1; i <= count; i++ {
			pages = append(pages, fmt.Sprintf("%d", i))
		}
		var out bytes.Buffer
		if err := api.AddWatermarks(bg, bytes.NewReader(data), &out, pages, wm, conf); err != nil {
			return nil, "", "", err
		}
		caption := Block("Watermark PDF", Row("Teks", text))
		return out.Bytes(), strings.TrimSuffix(name, ".pdf") + " watermark.pdf", caption, nil
	})
}

// handleProtect: .protect <pw> + reply/kirim PDF -> Encrypt (user+owner sama).
// CATATAN: password diketik di chat sehingga tercatat di log console panel.
func handleProtect(ctx context.Context, mctx *store.MessageContext) error {
	if len(mctx.Args) < 1 || strings.TrimSpace(mctx.Args[0]) == "" {
		return store.ReplyText(ctx, mctx, Block("Protect PDF", Row("Cara", ".protect <password> + reply PDF"), Row("Contoh", ".protect rahasia123")))
	}
	pw := mctx.Args[0]
	name, data, err := pdfLocalInput(ctx, mctx, "protect")
	if err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Protect PDF", err.Error()))
	}
	return pdfLocalRun(ctx, mctx, "protect", func(bg context.Context) ([]byte, string, string, error) {
		conf := model.NewDefaultConfiguration()
		// OwnerPW (current) wajib terisi agar read path lolos; New = password baru.
		conf.OwnerPW = pw
		conf.UserPW = pw
		conf.UserPWNew = &pw
		conf.OwnerPWNew = &pw
		var out bytes.Buffer
		if err := api.Encrypt(bg, bytes.NewReader(data), &out, conf); err != nil {
			return nil, "", "", err
		}
		caption := Block("Protect PDF", Row("Status", "Terkunci"))
		return out.Bytes(), strings.TrimSuffix(name, ".pdf") + " locked.pdf", caption, nil
	})
}

// handleUnlock: .unlock [pw] + reply/kirim PDF terkunci -> Decrypt.
// Password opsional: banyak PDF "terkunci" hanya punya restriksi dengan user
// password kosong — dicoba dulu; gagal baru minta password eksplisit.
func handleUnlock(ctx context.Context, mctx *store.MessageContext) error {
	pw := ""
	if len(mctx.Args) > 0 {
		pw = strings.TrimSpace(mctx.Args[0])
	}
	name, data, err := pdfLocalInput(ctx, mctx, "unlock")
	if err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Unlock PDF", err.Error()))
	}
	return pdfLocalRun(ctx, mctx, "unlock", func(bg context.Context) ([]byte, string, string, error) {
		conf := model.NewDefaultConfiguration()
		conf.UserPW = pw
		conf.OwnerPW = pw
		var out bytes.Buffer
		if err := api.Decrypt(bg, bytes.NewReader(data), &out, conf); err != nil {
			if pw == "" {
				return nil, "", "", fmt.Errorf("PDF ini butuh password: .unlock <password> + reply PDF")
			}
			return nil, "", "", fmt.Errorf("gagal buka (password salah?): %w", err)
		}
		caption := Block("Unlock PDF", Row("Status", "Terbuka"))
		return out.Bytes(), strings.TrimSuffix(name, ".pdf") + " unlocked.pdf", caption, nil
	})
}

// --- Staging multi-gambar .jpgpdf (cermin pdfmerge, lebih ringan) ---

// Batas staging gambar: 10 file, total 50MB, sesi basi >1 jam dibuang.
const (
	jpgStageMaxFiles = 10
	jpgStageMaxTotal = 50 << 20
	jpgStageTTL      = time.Hour
)

type jpgStageFile struct {
	msgID string
	name  string
	data  []byte
}

type jpgStage struct {
	files   []jpgStageFile
	total   int64
	outName string
	created time.Time
}

var (
	jpgStagesMu sync.Mutex
	jpgStages   = make(map[string]*jpgStage)
)

func jpgStageKey(sender, chat interface{ String() string }) string {
	return sender.String() + "|" + chat.String()
}

// jpgReact memberi reaksi emoji ke pesan (cermin pdfReact): ✅ tiap foto
// tertampung, ❌ bila gagal — hening tanpa bubble per foto.
func jpgReact(ctx context.Context, mctx *store.MessageContext, msgID, emoji string) {
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

// --- Tombol interaktif .jpgpdf (cermin pdfmerge) ---

// jpgButton adalah satu tombol quick_reply: id berprefix "jpgpdf:".
type jpgButton struct {
	id      string
	display string
}

// jpgMenuButtons adalah tombol menu utama (Mulai/Batal).
func jpgMenuButtons() []jpgButton {
	return []jpgButton{
		{id: "jpgpdf:mulai", display: "Mulai"},
		{id: "jpgpdf:batal", display: "Batal"},
	}
}

// jpgSessionButtons adalah tombol saat sesi aktif (Urutan/Buat PDF/Nama/Batal).
func jpgSessionButtons() []jpgButton {
	return []jpgButton{
		{id: "jpgpdf:daftar", display: "Urutan"},
		{id: "jpgpdf:gabung", display: "Buat PDF"},
		{id: "jpgpdf:nama", display: "Nama"},
		{id: "jpgpdf:batal", display: "Batal"},
	}
}

// jpgSendMenu mengirim kartu menu utama (.jpgpdf polos).
func jpgSendMenu(ctx context.Context, mctx *store.MessageContext) error {
	return jpgSendButtons(ctx, mctx, "*Tool JPG ke PDF*",
		"Klik *Mulai* lalu kirim foto yang ingin dijadikan PDF. Maksimal 10 foto (50MB).",
		jpgMenuButtons())
}

// jpgSendButtons mengirim bubble interactive + tombol quick_reply.
// Node `biz` SENGAJA tidak disertakan manual: hypermeow menempelkannya
// otomatis (lihat pdfSendButtons di pdfmerge.go) — node ganda = error.
func jpgSendButtons(ctx context.Context, mctx *store.MessageContext, title, body string, buttons []jpgButton) error {
	btns := make([]*waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton, 0, len(buttons))
	for _, b := range buttons {
		params, _ := json.Marshal(map[string]string{"display_text": b.display, "id": b.id})
		btns = append(btns, &waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
			Name:             proto.String("quick_reply"),
			ButtonParamsJSON: proto.String(string(params)),
		})
	}
	msg := &waE2E.Message{
		InteractiveMessage: &waE2E.InteractiveMessage{
			Header: &waE2E.InteractiveMessage_Header{
				Title: proto.String(title),
			},
			Body:   &waE2E.InteractiveMessage_Body{Text: proto.String(body)},
			Footer: &waE2E.InteractiveMessage_Footer{Text: proto.String("Flora Go • JPG ke PDF")},
			InteractiveMessage: &waE2E.InteractiveMessage_NativeFlowMessage_{
				NativeFlowMessage: &waE2E.InteractiveMessage_NativeFlowMessage{
					Buttons: btns,
				},
			},
		},
	}
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, msg,
		whatsmeow.SendRequestExtra{ID: whatsmeow.GenerateMessageID()})
	if err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("JPG ke PDF", fmt.Sprintf("Gagal kirim menu tombol: %v", err)))
	}
	store.LogOutgoing(mctx, "jpgpdf-menu", title)
	store.TrackBotMessageID(resp.ID, "jpgpdf: "+title)
	return nil
}

// jpgWords adalah kata tunggal sesi + aliasnya (cermin pdfWords).
// "buat" dipertahankan sebagai alias "gabung" (transisi dari versi teks).
var jpgWords = map[string]string{
	"mulai": "mulai", "start": "mulai",
	"daftar": "daftar", "list": "daftar",
	"gabung": "gabung", "buat": "gabung", "merge": "gabung",
	"batal": "batal", "cancel": "batal",
}

// CheckJpgButtonTap mencegat tap tombol jpgpdf (prefix "jpgpdf:") lalu
// menjalankan kata yang sama seperti diketik. true = ditangani.
func CheckJpgButtonTap(client *whatsmeow.Client, evt *events.Message) bool {
	id, ok := pdfTapID(evt)
	if !ok || !strings.HasPrefix(id, "jpgpdf:") {
		return false
	}
	word := strings.TrimPrefix(id, "jpgpdf:")
	ctx := context.Background()
	mctx := &store.MessageContext{Client: client, Chat: evt.Info.Chat, Sender: evt.Info.Sender, RawEvt: evt}
	if word == "nama" {
		_ = store.ReplyText(ctx, mctx, Block("JPG ke PDF", Row("Cara", ".jpgpdf nama <nama file>"), Row("Contoh", ".jpgpdf nama liburan")))
		return true
	}
	if word == "mulai" {
		return jpgOpenSession(ctx, mctx, true)
	}
	key := jpgStageKey(evt.Info.Sender, evt.Info.Chat)
	jpgStagesMu.Lock()
	_, ok = jpgStages[key]
	jpgStagesMu.Unlock()
	if !ok {
		_ = store.ReplyText(ctx, mctx, Block("JPG ke PDF", "Tidak ada sesi aktif. Ketuk Mulai dulu."))
		return true
	}
	switch word {
	case "daftar":
		return jpgListUI(ctx, mctx, true) == nil
	case "gabung":
		_ = jpgBuild(ctx, mctx)
		return true
	case "batal":
		jpgStagesMu.Lock()
		delete(jpgStages, key)
		jpgStagesMu.Unlock()
		_ = store.ReplyText(ctx, mctx, Block("JPG ke PDF", "Sesi dibatalkan, file dibuang."))
		return true
	}
	return false
}

// CheckJpgStaging menampung foto polos (tanpa caption command) yang masuk
// saat sesi jpgpdf aktif — cermin CheckPDFStaging milik pdfmerge. Dipanggil
// handler untuk pesan non-command (sebelum autodownload); true = ditelan.
// Tanpa sesi aktif atau bukan gambar: false (bukan urusan staging).
func CheckJpgStaging(client *whatsmeow.Client, evt *events.Message) bool {
	ctx := context.Background()
	mctx := &store.MessageContext{Client: client, Chat: evt.Info.Chat, Sender: evt.Info.Sender, RawEvt: evt}
	key := jpgStageKey(evt.Info.Sender, evt.Info.Chat)

	// Kata tunggal sesi (tanpa prefix, cermin pdfmerge): "nama X" langsung
	// set nama; kata kanonik lain jalan seperti diketik via .jpgpdf.
	text := handlerText(evt)
	if w, arg := pdfWordArg(text); w == "nama" || (w != "" && arg != "") || jpgWords[w] != "" {
		if w == "nama" {
			if arg == "" {
				_ = store.ReplyText(ctx, mctx, Block("JPG ke PDF", Row("Cara", ".jpgpdf nama <nama file>"), Row("Contoh", ".jpgpdf nama liburan")))
				return true
			}
			return jpgSetNamePlain(ctx, mctx, key, arg)
		}
		if canon, hit := jpgWords[w]; hit && arg == "" {
			switch canon {
			case "mulai":
				return jpgOpenSession(ctx, mctx, true)
			case "daftar":
				return jpgListUI(ctx, mctx, true) == nil
			case "gabung":
				_ = jpgBuild(ctx, mctx)
				return true
			case "batal":
				jpgStagesMu.Lock()
				delete(jpgStages, key)
				jpgStagesMu.Unlock()
				_ = store.ReplyText(ctx, mctx, Block("JPG ke PDF", "Sesi dibatalkan, file dibuang."))
				return true
			}
		}
	}

	jpgStagesMu.Lock()
	st, ok := jpgStages[key]
	jpgStagesMu.Unlock()
	if !ok || time.Since(st.created) > jpgStageTTL {
		return false
	}
	img := store.UnwrapMessage(evt.Message).GetImageMessage()
	if img == nil {
		// Teks angka + reply ke foto staging → ubah urutan (cermin pdfmerge).
		if n, isNum := pdfParseOrder(text); isNum {
			if qid := store.ExtractStanzaID(evt); qid != "" {
				if jpgStageReorder(ctx, mctx, key, qid, n) {
					return true
				}
			}
		}
		return false
	}
	data, err := client.Download(ctx, img)
	if err != nil || len(data) == 0 {
		jpgReact(ctx, mctx, evt.Info.ID, "❌")
		_ = store.ReplyText(ctx, mctx, ErrMsg("JPG ke PDF", fmt.Sprintf("Foto gagal ditampung (unduh gagal: %v).", err)))
		return true
	}
	if !isJpgBytes(data) {
		return false
	}
	jpgStagesMu.Lock()
	defer jpgStagesMu.Unlock()
	if len(st.files) >= jpgStageMaxFiles || st.total+int64(len(data)) > jpgStageMaxTotal {
		_ = store.ReplyText(ctx, mctx, Block("JPG ke PDF", "Staging penuh (maks 10 gambar / 50MB) — .jpgpdf gabung atau batal."))
		return true
	}
	st.files = append(st.files, jpgStageFile{msgID: evt.Info.ID, name: fmt.Sprintf("foto-%d.jpg", len(st.files)+1), data: data})
	st.total += int64(len(data))
	jpgReact(ctx, mctx, evt.Info.ID, "✅")
	return true
}

// handleJpgPDF: .jpgpdf mulai|daftar|gabung|nama|batal untuk staging
// (cermin kata-kata .pdfmerge; "buat" tetap diterima sebagai alias gabung);
// .jpgpdf + gambar (langsung/reply) menambah ke sesi bila ada, atau langsung
// jadi PDF 1 halaman bila tanpa sesi. Foto polos tanpa caption ditampung
// CheckJpgStaging saat sesi aktif.
func handleJpgPDF(ctx context.Context, mctx *store.MessageContext) error {
	img := store.UnwrapMessage(mctx.RawEvt.Message).GetImageMessage()
	if img == nil {
		img = store.UnwrapMessage(store.ExtractQuotedMessage(mctx.RawEvt)).GetImageMessage()
	}

	word := ""
	if len(mctx.Args) > 0 {
		word = strings.ToLower(strings.TrimSpace(mctx.Args[0]))
	}

	// Ada gambar + (tanpa kata / kata bukan subcommand sesi) -> tambah atau
	// single-shot.
	if img != nil && (word == "" || (word != "mulai" && word != "daftar" && word != "gabung" && word != "buat" && word != "nama" && word != "batal")) {
		return jpgAddOrSingle(ctx, mctx, img)
	}

	switch word {
	case "mulai":
		jpgOpenSession(ctx, mctx, false)
		return nil
	case "daftar":
		return jpgListUI(ctx, mctx, false)
	case "gabung", "buat":
		return jpgBuild(ctx, mctx)
	case "nama":
		return jpgSetName(ctx, mctx)
	case "batal":
		key := jpgStageKey(mctx.Sender, mctx.Chat)
		jpgStagesMu.Lock()
		delete(jpgStages, key)
		jpgStagesMu.Unlock()
		return store.ReplyText(ctx, mctx, Block("JPG ke PDF", "Sesi dibatalkan, file dibuang."))
	default:
		return jpgSendMenu(ctx, mctx)
	}
}

// jpgSession mengambil sesi aktif (nil bila tak ada/basi).
func jpgSession(mctx *store.MessageContext) (string, *jpgStage) {
	key := jpgStageKey(mctx.Sender, mctx.Chat)
	jpgStagesMu.Lock()
	defer jpgStagesMu.Unlock()
	st, ok := jpgStages[key]
	if !ok || time.Since(st.created) > jpgStageTTL {
		delete(jpgStages, key)
		return key, nil
	}
	return key, st
}

// jpgAddOrSingle menambah gambar ke sesi aktif (reaksi ✅, hening), atau
// langsung jadi PDF bila tanpa sesi.
func jpgAddOrSingle(ctx context.Context, mctx *store.MessageContext, img *waE2E.ImageMessage) error {
	data, err := mctx.Client.Download(ctx, img)
	if err != nil || len(data) == 0 {
		return store.ReplyText(ctx, mctx, ErrMsg("JPG ke PDF", fmt.Sprintf("Gagal unduh gambar: %v", err)))
	}
	if !isJpgBytes(data) {
		return store.ReplyText(ctx, mctx, ErrMsg("JPG ke PDF", "Hanya JPG/PNG yang diterima."))
	}
	if _, st := jpgSession(mctx); st != nil {
		jpgStagesMu.Lock()
		defer jpgStagesMu.Unlock()
		if len(st.files) >= jpgStageMaxFiles || st.total+int64(len(data)) > jpgStageMaxTotal {
			return store.ReplyText(ctx, mctx, Block("JPG ke PDF", "Staging penuh (maks 10 gambar / 50MB) — .jpgpdf gabung atau batal."))
		}
		st.files = append(st.files, jpgStageFile{msgID: mctx.RawEvt.Info.ID, name: fmt.Sprintf("foto-%d.jpg", len(st.files)+1), data: data})
		st.total += int64(len(data))
		jpgReact(ctx, mctx, mctx.RawEvt.Info.ID, "✅")
		return nil
	}
	// Tanpa sesi: single-shot 1 halaman.
	return pdfLocalRun(ctx, mctx, "jpgpdf", func(bg context.Context) ([]byte, string, string, error) {
		data, name, err := jpgBuildPDF(bg, []jpgStageFile{{name: "foto.jpg", data: data}}, "")
		if err != nil {
			return nil, "", "", err
		}
		caption := Block("JPG ke PDF", Row("Foto", "1"))
		return data, name, caption, nil
	})
}

// jpgStageReorder memindahkan foto yang di-reply ke posisi n (1-based),
// foto lain bergeser. Cermin pdfStageReorder: bukan foto staging → false
// (jangan telan); di luar 1..N → balas batas + telan; sukses → ✅ hening.
func jpgStageReorder(ctx context.Context, mctx *store.MessageContext, key, quotedID string, n int) bool {
	jpgStagesMu.Lock()
	st, ok := jpgStages[key]
	if !ok || time.Since(st.created) > jpgStageTTL {
		if ok {
			delete(jpgStages, key)
		}
		jpgStagesMu.Unlock()
		return false
	}
	idx := -1
	for i, f := range st.files {
		if f.msgID == quotedID {
			idx = i
			break
		}
	}
	if idx < 0 {
		jpgStagesMu.Unlock()
		return false
	}
	if n < 1 || n > len(st.files) {
		count := len(st.files)
		jpgStagesMu.Unlock()
		_ = store.ReplyText(ctx, mctx, Block("JPG ke PDF", fmt.Sprintf("Nomor harus 1..%d.", count)))
		return true
	}
	f := st.files[idx]
	st.files = append(st.files[:idx], st.files[idx+1:]...)
	pos := n - 1
	if pos > len(st.files) {
		pos = len(st.files)
	}
	st.files = append(st.files, jpgStageFile{})
	copy(st.files[pos+1:], st.files[pos:])
	st.files[pos] = f
	jpgStagesMu.Unlock()
	jpgReact(ctx, mctx, mctx.RawEvt.Info.ID, "✅")
	return true
}

// jpgOpenSession membuka sesi staging baru. withButtons = sertakan tombol
// sesi (dipakai alur tombol/menu; jalur teks murni tanpa tombol).
func jpgOpenSession(ctx context.Context, mctx *store.MessageContext, withButtons bool) bool {
	key := jpgStageKey(mctx.Sender, mctx.Chat)
	jpgStagesMu.Lock()
	jpgStages[key] = &jpgStage{created: time.Now()}
	jpgStagesMu.Unlock()
	if withButtons {
		_ = jpgSendButtons(ctx, mctx, "*JPG ke PDF*",
			"Sesi dibuka (maks 10 foto / 50MB). Kirim foto polos, lalu ketuk Gabung.",
			jpgSessionButtons())
		return true
	}
	_ = store.ReplyText(ctx, mctx, Block("JPG ke PDF", Row("Status", "Sesi dibuka"), Row("Batas", "10 foto / 50MB"), "Kirim foto polos, lalu .jpgpdf gabung."))
	return true
}

// jpgListUI menampilkan daftar staging; withButtons = sertakan tombol sesi.
func jpgListUI(ctx context.Context, mctx *store.MessageContext, withButtons bool) error {
	_, st := jpgSession(mctx)
	if st == nil || len(st.files) == 0 {
		return store.ReplyText(ctx, mctx, Block("JPG ke PDF", "Tidak ada sesi aktif. Mulai via .jpgpdf mulai."))
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s\n", Row("Foto", fmt.Sprintf("%d (%.1fMB)", len(st.files), float64(st.total)/(1<<20))))
	for i, f := range st.files {
		fmt.Fprintf(&sb, "%d. %s (%.1fMB)\n", i+1, f.name, float64(len(f.data))/(1<<20))
	}
	sb.WriteString("\nKetuk *Buat PDF* untuk gabung semua foto berdasarkan urutan ini. ")
	sb.WriteString("Untuk mengganti urutan, reply foto dengan nomor urut yang anda inginkan. ")
	sb.WriteString("Ubah nama file output dengan .jpgpdf nama <nama file>.")
	if withButtons {
		return jpgSendButtons(ctx, mctx, "*JPG ke PDF*", strings.TrimRight(sb.String(), "\n"), jpgSessionButtons())
	}
	return store.ReplyText(ctx, mctx, Title("JPG ke PDF")+"\n"+strings.TrimRight(sb.String(), "\n"))
}

// jpgSetNamePlain mengatur nama hasil dari kata polos "nama X" (tanpa
// prefix). Tanpa sesi: false (jangan telan chat biasa).
func jpgSetNamePlain(ctx context.Context, mctx *store.MessageContext, key, arg string) bool {
	jpgStagesMu.Lock()
	st, ok := jpgStages[key]
	jpgStagesMu.Unlock()
	if !ok {
		return false
	}
	name := sanitizeFileName(arg)
	if name == "" {
		_ = store.ReplyText(ctx, mctx, Block("JPG ke PDF", Row("Cara", ".jpgpdf nama <nama file>"), Row("Contoh", ".jpgpdf nama liburan")))
		return true
	}
	name = strings.TrimSuffix(name, ".pdf")
	jpgStagesMu.Lock()
	if cur, ok := jpgStages[key]; ok && cur == st {
		cur.outName = name
	}
	jpgStagesMu.Unlock()
	_ = store.ReplyText(ctx, mctx, Block("JPG ke PDF", Row("Nama hasil", name+".pdf")))
	return true
}

// jpgSetName mengatur nama file hasil (.jpgpdf nama liburan).
func jpgSetName(ctx context.Context, mctx *store.MessageContext) error {
	key := jpgStageKey(mctx.Sender, mctx.Chat)
	rest := ""
	if len(mctx.Args) > 0 {
		rest = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(mctx.RawArgs), mctx.Args[0]))
	}
	if jpgSetNamePlain(ctx, mctx, key, rest) {
		return nil
	}
	return store.ReplyText(ctx, mctx, Block("JPG ke PDF", "Tidak ada sesi aktif. Mulai via .jpgpdf mulai."))
}

func jpgBuild(ctx context.Context, mctx *store.MessageContext) error {
	key, st := jpgSession(mctx)
	if st == nil || len(st.files) == 0 {
		return store.ReplyText(ctx, mctx, Block("JPG ke PDF", "Tidak ada foto tertampung. .jpgpdf mulai dulu."))
	}
	files := append([]jpgStageFile{}, st.files...)
	outName := st.outName
	jpgStagesMu.Lock()
	delete(jpgStages, key)
	jpgStagesMu.Unlock()
	return pdfLocalRun(ctx, mctx, "jpgpdf", func(bg context.Context) ([]byte, string, string, error) {
		data, name, err := jpgBuildPDF(bg, files, outName)
		if err != nil {
			return nil, "", "", err
		}
		caption := Block("JPG ke PDF", Row("Foto", fmt.Sprintf("%d", len(files))))
		return data, name, caption, nil
	})
}

// jpgBuildPDF merakit foto-foto jadi 1 PDF via pdfcpu ImportImages.
// outName kosong = default foto.pdf.
func jpgBuildPDF(bg context.Context, files []jpgStageFile, outName string) ([]byte, string, error) {
	imgs := make([]io.Reader, 0, len(files))
	for _, f := range files {
		imgs = append(imgs, bytes.NewReader(f.data))
	}
	var out bytes.Buffer
	if err := api.ImportImages(bg, nil, &out, imgs, api.DefaultImportConfig(), model.NewDefaultConfiguration()); err != nil {
		return nil, "", err
	}
	name := strings.TrimSpace(outName)
	if name == "" {
		name = "foto"
	}
	return out.Bytes(), name + ".pdf", nil
}

// isJpgBytes true untuk JPEG/PNG via magic bytes.
func isJpgBytes(data []byte) bool {
	if len(data) < 4 {
		return false
	}
	if data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return true
	}
	return data[0] == 0x89 && data[1] == 0x50 && data[2] == 0x4E && data[3] == 0x47
}
