# AGENTS.md

Panduan untuk AI agent (dan developer baru) yang bekerja di repo `wabot-go` — bot WhatsApp "Flora GO" berbasis Go + whatsmeow.

## Build & Run

| Perintah | Fungsi |
|---|---|
| `go mod tidy` | Sinkronkan dependency |
| `go build .` / `go run .` | Build & jalankan lokal |
| `go vet ./...` | Cek statis |
| `GOOS=linux GOARCH=amd64 go build -o wabot-go-linux .` | Build binary untuk panel Pterodactyl |
| `.\deploy.ps1` | Build linux + sync binary ke panel via rclone |
| `.\push.ps1 "pesan"` | `git add` + `commit` + `push` (ada filter file sensitif) |

> Repo ini **belum punya test suite, linter config, atau CI**. Verifikasi minimal: `go vet ./...` lalu `go run .` dan chat bot di WhatsApp untuk uji manual.

## Arsitektur

### Self-registration pattern
- **Command baru**: buat file di `plugins/`, daftarkan di `init()` via `registerPlugin(RegisterX)`.
  **JANGAN** edit `plugins/registry.go` — `RegisterAll` menjalankan semua registrar otomatis.
- **Tool AI baru**: buat file di `internal/ai/tools/native/` (murni, tanpa akses WhatsApp) atau
  `internal/ai/tools/bridge/` (butuh whatsmeow client), daftarkan via `ai.RegisterTool(...)` di `init()`.
  **WAJIB** tambah side-effect import di `main.go` (baris `_ "wabot-go/internal/ai/tools/..."`) —
  tanpa import ini tool tidak akan pernah terdaftar meski file `.go`-nya ada.

### Arah dependency (JANGAN dibalik)
```
plugins  ->  internal/ai  ->  internal/store
   ^              ^
   |              |
   +--- tools/bridge (satu-satunya yang boleh import plugins)
```
- `internal/ai` TIDAK boleh import whatsmeow langsung — akses client/mctx lewat `AgentContext.Extra`.
- `internal/handler` adalah satu-satunya tempat routing pesan masuk.

### Command prefix
- Prefix `.` didefinisikan di `internal/handler/handler.go` (`CommandPrefix`) DAN terduplikasi di
  `plugins/menu.go` (`CommandPrefixForMenu`) untuk hindari dependency melingkar.
  **Ubah keduanya** kalau mau ganti prefix.

## Konvensi Kode

- Komentar panjang dalam **bahasa Indonesia**, jelaskan MENAPA sebuah keputusan dibuat, bukan hanya apa yang dilakukan.
- Komentar berlabel "Fase X" adalah riwayat evolusi fitur — pertahankan, jangan dihapus saat refactor.
- Pesan ke user: ramah & ringkas via `store.ReplyText`. Detail teknis: ke console (`fmt.Printf`) atau ke owner via WhatsApp.
- Operasi non-kritis (baca fakta, kirim notifikasi, presence "mengetik") wajib **fail-soft**: log error, jangan panic/gagalkan alur utama.
- Sebelum membaca isi `evt.Message`, selalu `store.UnwrapMessage(...)` — WhatsApp membungkus pesan dalam EphemeralMessage/ViewOnceMessage.
- Balasan AI panjang: pecah via `ai.SplitIntoBubbles` + typing delay (`sendBubbles` di `plugins/ai.go`), jangan kirim satu bubble raksasa.

## Database (SQLite via `modernc.org/sqlite`, pure Go, tanpa cgo)

| File | Init | Akses |
|---|---|---|
| `data/session.db` | whatsmeow `sqlstore` | Jangan disentuh manual |
| `data/group.db` | `store.InitGroupDB` | `store.Groups()` |
| `data/ai.db` | `store.InitAIDB` → `InitFactsDB` → `InitAISettingsDB` | `store.AI()` |

- **Urutan init di `main.go` WAJIB**: group → ai → facts → settings (facts & settings reuse koneksi ai.db).
- Pola migration: `CREATE TABLE IF NOT EXISTS` + toleransi error "duplicate column" (lihat `aisettingsdb.go`).
- Semua DSN pakai `journal_mode(WAL)` + `busy_timeout(10000)`.

## AI Engine

- **4 provider** di belakang interface `ProviderAdapter`: `google` (Gemini), `openrouter`, `nexarouter`, `ciora`.
  Pilihan aktif disimpan di SQLite + cache in-memory (`providers.go`), diganti via `.ai provider` / `.ai model`.
- **NexaRouter/Ciora**: adapter mencoba mengirim field `tools`; kalau server menolak (400 soal tools), otomatis fallback sekali tanpa tools (mode chat teks). Lihat `postChat` di `nexarouter_adapter.go` / `ciora_adapter.go`.
- **Session key**: selalu pakai `ai.BuildSessionKey(isGroup, chatJID, senderJID)` — jangan format string sendiri.
  Histori percakapan pakai sessionKey; fakta jangka panjang pakai `senderJID` murni (lintas chat/grup).
- **Tool calling**: saat menyusun ulang history, WAJIB echo balik apa adanya:
  - `ThoughtSignature` (di `Part`) — Gemini menolak request kalau hilang (error 400).
  - `ID` (di `FunctionCall`/`FunctionResponse`) — provider OpenAI-style mensyaratkan `tool_call_id` cocok persis.
- **Reliability**: retry transient (timeout/429/5xx) max 3x dengan backoff, lalu notifikasi owner.

## Hal yang Mudah Rusak

- **Dual-format JID (LID vs nomor telepon)**: selalu pakai `store.ParticipantMatchesJID` — jangan bandingkan `.User` polos, bisa false-negative di grup AddressingMode "lid", dan jangan bikin salinan logic-nya sendiri.
- **Baca ContextInfo** (reply/mention/quoted): selalu lewat helper di `internal/store/trigger_extract.go` (`ExtractStanzaID`, `ExtractMentionedJIDs`, `ExtractQuotedParticipant`, `ExtractQuotedMessage`) yang sudah unwrap Ephemeral/ViewOnce — jangan baca `RawEvt.Message.GetExtendedTextMessage()` langsung.
- `.sticker` / `.swm` butuh **ffmpeg** di PATH server.
- `LOGIN_METHOD` di `.env` wajib `qr` atau `pairing` — kosong/tidak valid = panic saat startup.
- `.env` berisi nomor HP & API key — **jangan pernah commit** (sudah di `.gitignore`, `push.ps1` juga memperingatkan).
- Jangan commit: `.env`, `data/`, `wabot-go-linux`.
- Jangan hapus `data/` di panel — session login + 3 database ada di sana.

## Deploy Workflow

Source code hanya hidup di laptop; panel Pterodactyl hanya menerima binary. Alur perubahan:
1. Edit kode → 2. Stop bot di panel → 3. `.\deploy.ps1` → 4. Start bot di panel.

Detail lengkap: `README.md`.
