// Package store berisi seluruh akses state bot: penyimpanan SQLite dan
// helper konteks/pesan yang dipakai lintas fitur.
//
// Paket ini SENGAJA tidak dipecah ke beberapa package karena simbolnya
// (store.*) dipakai di ratusan titik; memecahnya hanya menambah import
// tanpa manfaat nyata. Untuk navigasi, isinya terbagi dua kelompok:
//
//   - Layer database (pola *db.go / *s.go): absensidb, aidb, aisettingsdb,
//     crmdb, factsdb, financedb, groupdb, moderationdb, ownerdb, summariesdb,
//     clones, reminders, scheduledcalls.
//
//   - Helper konteks & pesan: context, quoted_context, message_unwrap,
//     group_helpers, trigger_extract, bot_message_tracker, log, tempdir,
//     autodownload, sticker_exif.
package store
