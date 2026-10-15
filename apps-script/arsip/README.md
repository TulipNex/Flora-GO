# arsip — tidak dipakai

Folder ini menyimpan `keuangan-sheet-legacy.gs`, Apps Script lama yang menjadi
sisi Google Sheet untuk fitur keuangan Flora.

Fitur Google Sheet sudah **pensiun**: keuangan Flora kini sepenuhnya
menggunakan database SQLite lokal (lihat `plugins/finance*.go`). Env
`SHEET_API_URL` / `SHEET_API_TOKEN` tidak lagi dibaca (catatan di
`internal/config/config.go`).

File di sini hanya arsip historis (rujukan cara deploy lama) — **jangan
di-deploy ulang** dan jangan dijadikan acuan perilaku bot saat ini.
