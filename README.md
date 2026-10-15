# Flora GO

Bot WhatsApp modular berbasis Go, memakai library [hypermeow](https://github.com/polymorfa/hypermeow) (fork whatsmeow). Dilengkapi downloader 7 platform (scraper-first + album media), command grup & owner lengkap, dan **AI agent Flora multi-provider** (Ciora / OpenAgentic, default Ciora — provider & model bisa ditambah/dihapus lewat command, khusus owner) dengan sistem trust, tool-calling, sesi percakapan, dan ingatan jangka panjang per user.

## Struktur Folder

```
wabot-go/
├── main.go                       # entry point: printBanner, InitCallClient, init DB (group/ai/facts/settings), login QR/pairing/session lama
├── internal/
│   ├── config/
│   │   └── config.go              # OwnerName, OwnerNumber, BotName, BotVersion, API key AI + kartu menu (dari .env), system prompt
│   ├── handler/
│   │   └── handler.go             # router pesan masuk -> command / prefix spesial ($, \) / trigger AI, LogIncoming di sini
│   ├── ai/                        # engine AI (multi-provider, agent loop, sesi, fakta, mood, bubble)
│   │   ├── types.go               # Content/Part/FunctionCall (format universal internal)
│   │   ├── provider_types.go      # interface ProviderAdapter, GenerateParams, ToolCall
│   │   ├── providers.go           # daftar provider/model, GetActiveProvider (baca dari SQLite + cache)
│   │   ├── openai_common.go       # konversi bersama Content/Part <-> format OpenAI
│   │   ├── openai_adapter.go      # adapter generik SEMUA provider OpenAI-compatible (tools + fallback teks)
│   │   ├── loop.go                # agent loop: generate -> eksekusi tool -> ulang (max 8 putaran)
│   │   ├── registry.go            # registry tool AI + AgentContext
│   │   ├── reliability.go         # retry error transient + notifikasi owner saat gagal
│   │   ├── session.go             # sesi percakapan per private/grup, idle timeout 10 mnt + ringkasan FIFO, hydrate dari SQLite
│   │   ├── trigger.go             # deteksi trigger: kata kunci "flora/flo", reply, sticker, media + lock/unlock
│   │   ├── lock.go                # perintah lock/unlock trigger natural (global, khusus owner)
│   │   ├── spam_gate.go           # anti-spam per sender + lock per sesi
│   │   ├── bubble.go              # split balasan jadi multi-bubble + delay ketik simulasi
│   │   ├── mood.go                # deteksi mood user dari teks -> modifier system prompt
│   │   ├── facts.go               # format & inject fakta user ke system prompt
│   │   ├── fact_extractor.go       # ekstraksi fakta tiap 8 giliran (fire-and-forget)
│   │   ├── reply_context.go       # sertakan teks bubble bot yang di-reply sebagai konteks
│   │   └── tools/
│   │       ├── native/system.go   # tool system_info (uptime, versi, OS/arch)
│   │       └── bridge/group.go    # tool kick/promote/demote, tag, link, buka/tutup grup, stiker, call (reuse validasi command)
│   ├── messaging/                 # merakit + mengirim pesan kaya (dipakai plugins & tool bridge)
│   │   ├── linkcard.go            # kartu link preview + thumbnail mini inline + dukungan mention
│   │   ├── mediathumb.go          # thumbnail inline mini (~1-3KB, agar preview tampil, bukan abu-abu)
│   │   ├── videonorm.go           # validasi + normalisasi video (MP4/H.264/AAC, transcode bila perlu)
│   │   └── album.go               # pengiriman album media (association + induk counts)
│   ├── scraper/                   # adapter unduhan per platform (rantai fallback, tanpa yt-dlp kecuali YouTube)
│   │   ├── scraper.go             # tipe ScrapeItem/ScrapeResult, HTTP client API (20 dtk) + unduhan (10 mnt, cap 100MB)
│   │   ├── tiktok.go              # tikwm → tiktokdl → musically
│   │   ├── instagram.go           # igexport → savereels (pilih resolusi terbesar)
│   │   ├── twitter.go             # xhancer
│   │   ├── facebook.go            # faa (foto difilter via omnify)
│   │   ├── pinterest.go           # klickpin (resolve shortlink + CSRF + parse tombol)
│   │   ├── youtube.go             # savetube-direct (AES, tanpa cookie/polling)
│   │   ├── spotify.go             # nexray → musicfab
│   │   ├── omnify.go              # Omnify AIO: fallback universal 45+ platform
│   │   ├── universal.go           # FetchUniversal (kontrak tetap, isi = omnify)
│   │   └── htmlhelp.go            # parse HTML + helper URL
│   └── store/
│       ├── context.go             # helper reply (ReplyText, ReplyTextWithMentions, SendText) & MessageContext
│       ├── group_helpers.go       # IsSenderAdmin, IsBotAdmin, ParticipantMatchesJID (LID vs PN)
│       ├── groupdb.go             # SQLite group.db: setting grup, ban, warn, autodownload, trusted_users, dst
│       ├── ownerdb.go             # ban/unban, mode self/public, lock AI, trust/untrust Flora
│       ├── aidb.go                # SQLite ai.db: histori percakapan AI (ai_history)
│       ├── factsdb.go             # tabel user_facts (fakta jangka panjang per sender + hapus)
│       ├── aisettingsdb.go        # tabel ai_settings (provider/model aktif)
│       ├── tempdir.go             # BotTempDir (<workdir>/wabot-tmp, bukan /tmp yang 100MB di panel)
│       ├── autodownload.go        # saklar autodownload per chat
│       ├── moderationdb.go        # antilink + warnlist per grup
│       ├── scheduledcalls.go      # jadwal panggilan (tahan restart)
│       ├── bot_message_tracker.go # cache ID+teks pesan bot (untuk deteksi reply & konteks)
│       ├── quoted_context.go      # ambil teks bubble bot yang di-reply user
│       ├── message_unwrap.go      # buka bungkus Ephemeral/ViewOnce message
│       ├── trigger_extract.go     # ExtractStanzaID, IsReplyToBot, IsSticker/IsMedia, mentions
│       ├── sticker_exif.go        # tulis/baca EXIF webp (packname & author stiker)
│       └── log.go                 # LogIncoming & LogOutgoing (log semua pesan ke console)
├── plugins/
│   ├── registry.go                # Registry generik + slice registrars, RegisterAll jalankan semua otomatis
│   ├── ping.go                    # .ping (dengan hitung latensi ms)
│   ├── menu.go                    # .menu berkartu (kategori/all/spesifik + sapaan mention + bold)
│   ├── serverinfo.go            # .server (alias .serverinfo/.si, owner): hardware, memori sistem, runtime + versi library
│   ├── serverinfo_sys_linux.go    # syscall khusus Linux (kernel release, memori)
│   ├── serverinfo_sys_other.go    # stub non-Linux (fail-soft "-")
│   ├── osinfo.go                  # .os (owner): OS + jaringan server (IP/ISP/lokasi via ip-api.com)
│   ├── rssinfo.go                 # .rss (owner): memori proses (RSS + heap)
│   ├── jadibot.go                 # .jadibot (owner, bot utama): pair/list/stop bot clone (session + group DB per nomor)
│   ├── downloader.go              # .tiktok/.youtube/.instagram/.twitter/.facebook/.pinterest/.spotify + album + WebP + batas ukuran/durasi
│   ├── ai.go                      # .agent (admin Flora: model/provider/status/trust/reset) + HandleAIMessage
│   ├── sticker.go                 # .sticker / .s (gambar/video -> stiker webp via ffmpeg)
│   ├── stickerwatermark.go        # .swm (ganti packname & author stiker)
│   ├── call.go                    # .call <nomor> dan .call <nomor>|<jam> (panggilan + penjadwalan WITA)
│   ├── groupmanage.go             # kick, promote, demote, tagall, .add <nomor...>
│   ├── groupsetting.go            # setname, setdesc, open/close, linkgc
│   ├── moderation.go              # antilink, warn/unwarn/warnlist + welcome/bye grup
│   ├── welcome.go                 # sapaan + perpisahan member grup (template)
│   ├── owneradmin.go              # ban/unban user & grup, mode self/public, gate owner + trust Flora
│   ├── ownerexec.go               # $ shell server dan \ eval JS (khusus owner, async + timeout)
│   ├── ownerget.go                # .get fetch URL (sidik TLS browser, anti-SSRF, >10MB jadi dokumen)
│   ├── ownerjoin.go               # .join/.leave grup via link undangan
│   ├── ownerrvo.go                # .rvo buka pesan sekali-lihat yang di-reply
│   ├── owner.go                   # .owner (kirim vCard owner)
│   ├── delete.go                  # .delete/.del hapus pesan yang di-reply
│   ├── docread.go                 # baca dokumen untuk AI (PDF/DOCX/XLSX/teks, map-reduce)
│   └── testthumb.go               # .testthumb (DEMO dev untuk kartu link preview, bukan fitur user)
├── data/                          # session WhatsApp & database SQLite (dibuat otomatis, JANGAN dihapus)
├── wabot-tmp/                     # temp kerja bot (dibuat otomatis, JANGAN dihapus, gitignored)
├── greeting.mp3                   # audio yang diputar saat panggilan (aset runtime, upload manual ke panel)
├── run.sh                         # startup script untuk Pterodactyl (source .env di awal)
├── deploy.ps1                     # script build + rclone copyto ke panel (dijalankan dari laptop)
├── push.ps1                       # script git add/commit/push cepat
├── go.mod / go.sum               # Go 1.26+, hypermeow terbaru
├── .env / .env.example
└── .gitignore
```

> Catatan: folder `internal/` di atas adalah source of truth yang dikembangkan di laptop. Di panel Pterodactyl, source `.go` **tidak** disimpan — panel hanya menyimpan binary hasil compile (`wabot-go-linux`) plus aset runtime (`.env`, `data/`, `greeting.mp3`). Lihat bagian [Workflow Deploy](#4-workflow-deploy-laptop--panel).

## 1. Setup di Laptop

Pastikan sudah install [Go 1.26+](https://go.dev/dl/). Bot butuh **ffmpeg** di PATH server tempat ia jalan — bukan cuma untuk `.sticker`, tapi juga thumbnail video, konversi WebP, dan transcode video asing.

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
		Category:    CategoryUmum, // wajib salah satu konstanta CategoryXxx di registry.go
		Handler: func(ctx context.Context, mctx *store.MessageContext) error {
			return store.ReplyText(ctx, mctx, "Halo! 👋")
		},
	})
}
```

2. Jalankan `go run .` — command `.hello` langsung aktif. `RegisterAll` di `registry.go` otomatis menjalankan semua fungsi yang terdaftar lewat `init()`, dan `.menu` otomatis membaca semua command yang ada tanpa perlu update manual.

Pola yang sama berlaku untuk **tool AI** — buat file baru di `internal/ai/tools/native/` (tool murni, tanpa akses WhatsApp) atau `internal/ai/tools/bridge/` (tool yang butuh WhatsApp client), lalu daftarkan lewat `ai.RegisterTool(...)` di dalam `init()`. Jangan lupa import package-nya sebagai side-effect import di `main.go` (lihat baris `_ "wabot-go/internal/ai/tools/bridge"`), kalau tidak tool-nya tidak akan pernah terdaftar.

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
3. Jalankan `.\deploy.ps1` — script ini menentukan **versi otomatis** dari file yang berubah sejak deploy terakhir (`version-bump.ps1`: file Go baru = minor, hapus command/tool atau naik Go = major, edit lain = patch, hanya README/data = tetap; snapshot di `.last-deploy-state`), menulisnya ke `VERSION`, menyuntik ke binary via ldflags, lalu build + `rclone copyto` binary ke root panel.
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
| `<NAMA>_API_KEY`      | API key provider AI (aturan seragam: `CIORA_API_KEY`, `OPENAGENTIC_API_KEY`, …) |
| `MENU_LINK`            | Link kartu `.menu` (default: wa.me nomor bot)                              |
| `MENU_TITLE`           | Judul kartu `.menu` (default: nama + versi bot)                            |
| `MENU_DESC`            | Deskripsi kartu `.menu` (default: jumlah command)                          |
| `MENU_THUMB_URL`       | Gambar thumbnail kartu `.menu`                                             |
| `MENU_FAVICON_URL`     | Favicon kartu `.menu`                                                      |

Identitas bot & owner (nama, versi, deskripsi, nomor owner) **tidak** disimpan di `.env`, melainkan dikompilasi langsung ke binary lewat `internal/config/config.go` — ubah di sana lalu deploy ulang jika perlu mengganti nilainya.

> API key AI cukup diisi untuk provider yang mau dipakai. Pilihan provider/model aktif disimpan di SQLite (`data/ai.db` tabel `ai_settings`), bisa diganti lewat command `.agent provider` / `.agent model` tanpa deploy ulang.

## Fitur Utama

### Umum
| Command | Keterangan |
|---|---|
| `.ping` | Cek bot hidup + latensi (ms) |
| `.menu` / `.menu all` / `.menu <kategori>` | Daftar command berkartu link + sapaan mention pengirim (bold) |
| `.testthumb` | Demo developer untuk kartu link preview (bukan fitur user) |
| `.owner` | vCard kontak owner |
| `.autodownload on/off` | Download otomatis link medsos di chat ini, per chat |

### Media (downloader)
| Command | Keterangan |
|---|---|
| `.tiktok` / `.tt`, `.youtube` / `.yt`, `.instagram` / `.ig`, `.twitter` / `.x`, `.facebook` / `.fb`, `.pinterest` / `.pin`, `.spotify` / `.sp` + `<url>` | Download via scraper API per platform (YouTube primer yt-dlp; lainnya scraper-first + fallback) |
| `.douyin` / `.dy` + `<url/judul>` | Download TikTok gaya Baileys: foto satu-per-satu, video + caption, audio mp3 (tikwm → siputzx) |
| `.youtubemp3` / `.ytmp3`, `.youtube --mp3` | Audio saja |
| Perilaku umum | Carousel/foto multi-item digabung **1 bubble album** (tanpa batas jumlah); WebP statis→JPEG / animasi→MP4 otomatis; video asing di-transcode ke MP4/H.264/AAC agar bisa diputar; thumbnail mini agar preview tampil |
| Batas (keputusan owner) | Media ≤100MB kirim normal; video YouTube 100–200MB jadi **dokumen** (>200MB ditolak); durasi YouTube maks **35 menit**; antrean maks 2 bersamaan |

### AI Agent (Flora — owner + user terpercaya saja)
| Command | Keterangan |
|---|---|
| trigger natural | Sebut "flora/flo", reply bubble bot, stiker, atau media (tanya-jawab HANYA lewat sini, tidak via command) |
| `.agent model/provider/status` | Lihat/ganti model & provider, cek status (khusus owner) |
| `.agent trust/untrust @user` | Beri/cabut kepercayaan akses Flora (khusus owner; target via mention/reply/nomor) |
| `.agent trustlist` | Daftar user terpercaya sebagai mention (khusus owner) |
| `.agent reset` | Reset sesi percakapan sendiri (boleh trusted) |
| `.agent reset -memory [@user]` | Hapus ingatan jangka panjang + sesi, milik sendiri atau target (khusus owner) |
| `lock flo` / `unlock flora` | Kunci/buka trigger natural global (khusus owner) |

### Grup
| Command | Keterangan |
|---|---|
| `.kick` / `.promote` / `.demote` / `.add` / `.tagall` | Manajemen member (deteksi mention/reply/nomor, paham LID) |
| `.setname` / `.setdesc` / `.open` / `.close` / `.linkgc` | Pengaturan grup |
| `.antilink` / `.warn` / `.unwarn` / `.warnlist` | Moderasi link + peringatan (maks 3 = kick) |
| Welcome/bye | Sapaan + perpisahan member (template @user/@group) |

### Alat
| Command | Keterangan |
|---|---|
| `.sticker` / `.s` | Gambar/video (langsung atau reply) jadi stiker webp via ffmpeg |
| `.swm` | Ganti packname & author stiker |
| `.rvo` | Buka pesan sekali-lihat yang di-reply (foto/video/voice note) |
| `.get <url>` | Fetch URL (sidik TLS browser, anti-SSRF); >10MB jadi dokumen |
| `.delete` / `.del` | Hapus pesan yang di-reply |

### Dokumen
| Command | Keterangan |
|---|---|
| `.pdfmerge` | Gabung PDF via staging (mulai/kirim/gabung/daftar/nama/batal) |
| `.compress` / `.split` / `.jpgpdf` / `.watermark` / `.protect` / `.unlock` | Alat PDF lokal via pdfcpu (offline, maks 50MB) |

### Owner
| Command | Keterangan |
|---|---|
| `.server` / `.si`, `.os`, `.rss` | Info server, OS + jaringan, memori proses (khusus owner) |
| `.jadibot qr/list/stop/start/hapus` | Kelola bot clone via scan QR (stop=start ulang tanpa QR, hapus=total + arsip grup) |
| `.call <target>[\|<jam>]` | Panggilan WhatsApp keluar / terjadwal WITA (tersimpan di SQLite, tahan restart) |
| `.ban` / `.unban` / `.mode` / `.join` / `.leave` | Ban user & grup, mode self/public, keluar-masuk grup via link |
| `$ <perintah>` | Eksekusi shell di server (async, timeout 120 dtk, output dipotong) |
| `\ <kode js>` | Eval JavaScript di dalam bot (async, timeout 30 dtk) |

Panggilan WhatsApp memakai library [meowcaller](https://github.com/purpshell/meowcaller). Data grup (setting, dsb) disimpan di SQLite lewat driver `modernc.org/sqlite` (pure Go, tanpa cgo).

## Fitur AI Agent

Bot merespons pesan **natural** (tanpa prefix) lewat trigger berikut — **hanya untuk owner dan user terpercaya** (yang lain diabaikan diam-diam):

| Trigger | Keterangan |
|---|---|
| Kata kunci | Sebut nama bot: "flora", "flor", atau "flo" di dalam pesan |
| Reply ke bot | Membalas bubble bot (hanya jika sesi masih aktif, belum idle 10 menit) |
| Sticker-reply | Membalas pesan bot dengan stiker (sesi aktif) |
| Media-reply | Membalas pesan bot dengan gambar/video/audio/dokumen tanpa caption (sesi aktif) |

### Multi-Provider

Provider & model dikelola lewat command (khusus owner, default `ciora`):

| Provider | Endpoint | Default Model |
|---|---|---|
| `ciora` | ciora.id | `ciora-ai-free` |
| `openagentic` | aimurah.my.id (canonical docs; openagentic.id alias) | `claude-sonnet-4.5` |

> **Catatan tool-calling:** adapter mencoba mengirim field `tools`; kalau server menolak (400 soal tools), otomatis fallback sekali tanpa tools (mode chat teks). Kalau tool (kick/promote) tidak pernah jalan di provider ini, berarti server memang menolak tools.

Model bisa diganti per provider lewat `.agent model <nama>`; provider/model baru bisa ditambah (`provider add`, `model add`) atau dihapus (`provider del`, `model del`) langsung dari chat — daftar tersimpan di SQLite (`internal/store/aisettingsdb.go`), bukan hardcode. API key tiap provider dibaca dari `.env` (`<NAMA>_API_KEY`). Pengaturan provider/model **global** (berlaku untuk semua user).

### Kemampuan AI

- **Tool-calling**: AI bisa mengeksekusi `system_info`, `kick_member`, `promote_member`, `demote_member`, `set_group_name`, `tag_all`, `group_link`, `group_close`, `group_open`, `make_sticker` (butuh hak admin grup yang cukup), serta `make_call` & `bot_mode` (khusus owner) — hanya jika user eksplisit minta aksi.
- **Sesi percakapan**: histori per user, terpisah antara chat private dan tiap grup. Idle timeout 10 menit (sesi diringkas ≤1000 karakter, FIFO 4, sebagai konteks), persist ke SQLite (survive restart).
- **Ingatan jangka panjang**: fakta tentang user (nama, hobi, dll) diekstrak otomatis tiap 8 giliran dan disuntik ke system prompt lintas chat/grup. Dihapus total via `.agent reset -memory`.
- **Multi-bubble**: balasan panjang dipecah jadi beberapa bubble dengan delay ketik simulasi + status "mengetik...".
- **Mood detection**: deteksi mood user dari teks (sedih/stres/senang) menyesuaikan nada balasan.
- **Konteks reply**: jika user reply bubble bot, teks bubble tersebut disertakan sebagai konteks.
- **Vision**: gambar/stiker di pesan pemicu (lampiran langsung atau reply) diteruskan ke model sebagai `image_url` base64; Flora dituntun merespons isi yang dilihatnya, bukan mendeskripsikannya. Batas 4MB, histori tersimpan tetap teks.
- **Baca dokumen**: file di pesan pemicu (lampiran langsung atau reply) diekstrak teksnya dan ditempel ke pesan — PDF (`ledongthuc/pdf`), Word `.docx`, Excel `.xlsx` (`excelize`), dan teks polos (TXT/CSV/Markdown). Dokumen sampai 12000 karakter ditempel utuh; selebihnya dibaca bertahap (map-reduce: potongan 8000 karakter diringkas paralel lalu digabung, maks 8 bagian). Batas file 8MB; format lain (termasuk `.doc`/`.xls` lawas) ditolak dengan catatan jujur.
- **Reliability**: retry otomatis untuk error transient (timeout/429/5xx), notifikasi ke owner via WhatsApp jika gagal total.

> Pemilihan provider & model juga menentukan jalannya fact extractor — ekstraksi fakta ikut lewat provider yang sedang aktif.

## Catatan Penting

- **Jangan hapus folder `data/` maupun `wabot-tmp/`** di panel — di `data/` ada session login WhatsApp (`session.db`), database grup (`group.db`: setting, ban, trust Flora, warn, autodownload), dan database AI (`ai.db` berisi histori percakapan, fakta user, pengaturan provider); di `wabot-tmp/` ada file kerja sementara. Kalau `data/` dihapus, bot harus login ulang dari nol dan seluruh data di atas hilang.
- Nomor yang dipakai bot sebaiknya bukan nomor utama sehari-hari, untuk menghindari risiko banned mengganggu akun pribadi.
- hypermeow (fork whatsmeow) adalah library tidak resmi (reverse-engineered protokol WhatsApp Web) — pemakaian berlebihan/spam berisiko nomor terkena banned oleh WhatsApp.
- Pengecekan admin grup (`IsSenderAdmin`/`IsBotAdmin`) membandingkan JID lewat `.User`, nomor telepon, **dan** LID sekaligus untuk menghindari kegagalan akibat dual-format identitas WhatsApp (LID vs nomor telepon).
- Fitur AI bergantung pada API key di `.env` — jika provider aktif tapi key-nya kosong, chat AI akan gagal (bot tetap jalan untuk command lain). Error AI yang gagal total dilaporkan ke nomor owner via WhatsApp.
- Bot butuh **ffmpeg** di PATH server — untuk `.sticker`, thumbnail video, konversi WebP, dan transcode video asing. Tanpa ffmpeg, fitur-fitur itu membalas error.
