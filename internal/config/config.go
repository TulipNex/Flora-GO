package config

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

// Catatan:
// - Konfigurasi statis/compile-time (identitas bot & owner) digabung di sini
//   (config.go), menggantikan owner.go dan bot.go yang sebelumnya terpisah.
// - Konfigurasi yang sifatnya rahasia atau berubah per-environment
//   (LOGIN_METHOD, PHONE_NUMBER untuk pairing/QR) TETAP di .env, dibaca
//   lewat os.Getenv di tempat lain (main.go), BUKAN dipindah ke sini.
