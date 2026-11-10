# Diagnostik performa

## Riwayat memori (`.memlog`, default aktif)

`data/memlog.db` diisi sampler bawaan: satu sampel per menit (heap, RSS,
cgroup, jumlah objek, RSS proses anak seperti Chrome/Xvfb/ffmpeg), turun ke
tiap 5 detik selama ada operasi berat berjalan (dipicu langsung oleh operasi,
jadi operasi pendek pun terpotret sebelum & sesudahnya), plus **satu baris per
operasi berat** (ffmpeg, unduhan media, upload media, giliran AI,
arena.ai/Chrome, Gemini, reactionwa) berisi `alloc` (byte yang dialokasikan,
kebal GC), delta heap/RSS, dan durasi.

Alur membacanya di HP (owner, DM maupun grup):

| Command | Isi |
|---|---|
| `.memlog` | Ringkasan 24 jam: RSS min–maks, puncak heap/objek, verdict tren, top alokasi, spike terbesar |
| `.memlog now` | Potret instan: heap sys/idle/released, cgroup, proses anak, antrean tulis DB |
| `.memlog day` | Agregat per jam (24 baris) — enak untuk melihat jam berapa memori naik |
| `.memlog raw 06` / `.memlog raw 06 12` | Sampel mentah per menit pada satu jam / satu menit |
| `.memlog top [jam]` | Operasi paling boros alokasi (total & rata-rata per operasi) |
| `.memlog spike [n]` | Lonjakan terbesar (heap +8MB, RSS +60MB, atau anak +50MB antar sampel) |
| `.memlog ops` | Operasi yang sedang berjalan sekarang + antrean tulis |
| `.memlog export [24h\|7d]` | ZIP `samples.csv` + `events.csv` untuk Excel |

Aturannya: `alloc` = penyebab, `heap_d`/`rss_d` = dampak tertahan (bisa
negatif bila GC jalan di tengah; baris ditandai `~gc`). `heap_released` yang
jauh di bawah `heap_idle` bukan kebocoran — itu page yang ditahan runtime
supaya tidak di-`madvise` ulang. Kalau `heap_alloc` kecil sementara RSS besar,
selisihnya ada di proses anak (`.memlog now` menampilkan `child_top`).

Env: `MEMLOG_SAMPLE_SECONDS` (default `60`, min `15`, `0` = mati),
`MEMLOG_BURST_SECONDS` (default `5`), `MEMLOG_RETENTION_DAYS` (default `7`,
maks `90`). Pengingat tren dikirim ke DM owner maksimal sekali per 24 jam bila
median heap naik >25% **dan** >15MB, atau jumlah objek naik >20% **dan** >2.000
dibanding 24 jam sebelumnya.

## Media besar: spool disk + upload streaming

Dulu satu `.bk` ~500MB ditampung utuh di RAM (`io.ReadAll` → TotalAlloc
~1,18GB), lalu upload membuat ciphertext + salinan MAC (~+1GB). Puncak RSS
1,65GB di panel 1,6GB = OOM kill. Sekarang alurnya dibatasi:

- hasil unduhan ≤32MB tetap di RAM (jalur cepat, tanpa I/O tambahan);
- di atas 32MB dialirkan ke `wabot-tmp/wabot-spool-*.bin` dan dibaca bertahap;
- upload dokumen memakai `UploadReader` yang mengenkripsi **di file yang sama**
  (plaintext ditimpa ciphertext), jadi tidak ada salinan kedua di RAM/disk;
- `Content-Length` diperiksa sebelum body dibaca, dan buffer dialokasikan persis
  sebesar ukuran yang dijanjikan server.

Artinya memori untuk berkas besar kini datar (buffer 32KB), bukan mengikuti
ukuran berkas. Yang masih perlu `[]byte` utuh hanya media ≤100MB yang dikirim
sebagai video (butuh normalisasi/transcode), dengan puncak ~2–3× ukuran berkas.

Cara memeriksa di `.memlog`:

- `upload/document-file` = jalur streaming (alloc harus kecil, puluhan KB);
- `upload/document` / `upload/video` = jalur RAM lama (alloc ± ukuran berkas);
- kalau `upload/document-file` menunjukkan alloc besar, ada kode yang menyalin
  berkas besar ke memori lagi — itu regresi, bukan perilaku normal.

## pprof dari HP (tanpa buka port)

`.memlog heap` mengirim `heap-<stamp>.pb.gz`, `.memlog goroutine` mengirim
dump teks. Simpan lampirannya di laptop, lalu:

```powershell
go tool pprof -sample_index=inuse_space heap-20261103-101500.pb.gz
# di prompt pprof:
top 20
list NamaFungsi
```

`inuse_space` = memori yang masih hidup (retensi); `alloc_space` = total
alokasi (churn). Untuk goroutine, cari stack yang menumpuk di jumlah besar.

## pprof via port (opsional, default mati)

Aktifkan dengan env `PPROF_ADDR` (hanya localhost):

```powershell
$env:PPROF_ADDR="127.0.0.1:6060"
go run .
```

Lalu buka profil:

```powershell
go tool pprof http://127.0.0.1:6060/debug/pprof/profile?seconds=30
go tool pprof http://127.0.0.1:6060/debug/pprof/heap
go tool pprof http://127.0.0.1:6060/debug/pprof/goroutine
```

Keamanan: host selain localhost/127.0.0.1/::1 dipaksa ke 127.0.0.1. Kosongkan env untuk mematikan.

## Benchmark

```powershell
go test -bench=. -benchmem ./internal/identity/ ./internal/textx/
```

## Verifikasi dasar

```powershell
go build ./...
go vet ./...
go test ./...
```

Catatan: race detector (`go test -race`) membutuhkan cgo (gcc) yang tidak tersedia di panel Windows ini; jalankan di environment dengan gcc bila perlu.
