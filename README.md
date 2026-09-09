# Flora GO 

Bot WhatsApp modular berbasis Go, memakai library [whatsmeow](https://github.com/tulir/whatsmeow).

## Struktur Folder

```
wabot-go/
├── main.go                       # entry point: printBanner, InitCallClient, login QR/pairing/session lama
├── internal/
│   ├── config/
│   │   └── config.go              # OwnerName, OwnerNumber, BotName, BotVersion, BotDescription
│   ├── handler/
│   │   └── handler.go             # router pesan masuk -> command, LogIncoming dipanggil di sini
│   └── store/
│       ├── context.go             # helper reply (ReplyText, dsb) & MessageContext
│       ├── group_helpers.go       # helper bersama (mis. participantMatchesJID untuk cek admin)
│       ├── groupdb.go             # lapisan SQLite untuk data grup (modernc.org/sqlite)
│       └── log.go                 # LogIncoming & LogOutgoing (log semua pesan ke console)
├── plugins/
│   ├── registry.go                # Registry generik + slice registrars, RegisterAll jalankan semua otomatis
│   ├── ping.go                    # .ping (dengan hitung latensi ms)
│   ├── menu.go                    # .menu (daftar command otomatis + kata sambutan bot)
│   ├── info.go                    # .info (uptime, versi Go, OS/arch, versi whatsmeow, dsb)
│   ├── owner.go                   # .owner (kirim vCard owner)
│   ├── sticker.go                 # .sticker (gambar/video -> stiker webp via ffmpeg)
│   ├── call.go                    # .call <nomor> dan .call <nomor>|<jam> (panggilan + penjadwalan WITA)
│   ├── groupmanage.go             # kick, promote, demote, tagall, .add <nomor...>
│   └── groupsetting.go            # setname, setdesc, open/close, linkgc (anti-link)
├── data/                          # session WhatsApp (dibuat otomatis, JANGAN dihapus)
├── greeting.mp3                   # audio yang diputar saat panggilan (aset runtime, upload manual ke panel)
├── run.sh                         # startup script untuk Pterodactyl (source .env di awal)
├── deploy.ps1                     # script build + rclone copyto ke panel (dijalankan dari laptop)
├── go.mod / go.sum
├── .env.example
└── .gitignore
```

> Catatan: folder `internal/` di atas adalah source of truth yang dikembangkan di laptop. Di panel Pterodactyl, source `.go` **tidak** disimpan — panel hanya menyimpan binary hasil compile (`wabot-go-linux`) plus aset runtime (`.env`, `data/`, `greeting.mp3`). Lihat bagian [Workflow Deploy](#4-workflow-deploy-laptop--panel).

## 1. Setup di Laptop

Pastikan sudah install [Go 1.22+](https://go.dev/dl/).

```bash
cd wabot-go
go mod tidy      # download semua dependency, akan mengoreksi versi di go.mod otomatis
go run .         # jalankan langsung untuk testing
```

> Jika muncul error verifikasi checksum ke `sum.golang.org` (biasanya karena DNS/jaringan), jalankan:
> ```bash
> go env -w GOSUMDB=off
> ```

Saat pertama kali jalan (atau kalau `LOGIN_METHOD` tidak diset), kamu akan ditanya mau login pakai **QR Code** atau **Pairing Code**. Di panel, metode ini dikendalikan penuh lewat `.env` (`LOGIN_METHOD`, `PHONE_NUMBER`) supaya tidak perlu input interaktif di console:

- **QR Code**: scan teks QR yang muncul di terminal pakai fitur "Tautkan Perangkat" di WhatsApp.
- **Pairing Code**: set `LOGIN_METHOD=pairing` dan `PHONE_NUMBER=62812xxxxxxx` di `.env`, lalu masukkan kode 8 digit yang muncul ke WhatsApp > Perangkat Tertaut > Tautkan dengan Nomor Telepon.

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

| Variable       | Keterangan                                                   |
|----------------|---------------------------------------------------------------|
| `LOGIN_METHOD` | `qr` atau `pairing`                                            |
| `PHONE_NUMBER` | Nomor HP bot untuk pairing (format `62812xxxxxxx`)             |

Identitas bot & owner (nama, versi, deskripsi, nomor owner) **tidak** disimpan di `.env`, melainkan dikompilasi langsung ke binary lewat `internal/config/config.go` — ubah di sana lalu deploy ulang jika perlu mengganti nilainya.

## Fitur Utama

| Command | Keterangan |
|---|---|
| `.ping` | Cek bot hidup + latensi (ms) |
| `.menu` | Daftar semua command otomatis |
| `.info` | Uptime, versi Go, OS/arch, versi whatsmeow, info bot |
| `.owner` | Kirim vCard kontak owner |
| `.sticker` | Konversi gambar/video (langsung atau reply) jadi stiker webp, termasuk animasi |
| `.call <nomor>` | Melakukan panggilan WhatsApp keluar |
| `.call <nomor>\|<jam>` | Menjadwalkan panggilan pada jam tertentu (format `HH.MM`, zona WITA); jadwal in-memory, hilang jika bot restart sebelum waktunya |
| `.add <nomor...>` | Menambahkan member ke grup langsung lewat nomor telepon (bisa banyak sekaligus) |
| `.kick` / `.promote` / `.demote` / `.tagall` | Manajemen dasar grup |
| `.setname` / `.setdesc` / `.open` / `.close` / `.linkgc` | Pengaturan grup |

Panggilan WhatsApp memakai library [meowcaller](https://github.com/purpshell/meowcaller). Data grup (setting, dsb) disimpan di SQLite lewat driver `modernc.org/sqlite` (pure Go, tanpa cgo).

## Catatan Penting

- **Jangan hapus folder `data/`** di panel — di situ session login WhatsApp & database SQLite grup disimpan. Kalau dihapus, bot harus login ulang dari nol dan data grup hilang.
- Nomor yang dipakai bot sebaiknya bukan nomor utama sehari-hari, untuk menghindari risiko banned mengganggu akun pribadi.
- whatsmeow adalah library tidak resmi (reverse-engineered protokol WhatsApp Web) — pemakaian berlebihan/spam berisiko nomor terkena banned oleh WhatsApp.
- Pengecekan admin grup (`IsSenderAdmin`/`IsBotAdmin`) membandingkan JID lewat `.User`, nomor telepon, **dan** LID sekaligus untuk menghindari kegagalan akibat dual-format identitas WhatsApp (LID vs nomor telepon).