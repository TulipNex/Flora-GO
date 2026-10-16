# Peta file `plugins/`

Semua file di folder ini **satu package Go (`plugins`)** dan mendaftar sendiri lewat
`init()` → `registerPlugin(...)` (lihat `registry.go`). Karena itu file **tidak**
dipisah ke subfolder — subfolder = package berbeda, dan self-registration akan putus.

Dokumen ini index navigasi saja. Menambah command: buat file baru di folder ini
(tidak perlu menyentuh `registry.go`). Kategori **wajib** memakai konstanta
`CategoryXxx` di `registry.go`.

Legenda: `•` = command publik, `◇` = helper/modul pendukung (bukan command),
`⛭` = khusus owner/admin, `✓` = command uji (owner/hidden).

---

## Inti, menu & helper bersama

| File | Isi | Peran |
|---|---|---|
| `registry.go` | — | Pusat registrasi command + konstanta `CategoryXxx` (**jangan diedit manual**) |
| `menu.go` | `• .menu` | Daftar command otomatis dari registry |
| `menucard.go` | `• .thumbnail`, `• .favicon` | Atur tampilan (thumbnail/favicon) menu |
| `replyfmt.go` | `◇` | Helper format balasan: `Title`, `Row`, `Block`, `ErrMsg`, `ErrNotGroup`, `ErrAdminCheck`, … |
| `pending.go` | `◇` | Helper antrian pilih-nomor generik (`pendingStore[T]`, `parsePlainPickIndex`) |
| `selfcmd.go` | `⛭ .selfcmd` | Saklar eksekusi command dari akun bot sendiri |
| `gameinput.go` | `◇` | `CheckGameInput` — routing input pemain game |

## Umum

| File | Isi |
|---|---|
| `ping.go` | `• .ping` |
| `tourl.go` | `• .tourl` |
| `tts.go` | `• .tts` |
| `delete.go` | `• .delete` / `• .del` |
| `speedtest.go` | `• .speedtest` |
| `rssinfo.go` | `• .rss` |

## Media & unduh

| File | Isi | Catatan |
|---|---|---|
| `downloader.go` (+ `downloader_ytdlp.go`, `downloader_send.go`) | `• .tiktok/.tt`, `• .youtube/.yt`, `• .youtubemp3/.ytmp3`, `• .instagram/.ig`, `• .twitter/.x/.tw`, `• .facebook/.fb`, `• .pinterest/.pin`, `• .spotify/.sp`, `• .autodownload` | Engine unduh multi-platform. `.tt` = tiktok (regression-tested). `_ytdlp` = jalur yt-dlp; `_send` = pengirim media (video/audio/gambar/webp) |
| `spotify_search.go` | `◇` | Pencarian Spotify + pending pick (dipakai `downloader`, `shazam`, `lrclib`) |
| `douyin.go` | `• .douyin` | |
| `bk.go` | `• .bk` / `• .po` | Unduh video embed |
| `insthumb.go` | `• .insthumb` | Thumbnail Instagram |
| `flaticon.go` | `• .flaticon` | Cari ikon |
| `lrclib.go` | `• .lrclib`, `• .lrclibs` | Lirik |
| `shazam.go` | `• .shazam` | Kenali lagu |
| `het.go` | `• .het` | Harga pangan (PIHPS) |
| `klasemen.go` | `• .klasemen` | Klasemen liga |
| `listgrup.go` | `• .listgrup` | |

## Stiker & gambar

| File | Isi |
|---|---|
| `sticker.go` | `• .sticker` |
| `stickerwatermark.go` | `• .stickerwatermark` |
| `smeme.go` | `• .smeme` |
| `brat.go` | `• .brat` / `• .br`, `• .bratvideo` |
| `setpp.go` | `• .setpp` |
| `tomedia.go` | `• .toimg`, `• .togif`, `• .tomp3` |

## AI

| File | Isi | Catatan |
|---|---|---|
| `ai.go` (+ `ai_trust.go`, `ai_runtime.go`) | `• .agent` | Sesi AI (provider/model runtime di SQLite). `_trust` = subcommand trust; `_runtime` = mesin percakapan (HandleAIMessage, bubble, suara) |
| `call.go` | `• .call` | Panggilan WhatsApp (jadwal) |
| `docread.go` | `◇` | Ekstrak teks PDF/DOCX/XLSX/plain + ringkas untuk AI; `sanitizeFileName` dipakai PDF |

## Grup

| File | Isi |
|---|---|
| `groupmanage.go` | `• .kick`, `• .promote`, `• .demote`, `• .tagall`, `• .hidetag`, `• .add` |
| `groupsetting.go` | `• .setname`, `• .setdesc`, `• .open`, `• .close`, `• .linkgc` |
| `moderation.go` | `• .antilink`, `• .warn`, `• .unwarn`, `• .warnlist` |
| `welcome.go` | `• .welcome`, `• .bye` |
| `swgc.go` | `• .swgc`, `• .antistatus` |

## Absensi & CRM

| File | Isi |
|---|---|
| `absensi.go` (+ `absensi_render.go`, `absensi_setup.go`) | `• .absen` | `_render` = parser hari/jam & format; `_setup` = wizard setup + ekspor xlsx |
| `crm.go` | `• .crm`, `• .insp`, `• .lastchat`, `• .run` |

## Dokumen & PDF

| File | Isi |
|---|---|
| `pdfmerge.go` | `• .pdfmerge` |
| `pdflocal.go` | `• .compress`, `• .split`, `• .jpgpdf`, `• .watermark`, `• .protect`, `• .unlock`, `• .foto.jpg` |

## Owner / admin

| File | Isi |
|---|---|
| `owner.go` | `• .owner` |
| `owneradmin.go` | `⛭ .ban`, `.unban`, `.banlist`, `.bangroup`, `.unbangroup`, `.bangrouplist`, `.mode` |
| `ownerexec.go` | `⛭ .$`, `.\\` (eksekusi perintah shell) |
| `ownerget.go` | `⛭ .get` |
| `ownerjoin.go` | `⛭ .join`, `⛭ .leave` |
| `ownerrvo.go` | `⛭ .rvo` |
| `jadibot.go` | `⛭ .jadibot` |
| `spr.go` (+ `spr_args.go`) | `⛭ .spr` — private reply grup per-target | `_args` = parsing argumen & helper target (dipakai sbpr juga) |
| `sbpr.go` | `⛭ .sbpr`, `.sbprprofile` — batch private relay |

## Info server

| File | Isi | Catatan |
|---|---|---|
| `serverinfo.go` | `• .server` | |
| `serverinfo_sys_linux.go` / `serverinfo_sys_other.go` | `◇` | Twin build-tag (`sysMemInfo`/`kernelRelease`) — **bukan** duplikasi |
| `osinfo.go` | `• .os` | |

## Game

| File | Isi | Catatan |
|---|---|---|
| `tictactoe.go` | `• .tictactoe` (`.ttt` / `.tc`) | `.tt` **milik tiktok**, bukan tictactoe |

## Command uji (owner/hidden — bukan fitur publik)

| File | Isi |
|---|---|
| `dev_booktest.go` | `✓ .booktest` |
| `dev_qtest.go` | `✓ .qtest` |
| `dev_btntest.go` | `✓ .btntest` (Hidden) |

## Berkas uji (Go test)

`pdflocal_test.go`, `pending_test.go`, `registry_test.go`, `sbpr_test.go`, `spr_test.go`
— unit test package. `registry_test.go` termasuk `TestNoCommandAliasCollision`
(penjaga tabrakan alias): `go test ./plugins/`.
