# Rencana Fitur Jadibot Flora GO

**Model (keputusan owner):** gratis bukan sewa, pairing butuh approval owner,
command clone dibatasi (whitelist), <10 instance, satu proses.

## 1. Arsitektur

- Registry `map[token]*botInstance` di satu binary; tiap instance =
  `*whatsmeow.Client` sendiri (satu `sqlstore` container bisa pegang banyak
  device — `container.NewDevice()` per instance baru).
- Tanpa load balancer/sharding (tidak perlu di skala ini); tanpa PM2
  (restart diserahkan ke panel).
- Bot utama jadi instance `CORE`, perilaku identik seperti sekarang
  (anti-regresi: semua singleton lama tetap dipakai CORE apa adanya).

Referensi yang dipelajari: `RemComp/rem-comp-botwa-legacy` (Node +
bridge Go `wuzapi`, webhook HTTP, `global.jadibot[token]`, cron health-check,
Mongo). Kita TIDAK meniru bridge HTTP-nya — Flora langsung multi-client
dalam satu proses Go (lebih sederhana, tanpa hop jaringan).

## 2. Alur

1. User: `.jadibot request` → tersimpan `pending` → owner terima pesan
   berisi nomor + ID permintaan.
2. Owner: `.jadibot approve <id>` / `.jadibot tolak <id>`.
3. Approve → bot buat device baru → tampilkan **pairing code 8 digit** →
   user tautkan di WA-nya (Perangkat Tertaut > Tautkan dengan Nomor).
4. Instance jalan: command difilter whitelist, sesi/fakta AI terpisah per
   instance (prefix token), owner global tetap bisa intervensi semua instance.
5. Owner: `.jadibot list` (status semua), `.jadibot stop <id>`,
   `.jadibot remove <id>` (matikan + hapus folder datanya).

Whitelist clone (usulan, belum final): Umum + AI + Grup dasar.
Tertutup: Owner, Alat sensitif (`$`, `\`, `.get`?), Shell, mode, ban.

## 3. Database

| Sekarang (satu bot) | Jadibot (multi-instance) |
|---|---|
| `data/session.db` (1 device) | Sama, N device (didukung sqlstore) |
| `data/group.db` (singleton global) | Satu per instance: `data/inst_<token>/group.db` |
| `data/ai.db` (histori, fakta, setting) | Satu per instance (terisolasi penuh) |

- Tabel baru `bot_instances(token PK, phone, owner_jid, status
  pending/active/stopped, created_at)` di DB utama (`data/group.db` CORE).
- Singleton global (`groupDB`, `aiDB`, `GlobalSession`, spam gate, timer
  jadwal, thumb cache) jadi per-instance handle; CORE memakai jalur lama
  supaya tidak regresi.

**Keputusan owner (belum dijawab):**

1. Setting provider AI per instance terpisah atau ikut CORE?
   (Rekomendasi: terpisah.)
2. Data clone yang di-remove: hapus permanen atau arsip?
   (Rekomendasi: hapus permanen + konfirmasi.)
3. Batas instance? (Rekomendasi: maks 8 slot + 1 CORE.)

## 4. Tahap eksekusi

1. File ini + tabel `bot_instances`.
2. Instance registry + pairing flow + approve/tolak.
3. Namespacing singleton (groupDB/aiDB/sesi/spam/timer) per instance.
4. Whitelist command + `.jadibot list/stop/remove`.
5. Uji: CORE tidak berubah + 1 clone penuh (pairing → chat → remove).
