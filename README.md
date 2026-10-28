# Flora GO

Bot WhatsApp modular berbasis Go, memakai library [hypermeow](https://github.com/polymorfa/hypermeow) (fork whatsmeow). Dilengkapi downloader 7 platform (scraper-first + album media), command grup & owner lengkap, dan **AI agent Flora multi-provider** (Ciora / OpenAgentic, default Ciora — provider & model bisa ditambah/dihapus lewat command, khusus owner) dengan sistem trust, tool-calling, sesi percakapan, dan ingatan jangka panjang per user.

## Struktur Folder

```
wabot-go/
├── main.go                       # entry point: printBanner, InitCallClient, init DB (group/ai/facts/settings/finance/...), login QR/pairing/session lama
├── internal/
│   ├── config/
│   │   └── config.go              # OwnerName, OwnerNumber, BotName, BotVersion, API key AI + kartu menu (dari .env), system prompt
│   ├── handler/
│   │   └── handler.go             # router pesan masuk -> command / prefix spesial ($, \) / trigger AI, LogIncoming di sini
│   ├── logfmt/                    # helper log terformat ke console (header/level)
│   ├── timex/                     # zona waktu aktif bot (BOT_TIMEZONE, nama IANA) + FormatDuration
│   ├── netx/                      # helper jaringan (IsPublicIP anti-SSRF, dipakai external & plugins)
│   ├── textx/                     # helper teks (TruncateRunes, dipakai lintas paket)
│   ├── identity/                  # normalisasi identitas WhatsApp terpusat (JID/LID/PN: Normalize, ResolvePN/LID, Keys, CanonicalUser, IsBot)
│   ├── sqlxlsx/                   # ubah database SQLite -> workbook Excel (.xlsx), satu sheet per tabel
│   ├── finance/                   # helper periode bulanan + rakit laporan Excel keuangan (dipakai penjadwal)
│   ├── tableimage/                # render tabel teks -> gambar
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
│   │   ├── session_summary.go      # ringkasan sesi yang sudah dipadatkan
│   │   ├── trigger.go             # deteksi trigger: kata kunci "flora/flo", reply, sticker, media + lock/unlock
│   │   ├── lock.go                # perintah lock/unlock trigger natural (global, khusus owner)
│   │   ├── spam_gate.go           # anti-spam per sender + lock per sesi
│   │   ├── bubble.go              # split balasan jadi multi-bubble + delay ketik simulasi
│   │   ├── mood.go                # deteksi mood user dari teks -> modifier system prompt
│   │   ├── facts.go               # format & inject fakta user ke system prompt
│   │   ├── fact_extractor.go      # ekstraksi fakta tiap 8 giliran (fire-and-forget)
│   │   ├── reply_context.go       # sertakan teks bubble bot yang di-reply sebagai konteks
│   │   ├── ailog.go               # log request AI + eksekusi tool (mode penuh opsional)
│   │   └── tools/
│   │       ├── native/system.go   # tool system_info (uptime, versi, OS/arch)
│   │       ├── native/finance.go  # tool keuangan
│   │       └── bridge/            # group.go, group_tools.go, excel.go, finance.go,
│   │                              #   reminder.go, special_tools.go (kick/promote,
│   │                              #   tag, link, buka/tutup grup, stiker, call) — reuse validasi command
│   ├── messaging/                 # merakit + mengirim pesan kaya (dipakai plugins & tool bridge)
│   │   ├── linkcard.go            # kartu link preview + thumbnail mini inline + dukungan mention
│   │   ├── mediathumb.go          # thumbnail inline mini (~1-3KB, agar preview tampil, bukan abu-abu)
│   │   ├── videonorm.go           # validasi + normalisasi video (MP4/H.264/AAC, transcode bila perlu)
│   │   └── album.go               # pengiriman album media (association + induk counts)
│   ├── external/                  # adapter unduhan/API per platform (rantai fallback, tanpa yt-dlp)
│   │   ├── external.go            # tipe ScrapeItem/ScrapeResult, HTTP client API + unduhan (cap 100MB)
│   │   ├── tiktok.go / instagram.go / twitter.go / facebook.go / pinterest.go
│   │   ├── youtube.go / spotify.go / spotify_search.go
│   │   ├── lrclib.go / shazam.go  # lirik + kenali lagu
│   │   ├── flaticon.go / xpvid.go # ikon + video X
│   │   ├── football.go / pihps.go # klasemen + harga pangan (het)
│   │   └── htmlhelp.go            # parse HTML + helper URL
│   └── store/
│       ├── context.go             # helper reply (ReplyText, ReplyTextWithMentions, SendText) & MessageContext
│       ├── group_helpers.go       # IsSenderAdmin, IsBotAdmin, IsBotJID, ParticipantMatchesJID (LID vs PN)
│       ├── groupdb.go             # SQLite group.db: setting grup, ban, warn, trusted, autodownload, dst
│       ├── ownerdb.go             # ban/unban, mode self/public, lock AI, trust/untrust Flora
│       ├── aidb.go                # SQLite ai.db: histori percakapan AI (ai_history)
│       ├── aisettingsdb.go        # tabel ai_settings (provider/model aktif)
│       ├── factsdb.go             # tabel user_facts (fakta jangka panjang per sender + hapus)
│       ├── summariesdb.go         # ringkasan sesi AI
│       ├── absensidb.go           # absensi grup (live-location)
│       ├── crmdb.go               # arsip pesan CRM (.lastchat/.crm/.insp)
│       ├── financedb.go           # keuangan lokal (pengganti Google Sheet) + kolom waktu input + periode bulanan
│       ├── moderationdb.go        # antilink + warnlist per grup
│       ├── scheduledcalls.go      # jadwal panggilan (tahan restart)
│       ├── reminders.go           # pengingat (tahan restart)
│       ├── clones.go              # data bot clone (jadibot)
│       ├── tempdir.go             # BotTempDir (<workdir>/wabot-tmp, bukan /tmp yang 100MB di panel)
│       ├── autodownload.go        # saklar autodownload per chat
│       ├── bot_message_tracker.go # cache ID+teks pesan bot (untuk deteksi reply & konteks)
│       ├── quoted_context.go      # ambil teks bubble bot yang di-reply user
│       ├── message_unwrap.go      # buka bungkus Ephemeral/ViewOnce message
│       ├── trigger_extract.go     # ExtractStanzaID, IsReplyToBot, IsSticker/IsMedia, mentions
│       ├── sticker_exif.go        # tulis/baca EXIF webp (packname & author stiker)
│       └── log.go                 # LogIncoming & LogOutgoing (log semua pesan ke console)
├── plugins/                       # command bot (self-registration: tiap file punya init())
│   ├── registry.go                # Registry generik + slice registrars, RegisterAll jalankan semua otomatis
│   ├── menu.go / menucard.go      # .menu berkartu + .thumbnail/.favicon (aset kartu menu)
│   ├── ping.go / owner.go / delete.go / tourl.go
│   ├── downloader.go              # .tiktok/.youtube/.instagram/.twitter/.facebook/.pinterest/.spotify + album + WebP + batas ukuran/durasi
│   ├── bk.go / insthumb.go / flaticon.go
│   ├── sticker.go / stickerwatermark.go / smeme.go / brat.go / tomedia.go / setpp.go / tts.go
│   ├── lrclib.go / shazam.go / spotify_search.go / speedtest.go / het.go / klasemen.go / listgrup.go
│   ├── absensi.go / crm.go / tictactoe.go / selfcmd.go / replyfmt.go / pending.go
│   ├── groupmanage.go / groupsetting.go / moderation.go / welcome.go / swgc.go
│   ├── owneradmin.go / ownerexec.go / ownerget.go / ownerjoin.go / ownerrvo.go / jadibot.go / identity.go / toexcel.go
│   ├── sbpr.go / spr.go           # batch & private reply (owner)
│   ├── pdfmerge.go / pdflocal.go / docread.go
│   ├── ai.go / call_voice.go + call_video.go  # admin Flora + panggilan WA (jadwal)
│   ├── serverinfo.go (+serverinfo_sys_linux.go/_other.go) / osinfo.go / rssinfo.go
│   └── booktest.go / buttontest.go   # command uji (owner/hidden, bukan fitur publik)
├── data/                          # session WhatsApp & database SQLite (dibuat otomatis, JANGAN dihapus)
├── wabot-tmp/                     # temp kerja bot (dibuat otomatis, JANGAN dihapus, gitignored)
├── greeting.mp3                   # audio yang diputar saat panggilan (aset runtime, upload manual ke panel)
├── apps-script/arsip/             # arsip Google Apps Script lama (fitur Sheet pensiun, tidak dipakai)
├── run.sh                         # startup script untuk Pterodactyl
├── scripts/                       # tooling build/deploy (dijalankan dari laptop)
│   ├── deploy.ps1                 # build + rclone copyto ke panel
│   ├── version-bump.ps1           # tentukan naik versi otomatis dari file yang berubah (dipakai deploy.ps1)
│   ├── push.ps1                   # script git add/commit/push cepat
│   └── pair-lan.ps1               # pairing opencode via Wi-Fi (QR)
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
3. Jalankan `.\scripts\deploy.ps1` — script ini menentukan **versi otomatis** (`version-bump.ps1`: format CalVer `v<bulan>.<tanggal>.<urut>` = tanggal deploy lokal + urutan deploy hari itu, hanya naik bila ada file kode/tooling berubah sejak deploy terakhir; README/data saja = tetap; snapshot di `.last-deploy-state`), menulisnya ke `VERSION`, menyuntik ke binary via ldflags, lalu build + `rclone copyto` binary ke root panel.
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
| `AI_REQUEST_TIMEOUT`   | Batas tunggu satu request AI, format durasi Go (default `150s`)            |
| `MENU_LINK`            | Link kartu `.menu` (default: wa.me nomor bot)                              |
| `MENU_TITLE`           | Judul kartu `.menu` (default: nama + versi bot)                            |
| `MENU_DESC`            | Deskripsi kartu `.menu` (default: jumlah command)                          |
| `MENU_THUMB_URL`       | Gambar thumbnail kartu `.menu` (cadangan; `.thumbnail` menang)             |
| `MENU_FAVICON_URL`     | Favicon kartu `.menu`                                                      |
| `BOT_TIMEZONE`         | Zona waktu bot, nama IANA (mis. `Asia/Makassar`); alias `WIB`/`WITA`/`WIT` masih diterima, kosong = `Asia/Makassar`; ubah lalu restart |
| `ARENA_CHROME_PATH`    | Path Chrome untuk `.seedream` (default `/usr/bin/google-chrome`)           |
| `ARENA_HEADED`         | `auto` (default: pakai Xvfb kalau ada), `0` = paksa headless, `1` = headed |
| `ARENA_XVFB_PATH`      | Path binary Xvfb (default cari di PATH)                                    |
| `ARENA_READY_SECONDS`  | Batas menunggu halaman arena.ai siap (default `45` di mode tampilan, `30` headless) |
| `ARENA_HEADED_GPU`     | `1` = biarkan Chrome memakai jalur GPU di Xvfb (default software, `--disable-gpu`) |
| `ARENA_MODEL`          | Model gambar arena.ai (default `seedream-5.0-pro`; boleh nama atau UUID)   |
| `ARENA_EDIT_MODEL`     | Model untuk edit foto `.seedream` (default ikut `ARENA_MODEL`)             |
| `ARENA_CX`             | Field client-interaction arena.ai (default nilai bawaan; `-` = tak dikirim) |
| `ARENA_WARMUP_SECONDS` | Jeda halaman arena.ai sebelum minta token reCAPTCHA (default `6`)          |
| `ARENA_CHROME_IDLE_SECONDS` | Chrome dimatikan setelah sekian detik nganggur (default `150`; tidak berlaku selama permintaan berjalan) |
| `ARENA_MAX_DAILY`      | Kuota gambar `.seedream` per hari (default `20`)                           |
| `ARENA_COOLDOWN_SECONDS` | Jeda antar permintaan `.seedream` (default `30`)                         |
| `ARENA_TIMEOUT`        | Batas waktu satu proses gambar (default `240`, detik atau `4m`)            |
| `GEMINI_MODEL_TEXT`    | Model chat `.gemini`: `lite` (generasi paling singkat), `flash`, atau `pro`  |
| `GEMINI_THINKING`      | `1` = mode berpikir diperluas Gemini (lebih lambat, lebih teliti)          |
| `GEMINI_MAX_UPLOAD_MB` | Batas ukuran lampiran yang diunggah ke Gemini (default `20`)               |
| `GEMINI_CHROME_PATH`   | Path Chrome untuk mengunduh gambar `.gemini` (default cari di PATH/sistem) |
| `GEMINI_XVFB_PATH`     | Path binary Xvfb untuk Chrome headed saat mengunduh gambar (default PATH)  |
| `GEMINI_HEADED`        | `0` = paksa Chrome headless saat mengunduh gambar `.gemini`                |
| `GOMEMLIMIT`           | Batas memori Go (mis. `512MiB`) — disarankan di panel 1,6GB                |

Identitas bot & owner (nama, versi, deskripsi, nomor owner) **tidak** disimpan di `.env`, melainkan dikompilasi langsung ke binary lewat `internal/config/config.go` — ubah di sana lalu deploy ulang jika perlu mengganti nilainya.

> API key AI cukup diisi untuk provider yang mau dipakai. Pilihan provider/model aktif disimpan di SQLite (`data/ai.db` tabel `ai_settings`), bisa diganti lewat command `.agent provider` / `.agent model` tanpa deploy ulang.

## Fitur Utama

### Umum
| Command | Keterangan |
|---|---|
| `.ping` | Cek bot hidup + latensi (ms) |
| `.menu` / `.menu all` / `.menu <kategori>` | Daftar command berkartu link + sapaan mention pengirim (bold) |
| `.owner` | vCard kontak owner |
| `.autodownload on/off` | Download otomatis link medsos di chat ini, per chat |
| `.tourl` | Upload media (reply) jadi link |
| `.insthumb` `<url>` | Lihat metadata link |
| `.selfcmd on\|off` | Saklar eksekusi command dari akun bot sendiri (owner) |

### Media (downloader)
| Command | Keterangan |
|---|---|
| `.tiktok` / `.tt`, `.youtube` / `.yt`, `.instagram` / `.ig`, `.twitter` / `.x`, `.facebook` / `.fb`, `.pinterest` / `.pin`, `.spotify` / `.sp` + `<url>` | Download via scraper API per platform (YouTube primer yt-dlp; lainnya scraper-first + fallback) |
| `.bk` `<link> [d]` | Download video embed |
| `.gitclone` / `.gh` + `<owner/repo[@branch]>` | Download repo GitHub publik sebagai ZIP (maks 100MB) |
| `.youtubemp3` / `.ytmp3`, `.youtube --mp3` | Audio saja |
| `.shazam` + reply VN/audio | Kenali lagu (tombol **Unduh** bila ada track Spotify, selain itu **Buka Spotify**) |
| `.lrclib` / `.lrc`, `.lrclibs` / `.lrcs` + `<judul>` | Cari lirik plain / synced |
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
| `.kick` / `.promote` / `.demote` / `.add` / `.tagall` / `.hidetag` | Manajemen member & tag (deteksi mention/reply/nomor, paham LID) |
| `.setname` / `.setdesc` / `.open` / `.close` / `.linkgc` / `.setpp` | Pengaturan grup + ganti foto grup |
| `.antilink` / `.warn` / `.unwarn` / `.warnlist` | Moderasi link + peringatan (maks 3 = kick) |
| `.welcome` / `.bye` | Sapaan + perpisahan member (on/off/set/simulate) |
| `.swgc` `<teks>` / `.antistatus on\|off` | Kirim status grup + auto-hapus SWGC orang |
| `.absen mulai/berhenti/hasil/...` | Absensi live-location |
| Welcome/bye | Template @user/@group |

### Alat
| Command | Keterangan |
|---|---|
| `.sticker` / `.s` | Gambar/video (langsung atau reply) jadi stiker webp via ffmpeg |
| `.swm` | Ganti packname & author stiker |
| `.smeme atas \| bawah` | Stiker meme dari gambar (reply) |
| `.brat` | Stiker bergaya brat |
| `.toimg` / `.togif` / `.tomp3` | Stiker → foto / GIF bergerak / audio mp3 |
| `.tts <teks> [\| ardi]` | Ubah teks jadi voice note |
| `.speedtest` | Tes kecepatan internet panel (kartu hasil Ookla) |
| `.rvo` | Buka pesan sekali-lihat yang di-reply |
| `.get <url>` | Fetch URL (sidik TLS browser, anti-SSRF); >10MB jadi dokumen |
| `.delete` / `.del` | Hapus pesan yang di-reply |
| `.ttt` / `.tictactoe` | Tictactoe (`.ttt @user tantang`/`bot`/`5`/`stop`) — catatan: `.tt` milik TikTok |

### Internet / Info
| Command | Keterangan |
|---|---|
| `.het` | Harga pangan Sultra 7 hari |
| `.klasemen` / `.liga [liga]` | Klasemen liga top Eropa |
| `.jadwal [liga]` / `.hasil [liga]` | Jadwal pekan berjalan + hasil pekan selesai (onefootball) |
| `.flaticon <id/link>` | Link CDN ikon |

### Dokumen
| Command | Keterangan |
|---|---|
| `.pdfmerge` | Gabung s.d. 15 PDF via staging (mulai/kirim/gabung/daftar/nama/batal) |
| `.compress` / `.split` / `.jpgpdf` / `.watermark` / `.protect` / `.unlock` | Alat PDF lokal via pdfcpu (offline, maks 50MB) |

### Owner
| Command | Keterangan |
|---|---|
| `.server` / `.si`, `.os`, `.rss` | Info server, OS + jaringan, memori proses |
| `.jadibot qr/list/stop/start/hapus` | Kelola bot clone via scan QR (stop=start ulang tanpa QR, hapus=total + arsip grup) |
| `.voicecall <target>[\|<jam>] [#kw]` (alias `.call`) | Panggilan suara WhatsApp keluar / terjadwal (zona aktif bot, tersimpan di SQLite) |
| `.voicecall save\|del\|list` · `.voicecall jadwal` · `.voicecall cancel <id>` | Kelola aset suara (`#keyword`) & jadwal panggilan |
| `.videocall <target> [#kw]` (alias `.vcall`) | Video call WhatsApp keluar (sumber: reply video atau aset `#keyword`) |
| `.videocall save\|del\|list` | Kelola aset video (`#keyword`) |
| `.ban` / `.unban` / `.bangroup` / `.unbangroup` / `.banlist` / `.bangrouplist` | Ban user & grup |
| `.mode` / `.join` / `.leave` | Mode self/public, keluar-masuk grup via link |
| `.sbpr @a <pesan>` | Batch private relay 1 ID (mis. `.sbpr @a Halo {nama}! \| sapa`) |
| `.spr @target <pesan> [\| decoy]` | Kirim private reply grup per-target |
| `.thumbnail` / `.favicon <gambar\|link\|reset>` | Set aset kartu `.menu` (kirim/reply gambar = disimpan lokal, tanpa host; atau URL) |
| `.lastchat` / `.crm` / `.insp` / `.run` | Arsip pesan (cek metadata/JSON, kirim ulang) |
| `.backup [list\|get <nama>\|jadwal <interval\|off>]` | Backup database (ZIP semua DB / satu `.db` / jadwal otomatis) — hanya via DM owner |
| `.getexcel [nama.db]` | Ubah file DB jadi Excel (`.xlsx`): reply file `.db`/ZIP hasil backup, atau sebut nama DB — hanya via DM owner |
| `.id [nomor\|@jid]` | Diagnostik normalisasi identitas: JID mentah, alt, hasil resolve PN/LID, kunci kanonik (owner) |
| `.seedream <prompt>` (alias `.sd`) | Generate gambar via arena.ai model `seedream-5.0-pro` (owner; prompt bisa juga dari pesan yang di-reply) |
| `.seedream <prompt>` + foto | Edit foto via arena.ai: kirim/reply foto dengan perintah (owner; 1 foto referensi, hasil 1:1) — otomatis terdeteksi dari adanya gambar |
| `.seedream cookie <cookie>` / `.seedream cookie` | Perbarui cookie login arena.ai, atau (tanpa argumen) tampilkan status sesi: status, masa berlaku, sisa waktu, dan model — cookie tidak pernah dikembalikan ke chat |
| `.rch <link pesan> <emoji>` | Kirim reaksi ke pesan WhatsApp Channel lewat auto.reactionwa.online, maks 5 emoji (owner) |
| `.rch status` / `.rch login <refresh token>` | Cek sesi/akun, atau simpan refresh token Firebase sekali (tanpa argumen = petunjuk login) |
| `.gemini on` / `off` / `new` | Sesi chat Gemini web di DM owner: aktifkan, akhiri, atau mulai percakapan baru (riwayat disimpan di server Gemini) |
| `.gemini login <cookie>` / `.gemini status` / `.gemini diag` / `.gemini diag upload` / `.gemini diag image` | Simpan cookie akun Google bot (header `Cookie` dari gemini.google.com), cek status sesi, diagnosa cookie (TS basi vs sesi mati), uji jalur lampiran, atau uji generate + unduh gambar — cookie tidak pernah dikembalikan ke chat |
| `gemini <pertanyaan>` / reply bubble bot | Kirim prompt ke Gemini saat sesi aktif (juga menerima gambar/PDF sebagai lampiran) |
| `.listgrup` | Daftar grup bot + JID (untuk flag `-g`) |
| `$ <perintah>` | Eksekusi shell di server (async, timeout 120 dtk, output dipotong) |
| `\ <kode js>` | Eval JavaScript di dalam bot (async, timeout 30 dtk) |

Catatan `.seedream`: gambar dibuat lewat akun arena.ai milik owner memakai **Chrome di panel**, bukan solver captcha pihak ketiga. **Chrome headless ditolak reCAPTCHA** (`recaptcha validation failed`) walau UA/layar/WebGL sudah disamarkan, sedangkan Chrome biasa (non-headless) diterima — karena itu bot otomatis memakai **Xvfb** (layar virtual) bila binary `Xvfb` tersedia, dan jatuh ke headless kalau tidak. Di panel Debian: `apt-get install -y xvfb` (butuh root) atau pastikan `Xvfb` ada di PATH. Chrome hanya menyala saat dipakai dan mati sendiri setelah nganggur, karena panel 1,6GB/1 core — menjalankan Chrome terus-menerus akan memicu OOM. Sesi login arena.ai disimpan di `data/arena-cookie.txt` dan **diperbarui otomatis**: setiap kali generate/edit berhasil, bot membaca cookie aktif dari Chrome (`Storage.getCookies`, termasuk yang `HttpOnly`) lalu menulisnya kembali ke file, sehingga rotasi token oleh arena.ai tidak hilang. `.seedream cookie` manual baru perlu saat refresh token benar-benar mati (mingguan) atau setelah logout; `.seedream cookie` tanpa argumen menampilkan masa berlaku sesi. Kalau bot membalas "cookie sudah tidak berlaku", ambil cookie baru dari browser (DevTools → Network → request `create-evaluation` → header `Cookie`) lalu kirim `.seedream cookie <cookie>` tanpa deploy ulang. Uji cepat di panel: `ARENA_SMOKE=1 go test ./internal/arena/ -run TestSmokeBrowser -v` (cek Chrome + reCAPTCHA siap) atau `ARENA_SMOKE=3` (satu gambar sungguhan, memakai kuota akun). Untuk edit foto (`seedream` dengan gambar referensi), `ARENA_SMOKE=4` membuat satu gambar edit dari referensi buatan sendiri - cara termurah memastikan jalur image-to-image masih diterima arena.ai. Gambar hasil selalu dikirim sebagai **balasan (reply) ke perintah user**, didahului pesan tunggu singkat tanpa quote; caption hasil berbentuk `Generate With <model>` / `Edit With <model>` diikuti baris `> prompt` (tanpa menyebut arena.ai). Gambar referensi **wajib diunggah ke storage arena.ai (R2)**: server menolak URL lain (termasuk `data:`) dengan `400 Invalid content`, jadi bot memakai Server Action aplikasi (`generateUploadUrl` + `getSignedUrl`) yang id-nya dipanen runtime. Batas 4 MB per gambar, satu referensi.

Catatan keuangan (lewat AI): tiap transaksi menyimpan **waktu input** (tanggal + jam, berbeda dari tanggal transaksi). **Saat fitur baru aktif**, seluruh transaksi lama diarsipkan (dikirim ke DM owner) lalu buku dimulai ulang dari tanggal aktivasi dengan saldo awal = net transaksi lama. Bulan berjalan ditutup otomatis saat pergantian bulan — bot mengirim **laporan Excel bulanan** ke DM owner, mengarsipkannya di `data/arsip-keuangan/<tahun>/`, lalu mengosongkan transaksi bulan itu dan membawa saldo akhir (kas & rekening) sebagai saldo awal bulan berikutnya.

Catatan `.rch`: memakai situs **auto.reactionwa.online** (endpoint `POST https://api-v1.reactionwa.online`), bukan apikey. Login Google dilakukan **sekali** oleh owner di browser pribadinya; bot hanya menyimpan **refresh token** Firebase di `data/rwa-token.txt` (gitignored, ditulis atomik, tidak pernah di-echo) lewat `.rch login <refresh token>` (ambil dari DevTools > Console halaman `/Free/`: IndexedDB `firebaseLocalStorageDb` > `firebaseLocalStorage` > `stsTokenManager.refreshToken`). Access token dicetak bot sendiri dari `securetoken.googleapis.com` (wajib header `Origin`/`Referer` situs), lalu cache ±55 menit. Token Cloudflare Turnstile **wajib** dan diverifikasi server, jadi setiap kirim bot menyalakan **Chrome + Xvfb** di panel untuk membuka `/Free/` (tanpa login) dan memanen token dari `turnstile.getResponse()`; token itu dipakai ulang sampai ±4 menit supaya kiriman beruntun tidak menyalakan Chrome lagi. Link harus link **pesan** channel (ada nomor di belakang), bukan link profil. Maksimal 5 emoji per kirim (dipisah spasi/koma; emoji ZWJ tidak dipecah). Kuota gratis dibatasi **server** (1×/24 jam per akun/IP); kalau habis, bot menampilkan pesan limit dari server apa adanya (top up/upgrade lewat situs). Chrome/Xvfb memakai profil `rwa-profile/` (gitignored) agar cookie clearance Cloudflare bertahan, dan tidak dinyalakan bila sisa memori cgroup <350 MB.

Catatan `.gemini`: chat Gemini lewat **antarmuka web** (bukan API resmi, bukan Chrome), memakai akun Google khusus bot. Pesan prompt ditandai **reaksi ✨** (berubah menjadi **⏳** bila jawaban belum selesai setelah ±10 detik; tidak ada bubble "sedang meneruskan…"), jawaban dirapikan dulu seperti balasan flora agent (bold Markdown → bold WhatsApp, heading/pemisah/karakter tak terlihat dibuang), dan jawaban panjang dipotong per 3500 karakter. Pembacaan balasan berhenti di penanda akhir stream (atau setelah diam 4 detik bila jawaban sudah ada), bukan menunggu koneksi ditutup — server Gemini kadang menahan koneksi ±20 detik setelah jawaban lengkap, dan itulah yang dulu membuat `.gemini` terasa lambat. Kredensial satu-satunya adalah cookie sesi di `data/gemini-cookie.txt` (gitignored, ditulis atomik 0600, tidak pernah di-echo ke chat) yang diisi lewat `.gemini login <cookie>` — ambil dari DevTools → Network → request `gemini.google.com/app` → header `Cookie`, cukup `__Secure-1PSID` (cookie pendamping ikut kalau ada). Setelah login, bot menyimpan **tembolok token halaman** (`at`/build/f.sid/push id) di `data/gemini-meta.json` (0600, umur pakai 1 jam, dihapus saat cookie identitas berganti), jadi restart tidak perlu membuka `/app` lagi dan tidak menuntut login ulang. Kalau `/app` tetap harus dibuka dan menolak, bot berlapis: rotasi cookie → coba lagi → coba lagi **tanpa cookie volatile** (`__Secure-1PSIDTS` basi kadang membuat permintaan dianggap tidak sah) → baru melaporkan cookie ditolak. `.gemini status` menampilkan keadaan berkas, dan `.gemini diag` menunjukkan apakah masalahnya cookie volatile (bisa diperbaiki bot) atau `__Secure-1PSID` yang sudah tidak berlaku (harus login ulang). Gambar hasil Gemini diunduh memakai sesi bot dengan beberapa percobaan berurutan (mirip permintaan browser: cookie, UA, `Sec-Fetch-*`, tanpa `Origin`; lalu tanpa cookie, tanpa parameter ukuran, dan `=s0`); kalau semuanya ditolak 403, bot menyalakan Chrome di panel sebentar untuk membuka URL yang sama dan mengambil gambar dari halaman yang berhasil dimuat (cookie tidak disuntikkan ke host media, profil `gemini-profile/`), lalu mematikan Chrome lagi — rung yang berhasil dicatat di log (`media ok via …`). Kalau semua gambar gagal diunduh, bot melaporkan alasan ringkasnya (mis. `403 · lh3.googleusercontent.com`), bukan mengirim pesan kosong. `.gemini diag image` meminta satu gambar, memindai semua URL media di payload (kandidat kanonik ditaruh paling depan) lalu menampilkan bentuk tiap kandidat + status tiap percobaan unduh (termasuk rung Chrome untuk kandidat pertama) + kesimpulan (masalah generasi, penolakan 403, atau jalur sehat); biayanya satu kuota generate. Semua kandidat URL tidak ditampilkan di chat, tapi ditulis ke `wabot-tmp/gemini-image-url-*.txt` agar bisa diuji di HP: berhasil di incognito = URL publik (berarti IP/TLS panel yang diblokir), hanya berhasil saat login = gambar butuh cookie `googleusercontent.com`, gagal di keduanya = URL rusak/kedaluwarsa. URL dari stream dibersihkan dulu (trim, `&amp;` → `&`, potong pada spasi/kuota/tanda penutup markup) karena satu karakter sisa markup sudah cukup membuat tanda tangan Google ditolak 403. Untuk balasan ber-lampiran yang bermasalah, `.gemini diag upload` menguji unggah + satu giliran chat dan menunjukkan tahap mana yang gagal (berkas dump mentah di `wabot-tmp/gemini-empty-*.txt` bisa diperiksa lalu dihapus). **Riwayat percakapan disimpan di server Gemini**, bukan di bot: yang ada di memori hanya pointer percakapan (`conv_id`/`resp_id`/`choice_id`), sehingga mode kembali nonaktif setelah bot restart dan `.gemini new` cukup membuang pointer. Pesan sesi ini sengaja **tidak masuk arsip CRM** (tidak ada transkrip lokal). Model: `GEMINI_MODEL_TEXT` (`lite` = generasi paling singkat, `flash`/`pro` untuk kualitas lebih tinggi), `GEMINI_THINKING=1` untuk mode berpikir. Catatan penting di panel: setelah perbaikan pembacaan stream, sisa latensi datang dari **server Gemini** (antrean 8–20 detik sebelum header balasan ditambah ±12 detik inferensi senyap), bukan dari bot — generasi aslinya hanya 2–5 detik, jadi mengecilkan model tidak lagi banyak menolong dan tidak ada setting env untuk memangkasnya. Uji tanpa WhatsApp: `GEMINI_SMOKE=1 go test ./internal/geminiapi/ -run TestSmoke -v` (teks), `=2` (unggah gambar), `=3` (minta gambar), `=4` (tiga giliran) — memakai cookie dan kuota akun asli.

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

## Environment tambahan (opsional, optimasi)

Variabel berikut opsional; kosong berarti pakai default.

- `PPROF_ADDR` (default kosong): nyalakan server pprof (hanya localhost) untuk profiling CPU/mem/goroutine. Contoh `127.0.0.1:6060`.
- `METRICS_LOG_INTERVAL` (default `5m`): interval log metrik ringan; `off`/`0` mematikan.
- `HEAVY_MAX_PAR` (default `1`, 1–4): jumlah transcode ffmpeg paralel.
- `HEAVY_QUEUE_TIMEOUT` (default `30s`): lama antre slot transcode.
- `DL_MAX_PAR` (default `2`, 1–8): jumlah download paralel.
- `EXTERNAL_RATE_RPS` (default `10`, cap 50): rate limit request API eksternal per engine.
