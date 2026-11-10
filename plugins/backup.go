package plugins

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"wabot-go/internal/config"
	"wabot-go/internal/logfmt"
	"wabot-go/internal/sqlxlsx"
	"wabot-go/internal/store"
	"wabot-go/internal/timex"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types"
	"google.golang.org/protobuf/proto"
)

func init() {
	registerPlugin(RegisterBackup)
}

// backup.go — backup database bot untuk owner (khusus DM/pribadi, owner-only).
//
//   - .backup            : snapshot SEMUA file data/*.db → ZIP dikirim ke chat.
//   - .backup list       : daftar file database + ukuran + nomor urut.
//   - .backup <nomor>    : ambil 1 file DB berdasarkan nomor urut (default output .xlsx).
//   - .backup <nomor> -db: ambil 1 file DB berdasarkan nomor urut (output format .db).
//   - .backup get <nama> : backward-compatible: snapshot 1 file database via nama.
//   - .backup jadwal ... : backup otomatis berinterval, hasil ke DM owner.
//
// Semua snapshot dibuat lewat store.SnapshotDB (VACUUM INTO) supaya konsisten
// walau DB sedang memakai mode WAL. Temp SELALU dihapus setelah dipakai —
// tidak ada salinan backup yang ditinggal di server.

const backupDataDir = "data"

// Batas interval backup otomatis (menit).
const (
	backupIntervalMin = 15
	backupIntervalMax = 30 * 24 * 60 // 30 hari
)

func RegisterBackup(r *Registry) {
	r.Register(Command{
		Name:        "backup",
		Description: "Backup database (owner, via DM)",
		Category:    CategoryOwner,
		Handler:     handleBackup,
	})
}

const backupUsage = `*DATABASE BACKUP*
Panel cadangan & ekspor database SQLite.

*Perintah:*
• *.backup*
> Unduh seluruh database (arsip .zip)
• *.backup list*
> Lihat daftar file database & nomor urut
• *.backup <nomor>*
> Ekspor database terpilih ke Excel (.xlsx)
• *.backup <nomor> -db*
> Unduh file database asli (.db)
• *.backup jadwal <interval|off>*
> Atur backup otomatis (contoh: 30m, 6h, 1d)`

func handleBackup(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	// Hanya chat pribadi (DM owner), bukan grup. Cek pakai IsGroup() (bukan
	// membandingkan Chat.Server ke "s.whatsapp.net"): DM mode-LID datang
	// sebagai "@lid" sehingga server-nya BUKAN DefaultUserServer — perbandingan
	// lama menolak semua DM owner.
	if mctx.IsGroup() {
		return store.ReplyText(ctx, mctx, "Command backup hanya bisa dipakai di chat pribadi (DM) owner.")
	}

	sub := ""
	if len(mctx.Args) > 0 {
		sub = strings.ToLower(strings.TrimSpace(mctx.Args[0]))
	}

	// Cek apakah argumen pertama adalah nomor urut
	if _, err := strconv.Atoi(sub); err == nil {
		return backupByNumber(ctx, mctx)
	}

	switch sub {
	case "":
		return backupAll(ctx, mctx)
	case "list":
		return backupList(ctx, mctx)
	case "get":
		return backupGet(ctx, mctx)
	case "jadwal", "schedule":
		return backupSchedule(ctx, mctx)
	default:
		return store.ReplyText(ctx, mctx, backupUsage)
	}
}

// listDBFiles mengembalikan semua file data/*.db (grup, ai, finance, crm,
// absensi, session, plus per-clone) terurut.
func listDBFiles() ([]string, error) {
	paths, err := filepath.Glob(filepath.Join(backupDataDir, "*.db"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
}

// backupAll snapshot semua DB → ZIP, dikirim sebagai dokumen ke chat ini.
func backupAll(ctx context.Context, mctx *store.MessageContext) error {
	paths, err := listDBFiles()
	if err != nil || len(paths) == 0 {
		return store.ReplyText(ctx, mctx, "Tidak ada file database ditemukan.")
	}
	if err := store.ReplyText(ctx, mctx, "Menyiapkan snapshot seluruh database…"); err != nil {
		return err
	}
	chat := mctx.Chat
	client := mctx.Client
	// ZIP + upload bisa makan waktu: kerjakan di latar supaya pipeline
	// pesan tidak tertahan.
	go func() {
		bg := context.Background()
		data, name, err := buildBackupZip(paths)
		if err != nil {
			_ = store.ReplyText(bg, mctx, fmt.Sprintf("Gagal membuat backup: %v", err))
			return
		}
		caption := backupZipCaption(name, paths)
		if err := sendDocBytes(bg, client, chat, name, "application/zip", data, caption); err != nil {
			_ = store.ReplyText(bg, mctx, fmt.Sprintf("Gagal kirim backup: %v", err))
		}
	}()
	return nil
}

// backupList menampilkan daftar file DB + ukurannya.
func backupList(ctx context.Context, mctx *store.MessageContext) error {
	paths, err := listDBFiles()
	if err != nil || len(paths) == 0 {
		return store.ReplyText(ctx, mctx, "Tidak ada file database ditemukan.")
	}
	var total int64
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil {
			total += fi.Size()
		}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "*DAFTAR DATABASE*\nTotal: %d file (%s)\n\n", len(paths), humanBytes(total))
	for i, p := range paths {
		var size int64
		if fi, err := os.Stat(p); err == nil {
			size = fi.Size()
		}
		fmt.Fprintf(&sb, "%d. *%s* (%s)\n", i+1, filepath.Base(p), humanBytes(size))
	}
	sb.WriteString("\n_Ketik *.backup <no>* untuk unduh Excel (.xlsx)_\n")
	sb.WriteString("_Ketik *.backup <no> -db* untuk file SQLite (.db)_")
	return store.ReplyText(ctx, mctx, sb.String())
}

// backupByNumber snapshot satu file DB berdasarkan nomor urut di listDBFiles.
// Default format: Excel (.xlsx). Jika diberi flag "-db", format: SQLite (.db).
func backupByNumber(ctx context.Context, mctx *store.MessageContext) error {
	numStr := strings.TrimSpace(mctx.Args[0])
	num, err := strconv.Atoi(numStr)
	if err != nil || num <= 0 {
		return store.ReplyText(ctx, mctx, "Nomor urut tidak valid. Ketik .backup list untuk melihat daftar.")
	}

	paths, err := listDBFiles()
	if err != nil || len(paths) == 0 {
		return store.ReplyText(ctx, mctx, "Tidak ada file database ditemukan.")
	}
	if num > len(paths) {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Nomor urut %d tidak ditemukan (maksimal %d).\nKetik .backup list untuk melihat daftar.", num, len(paths)))
	}

	src := paths[num-1]
	want := filepath.Base(src)

	formatDB := false
	for _, a := range mctx.Args[1:] {
		if strings.EqualFold(strings.TrimSpace(a), "-db") {
			formatDB = true
			break
		}
	}

	if formatDB {
		_ = store.ReplyText(ctx, mctx, fmt.Sprintf("Menyiapkan file database %s…", want))
	} else {
		_ = store.ReplyText(ctx, mctx, fmt.Sprintf("Mengonversi %s ke Excel (.xlsx)…", want))
	}

	workDir, err := os.MkdirTemp(store.MustBotTempDir(), "backup-*")
	if err != nil {
		return store.ReplyText(ctx, mctx, "Gagal menyiapkan temp: "+err.Error())
	}
	defer os.RemoveAll(workDir)

	stamp := time.Now().In(timex.Zone()).Format("20060102-150405")
	baseNoExt := strings.TrimSuffix(want, ".db")

	snap := filepath.Join(workDir, baseNoExt+"-"+stamp+".db")
	if err := store.SnapshotDB(src, snap); err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal snapshot %s: %v", want, err))
	}

	if formatDB {
		data, err := os.ReadFile(snap)
		if err != nil {
			return store.ReplyText(ctx, mctx, "Gagal membaca snapshot: "+err.Error())
		}
		outName := baseNoExt + "-" + stamp + ".db"
		caption := fmt.Sprintf("*SNAPSHOT DATABASE SQLITE*\n• *File:* %s\n• *Ukuran:* %s\n• *Waktu:* %s",
			want, humanBytes(int64(len(data))), time.Now().In(timex.Zone()).Format("02 Jan 2006, 15:04 MST"))
		return sendDocBytes(ctx, mctx.Client, mctx.Chat, outName, "application/vnd.sqlite3", data, caption)
	}

	// Format default: XLSX
	f := sqlxlsx.NewWorkbook()
	rep, err := sqlxlsx.ConvertInto(f, snap, "", sqlxlsx.Options{IncludeInfo: true})
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal konversi ke Excel: %v", err))
	}
	xlsxBytes, err := sqlxlsx.Render(f)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal render Excel: %v", err))
	}

	outName := baseNoExt + "-" + stamp + ".xlsx"
	caption := fmt.Sprintf("*EXPORT DATABASE EXCEL*\n• *File:* %s\n• *Tabel:* %d sheet\n• *Total Baris:* %d baris\n• *Ukuran:* %s\n• *Waktu:* %s",
		want, len(rep.Tables), rep.TotalRows, humanBytes(int64(len(xlsxBytes))), time.Now().In(timex.Zone()).Format("02 Jan 2006, 15:04 MST"))
	return sendDocBytes(ctx, mctx.Client, mctx.Chat, outName, xlsxMIME, xlsxBytes, caption)
}

// backupGet snapshot satu file DB → dikirim sebagai .db.
func backupGet(ctx context.Context, mctx *store.MessageContext) error {
	if len(mctx.Args) < 2 {
		return store.ReplyText(ctx, mctx, "Format: .backup get <nama>\nContoh: .backup get group.db")
	}
	want := filepath.Base(strings.TrimSpace(strings.Join(mctx.Args[1:], " ")))
	paths, err := listDBFiles()
	if err != nil {
		return store.ReplyText(ctx, mctx, "Gagal membaca daftar database: "+err.Error())
	}
	src := ""
	for _, p := range paths {
		if filepath.Base(p) == want {
			src = p
			break
		}
	}
	if src == "" {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("File %q tidak ditemukan.\nKetik .backup list untuk melihat daftar.", want))
	}

	workDir, err := os.MkdirTemp(store.MustBotTempDir(), "backup-*")
	if err != nil {
		return store.ReplyText(ctx, mctx, "Gagal menyiapkan temp: "+err.Error())
	}
	defer os.RemoveAll(workDir)

	stamp := time.Now().In(timex.Zone()).Format("20060102-150405")
	outName := strings.TrimSuffix(want, ".db") + "-" + stamp + ".db"
	snap := filepath.Join(workDir, outName)
	if err := store.SnapshotDB(src, snap); err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal snapshot %s: %v", want, err))
	}
	data, err := os.ReadFile(snap)
	if err != nil {
		return store.ReplyText(ctx, mctx, "Gagal membaca snapshot: "+err.Error())
	}
	caption := fmt.Sprintf("*SNAPSHOT DATABASE SQLITE*\n• *File:* %s\n• *Ukuran:* %s\n• *Waktu:* %s",
		want, humanBytes(int64(len(data))), time.Now().In(timex.Zone()).Format("02 Jan 2006, 15:04 MST"))
	return sendDocBytes(ctx, mctx.Client, mctx.Chat, outName, "application/vnd.sqlite3", data, caption)
}

// backupSchedule menangani ".backup jadwal [interval|off]".
func backupSchedule(ctx context.Context, mctx *store.MessageContext) error {
	g := store.GroupsFor(mctx.Client)
	if len(mctx.Args) < 2 {
		return store.ReplyText(ctx, mctx, backupScheduleStatus(ctx, g))
	}
	arg := strings.ToLower(strings.TrimSpace(mctx.Args[1]))
	if arg == "off" || arg == "0" {
		if err := g.SetBackupSchedule(ctx, 0); err != nil {
			return store.ReplyText(ctx, mctx, "Gagal menyimpan: "+err.Error())
		}
		return store.ReplyText(ctx, mctx, "*BACKUP OTOMATIS DIMATIKAN*\nJadwal rutin telah dinonaktifkan. Anda tetap dapat melakukan backup manual kapan saja dengan *.backup*.")
	}
	mins, err := parseBackupInterval(arg)
	if err != nil {
		return store.ReplyText(ctx, mctx, err.Error())
	}
	if err := g.SetBackupSchedule(ctx, mins); err != nil {
		return store.ReplyText(ctx, mctx, "Gagal menyimpan: "+err.Error())
	}
	// Hitung mundur dari sekarang: backup pertama setelah 1 interval.
	now := time.Now()
	if err := g.SetBackupLastRun(ctx, now); err != nil {
		logfmt.Warn("Gagal mencatat waktu backup terakhir: %v\n", err)
	}
	next := now.Add(time.Duration(mins) * time.Minute)
	return store.ReplyText(ctx, mctx, fmt.Sprintf(
		"*JADWAL BACKUP BERHASIL DIATUR*\n• *Interval:* Tiap %s\n• *Eksekusi Pertama:* %s\n• *Tujuan:* DM Owner (dokumen .zip)",
		humanDuration(mins), next.In(timex.Zone()).Format("02 Jan 2006, 15:04 MST")))
}

func backupScheduleStatus(ctx context.Context, g *store.GroupDB) string {
	mins, _ := g.GetBackupSchedule(ctx)
	if mins <= 0 {
		return "*STATUS BACKUP OTOMATIS*\n• *Status:* Nonaktif\n\n_Aktifkan dengan: *.backup jadwal <interval>*_\n_Contoh: *.backup jadwal 6h* (pilihan: 30m, 6h, 1d)_"
	}
	last, _ := g.GetBackupLastRun(ctx)
	next := last.Add(time.Duration(mins) * time.Minute)
	lastStr := "belum pernah"
	if !last.IsZero() {
		lastStr = last.In(timex.Zone()).Format("02 Jan 2006, 15:04 MST")
	}
	return fmt.Sprintf(
		"*STATUS BACKUP OTOMATIS*\n• *Status:* Aktif\n• *Interval:* Tiap %s\n• *Terakhir:* %s\n• *Berikutnya:* %s\n\n_Untuk mematikan: *.backup jadwal off*_",
		humanDuration(mins), lastStr, next.In(timex.Zone()).Format("02 Jan 2006, 15:04 MST"))
}

// parseBackupInterval menerima "30m", "6h"/"6j", "1d", atau angka polos
// (dianggap JAM). Mengembalikan menit, sudah divalidasi ke batas aman.
func parseBackupInterval(s string) (int, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, fmt.Errorf("Interval kosong. Contoh: .backup jadwal 6h")
	}
	unit := "h"
	num := s
	// Cek unit terpanjang dulu ("jam" sebelum "m") supaya suffix tidak salah.
	for _, u := range []string{"menit", "jam", "hari", "m", "h", "j", "d"} {
		if strings.HasSuffix(s, u) {
			num = strings.TrimSpace(strings.TrimSuffix(s, u))
			switch u {
			case "m", "menit":
				unit = "m"
			case "d", "hari":
				unit = "d"
			default:
				unit = "h"
			}
			break
		}
	}
	n, err := strconv.Atoi(num)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("Format tidak dikenali. Contoh: .backup jadwal 30m / 6h / 1d / off")
	}
	var mins int
	switch unit {
	case "m":
		mins = n
	case "d":
		mins = n * 24 * 60
	default:
		mins = n * 60
	}
	if mins < backupIntervalMin {
		return 0, fmt.Errorf("Interval minimal 15 menit. Contoh: .backup jadwal 30m / 6h / 1d")
	}
	if mins > backupIntervalMax {
		return 0, fmt.Errorf("Interval maksimal 30 hari.")
	}
	return mins, nil
}

// --- Pembuatan arsip ---

// buildBackupZip membuat ZIP berisi snapshot semua dbPaths. Semua temp
// dibersihkan (tidak ada yang disimpan di server). Mengembalikan byte + nama.
func buildBackupZip(dbPaths []string) ([]byte, string, error) {
	workDir, err := os.MkdirTemp(store.MustBotTempDir(), "backup-*")
	if err != nil {
		return nil, "", err
	}
	defer os.RemoveAll(workDir)

	name := "flora-backup-" + time.Now().In(timex.Zone()).Format("20060102-150405") + ".zip"
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, src := range dbPaths {
		base := filepath.Base(src)
		snap := filepath.Join(workDir, base)
		if err := store.SnapshotDB(src, snap); err != nil {
			_ = zw.Close()
			return nil, "", fmt.Errorf("snapshot %s: %w", base, err)
		}
		if err := addFileToZip(zw, snap, base); err != nil {
			_ = zw.Close()
			return nil, "", fmt.Errorf("zip %s: %w", base, err)
		}
	}
	if err := zw.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), name, nil
}

func addFileToZip(zw *zip.Writer, path, name string) error {
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	hdr, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	hdr.Name = name
	hdr.Method = zip.Deflate
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, in)
	return err
}

func backupZipCaption(name string, dbPaths []string) string {
	var total int64
	for _, p := range dbPaths {
		if fi, err := os.Stat(p); err == nil {
			total += fi.Size()
		}
	}
	return fmt.Sprintf("*FULL DATABASE BACKUP*\n• *Arsip:* %s\n• *Jumlah:* %d file database (.db)\n• *Total Ukuran:* %s\n• *Waktu:* %s",
		name, len(dbPaths), humanBytes(total), time.Now().In(timex.Zone()).Format("02 Jan 2006, 15:04 MST"))
}

// sendDocBytes mengunggah byte dan mengirimnya sebagai dokumen WhatsApp.
func sendDocBytes(ctx context.Context, client *whatsmeow.Client, chat types.JID, filename, mime string, data []byte, caption string) error {
	if client == nil {
		return fmt.Errorf("client tidak tersedia")
	}
	uploaded, err := client.Upload(ctx, data, whatsmeow.MediaDocument)
	if err != nil {
		return err
	}
	doc := &waE2E.DocumentMessage{
		URL:           proto.String(uploaded.URL),
		DirectPath:    proto.String(uploaded.DirectPath),
		MediaKey:      uploaded.MediaKey,
		FileEncSHA256: uploaded.FileEncSHA256,
		FileSHA256:    uploaded.FileSHA256,
		FileLength:    proto.Uint64(uploaded.FileLength),
		FileName:      proto.String(filename),
		Mimetype:      proto.String(mime),
	}
	if caption != "" {
		doc.Caption = proto.String(caption)
	}
	_, err = client.SendMessage(ctx, chat, &waE2E.Message{DocumentMessage: doc})
	return err
}

// --- Penjadwal backup otomatis ---

const backupSweepInterval = 60 * time.Second

var (
	backupClient *whatsmeow.Client
	backupMu     sync.Mutex
	backupOnce   sync.Once
)

// InitBackupScheduler memasang client + menyalakan ticker backup otomatis.
// Dipanggil sekali di main.go setelah client dibuat.
func InitBackupScheduler(client *whatsmeow.Client) {
	backupClient = client
	backupOnce.Do(func() {
		go backupSchedulerLoop()
	})
}

func backupSchedulerLoop() {
	ticker := time.NewTicker(backupSweepInterval)
	defer ticker.Stop()
	for range ticker.C {
		maybeRunScheduledBackup()
	}
}

// maybeRunScheduledBackup dijalankan tiap tick: kalau interval sudah lewat,
// kirim SATU backup (walau banyak slot terlewat — cukup yang terakhir).
func maybeRunScheduledBackup() {
	client := backupClient
	if client == nil || !client.IsConnected() {
		return
	}
	ctx := context.Background()
	g := store.Groups()
	mins, err := g.GetBackupSchedule(ctx)
	if err != nil || mins <= 0 {
		return
	}
	last, err := g.GetBackupLastRun(ctx)
	if err != nil {
		return
	}
	now := time.Now()
	if !last.IsZero() && now.Sub(last) < time.Duration(mins)*time.Minute {
		return
	}
	if !backupMu.TryLock() {
		return
	}
	defer backupMu.Unlock()

	// Catat percobaan LEBIH DULU supaya tick berikutnya tidak menumpuk
	// walau pengiriman gagal (owner selalu bisa .backup manual).
	_ = g.SetBackupLastRun(ctx, now)
	runScheduledBackup(ctx, client)
}

func runScheduledBackup(ctx context.Context, client *whatsmeow.Client) {
	paths, err := listDBFiles()
	if err != nil || len(paths) == 0 {
		logfmt.Warn("Backup otomatis: tidak ada file database.\n")
		return
	}
	data, name, err := buildBackupZip(paths)
	if err != nil {
		logfmt.Warn("Backup otomatis gagal snapshot/zip: %v\n", err)
		return
	}
	caption := backupZipCaption(name, paths)
	sent := 0
	for _, owner := range ownerJIDs() {
		if err := sendDocBytes(ctx, client, owner, name, "application/zip", data, caption); err != nil {
			logfmt.Warn("Backup otomatis: gagal kirim ke %s: %v\n", owner.String(), err)
			continue
		}
		sent++
	}
	logfmt.Plain("Backup otomatis terkirim ke %d owner.\n", sent)
}

// ownerJIDs menyusun JID semua owner dari OWNER_NUMBERS (.env).
func ownerJIDs() []types.JID {
	out := make([]types.JID, 0, len(config.OwnerNumbers))
	for _, n := range config.OwnerNumbers {
		j, err := types.ParseJID(strings.TrimSpace(n) + "@" + types.DefaultUserServer)
		if err == nil && !j.IsEmpty() {
			out = append(out, j)
		}
	}
	return out
}

// --- Helper format ---

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func humanDuration(mins int) string {
	if mins%1440 == 0 {
		return fmt.Sprintf("%d hari", mins/1440)
	}
	if mins%60 == 0 {
		return fmt.Sprintf("%d jam", mins/60)
	}
	return fmt.Sprintf("%d menit", mins)
}
