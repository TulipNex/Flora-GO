# Struktur folder wabot-go

Peta ini menjelaskan **fungsi tiap folder** dan **di mana file baru seharusnya ditaruh**.
Prinsip: satu folder = satu tanggung jawab.

```
wabot-go/
├── main.go                 # entry point (wiring env, client, handler, side-effect import)
├── internal/               # seluruh kode inti (tidak diimpor dari luar modul)
│   ├── config/             # identitas & konfigurasi bot (compile-time)
│   ├── handler/            # router pesan masuk -> command/event
│   ├── logfmt/             # format log terstruktur
│   ├── messaging/          # merakit & mengirim pesan kaya
│   ├── store/              # akses DB SQLite + state/konteks bot
│   ├── external/           # adapter sumber luar (media & data non-media)
│   ├── netx/               # helper jaringan kecil (IsPublicIP, dll)
│   ├── textx/              # helper teks kecil (TruncateRunes, dll)
│   ├── timex/              # helper waktu kecil (FormatDuration, Zone)
│   ├── tableimage/         # render tabel -> gambar (+ font/logo)
│   └── ai/                 # mesin AI
│       └── tools/
│           ├── native/     # tool AI murni (tanpa akses WhatsApp)
│           └── bridge/     # tool AI yang butuh WhatsApp client
├── plugins/                # command bot (1 package datar, self-registration)
├── scripts/                # tooling build/deploy (dijalankan dari laptop)
├── apps-script/arsip/      # arsip Google Apps Script lama (pensiun)
├── third_party/hypermeow/  # fork whatsmeow (submodule, ter-patch)
├── data/                   # runtime: session + DB SQLite (gitignored, JANGAN dihapus)
├── wabot-tmp/              # temp kerja bot (gitignored, JANGAN dihapus)
└── run.sh                  # startup script Pterodactyl
```

## Aturan penempatan file baru

| Mau menambah… | Taruh di | Catatan |
|---|---|---|
| Command bot | `plugins/<nama>.go` | `init() → registerPlugin`; jangan edit `registry.go`. Lihat `plugins/README.md` |
| Tool AI murni | `internal/ai/tools/native/` | Daftar via `ai.RegisterTool` di `init()` |
| Tool AI ber-akses WhatsApp | `internal/ai/tools/bridge/` | + **side-effect import di `main.go`**, kalau tidak tool tidak terdaftar |
| Helper kecil lintas paket | `internal/<bidang>x/` (`netx`, `textx`, `timex`, …) | Buat paket leaf, tanpa import balik ke paket besar |
| Akses DB / state baru | `internal/store/` | Ikuti pola `*db` atau helper konteks yang ada |
| Adapter API luar | `internal/external/` | Namespace "adapter sumber luar" (tak semua media) |
| Skrip build/deploy lokal | `scripts/` | Jangan taruh skrip runtime panel di sini |

## Yang **tidak** boleh dipisah ke subfolder

- `plugins/` harus tetap **satu package datar**. Memindah file ke subfolder membuat
  package baru sehingga `init()`/`registerPlugin` tidak lagi jalan.
- `internal/store` diakses sebagai `store.*` di ratusan titik; **jangan** pecah paketnya.
- `run.sh` & `greeting.mp3` tetap di root (kebutuhan panel/runtime).

## Referensi

- Peta command: `plugins/README.md`
- Aturan kontribusi & deploy: `AGENTS.md`
