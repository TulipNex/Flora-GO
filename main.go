package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"runtime"

	"wabot-go/internal/ai"
	"wabot-go/internal/config"
	"wabot-go/internal/handler"
	"wabot-go/internal/logfmt"
	"wabot-go/internal/store"
	"wabot-go/plugins"

	// Side-effect import: menjalankan init() di masing-masing file tool AI,
	// yang mendaftarkan dirinya ke internal/ai registry lewat RegisterTool.
	// Tanpa baris import ini, tool-tool tersebut TIDAK akan pernah terdaftar
	// meski file .go-nya ada di project (persis prinsip yang sama dengan
	// kenapa plugins/ perlu di-import agar init() plugin jalan).
	// Bridge di-import BERNAMA (bukan blank) karena main butuh memanggil
	// InitReminderClient + RestoreReminders di bawah.
	"wabot-go/internal/ai/tools/bridge"
	_ "wabot-go/internal/ai/tools/native"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/types"
	qrcode "github.com/skip2/go-qrcode"
	_ "modernc.org/sqlite"

	"github.com/polymorfa/hypermeow/store/sqlstore"
)

// onlinePresenceInterval adalah jeda heartbeat status "online" nomor bot.
// WhatsApp menganggap presence basi setelah beberapa puluh detik tanpa
// update (prinsip yang sama dengan WA Web yang terbuka) — tanpa heartbeat
// ini, kontak hanya melihat "terakhir dilihat ..." dari terakhir kali akun
// HP bot dibuka. Kirim ulang berkala membuat status tampil "online" terus.
//
// Kalau heartbeat ini TIDAK diinginkan (mis. ingin bot terlihat offline),
// hapus/komentari pemanggilan startOnlinePresence di main() di bawah.
const onlinePresenceInterval = 30 * time.Second

// startOnlinePresence mengirim presence "available" SEKARANG lalu mengulang
// tiap onlinePresenceInterval selama proses hidup. Kegagalan kirim
// (mis. socket belum tersambung saat start) hanya dicatat ke console dan
// dicoba lagi di tick berikutnya — tidak pernah mematikan bot.
func startOnlinePresence(client *whatsmeow.Client) {
	send := func() {
		if err := client.SendPresence(context.Background(), types.PresenceAvailable); err != nil {
			logfmt.Warn("Gagal kirim presence online: %v\n", err)
		}
	}
	go func() {
		send()
		ticker := time.NewTicker(onlinePresenceInterval)
		defer ticker.Stop()
		for range ticker.C {
			send()
		}
	}()
}

// Path file PNG QR yang akan dibuat di root project, bisa didownload lewat
// file manager panel (Pterodactyl) untuk di-scan dari HP.
const qrImagePath = "qr.png"

// printBanner menampilkan info identitas bot (nama, versi, bahasa, library,
// owner) di terminal saat startup. Dipanggil paling awal di main() supaya
// info ini selalu muncul, baik saat login QR/pairing maupun session lama.
func printBanner() {
	line := strings.Repeat("─", 46)
	fmt.Println(line)
	fmt.Printf("  %s %s\n", config.BotName, config.BotVersion)
	fmt.Println(line)
	fmt.Printf("  Language : Go (%s)\n", runtime.Version())
	fmt.Println("  Library  : hypermeow")
	fmt.Printf("  Owner    : %s (%s)\n", config.OwnerName, strings.Join(config.OwnerNumbers, ", "))
	fmt.Println(line)
	fmt.Println()
}

func main() {
	// Validasi owner PALING AWAL, sebelum apa pun (termasuk banner): nomor
	// owner WAJIB dari .env (OWNER_NUMBERS), tanpa fallback hardcoded.
	// Kosong = semua command admin terkunci + notifikasi error AI tanpa
	// tujuan — lebih baik tolak start dengan pesan eksplisit (preseden:
	// LOGIN_METHOD kosong juga panic).
	if len(config.OwnerNumbers) == 0 {
		panic("OWNER_NUMBERS kosong di file .env. Isi dengan 1-2 nomor, contoh: OWNER_NUMBERS=62812xxxxxxx,62813xxxxxxx (digit saja, pisah koma).")
	}

	printBanner()

	// ==========================================================
	// 0a. Sapu folder temp yatim downloader (wabot-dl-*) yang umurnya
	//    lebih dari 24 jam — sisa crash di tengah download yang defer-nya
	//    tidak sempat jalan. Aman & cepat, fail-soft di dalamnya.
	// ==========================================================
	plugins.CleanStaleDownloads()

	// ==========================================================
	// 0b. Siapkan database SQLite untuk pengaturan grup (kick, promote,
	//    open/close, dll). Terpisah dari data/session.db milik whatsmeow.
	// ==========================================================
	if err := store.InitGroupDB("data/group.db"); err != nil {
		panic(fmt.Sprintf("gagal inisialisasi group.db: %v", err))
	}

	// ==========================================================
	// 0b2. Siapkan database SQLite untuk keuangan Flora (catat/baca/
	//     rekap/edit/hapus/export lokal — pengganti Google Sheet).
	//     File sendiri supaya backup/retensi mandiri.
	// ==========================================================
	if err := store.InitFinanceDB("data/finance.db"); err != nil {
		panic(fmt.Sprintf("gagal inisialisasi finance.db: %v", err))
	}

	// ==========================================================
	// 0c. Siapkan database SQLite untuk histori percakapan AI (command
	//     .agent + trigger natural). Terpisah dari group.db dan session.db.
	// ==========================================================
	if err := store.InitAIDB("data/ai.db"); err != nil {
		panic(fmt.Sprintf("gagal inisialisasi ai.db: %v", err))
	}

	// ==========================================================
	// 0d. Siapkan tabel fakta jangka panjang per-user (Fase 5, fact
	//     extractor). Reuse koneksi ai.db yang sama, hanya menambah tabel.
	// ==========================================================
	if err := store.InitFactsDB(); err != nil {
		panic(fmt.Sprintf("gagal inisialisasi facts DB: %v", err))
	}

	// ==========================================================
	// 0e. Siapkan tabel setting + registry provider/model AI aktif.
	//     Reuse koneksi ai.db yang sama.
	// ==========================================================
	if err := store.InitAISettingsDB(); err != nil {
		panic(fmt.Sprintf("gagal inisialisasi AI settings DB: %v", err))
	}

	// ==========================================================
	// 0e2. Siapkan tabel ringkasan sesi (ingatan hemat: sesi idle 10 menit
	//     diringkas ≤1000 karakter, FIFO maksimal 4 per kunci sesi).
	//     Reuse koneksi ai.db yang sama.
	// ==========================================================
	if err := store.InitSummariesDB(); err != nil {
		panic(fmt.Sprintf("gagal inisialisasi summaries DB: %v", err))
	}

	// ==========================================================
	// 0e3. Siapkan arsip pesan ala FIORA/CRM (data/crm.db, maks 200
	//     terbaru global) untuk .lastchat/.crm/.insp. SENGAJA fail-soft
	//     (Warn + lanjut tanpa arsip, BUKAN panic seperti DB operasional)
	//     — forensik tidak boleh membunuh bot.
	// ==========================================================
	if err := store.InitCRMDB("data/crm.db"); err != nil {
		logfmt.Warn("gagal inisialisasi crm.db (arsip pesan mati): %v\n", err)
	}

	// ==========================================================
	// 0e4. Siapkan database absensi (data/absensi.db): patokan, sesi,
	//     catatan hadir, jadwal harian. Fail-soft seperti CRM (Warn +
	//     lanjut tanpa absensi) — presensi tak boleh membunuh bot.
	// ==========================================================
	if err := store.InitAbsensiDB("data/absensi.db"); err != nil {
		logfmt.Warn("gagal inisialisasi absensi.db (absensi mati): %v\n", err)
	}

	// Penyapu sesi idle → ringkas background. Hidup seumur proses;
	// berhenti saat main return (tak pernah, kecuali shutdown).
	ai.StartSummarySweeper(context.Background())

	// Muat sesi AI yang tersimpan di ai.db ke memori supaya penyapu di atas
	// ikut menangani sesi yang terhenti saat bot restart (tanpa ini,
	// barisnya menetap sampai kunci-nya diakses lagi).
	ai.AdoptPersistedSessions(context.Background())

	// Status warna log (diagnostik NO_COLOR/FORCE_COLOR — kondisi langsung
	// terlihat di console, tak perlu menebak kenapa header polos).
	fmt.Printf("warna log: %s\n", logfmt.ColorStatus())

	// ==========================================================
	// 1. Siapkan penyimpanan session (auth) memakai SQLite.
	//    File ini yang membuat bot tidak perlu scan ulang tiap restart.
	// ==========================================================
	dbLog := logfmt.NewWALogger("Database", "ERROR")

	container, err := sqlstore.New(context.Background(), "sqlite", "file:data/session.db?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)", dbLog)
	if err != nil {
		panic(fmt.Sprintf("gagal membuka session store: %v", err))
	}

	deviceStore, err := container.GetFirstDevice(context.Background())
	if err != nil {
		panic(fmt.Sprintf("gagal mengambil device: %v", err))
	}

	clientLog := logfmt.NewWALogger("Client", "INFO")
	client := whatsmeow.NewClient(deviceStore, clientLog)

	// Daftarkan client bot utama ke registry clone (fondasi jadibot):
	// router + store memakai ini untuk membedakan bot utama vs clone.
	store.SetMainClient(client)

	// ==========================================================
	// 2. Daftarkan event handler (pesan masuk, dsb) dari package handler
	// ==========================================================
	client.AddEventHandler(handler.NewEventHandler(client))

	// ==========================================================
	// 2b. Pasang meowcaller (fitur panggilan) SEBELUM Connect() dipanggil
	//     di jalur mana pun (QR, pairing, atau session lama), karena
	//     interceptor <call>/<ack> harus terpasang sebelum receive loop
	//     whatsmeow mulai jalan.
	// ==========================================================
	plugins.InitCallClient(client)

	// 2b1. Pasang hook retry SPR (Send Private Reply): PreRetryCallback
	//     penukar isi per penerima + fallback GetMessageForRetry. Harus
	//     sebelum Connect supaya retry pertama pun sudah terlayani.
	plugins.InitSPR(client)

	// 2b2. Pasang client untuk scheduler reminder Flora (pengiriman DM +
	//     telepon eskalasi dari timer). Sama posisinya dengan InitCallClient.
	bridge.InitReminderClient(client)

	// 2b3. Pasang client untuk timer absensi (reminder H-5, tutup otomatis,
	//     rekap) + ticker jadwal harian. Sama posisinya dengan di atas.
	plugins.InitAbsensiScheduler(client)

	// 2b4. Pasang client untuk backup database otomatis (ticker interval;
	//     hasil dikirim ke DM owner). Sama posisinya dengan di atas.
	plugins.InitBackupScheduler(client)

	// ==========================================================
	// 2c. Jaga nomor bot tampil "online" (bukan "terakhir dilihat").
	//     Heartbeat presence berkala, fail-soft kalau belum tersambung.
	// ==========================================================
	startOnlinePresence(client)

	// ==========================================================
	// 3. Login: SEPENUHNYA dikendalikan lewat .env, TIDAK menunggu
	//    input interaktif dari console (banyak panel/Console web tidak
	//    meneruskan stdin dengan baik, sehingga proses tampak "diam").
	// ==========================================================
	if client.Store.ID == nil {
		loginMethod := strings.ToLower(strings.TrimSpace(os.Getenv("LOGIN_METHOD")))

		switch loginMethod {
		case "pairing":
			if err := loginWithPairingCode(client); err != nil {
				panic(fmt.Sprintf("gagal login dengan pairing code: %v", err))
			}
		case "qr":
			if err := loginWithQR(client); err != nil {
				panic(fmt.Sprintf("gagal login dengan QR: %v", err))
			}
		default:
			panic("LOGIN_METHOD di file .env harus diisi 'qr' atau 'pairing'. " +
				"Proses tidak bisa menunggu input manual di console panel.")
		}
	} else {
		// Sudah pernah login sebelumnya, tinggal connect ulang
		err = client.Connect()
		if err != nil {
			panic(fmt.Sprintf("gagal connect: %v", err))
		}
		fmt.Println("Bot berhasil terhubung ke WhatsApp (session lama)!")
	}

	// ==========================================================
	// 3b. Pulihkan jadwal panggilan tersimpan (tahan restart). Dijalankan
	//     sekali setelah connect di jalur mana pun — jadwal yang waktunya
	//     sudah lewat selama bot mati diberitahu "terlewat" ke chat-nya.
	// ==========================================================
	plugins.RestoreScheduledCalls()

	// 3b2. Pulihkan pengingat Flora (berdampingan jadwal call di atas).
	bridge.RestoreReminders()

	// 3b3. Pulihkan timer sesi absen aktif + nyalakan ticker jadwal harian.
	plugins.RestoreAbsensiSessions()

	// 3b4. Pulihkan bot clone (jadibot) yang terdaftar. Jalan setelah
	// connect utama; tiap clone connect dengan session-nya sendiri.
	plugins.RestoreClones()

	// ==========================================================
	// 4. Tunggu sinyal berhenti (Ctrl+C atau kill dari Pterodactyl)
	//    supaya proses shutdown-nya bersih.
	// ==========================================================
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	fmt.Println("Menutup koneksi...")
	// Putus clone dulu supaya tidak ada koneksi menggantung yang menyeret
	// shutdown melewati stop timeout panel (lihat DisconnectAllClones).
	store.DisconnectAllClones()
	client.Disconnect()
}

// loginWithQR menyambungkan client, lalu tiap kali WhatsApp mengirim kode QR baru,
// simpan sebagai file PNG (qr.png) DAN print teksnya di console sebagai cadangan.
func loginWithQR(client *whatsmeow.Client) error {
	qrChan, _ := client.GetQRChannel(context.Background())
	err := client.Connect()
	if err != nil {
		return err
	}

	for evt := range qrChan {
		switch evt.Event {
		case "code":
			if genErr := qrcode.WriteFile(evt.Code, qrcode.Medium, 512, qrImagePath); genErr != nil {
				logfmt.Warn(" Gagal membuat file QR PNG: %v\n", genErr)
			} else {
				logfmt.Plain("\nQR code tersimpan sebagai gambar: %s\n", qrImagePath)
				fmt.Println("   Buka file manager panel, download/preview file itu, lalu scan dari WhatsApp > Perangkat Tertaut.")
			}
			fmt.Println("\nAtau salin teks QR mentah ini ke generator QR online:")
			fmt.Println(evt.Code)
		case "success":
			fmt.Println("\nBerhasil login lewat QR!")
			// Hapus file QR setelah berhasil supaya tidak membingungkan di restart berikutnya
			_ = os.Remove(qrImagePath)
		case "timeout":
			fmt.Println("QR code kadaluarsa, menunggu kode baru...")
		default:
			fmt.Println("Status login:", evt.Event)
		}
	}
	return nil
}

// loginWithPairingCode langsung memakai PHONE_NUMBER dari .env — TIDAK menunggu
// input manual apa pun, supaya tidak macet di console panel yang tidak
// meneruskan stdin.
func loginWithPairingCode(client *whatsmeow.Client) error {
	phone := strings.TrimSpace(os.Getenv("PHONE_NUMBER"))
	if phone == "" {
		return fmt.Errorf("PHONE_NUMBER kosong di file .env. Isi dengan format: 62812xxxxxxx (tanpa + atau spasi)")
	}

	err := client.Connect()
	if err != nil {
		return err
	}

	code, err := client.PairPhone(context.Background(), phone, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
	if err != nil {
		return err
	}

	fmt.Printf("\nMasukkan kode berikut di WhatsApp > Perangkat Tertaut > Tautkan dengan nomor telepon:\n\n   %s\n\n", code)
	fmt.Println("Kode berlaku sekitar 1-2 menit. Kalau kadaluarsa, restart server untuk mendapat kode baru.")
	return nil
}
