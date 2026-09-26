# Flora GO 

Bot WhatsApp modular berbasis Go, memakai library [whatsmeow](https://github.com/tulir/whatsmeow). Dilengkapi command bawaan, fitur panggilan WhatsApp, manajemen grup, dan **AI agent multi-provider** (Gemini / OpenRouter / NexaRouter / Ciora) dengan tool-calling, sesi percakapan, dan ingatan jangka panjang per user.

## Struktur Folder

```
wabot-go/
├── main.go                       # entry point: printBanner, InitCallClient, init DB (group/ai/facts/settings), login QR/pairing/session lama
├── internal/
│   ├── config/
│   │   └── config.go              # OwnerName, OwnerNumber, BotName, BotVersion, API key AI (dari .env), system prompt
│   ├── handler/
│   │   └── handler.go             # router pesan masuk -> command atau trigger AI, LogIncoming dipanggil di sini
│   ├── ai/                        # engine AI (multi-provider, agent loop, sesi, fakta, mood, bubble)
│   │   ├── types.go               # Content/Part/FunctionCall (format universal internal)
│   │   ├── provider_types.go      # interface ProviderAdapter, GenerateParams, ToolCall
│   │   ├── providers.go           # daftar provider/model, GetActiveProvider (baca dari SQLite + cache)
│   │   ├── gemini_adapter.go      # adapter Gemini API (function-calling native)
│   │   ├── openrouter_adapter.go  # adapter OpenRouter (OpenAI-compatible)
│   │   ├── nexarouter_adapter.go  # adapter NexaRouter (OpenAI-compatible, tools + fallback teks)
│   │   ├── ciora_adapter.go       # adapter Ciora AI (OpenAI-compatible, tools + fallback teks)
│   │   ├── loop.go                # agent loop: generate -> eksekusi tool -> ulang (max 8 putaran)
│   │   ├── registry.go            # registry tool AI + AgentContext
│   │   ├── reliability.go         # retry error transient + notifikasi owner saat gagal
│   │   ├── session.go             # sesi percakapan per private/grup, idle timeout 30 mnt, hydrate dari SQLite
│   │   ├── trigger.go             # deteksi trigger: kata kunci "flora/flo", reply, sticker, media
│   │   ├── spam_gate.go           # anti-spam per sender + lock per sesi
│   │   ├── bubble.go              # split balasan jadi multi-bubble + delay ketik simulasi
│   │   ├── mood.go                # deteksi mood user dari teks -> modifier system prompt
│   │   ├── facts.go               # format & inject fakta user ke system prompt
│   │   ├── fact_extractor.go       # ekstraksi fakta tiap 8 giliran (fire-and-forget)
│   │   ├── reply_context.go       # sertakan teks bubble bot yang di-reply sebagai konteks
│   │   └── tools/
│   │       ├── native/system.go   # tool system_info (uptime, versi, OS/arch)
│   │       └── bridge/group.go    # tool kick_member, promote_member (reuse validasi admin command)
│   └── store/
│       ├── context.go             # helper reply (ReplyText, SendText) & MessageContext
│       ├── group_helpers.go       # IsSenderAdmin, IsBotAdmin, participantMatchesJID (LID vs PN)
│       ├── groupdb.go             # lapisan SQLite untuk data grup (modernc.org/sqlite)
│       ├── aidb.go                # SQLite histori percakapan AI (ai_history)
│       ├── factsdb.go             # tabel user_facts (fakta jangka panjang per sender)
│       ├── aisettingsdb.go        # tabel ai_settings (provider/model aktif)
│       ├── bot_message_tracker.go # cache ID+teks pesan bot (untuk deteksi reply & konteks)
│       ├── quoted_context.go      # ambil teks bubble bot yang di-reply user
│       ├── message_unwrap.go      # buka bungkus Ephemeral/ViewOnce message
│       ├── trigger_extract.go     # ExtractStanzaID, IsReplyToBot, IsSticker/IsMedia
│       ├── sticker_exif.go        # tulis/baca EXIF webp (packname & author stiker)
│       └── log.go                 # LogIncoming & LogOutgoing (log semua pesan ke console)
├── plugins/
│   ├── registry.go                # Registry generik + slice registrars, RegisterAll jalankan semua otomatis
│   ├── ping.go                    # .ping (dengan hitung latensi ms)
│   ├── menu.go                    # .menu (daftar command otomatis + kata sambutan bot)
│   ├── info.go                    # .info (uptime, versi Go, OS/arch, jumlah command, dsb)
│   ├── owner.go                   # .owner (kirim vCard owner)
│   ├── sticker.go                 # .sticker / .s (gambar/video -> stiker webp via ffmpeg)
│   ├── stickerwatermark.go        # .stickerwatermark / .swm (ganti packname & author stiker)
│   ├── call.go                    # .call <nomor> dan .call <nomor>|<jam> (panggilan + penjadwalan WITA)
│   ├── groupmanage.go             # kick, promote, demote, tagall, .add <nomor...>
│   ├── groupsetting.go            # setname, setdesc, open/close, linkgc
│   └── ai.go                      # .ai (chat, reset, provider, model) + HandleAIMessage
├── data/                          # session WhatsApp & database SQLite (dibuat otomatis, JANGAN dihapus)
├── greeting.mp3                   # audio yang diputar saat panggilan (aset runtime, upload manual ke panel)
├── run.sh                         # startup script untuk Pterodactyl (source .env di awal)
├── deploy.ps1                     # script build + rclone copyto ke panel (dijalankan dari laptop)
├── push.ps1                       # script git add/commit/push cepat
├── go.mod / go.sum
├── .env / .env.example
└── .gitignore
```

> Catatan: folder `internal/` di atas adalah source of truth yang dikembangkan di laptop. Di panel Pterodactyl, source `.go` **tidak** disimpan — panel hanya menyimpan binary hasil compile (`wabot-go-linux`) plus aset runtime (`.env`, `data/`, `greeting.mp3`). Lihat bagian [Workflow Deploy](#4-workflow-deploy-laptop--panel).

## 1. Setup di Laptop

Pastikan sudah install [Go 1.22+](https://go.dev/dl/). Untuk fitur `.sticker`, server tempat bot jalan juga butuh **ffmpeg** terinstall.

```bash
cd wabot-go
go mod tidy      # download semua dependency, akan mengoreksi versi di go.mod otomatis
cp .env.example .env   # lalu isi LOGIN_METHOD, PHONE_NUMBER, dan API key AI
go run .         # jalankan langsung untuk testing
```

> Jika muncul error verifikasi checksum ke `sum.golang.org` (biasanya karena DNS/jaringan), jalankan:
> ```bash
> go env -w GOSUMDB=off
> ```

Login dikendalikan penuh lewat `.env` (`LOGIN_METHOD`, `PHONE_NUMBER`) supaya tidak perlu input interaktif dari console (banyak panel web tidak meneruskan stdin dengan baik):

- **QR Code**: set `LOGIN_METHOD=qr`, jalankan bot, lalu scan QR yang muncul di terminal (atau download `qr.png` dari file manager panel) pakai fitur "Tautkan Perangkat" di WhatsApp.
- **Pairing Code**: set `LOGIN_METHOD=pairing` dan `PHONE_NUMBER=62812xxxxxxx` di `.env`, lalu masukkan kode 8 digit yang muncul ke WhatsApp > Perangkat Tertaut > Tautkan dengan Nomor Telepon.

> `LOGIN_METHOD` wajib diisi `qr` atau `pairing` — bot akan panic (berhenti) jika kosong/tidak valid.

Setelah berhasil login, session tersimpan di `data/session.db` (+`.db-shm`, `.db-wal`) — tidak perlu login ulang tiap restart selama file ini tidak dihapus.

## 2. Menambah Command Baru

Berkat pola self-registration, menambah command baru **tidak perlu** menyentuh `registry.go` sama sekali.

1. Buat file baru di folder `plugins/`, misal `plugins/hello.go`:

```go
package plugins

import (
	"context"
	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterHello)
}

func RegisterHello(r *Registry) {
	r.Register(Command{
		Name:        "hello",
		Description: "Menyapa pengirim",
		Handler: func(ctx context.Context, mctx *store.MessageContext) error {
			return store.ReplyText(ctx, mctx, "Halo! 👋")
		},
	})
}
```

2. Jalankan `go run .` — command `.hello` langsung aktif. `RegisterAll` di `registry.go` otomatis menjalankan semua fungsi yang terdaftar lewat `init()`, dan `.menu` otomatis membaca semua command yang ada tanpa perlu update manual.

Pola yang sama berlaku untuk **tool AI** — buat file baru di `internal/ai/tools/native/` (tool murni, tanpa akses WhatsApp) atau `internal/ai/tools/bridge/` (tool yang butuh whatsmeow client), lalu daftarkan lewat `ai.RegisterTool(...)` di dalam `init()`. Jangan lupa import package-nya sebagai side-effect import di `main.go` (lihat baris `_ "wabot-go/internal/ai/tools/bridge"`), kalau tidak tool-nya tidak akan pernah terdaftar.

## 3. Build Binary untuk Pterodactyl

Panel Pterodactyl menjalankan container Linux, jadi build target Linux dari laptop (berlaku juga kalau laptopmu Windows/Mac):

```bash
GOOS=linux GOARCH=amd64 go build -o wabot-go-linux .
```

Ini menghasilkan satu file binary `wabot-go-linux` yang siap disinkronkan ke panel.

## 4. Workflow Deploy: Laptop → Panel

Source code hanya hidup di laptop. Panel hanya menerima **hasil compile** (binary) via rclone/SFTP — bukan source `.go`.

**Alur tiap ada perubahan kode:**

1. Edit kode di laptop (folder project, disarankan lewat VS Code dengan terminal bawaan PowerShell).
2. Stop bot lewat panel (web).
3. Jalankan `.\deploy.ps1` — script ini build Go lalu `rclone copyto` binary ke root panel.
4. Start bot lagi lewat panel (web).

**Setup rclone (sekali saja):**

```powershell
winget install Rclone.Rclone
rclone config   # buat remote SFTP bernama "panel"
```

Contoh isi remote `panel`: host `sftp.xxxxx`, port sesuai panel, user sesuai kredensial SFTP, auth password.

`deploy.ps1` memakai flag `--inplace` pada `rclone copyto` (tulis langsung ke nama file final, tanpa rename) — beberapa server SFTP panel menolak proses rename (`SSH_FX_FAILURE`), terutama saat binary lama sedang berjalan.

> Jika tidak punya akses Pterodactyl Client API Key, stop/start bot **tidak bisa diotomatisasi** lewat API dan harus dilakukan manual di panel setiap deploy.

**Aset runtime** (bukan source code) seperti `greeting.mp3` diupload langsung ke panel lewat File Manager/SFTP — tidak lewat `deploy.ps1`, karena tidak perlu dikompilasi.

### Opsi Alternatif — Build di dalam Container

Jika Egg yang dipakai sudah menyediakan Go di dalam container, bisa upload seluruh source code (tanpa binary) dan biarkan `run.sh` mendeteksi Go lalu build sendiri saat start pertama. Startup Command tetap:
```
bash run.sh
```
> Build otomatis di container biasanya lebih lambat (proses `go mod tidy`), dan berisiko gagal kalau resource disk/RAM container kecil. Upload binary jadi (Opsi utama di atas) jauh lebih cepat dan direkomendasikan untuk pemakaian sehari-hari.

## 5. Environment Variables di Panel

Diset lewat file `.env` di root panel (`run.sh` sudah otomatis `source .env` di awal):

| Variable               | Keterangan                                                                 |
|------------------------|----------------------------------------------------------------------------|
| `LOGIN_METHOD`         | `qr` atau `pairing`                                                        |
| `PHONE_NUMBER`         | Nomor HP bot untuk pairing (format `62812xxxxxxx`)                         |
| `AI_API_KEY`           | API key Google Gemini (provider `google`)                                  |
| `OPENROUTER_API_KEY`   | API key OpenRouter (provider `openrouter`)                                 |
| `NEXAROUTER_API_KEY`   | API key NexaRouter (provider `nexarouter`, https://router.nexaworks.web.id) |
| `CIORA_API_KEY`        | API key Ciora AI (provider `ciora`, https://ciora.id) |

Identitas bot & owner (nama, versi, deskripsi, nomor owner) **tidak** disimpan di `.env`, melainkan dikompilasi langsung ke binary lewat `internal/config/config.go` — ubah di sana lalu deploy ulang jika perlu mengganti nilainya.

> API key AI cukup diisi untuk provider yang mau dipakai. Pilihan provider/model aktif disimpan di SQLite (`data/ai_settings`), bisa diganti lewat command `.ai provider` / `.ai model` tanpa deploy ulang.

## Fitur Utama

| Command | Keterangan |
|---|---|
| `.ping` | Cek bot hidup + latensi (ms) |
| `.menu` | Daftar semua command otomatis |
| `.info` | Uptime, versi Go, OS/arch, jumlah command, info bot |
| `.owner` | Kirim vCard kontak owner |
| `.sticker` / `.s` | Konversi gambar/video (langsung atau reply) jadi stiker webp, termasuk animasi |
| `.stickerwatermark` / `.swm` | Ganti metadata packname & author stiker (reply gambar/stiker + `.swm <pack>\|<author>`) |
| `.call <target>` | Melakukan panggilan WhatsApp keluar, khusus owner (target: mention, reply, atau nomor `628xxx`/`08xx`/`+62 8xx-xxxx`). Progres (memanggil/berdering/terhubung/berakhir+durasi/ditolak/tidak dijawab) tampil dalam 1 bubble yang diedit |
| `.call <target>\|<jam>` | Menjadwalkan panggilan pada jam tertentu (format `HH.MM`, zona WITA); tersimpan di SQLite, tahan restart. Bisa juga via Flora: `telpon X jam 17.00` |
| `.add <nomor...>` | Menambahkan member ke grup langsung lewat nomor telepon (bisa banyak sekaligus) |
| `.kick` / `.promote` / `.demote` / `.tagall` | Manajemen dasar grup |
| `.setname` / `.setdesc` / `.open` / `.close` / `.linkgc` | Pengaturan grup |
| `.ai <pesan>` | Ngobrol dengan AI (lihat bagian [AI Agent](#fitur-ai-agent)) |
| `.ai reset` | Reset sesi percakapan AI (per private/grup) |
| `.ai provider [nama]` | Lihat/ganti provider AI aktif (google/openrouter/nexarouter/ciora) |
| `.ai model [nama]` | Lihat/ganti model AI untuk provider aktif |
| `$ <perintah>` | Eksekusi shell di server, khusus owner (async, timeout 120 dtk, output dipotong) |
| `\ <kode js>` | Eval JavaScript di dalam bot, khusus owner (async, timeout 30 dtk, tanpa akses Conn) |
| `.delete` / `.del` | Hapus pesan yang di-reply (pesan bot: siapa saja; pesan orang: admin + bot admin) |
| `.get <url> [browser]` | Fetch URL dengan sidik TLS browser + kirim isinya (host internal diblokir anti-SSRF) |
| `.rvo` | Buka pesan sekali-lihat yang di-reply |

Panggilan WhatsApp memakai library [meowcaller](https://github.com/purpshell/meowcaller). Data grup (setting, dsb) disimpan di SQLite lewat driver `modernc.org/sqlite` (pure Go, tanpa cgo).

## Fitur AI Agent

Selain command `.ai`, bot juga merespons pesan **natural** (tanpa prefix) lewat trigger berikut:

| Trigger | Keterangan |
|---|---|
| Kata kunci | Sebut nama bot: "flora", "flor", atau "flo" di dalam pesan |
| Reply ke bot | Membalas bubble bot (hanya jika sesi masih aktif, belum idle 30 menit) |
| Sticker-reply | Membalas pesan bot dengan stiker (sesi aktif) |
| Media-reply | Membalas pesan bot dengan gambar/video/audio/dokumen tanpa caption (sesi aktif) |

### Multi-Provider

Empat provider tersedia, bisa diganti kapan saja lewat `.ai provider`:

| Provider | Endpoint | Tool-calling | Default Model |
|---|---|---|---|
| `google` | generativelanguage.googleapis.com | ✅ | `gemini-3.6-flash` |
| `openrouter` | openrouter.ai | ✅ | `openai/gpt-4o-mini` |
| `nexarouter` | router.nexaworks.web.id | fallback teks* | `deepseek-v4-flash` |
| `ciora` | ciora.id | ✅ | `deepseek/deepseek-v4.1-flash` |

> **Catatan NexaRouter/Ciora:** adapter mencoba mengirim field `tools`; kalau server menolak (400 soal tools), otomatis fallback sekali tanpa tools (mode chat teks). Kalau tool (kick/promote) tidak pernah jalan di provider ini, berarti server memang menolak tools.

Model bisa diganti per provider lewat `.ai model <nama>` (daftar model hardcode di `internal/ai/providers.go`). Pengaturan provider/model **global** (berlaku untuk semua user) dan persist di SQLite.

### Kemampuan AI

- **Tool-calling**: AI bisa mengeksekusi `system_info`, `kick_member`, `promote_member`, `demote_member`, `set_group_name`, `tag_all`, `group_link`, `group_close`, `group_open`, `make_sticker` (butuh hak admin grup yang cukup), serta `make_call` & `bot_mode` (khusus owner) — hanya jika user eksplisit minta aksi.
- **Sesi percakapan**: histori per user, terpisah antara chat private dan tiap grup. Idle timeout 30 menit, persist ke SQLite (survive restart).
- **Ingatan jangka panjang**: fakta tentang user (nama, hobi, dll) diekstrak otomatis tiap 8 giliran dan disuntik ke system prompt lintas chat/grup.
- **Multi-bubble**: balasan panjang dipecah jadi beberapa bubble dengan delay ketik simulasi + status "mengetik...".
- **Mood detection**: deteksi mood user dari teks (sedih/stres/senang) menyesuaikan nada balasan.
- **Konteks reply**: jika user reply bubble bot, teks bubble tersebut disertakan sebagai konteks.
- **Vision**: gambar di pesan pemicu (lampiran langsung atau reply) diteruskan ke model yang mendukung (provider OpenAI-compatible) sebagai `image_url` base64; provider Gemini mengabaikannya. Batas 4MB, histori tersimpan tetap teks.
- **Baca dokumen**: file di pesan pemicu (lampiran langsung atau reply) diekstrak teksnya dan ditempel ke pesan — PDF (`ledongthuc/pdf`), Word `.docx`, Excel `.xlsx` (`excelize`), dan teks polos (TXT/CSV/Markdown). Dokumen sampai 12000 karakter ditempel utuh; selebihnya dibaca bertahap (map-reduce: potongan 8000 karakter diringkas paralel lalu digabung, maks 8 bagian). Batas file 8MB; format lain (termasuk `.doc`/`.xls` lawas) ditolak dengan catatan jujur.
- **Reliability**: retry otomatis untuk error transient (timeout/429/5xx), notifikasi ke owner via WhatsApp jika gagal total.

> Pemilihan provider & model juga menentukan jalannya fact extractor — kalau OpenRouter sedang aktif, ekstraksi fakta ikut lewat OpenRouter (bukan selalu Gemini).

## Catatan Penting

- **Jangan hapus folder `data/`** di panel — di situ session login WhatsApp (`session.db`), database grup (`group.db`), dan database AI (`ai.db` berisi histori percakapan, fakta user, pengaturan provider) disimpan. Kalau dihapus, bot harus login ulang dari nol dan seluruh data di atas hilang.
- Nomor yang dipakai bot sebaiknya bukan nomor utama sehari-hari, untuk menghindari risiko banned mengganggu akun pribadi.
- whatsmeow adalah library tidak resmi (reverse-engineered protokol WhatsApp Web) — pemakaian berlebihan/spam berisiko nomor terkena banned oleh WhatsApp.
- Pengecekan admin grup (`IsSenderAdmin`/`IsBotAdmin`) membandingkan JID lewat `.User`, nomor telepon, **dan** LID sekaligus untuk menghindari kegagalan akibat dual-format identitas WhatsApp (LID vs nomor telepon).
- Fitur AI bergantung pada API key di `.env` — jika provider aktif tapi key-nya kosong, chat AI akan gagal (bot tetap jalan untuk command lain). Error AI yang gagal total dilaporkan ke nomor owner via WhatsApp.
- Fitur `.sticker` butuh **ffmpeg** di PATH server. Tanpa ffmpeg, command akan membalas error "Gagal convert ke stiker".