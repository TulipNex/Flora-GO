package config

import "os"

// ==========================
// Data Owner
// ==========================
// Dipakai bersama oleh plugin manapun yang butuh info owner
// (misal plugin .owner, pengecekan hak akses admin, dll).
// Ganti nilai di bawah dengan data owner yang sebenarnya.
const (
	// OwnerName adalah nama yang ditampilkan (contoh: di contact card .owner)
	OwnerName = "Mitra"

	// OwnerNumber adalah nomor WhatsApp owner, format internasional,
	// TANPA tanda "+", TANPA spasi/strip. Contoh: "6283853531950"
	OwnerNumber = "6285324023198"
)

// ==========================
// Identitas Bot
// ==========================
// Dipakai bersama oleh plugin manapun yang perlu menampilkan
// info bot (misal plugin .info, .menu, atau pesan startup di main.go).
const (
	// BotName adalah nama bot.
	BotName = "Flora GO"

	// BotVersion mengikuti versi rilis bot ini.
	BotVersion = "v1.1.0"

	// BotDescription adalah deskripsi singkat bot ini.
	BotDescription = "Bot WhatsApp multifungsi yang dibangun dengan bahasa pemrograman Golang menggunakan library whatsmeow"
)

// ==========================
// Metadata Sticker
// ==========================
// Dipakai sebagai nilai default oleh plugin .sticker dan .stickerwatermark
// saat user tidak menentukan packname/author sendiri lewat command.
const (
	// StickerPackname adalah nama pack default yang muncul di stiker.
	StickerPackname = "Flora GO"

	// StickerAuthor adalah nama author/publisher default yang muncul di stiker.
	StickerAuthor = "Mitra"
)

// ==========================
// Konfigurasi AI Agent
// ==========================
// Dipakai oleh plugin .ai dan seluruh isi internal/ai/.
// AIApiKey rahasia & per-environment sehingga tetap dibaca dari .env
// (pola sama seperti LOGIN_METHOD/PHONE_NUMBER di main.go), sedangkan
// AIModelDefault & AISystemPromptBase statis/compile-time sehingga
// ditulis langsung di sini seperti konfigurasi lain di file ini.
var (
	// AIApiKey dibaca dari .env (AI_API_KEY=...). Diisi lewat var+os.Getenv,
	// bukan const, karena Go tidak mengizinkan const diisi dari os.Getenv.
	AIApiKey = os.Getenv("AI_API_KEY")

	// OpenRouterAPIKey dibaca dari .env (OPENROUTER_API_KEY=...). Provider
	// kedua sejak fitur multi-provider — kredensial tetap dari .env,
	// terpisah dari pemilihan provider/model aktif yang tersimpan di SQLite
	// (lihat internal/ai/providers.go).
	OpenRouterAPIKey = os.Getenv("OPENROUTER_API_KEY")

	// NexaRouterAPIKey dibaca dari .env (NEXAROUTER_API_KEY=...). Provider
	// ketiga (https://router.nexaworks.web.id), API-nya OpenAI-compatible
	// sama seperti OpenRouter.
	NexaRouterAPIKey = os.Getenv("NEXAROUTER_API_KEY")

	// CioraAPIKey dibaca dari .env (CIORA_API_KEY=...). Provider keempat
	// (https://ciora.id), API-nya OpenAI-compatible dan dokumentasinya
	// menyatakan mendukung tool calling standar OpenAI.
	CioraAPIKey = os.Getenv("CIORA_API_KEY")

	// Kartu link `.menu` — semuanya opsional, kosong berarti pakai default
	// otomatis (link = wa.me nomor bot, judul = nama+versi bot, deskripsi =
	// jumlah command, tanpa thumbnail/favicon). Dipisah ke .env supaya
	// teks/link/gambar kartu bisa diganti tanpa compile ulang.
	MenuLink       = os.Getenv("MENU_LINK")
	MenuTitle      = os.Getenv("MENU_TITLE")
	MenuDesc       = os.Getenv("MENU_DESC")
	MenuThumbURL   = os.Getenv("MENU_THUMB_URL")
	MenuFaviconURL = os.Getenv("MENU_FAVICON_URL")
)

const (
	// AIModelDefault adalah model Gemini yang dipakai command .ai.
	// gemini-2.5-flash sudah dimatikan Google (404) per akhir 2026,
	// diganti ke gemini-3.6-flash (GA, generasi 3.x, harga masih di kelas "flash").
	AIModelDefault = "gemini-3.6-flash"

	// AISystemPromptBase adalah persona utama Flora GO.
	//
	// Fase 6: direfine dari versi minimal Fase 1 — sekarang berisi identitas
	// eksplisit (nama bot + siapa yang membuat), gaya bahasa santai/gaul ala
	// teman ngobrol, dan panduan eksplisit soal kapan pakai tool vs kapan
	// cukup jawab teks biasa (relevan sejak tool-calling masuk di Fase 3).
	//
	// Masih berupa const Go biasa (BUKAN file prompt.txt eksternal) — loader
	// file eksternal terpisah dari blueprint awal belum dikerjakan, ini murni
	// perbaikan ISI prompt-nya saja.
	AISystemPromptBase = "Kamu adalah " + BotName + ", bot WhatsApp yang dibuat sama " + OwnerName + ". " +
		"Kamu ngobrol kayak temen deket, bukan asisten kaku formal — santai, gaul, boleh pake bahasa sehari-hari " +
		"dan sesekali emoji secukupnya (jangan berlebihan tiap kalimat). Jawab pake bahasa Indonesia, ringkas, " +
		"nggak usah bertele-tele atau muter-muter.\n\n" +
		"Kalau ditanya siapa kamu atau siapa yang bikin kamu, jawab jujur apa adanya: kamu Flora, dibuat sama " + OwnerName + ".\n\n" +
		"Soal tool yang kamu punya (kick member, promote admin, dll): pakai tool itu HANYA kalau user MEMANG " +
		"eksplisit minta kamu melakukan aksi itu (misal \"tendang si X\", \"jadiin admin si Y\") DAN kamu di grup " +
		"yang relevan. Kalau user cuma nanya-nanya, curhat, atau ngobrol biasa — walau topiknya nyerempet ke hal " +
		"yang bisa dilakukan tool (misal \"gimana caranya nge-kick orang di grup\"), itu artinya dia nanya CARA, " +
		"BUKAN minta kamu eksekusi — jawab pakai teks biasa, jangan asal panggil tool. Kalau ragu apakah user " +
		"benar-benar minta aksi atau cuma nanya, lebih aman tanya balik dulu daripada langsung eksekusi tool."
)

// Catatan:
// - Konfigurasi statis/compile-time (identitas bot & owner) digabung di sini
//   (config.go), menggantikan owner.go dan bot.go yang sebelumnya terpisah.
// - Konfigurasi yang sifatnya rahasia atau berubah per-environment
//   (LOGIN_METHOD, PHONE_NUMBER untuk pairing/QR, AI_API_KEY) TETAP dibaca
//   dari .env lewat os.Getenv — untuk AIApiKey dibaca langsung di file ini
//   (bukan main.go) supaya seluruh plugin AI cukup import config, konsisten
//   dengan cara plugin lain mengakses OwnerName/BotName dkk.
