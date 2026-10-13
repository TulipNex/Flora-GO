package config

import (
	"os"
	"strings"
)

// ==========================
// Data Owner
// ==========================
// Dipakai bersama oleh plugin manapun yang butuh info owner
// (misal plugin .owner, pengecekan hak akses admin, dll).
const (
	// OwnerName adalah nama yang ditampilkan (contoh: di contact card .owner)
	OwnerName = "Mitra"
)

var (
	// OwnerNumbers adalah daftar nomor WhatsApp owner, format internasional,
	// TANPA tanda "+", TANPA spasi/strip. Contoh: "6283853531950".
	// Dibaca MURNI dari .env (OWNER_NUMBERS=..., pisah koma) — tidak ada
	// fallback hardcoded: .env kosong berarti TIDAK ADA owner, dan main.go
	// menolak start (fail-fast) supaya bot tidak jalan pincang tanpa admin.
	OwnerNumbers = parseOwnerNumbers(os.Getenv("OWNER_NUMBERS"))
)

// parseOwnerNumbers memecah nilai OWNER_NUMBERS (koma) jadi daftar digit
// bersih: trim spasi, buang "+", strip, dan karakter non-digit; entri
// kosong dibuang. "62812xxx, 62813xxx" -> ["62812xxx","62813xxx"].
func parseOwnerNumbers(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		var digits strings.Builder
		for _, r := range strings.TrimSpace(part) {
			if r >= '0' && r <= '9' {
				digits.WriteRune(r)
			}
		}
		if s := digits.String(); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// PrimaryOwnerNumber mengembalikan owner pertama untuk keperluan yang butuh
// SATU nomor (vCard .owner, target notifikasi error AI, fallback link
// .menu, banner startup). "" kalau OWNER_NUMBERS kosong (main.go sudah
// memastikan kondisi ini tidak lolos saat start normal).
func PrimaryOwnerNumber() string {
	if len(OwnerNumbers) == 0 {
		return ""
	}
	return OwnerNumbers[0]
}

// ==========================
// Identitas Bot
// ==========================
// Dipakai bersama oleh plugin manapun yang perlu menampilkan
// info bot (misal plugin .menu atau pesan startup di main.go).
const (
	// BotName adalah nama bot.
	BotName = "Flora GO"

	// BotDescription adalah deskripsi singkat bot ini.
	BotDescription = "Sistem otomatis (Bot WhatsApp) yang dapat membantu melakukan sesuatu, mencari dan mendapatkan data/informasi hanya melalui WhatsApp"
)

var (
	// BotVersion mengikuti versi rilis bot ini.
	//
	// Nilai di bawah adalah fallback untuk build manual (mis. `go run .`).
	// Build resmi via deploy.ps1 / version-bump.ps1 menyuntik versi aktif
	// dari file VERSION lewat linker flag:
	//   go build -ldflags "-X wabot-go/internal/config.BotVersion=vX.Y.Z" .
	// sehingga const sengaja dijadikan var agar bisa di-override saat build.
	BotVersion = "v5.1.1"
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
	StickerAuthor = "By Mitra"
)

// ==========================
// Konfigurasi AI Agent
// ==========================
// Dipakai oleh plugin .agent dan seluruh isi internal/ai/.
// API key rahasia & per-environment sehingga tetap dibaca dari .env
// (pola sama seperti LOGIN_METHOD/PHONE_NUMBER di main.go), sedangkan
// AISystemPromptBase statis/compile-time sehingga
// ditulis langsung di sini seperti konfigurasi lain di file ini.
var (
	// NexaRouterAPIKey dibaca dari .env (NEXAROUTER_API_KEY=...).
	// (https://router.nexaworks.web.id), API-nya OpenAI-compatible.
	NexaRouterAPIKey = os.Getenv("NEXAROUTER_API_KEY")

	// CioraAPIKey dibaca dari .env (CIORA_API_KEY=...). Provider utama Flora
	// (https://ciora.id), API-nya OpenAI-compatible dan dokumentasinya
	// menyatakan mendukung tool calling standar OpenAI.
	CioraAPIKey = os.Getenv("CIORA_API_KEY")

	// (Keuangan Flora kini database lokal; SHEET_API_URL/TOKEN pensiun
	// bersama Google Sheet — tidak dibaca lagi.)

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
	AISystemPromptBase = "Kamu adalah Flora GO, bot WhatsApp multifungsi yang dibuat oleh Mitra. " +
		"Gunakan Bahasa Indonesia sehari-hari yang wajar, ringkas dan langsung ke inti, emoji sesekali secukupnya, " +
		"hindari jawaban format list kecuali user memintanya atau hal tersebut memang lebih jelas dijelaskan dengan list " +
		"— khususnya rekap, daftar, dan jadwal. " +
		"Gaya bahasa menyesuaikan user: formal jika user formal, kasual jika user netral, dan gaul jika user menggunakan bahasa gaul.\n\n" +
		"Soal tool (kick member, promote admin, dll): pakai HANYA jika user eksplisit meminta aksi itu " +
		"di grup yang relevan. Kalau user cuma tanya cara atau mengobrol biasa walau topiknya berkaitan, " +
		"jawab teks biasa. Kalau ragu antara minta aksi atau cuma nanya, tanya balik dulu.\n\n" +
		"Kalau user mengirim gambar atau stiker, tanggapi natural seperti melihatnya langsung; " +
		"jangan deskripsikan isinya kecuali user bertanya."
)

// Catatan:
// - Konfigurasi statis/compile-time (identitas bot & owner) digabung di sini
//   (config.go), menggantikan owner.go dan bot.go yang sebelumnya terpisah.
// - Konfigurasi yang sifatnya rahasia atau berubah per-environment
//   (LOGIN_METHOD, PHONE_NUMBER untuk pairing/QR, NEXAROUTER_API_KEY,
//   CIORA_API_KEY) TETAP dibaca dari .env lewat os.Getenv — dibaca langsung
//   di file ini (bukan main.go) supaya seluruh plugin AI cukup import config,
//   konsisten dengan cara plugin lain mengakses OwnerName/BotName dkk.
