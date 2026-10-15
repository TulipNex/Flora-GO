package plugins

import (
	"context"
	"fmt"
	"os"
	"strings"

	"wabot-go/internal/logfmt"
	"wabot-go/internal/messaging"
	"wabot-go/internal/store"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/store/sqlstore"
	"github.com/polymorfa/hypermeow/types"
	"github.com/polymorfa/hypermeow/types/events"
	qrcode "github.com/skip2/go-qrcode"
	"google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite"
)

func init() {
	registerPlugin(RegisterJadibot)
}

// NewCloneHandlerFactory dipasang oleh package handler (init) ke
// NewEventHandler-nya sendiri. Pola ini menghindari import cycle:
// handler import plugins, jadi plugins tidak boleh import handler balik.
// Tanpa factory terpasang, clone tidak bisa dibuat (error jelas, bukan nil).
var NewCloneHandlerFactory func(client *whatsmeow.Client) func(interface{})

// RegisterJadibot mendaftarkan command `.jadibot` KHUSUS owner di BOT UTAMA
// (kategori Owner otomatis main-only): mengelola bot clone.
//
// Subcommand:
//	.jadibot qr <nomor>    — daftarkan nomor jadi clone via scan QR
//	.jadibot list          — daftar clone + status koneksi
//	.jadibot stop <nomor>  — matikan clone (session tersimpan, bisa start lagi)
//	.jadibot start <nomor> — hidupkan lagi clone yang di-stop
//	.jadibot hapus <nomor> — hapus total: pendaftaran + session + arsip group DB
//
// Pendaftaran HANYA via QR: pairing code model lama ditolak aplikasi WA baru
// (alur passkey), jadi tidak disediakan.
//
// Tiap clone = client hypermeow sendiri + session data/session-<nomor>.db +
// group DB data/group-<nomor>.db. Flora AI, fitur owner, dan fitur sensitif
// tidak jalan di clone (gerbang CloneBlocked di router).
func RegisterJadibot(r *Registry) {
	r.Register(Command{
		Name:        "jadibot",
		Description: "Kelola bot clone via QR (khusus bot utama)",
		Category:    CategoryOwner,
		Handler:     handleJadibot,
	})
}

func handleJadibot(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	if !store.IsMainClient(mctx.Client) {
		return store.ReplyText(ctx, mctx, "Perintah ini hanya tersedia di bot utama.")
	}
	if len(mctx.Args) == 0 {
		return store.ReplyText(ctx, mctx, "Pakai: .jadibot qr <nomor> | .jadibot list | .jadibot stop <nomor> | .jadibot start <nomor> | .jadibot hapus <nomor>")
	}
	switch strings.ToLower(strings.TrimSpace(mctx.Args[0])) {
	case "qr":
		return handleJadibotQR(ctx, mctx)
	case "list":
		return handleJadibotList(ctx, mctx)
	case "stop":
		return handleJadibotStop(ctx, mctx)
	case "start":
		return handleJadibotStart(ctx, mctx)
	case "hapus":
		return handleJadibotDrop(ctx, mctx)
	default:
		return store.ReplyText(ctx, mctx, "Pakai: .jadibot qr <nomor> | .jadibot list | .jadibot stop <nomor> | .jadibot start <nomor> | .jadibot hapus <nomor>")
	}
}

// wrapCloneEvents membungkus event handler standar dengan log debug khusus
// clone (prefix [jadibot <nomor>]): PairSuccess/PairError/Connected/
// Disconnected/LoggedOut. Tanpa ini, kegagalan pairing sunyi total di
// console dan mustahil didiagnosis — kejadian persis kasus 6283853531950.
func wrapCloneEvents(phone string, client *whatsmeow.Client, base func(interface{})) func(interface{}) {
	return func(rawEvt interface{}) {
		switch evt := rawEvt.(type) {
		case *events.PairSuccess:
			logfmt.Plain("[jadibot %s] pairing BERHASIL (JID=%s LID=%s)\n", phone, evt.ID.String(), evt.LID.String())
		case *events.PairError:
			logfmt.Warn("[jadibot %s] pairing GAGAL di sisi library: %+v\n", phone, evt)
		case *events.Connected:
			logfmt.Plain("[jadibot %s] terhubung ke server WhatsApp\n", phone)
		case *events.Disconnected:
			logfmt.Warn("[jadibot %s] terputus dari server WhatsApp\n", phone)
		case *events.LoggedOut:
			logfmt.Warn("[jadibot %s] logout (OnConnect=%v Reason=%v)\n", phone, evt.OnConnect, evt.Reason)
		}
		base(rawEvt)
	}
}

// handleJadibotList menampilkan semua clone terdaftar + status koneksinya.
func handleJadibotList(ctx context.Context, mctx *store.MessageContext) error {
	entries, err := store.Groups().ListCloneEntries(ctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal baca daftar clone: %v", err))
	}
	if len(entries) == 0 {
		return store.ReplyText(ctx, mctx, "Belum ada clone. Tambah via .jadibot qr <nomor>.")
	}
	var sb strings.Builder
	sb.WriteString("*Daftar Clone*\n\n")
	for _, e := range entries {
		status := "mati"
		if c := store.CloneClient(e.Phone); c != nil {
			switch {
			case c.IsConnected() && c.Store.ID != nil:
				status = "online"
			case c.Store.ID == nil:
				status = "menunggu pairing"
			}
		} else if e.Status == store.CloneStopped {
			status = "mati (stopped)"
		}
		fmt.Fprintf(&sb, "• %s — %s\n", e.Phone, status)
	}
	return store.ReplyText(ctx, mctx, sb.String())
}

// handleJadibotStop mematikan clone TANPA menghapus apa pun: disconnect,
// lepas registrasi client, tandai stopped. Session + arsip group DB tetap
// tersimpan sehingga bisa dihidupkan lagi via .jadibot start.
func handleJadibotStop(ctx context.Context, mctx *store.MessageContext) error {
	if len(mctx.Args) < 2 {
		return store.ReplyText(ctx, mctx, "Sertakan nomor: .jadibot stop 62812xxxxxxx")
	}
	phone := store.SanitizePhone(mctx.Args[1])
	if phone == "" {
		return store.ReplyText(ctx, mctx, "Nomor tidak valid.")
	}
	if c := store.CloneClient(phone); c != nil {
		c.Disconnect()
		store.UnregisterCloneClient(c)
	}
	if err := store.Groups().SetCloneStatus(ctx, phone, store.CloneStopped); err != nil {
		logfmt.Warn("Gagal tandai stop clone %s: %v\n", phone, err)
	}
	return store.ReplyText(ctx, mctx, "Clone "+phone+" dimatikan (session tersimpan — hidupkan lagi via .jadibot start "+phone+").")
}

// handleJadibotStart menghidupkan lagi clone yang di-stop dari session
// tersimpannya (tanpa QR ulang). Session hilang/belum pairing = ditolak
// dengan arahan .jadibot qr.
func handleJadibotStart(ctx context.Context, mctx *store.MessageContext) error {
	if len(mctx.Args) < 2 {
		return store.ReplyText(ctx, mctx, "Sertakan nomor: .jadibot start 62812xxxxxxx")
	}
	phone := store.SanitizePhone(mctx.Args[1])
	if phone == "" {
		return store.ReplyText(ctx, mctx, "Nomor tidak valid.")
	}
	if c := store.CloneClient(phone); c != nil && c.IsConnected() {
		return store.ReplyText(ctx, mctx, "Clone "+phone+" sudah online.")
	}
	clone, err := newCloneClient(ctx, phone)
	if err != nil {
		logfmt.Warn("[jadibot %s] start gagal: %v\n", phone, err)
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal menghidupkan clone: %v", err))
	}
	if clone.Store.ID == nil {
		clone.Disconnect()
		return store.ReplyText(ctx, mctx, "Session "+phone+" tidak ada/belum pairing — daftar via .jadibot qr "+phone+".")
	}
	if err := store.Groups().SetCloneStatus(ctx, phone, store.CloneActive); err != nil {
		logfmt.Warn("Gagal tandai active clone %s: %v\n", phone, err)
	}
	store.RegisterCloneClient(phone, clone)
	logfmt.Plain("[jadibot %s] start manual: online.\n", phone)
	return store.ReplyText(ctx, mctx, "Clone "+phone+" online lagi.")
}

// handleJadibotDrop menghapus TOTAL satu clone: disconnect, lepas registrasi,
// hapus baris pendaftaran, hapus file session, DAN hapus arsip group DB-nya.
// Tidak bisa dibatalkan — untuk berhenti sementara pakai .jadibot stop.
func handleJadibotDrop(ctx context.Context, mctx *store.MessageContext) error {
	if len(mctx.Args) < 2 {
		return store.ReplyText(ctx, mctx, "Sertakan nomor: .jadibot hapus 62812xxxxxxx")
	}
	phone := store.SanitizePhone(mctx.Args[1])
	if phone == "" {
		return store.ReplyText(ctx, mctx, "Nomor tidak valid.")
	}
	if c := store.CloneClient(phone); c != nil {
		c.Disconnect()
		store.UnregisterCloneClient(c)
	}
	if err := store.Groups().RemoveClone(ctx, phone); err != nil {
		logfmt.Warn("Gagal hapus baris clone %s: %v\n", phone, err)
	}
	store.CloseCloneDB(phone)
	for _, suffix := range []string{"", "-shm", "-wal"} {
		_ = os.Remove(store.CloneSessionPath(phone) + suffix)
		_ = os.Remove("data/group-" + phone + ".db" + suffix)
	}
	logfmt.Plain("[jadibot %s] dihapus total (session + arsip grup).\n", phone)
	return store.ReplyText(ctx, mctx, "Clone "+phone+" dihapus total (session + arsip grup).")
}

// makeCloneClient membuka session + device dan merakit client clone TANPA
// connect. Dipakai alur pair (connect langsung) dan alur QR (pasang QR
// channel dulu sebelum connect).
func makeCloneClient(ctx context.Context, phone string) (*whatsmeow.Client, error) {
	logfmt.Plain("[jadibot %s] buka session %s...\n", phone, store.CloneSessionPath(phone))
	container, err := sqlstore.New(ctx, "sqlite",
		"file:"+store.CloneSessionPath(phone)+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)",
		logfmt.NewWALogger("Database-"+phone, "ERROR"))
	if err != nil {
		return nil, fmt.Errorf("gagal buka session clone: %w", err)
	}
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		return nil, fmt.Errorf("gagal ambil device clone: %w", err)
	}
	if device == nil {
		logfmt.Plain("[jadibot %s] device baru (belum pernah pairing)\n", phone)
		device = container.NewDevice()
	} else if device.ID == nil {
		logfmt.Plain("[jadibot %s] device lama tanpa ID (pairing dulu tak selesai)\n", phone)
	} else {
		logfmt.Plain("[jadibot %s] device lama dengan ID %s\n", phone, device.ID.String())
	}
	clone := whatsmeow.NewClient(device, logfmt.NewWALogger("Client-"+phone, "INFO"))
	clone.AddEventHandler(wrapCloneEvents(phone, clone, NewCloneHandlerFactory(clone)))
	return clone, nil
}

// newCloneClient membuat device + client hypermeow untuk nomor clone,
// memasang event handler, dan connect. Dipakai RestoreClones. Ingat: TANPA
// meowcaller (telepon) dan TANPA reminder/absensi — semua itu main-only dan
// terpasang hanya di main.go untuk bot utama.
func newCloneClient(ctx context.Context, phone string) (*whatsmeow.Client, error) {
	clone, err := makeCloneClient(ctx, phone)
	if err != nil {
		return nil, err
	}
	if err := clone.Connect(); err != nil {
		return nil, fmt.Errorf("gagal connect clone: %w", err)
	}
	return clone, nil
}

// handleJadibotQR mendaftarkan nomor jadi clone via scan QR (satu-satunya
// metode: pairing code ditolak aplikasi WA baru). QR dikirim sebagai gambar
// ke chat owner, scan dari HP clone (Perangkat Tertaut > Tautkan Perangkat).
// Maks 3 gambar (tiap ~20 detik); sukses/gagal dilaporkan ke chat + console.
func handleJadibotQR(ctx context.Context, mctx *store.MessageContext) error {
	if NewCloneHandlerFactory == nil {
		return store.ReplyText(ctx, mctx, "Factory handler clone belum siap (restart bot).")
	}
	if len(mctx.Args) < 2 {
		return store.ReplyText(ctx, mctx, "Sertakan nomor: .jadibot qr 62812xxxxxxx")
	}
	phone := store.SanitizePhone(mctx.Args[1])
	if phone == "" {
		return store.ReplyText(ctx, mctx, "Nomor tidak valid.")
	}
	if store.CloneClient(phone) != nil {
		return store.ReplyText(ctx, mctx, "Nomor "+phone+" sudah terdaftar sebagai clone.")
	}
	clone, err := makeCloneClient(ctx, phone)
	if err != nil {
		logfmt.Warn("[jadibot %s] qr: setup gagal: %v\n", phone, err)
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal menyiapkan clone: %v", err))
	}
	if clone.Store.ID != nil {
		return store.ReplyText(ctx, mctx, "Nomor "+phone+" sudah punya session (cek .jadibot list).")
	}
	qrChan, err := clone.GetQRChannel(context.Background())
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal buka QR channel: %v", err))
	}
	if err := clone.Connect(); err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal connect clone: %v", err))
	}
	if err := store.Groups().AddClone(ctx, phone); err != nil {
		logfmt.Warn("Gagal catat clone %s: %v\n", phone, err)
	}
	store.RegisterCloneClient(phone, clone)
	logfmt.Plain("[jadibot %s] menunggu scan QR...\n", phone)

	mainCli := mctx.Client
	chat := mctx.Chat
	go func() {
		sent := 0
		for evt := range qrChan {
			switch evt.Event {
			case "code":
				sent++
				if sent > 3 {
					_ = replyCloneText(mainCli, chat, "QR clone "+phone+" kedaluwarsa berulang — ulangi .jadibot qr "+phone+" untuk kode baru.")
					return
				}
				png, err := qrcode.Encode(evt.Code, qrcode.Medium, 512)
				if err != nil {
					logfmt.Warn("[jadibot %s] qr: encode gagal: %v\n", phone, err)
					continue
				}
				if err := sendCloneQRImage(context.Background(), mainCli, chat, phone, sent, png); err != nil {
					logfmt.Warn("[jadibot %s] qr: kirim gambar gagal: %v\n", phone, err)
				}
			case "success":
				logfmt.Plain("[jadibot %s] pairing QR BERHASIL.\n", phone)
				_ = replyCloneText(mainCli, chat, "Clone "+phone+" online via QR.")
				return
			case "timeout":
				logfmt.Warn("[jadibot %s] qr: kode kedaluwarsa, menunggu kode baru...\n", phone)
			default:
				logfmt.Plain("[jadibot %s] qr: status %s\n", phone, evt.Event)
			}
		}
	}()
	return store.ReplyText(ctx, mctx, "QR clone "+phone+" disiapkan — gambar QR menyusul di chat ini, scan dari HP clone.")
}

// replyCloneText mengirim teks lepas (tanpa quote) dari bot utama — dipakai
// update async alur QR yang hidup lebih lama dari handler command.
func replyCloneText(mainCli *whatsmeow.Client, chat types.JID, text string) error {
	_, err := mainCli.SendMessage(context.Background(), chat, &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String(text)},
	})
	return err
}

// sendCloneQRImage mengupload PNG QR lalu mengirimnya sebagai gambar dari
// bot utama ke chat owner.
func sendCloneQRImage(ctx context.Context, mainCli *whatsmeow.Client, chat types.JID, phone string, seq int, png []byte) error {
	uploaded, err := mainCli.Upload(ctx, png, whatsmeow.MediaImage)
	if err != nil {
		return err
	}
	thumb := messaging.InlineThumbForImage(png)
	_, err = mainCli.SendMessage(ctx, chat, &waE2E.Message{
		ImageMessage: &waE2E.ImageMessage{
			URL:           proto.String(uploaded.URL),
			DirectPath:    proto.String(uploaded.DirectPath),
			MediaKey:      uploaded.MediaKey,
			Mimetype:      proto.String("image/png"),
			FileEncSHA256: uploaded.FileEncSHA256,
			FileSHA256:    uploaded.FileSHA256,
			FileLength:    proto.Uint64(uploaded.FileLength),
			Caption:       proto.String(fmt.Sprintf("Scan QR clone %s (%d/3) dari HP clone: Perangkat Tertaut > Tautkan Perangkat", phone, seq)),
			JPEGThumbnail: thumb,
		},
	})
	return err
}

// RestoreClones menghidupkan ulang semua clone berstatus active setelah bot
// start. Clone stopped dilewati (menunggu .jadibot start manual). Dipanggil
// sekali dari main.go (setelah connect bot utama). Clone yang session-nya
// belum selesai pairing (ID nil) dilewati dengan log — owner tinggal
// `.jadibot qr` ulang nomor itu.
func RestoreClones() {
	if NewCloneHandlerFactory == nil {
		logfmt.Warn("Factory handler clone belum siap, clone tidak direstore.\n")
		return
	}
	ctx := context.Background()
	entries, err := store.Groups().ListCloneEntries(ctx)
	if err != nil {
		logfmt.Warn("Gagal baca daftar clone (restore dibatalkan): %v\n", err)
		return
	}
	for _, e := range entries {
		phone := e.Phone
		if e.Status == store.CloneStopped {
			logfmt.Plain("[jadibot %s] stopped, dilewati (.jadibot start untuk menghidupkan).\n", phone)
			continue
		}
		container, err := sqlstore.New(ctx, "sqlite",
			"file:"+store.CloneSessionPath(phone)+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)",
			logfmt.NewWALogger("Database-"+phone, "ERROR"))
		if err != nil {
			logfmt.Warn("Clone %s: gagal buka session (%v), dilewati.\n", phone, err)
			continue
		}
		device, err := container.GetFirstDevice(ctx)
		if err != nil || device == nil || device.ID == nil {
			logfmt.Warn("Clone %s: belum pairing, dilewati (.jadibot qr ulang).\n", phone)
			continue
		}
		clone := whatsmeow.NewClient(device, logfmt.NewWALogger("Client-"+phone, "INFO"))
		clone.AddEventHandler(wrapCloneEvents(phone, clone, NewCloneHandlerFactory(clone)))
		if err := clone.Connect(); err != nil {
			logfmt.Warn("[jadibot %s] restore: gagal connect (%v).\n", phone, err)
			continue
		}
		store.RegisterCloneClient(phone, clone)
		logfmt.Plain("Clone %s online.\n", phone)
	}
}
