package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"runtime"

	"wabot-go/internal/config"
	"wabot-go/internal/handler"
	"wabot-go/internal/store"
	"wabot-go/plugins"

	// Side-effect import: menjalankan init() di masing-masing file tool AI,
	// yang mendaftarkan dirinya ke internal/ai registry lewat RegisterTool.
	// Tanpa baris import ini, tool-tool tersebut TIDAK akan pernah terdaftar
	// meski file .go-nya ada di project (persis prinsip yang sama dengan
	// kenapa plugins/ perlu di-import agar init() plugin jalan).
	_ "wabot-go/internal/ai/tools/bridge"
	_ "wabot-go/internal/ai/tools/native"

	qrcode "github.com/skip2/go-qrcode"
	"go.mau.fi/whatsmeow"
	waLog "go.mau.fi/whatsmeow/util/log"
	_ "modernc.org/sqlite"

	"go.mau.fi/whatsmeow/store/sqlstore"
)

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
	fmt.Println("  Library  : whatsmeow")
	fmt.Printf("  Owner    : %s (%s)\n", config.OwnerName, config.OwnerNumber)
	fmt.Println(line)
	fmt.Println()
}

func main() {
	printBanner()

	// ==========================================================
	// 0. Siapkan database SQLite untuk pengaturan grup (kick, promote,
	//    open/close, dll). Terpisah dari data/session.db milik whatsmeow.
	// ==========================================================
	if err := store.InitGroupDB("data/group.db"); err != nil {
		panic(fmt.Sprintf("gagal inisialisasi group.db: %v", err))
	}

	// ==========================================================
	// 0b. Siapkan database SQLite untuk histori percakapan AI (command
	//     .ai + trigger natural). Terpisah dari group.db dan session.db.
	// ==========================================================
	if err := store.InitAIDB("data/ai.db"); err != nil {
		panic(fmt.Sprintf("gagal inisialisasi ai.db: %v", err))
	}

	// ==========================================================
	// 0c. Siapkan tabel fakta jangka panjang per-user (Fase 5, fact
	//     extractor). Reuse koneksi ai.db yang sama, hanya menambah tabel.
	// ==========================================================
	if err := store.InitFactsDB(); err != nil {
		panic(fmt.Sprintf("gagal inisialisasi facts DB: %v", err))
	}

	// ==========================================================
	// 0d. Siapkan tabel setting provider/model AI aktif (fitur
	//     multi-provider Gemini/OpenRouter). Reuse koneksi ai.db yang sama.
	// ==========================================================
	if err := store.InitAISettingsDB(); err != nil {
		panic(fmt.Sprintf("gagal inisialisasi AI settings DB: %v", err))
	}

	// ==========================================================
	// 1. Siapkan penyimpanan session (auth) memakai SQLite.
	//    File ini yang membuat bot tidak perlu scan ulang tiap restart.
	// ==========================================================
	dbLog := waLog.Stdout("Database", "ERROR", true)

	container, err := sqlstore.New(context.Background(), "sqlite", "file:data/session.db?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)", dbLog)
	if err != nil {
		panic(fmt.Sprintf("gagal membuka session store: %v", err))
	}

	deviceStore, err := container.GetFirstDevice(context.Background())
	if err != nil {
		panic(fmt.Sprintf("gagal mengambil device: %v", err))
	}

	clientLog := waLog.Stdout("Client", "INFO", true)
	client := whatsmeow.NewClient(deviceStore, clientLog)

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
		fmt.Println("✅ Bot berhasil terhubung ke WhatsApp (session lama)!")
	}

	// ==========================================================
	// 4. Tunggu sinyal berhenti (Ctrl+C atau kill dari Pterodactyl)
	//    supaya proses shutdown-nya bersih.
	// ==========================================================
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	fmt.Println("⏳ Menutup koneksi...")
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
				fmt.Printf("⚠️  Gagal membuat file QR PNG: %v\n", genErr)
			} else {
				fmt.Printf("\n📷 QR code tersimpan sebagai gambar: %s\n", qrImagePath)
				fmt.Println("   Buka file manager panel, download/preview file itu, lalu scan dari WhatsApp > Perangkat Tertaut.")
			}
			fmt.Println("\nAtau salin teks QR mentah ini ke generator QR online:")
			fmt.Println(evt.Code)
		case "success":
			fmt.Println("\n✅ Berhasil login lewat QR!")
			// Hapus file QR setelah berhasil supaya tidak membingungkan di restart berikutnya
			_ = os.Remove(qrImagePath)
		case "timeout":
			fmt.Println("⌛ QR code kadaluarsa, menunggu kode baru...")
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

	fmt.Printf("\n📱 Masukkan kode berikut di WhatsApp > Perangkat Tertaut > Tautkan dengan nomor telepon:\n\n   %s\n\n", code)
	fmt.Println("Kode berlaku sekitar 1-2 menit. Kalau kadaluarsa, restart server untuk mendapat kode baru.")
	return nil
}
