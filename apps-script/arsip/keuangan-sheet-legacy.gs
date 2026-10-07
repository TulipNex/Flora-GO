/**
 * Flora Finance — Google Apps Script (sisi Google Sheet).
 *
 * CARA DEPLOY (5 menit, lakukan SEKALI + tiap edit script):
 * 1. Buka Google Sheet kosong (ini file datanya) → Extensions → Apps Script.
 * 2. Hapus isi editor, paste SELURUH file ini, Save.
 * 3. Ubah konstanta TOKEN di bawah (samakan dengan SHEET_API_TOKEN di .env bot).
 * 4. Deploy → New deployment → tipe Web app →
 *    Execute as: Me, Who has access: Anyone → Deploy.
 * 5. Salin Web app URL → pasang sebagai SHEET_API_URL di .env bot.
 * 6. PENTING: setiap edit script WAJIB Deploy → Manage deployments →
 *    Edit → New version, kalau tidak yang jalan tetap versi lama!
 * 7. GANTI SKEMA (v2): sheet lama 7 kolom WAJIB dikosongkan total
 *    (hapus tab / seluruh isi + header) agar header 8 kolom ter-generate
 *    ulang otomatis. Data lama abaikan.
 * 8. VERIFIKASI SEBELUM DEPLOY: di editor Ctrl+F "function readRows",
 *    "function sheetTanggalStr", "function rekapRows" — masing-masing
 *    harus ketemu (memastikan tidak ada fungsi tertelan komentar).
 *
 * KONTRAK (JSON POST, Content-Type: application/json):
 *   Request:  {token, owner, action, ...params} (client_req_id wajib di
 *   append untuk idempotency)
 *   Response: {status: "ok", ...data} atau {status:"error", code, error}
 *   - owner   : nomor HP digit (tab per owner, auto-buat + header)
 *   - Tab per owner terisolasi total (append/read/update/delete).
 *   - Kolom: Nomor | Id | Transaksi | Masuk | Keluar | Sumber | Tanggal | Bukti
 *     (+ kolom I client_req_id, terlihat di sheet — jangan dihapus/diubah
 *     manual agar dedup tidak rusak). Tampil kapital + bold + border;
 *     logika memakai posisi, bukan nama.
 *   - sumber dinormalisasi saat baca (alias→kanonis; tak dikenal
 *     dipertahankan, dihitung di total tapi hilang di per_sumber).
 *   - id stabil (UUID); nomor display dirapikan 1..N tiap delete via script
 *     (hapus manual dari UI / baca konkuren bisa melihat lubang sesaat).
 *   - LockService HANYA di tulis (append/update/delete); read/rekap bebas
 *     lock (tab tak ada = hasil kosong tanpa membuat tab).
 *   - Error berkode: {status:"error", code, error} dengan code AUTH /
 *     VALIDATION / NOT_FOUND / DUPLICATE / LOCK_TIMEOUT / SERVER.
 *   - Idempotency: client_req_id ditolak bila sudah ada di 1000 baris
 *     terakhir (kolom I hidden); respons DUPLICATE.
 *   - read: chunked 1000 baris dari terbaru + flag truncated bila terpotong
 *     limit (default 50, maks 500); rekap: chunked + katup 20 detik + flag
 *     partial + examined (examined selalu ada, 0 bila tab kosong).
 *   - Zona tanggal: samakan zona file (File → Settings) dengan BOT_TIMEZONE
 *     bot; fallback bila zona tak terbaca = Asia/Jakarta.
 *   - Baris kosong (tanpa id/transaksi/tanggal) dilewati read/rekap.
 *   - File bukti Drive ikut di-trash saat baris dihapus.
 *   - Validasi server-side: nominal integer >0, tanggal YYYY-MM-DD, tipe &
 *     sumber enum. Jangan percaya input buta.
 *   - Tanggal dinormalisasi saat baca (sheetTanggalStr): sel Date hasil
 *     auto-parse Sheets (kolom format Otomatis) diformat ulang ke
 *     YYYY-MM-DD zona spreadsheet; kolom G dikunci Plain Text saat tab
 *     dibuat agar append baru tersimpan sebagai teks murni.
 *   - Aksi "rekap": agregasi server-side TANPA limit (count, total_masuk,
 *     total_keluar, saldo, per_sumber) — untuk tanya total/saldo walau baris
 *     ribuan; "read" tetap untuk rincian (limit 200, terbaru dulu).
 */

var TOKEN = "b3ccdab45a2c597fbde079958eea536b626e701616e5be2d";
var FOLDER_NAME = "Bukti Flora";

// HEADERS adalah kunci logika (JANGAN ubah urutan/makna): seluruh baca/
// tulis memakai POSISI kolom A..I, bukan nama. HEADERS_DISPLAY adalah teks
// yang terlihat di baris 1 (kapital) + kolom I teknis tetap lowercase.
var HEADERS = ["nomor", "id", "transaksi", "masuk", "keluar", "sumber", "tanggal", "bukti"];
var HEADERS_DISPLAY = ["Nomor", "Id", "Transaksi", "Masuk", "Keluar", "Sumber", "Tanggal", "Bukti"];

var SUMBER_ALIAS = {
  "kas": "kas", "cash": "kas", "tunai": "kas",
  "rekening": "rekening", "bank": "rekening", "transfer": "rekening"
};

function doPost(e) {
  try {
    var req = JSON.parse(e.postData.contents);
    if (req.token !== TOKEN) {
      return jsonErr("AUTH", "token salah");
    }
    if (!req.owner || !/^\d+$/.test(String(req.owner))) {
      return jsonErr("VALIDATION", "owner wajib digit");
    }
    var a = String(req.action || "").toLowerCase();
    // Baca (read/rekap) bebas lock (audit D1): tidak mutasi apa pun,
    // jadi antrean tulis (mis. upload bukti 10 detik) tidak boleh
    // memblokir yang cuma lihat data. Tab tak ada = hasil kosong
    // (tanpa membuat tab, tanpa lock).
    if (a === "read" || a === "rekap") {
      var sh = getOwnerSheet(String(req.owner), false);
      if (!sh) {
        return jsonOut(a === "read"
          ? { status: "ok", rows: [], truncated: false }
          : { status: "ok", rekap: emptyRekap(), partial: false });
      }
      if (a === "read") return readRows(sh, req);
      return rekapRows(sh, req);
    }
    // Tulis (append/update/delete) di bawah lock script-wide.
    var lock = LockService.getScriptLock();
    try {
      lock.waitLock(10000);
    } catch (lockErr) {
      return jsonErr("LOCK_TIMEOUT", "server sibuk, coba lagi sebentar");
    }
    try {
      var wsh = getOwnerSheet(String(req.owner), true);
      if (a === "append") return appendRow(wsh, req);
      if (a === "update") return updateRow(wsh, req);
      if (a === "delete") return deleteRow(wsh, req);
      return jsonErr("VALIDATION", "action tidak dikenal: " + req.action);
    } finally {
      lock.releaseLock();
    }
  } catch (err) {
    return jsonErr("SERVER", "exception: " + err);
  }
}

function jsonOut(obj) {
  return ContentService.createTextOutput(JSON.stringify(obj))
    .setMimeType(ContentService.MimeType.JSON);
}

// jsonErr adalah respons error berkode (audit D2) supaya bot bisa
// membedakan: coba-lagi (LOCK_TIMEOUT/SERVER) vs input-salah
// (VALIDATION/NOT_FOUND/DUPLICATE). "error" tetap teks Indonesia.
function jsonErr(code, msg) {
  return jsonOut({ status: "error", code: code, error: msg });
}

// emptyRekap adalah agregat nol (dipakai tab kosong + rekapRows).
function emptyRekap() {
  return {
    count: 0, total_masuk: 0, total_keluar: 0, saldo: 0,
    per_sumber: { kas: { count: 0, masuk: 0, keluar: 0 }, rekening: { count: 0, masuk: 0, keluar: 0 } }
  };
}

// getOwnerSheet mengambil tab milik owner (atau buat + header bila belum
// ada dan create !== false). Baca memanggil dengan create=false: tab tak
// ada = null (hasil kosong) tanpa membuat tab dan tanpa lock.
// Tiap panggil: pastikan header kapital + format tabel (migrasi tab lama
// lowercase otomatis; header kustom/berbeda tidak disentuh).
function getOwnerSheet(owner, create) {
  var ss = SpreadsheetApp.getActiveSpreadsheet();
  var sh = ss.getSheetByName(owner);
  if (!sh && create !== false) {
    sh = ss.insertSheet(owner);
    sh.appendRow(HEADERS_DISPLAY.concat(["client_req_id"]));
    formatFinanceTable(sh);
    try {
      sh.getRange("G2:G").setNumberFormat("@");
    } catch (e) { /* format gagal = non-fatal */ }
  } else if (sh) {
    try {
      ensureFinanceHeader(sh);
    } catch (e) { /* kosmetik gagal = non-fatal, logika tetap jalan */ }
  }
  return sh;
}

// ensureFinanceHeader meng-upgrade baris 1 lowercase lama ke kapital bila
// PERSIS cocok (perbandingan per sel, trim). Header berbeda (kustom/user)
// tidak disentuh. Dipanggil tiap request — 1 getRange kecil, di luar lock
// untuk baca, di dalam lock untuk tulis (aman dua-duanya: tulis idempoten).
function ensureFinanceHeader(sh) {
  if (sh.getLastRow() < 1 || sh.getLastColumn() < 9) return;
  var cur = sh.getRange(1, 1, 1, 9).getValues()[0];
  var want = HEADERS_DISPLAY.concat(["client_req_id"]);
  var same = true, legacy = true;
  for (var i = 0; i < 9; i++) {
    var c = String(cur[i] == null ? "" : cur[i]).trim();
    if (c !== want[i]) same = false;
    var old = i < 8 ? HEADERS[i] : "client_req_id";
    if (c !== old) legacy = false;
  }
  if (same) return;
  if (!legacy) return;
  sh.getRange(1, 1, 1, 9).setValues([want]);
  formatFinanceTable(sh); // tab lama: sekalian beri border (sekali, idempoten)
}

// formatFinanceTable memberi format tabel SEKALI saat tab dibuat: header
// bold + border tipis area A1:I1000 (mencakup ~1000 baris ke depan tanpa
// biaya per-append; append baru otomatis masuk area ber-border karena
// border sel kosong tetap tampil). Gagal = non-fatal.
function formatFinanceTable(sh) {
  try {
    sh.getRange("A1:I1").setFontWeight("bold");
    sh.getRange("A1:I1000").setBorder(true, true, true, true, true, true);
    sh.setFrozenRows(1);
  } catch (e) { /* format gagal = non-fatal */ }
}

function findRowById(sh, id) {
  if (!id) return -1;
  var last = sh.getLastRow();
  if (last < 2) return -1;
  var ids = sh.getRange(2, 2, last - 1, 1).getValues();
  for (var i = 0; i < ids.length; i++) {
    if (String(ids[i][0]) === String(id)) return i + 2;
  }
  return -1;
}

/** Rapatkan kolom nomor 1..N (dipanggil tiap delete). Lewati tulis bila
 *  sudah rapat (audit R13): delete = O(2N) tulis; skip ini memangkas rewrite
 *  kolom-A penuh pada kasus umum tanpa mengurangi garansi. */
function renumberCol(sh) {
  var last = sh.getLastRow();
  if (last < 2) return;
  var cur = sh.getRange(2, 1, last - 1, 1).getValues();
  for (var i = 0; i < cur.length; i++) {
    if (parseInt(cur[i][0], 10) !== i + 1) {
      var nums = [];
      for (var j = 0; j < last - 1; j++) nums.push([j + 1]);
      sh.getRange(2, 1, last - 1, 1).setValues(nums);
      return;
    }
  }
}

function validTanggal(s) {
  // Ketat: pecah komponen lalu pastikan Date tidak rollover (2026-02-30
  // menjadi 2026-03-02 di parser — dulu lolos sebagai "valid" lalu tersimpan
  // sebagai teks mustahil dan membocorkan rekap bulanan).
  var m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(String(s || ""));
  if (!m) return false;
  var y = +m[1], mo = +m[2], d = +m[3];
  if (mo < 1 || mo > 12 || d < 1 || d > 31) return false;
  var dt = new Date(Date.UTC(y, mo - 1, d));
  return dt.getUTCFullYear() === y && dt.getUTCMonth() === mo - 1 && dt.getUTCDate() === d;
}

// parseNominalStrict mengubah input jadi integer rupiah atau NaN. Ditolak:
// desimal ("50.5"→bukan 50), pemisah ribuan ("50,000"/"50.000"), suffix
// ("50000abc", "50.5rb"), kosong. parseInt mentah prefix-parse semua itu
// diam-diam (50 ribu jadi 50) — dilarang dipakai untuk nominal.
function parseNominalStrict(v) {
  var s = String(v == null ? "" : v).trim().replace(/\s/g, "");
  if (!/^\d{1,15}$/.test(s)) return NaN;
  var n = parseInt(s, 10);
  if (isNaN(n) || n <= 0) return NaN;
  return n;
}

function normSumber(s) {
  var k = String(s || "").trim().toLowerCase();
  // hasOwnProperty via prototype (bukan method instance): key warisan
  // seperti "toString"/"constructor" dulu lolos sebagai function object
  // truthy lalu tertulis ke sheet sebagai sampah.
  if (Object.prototype.hasOwnProperty.call(SUMBER_ALIAS, k)) return SUMBER_ALIAS[k];
  return "";
}

function appendRow(sh, req) {
  // SATU baca A..I untuk dedup (kolom I) + nomor max (kolom A) — dulu 2
  // getRange penuh per append. Jendela 1000 baris terakhir membatasi biaya
  // pada tab besar (trade-off terdokumentasi: retry atas baris yang lebih
  // tua dari itu bisa lolos dedup; idempotency realistis untuk retry
  // jam-jaman, bukan tahunan).
  var maxNomor = 0;
  var reqId = String(req.client_req_id || "").trim();
  var last = sh.getLastRow();
  if (last > 1) {
    var fromRow = last - 999;
    if (fromRow < 2) fromRow = 2;
    var vals = sh.getRange(fromRow, 1, last - fromRow + 1, 9).getValues();
    for (var i = 0; i < vals.length; i++) {
      var nm = parseInt(vals[i][0], 10);
      if (!isNaN(nm) && nm > maxNomor) maxNomor = nm;
      if (reqId && String(vals[i][8]) === reqId) {
        return jsonErr("DUPLICATE", "duplikat (sudah tercatat)");
      }
    }
  }
  var tanggal = String(req.tanggal || "");
  var tipe = String(req.tipe || "").toLowerCase();
  var nominal = parseNominalStrict(req.nominal);
  var kategori = String(req.kategori || "").trim();
  var keterangan = String(req.keterangan || "").trim();
  var sumber = normSumber(req.sumber);
  if (!validTanggal(tanggal)) return jsonErr("VALIDATION", "tanggal harus YYYY-MM-DD yang valid");
  if (tipe !== "masuk" && tipe !== "keluar") return jsonErr("VALIDATION", "tipe harus masuk/keluar");
  if (isNaN(nominal)) return jsonErr("VALIDATION", "nominal harus digit bulat > 0 (tanpa titik/koma/suffix)");
  if (!keterangan && !kategori) return jsonErr("VALIDATION", "keterangan/kategori kosong");
  if (!sumber) return jsonErr("VALIDATION", "sumber harus kas/rekening");

  var transaksi = kategori ? (kategori + ": " + keterangan) : keterangan;
  var masuk = tipe === "masuk" ? nominal : "";
  var keluar = tipe === "keluar" ? nominal : "";

  var bukti = "";
  if (req.bukti_bytes) {
    try {
      var blob = Utilities.newBlob(
        Utilities.base64Decode(String(req.bukti_bytes)),
        String(req.bukti_mime || "image/jpeg"),
        String(req.bukti_name || "bukti.jpg")
      );
      var folder = getBuktiFolder();
      var file = folder.createFile(blob);
      file.setSharing(DriveApp.Access.ANYONE_WITH_LINK, DriveApp.Permission.VIEW);
      bukti = "https://drive.google.com/file/d/" + file.getId() + "/view";
    } catch (err) {
      return jsonErr("SERVER", "gagal simpan bukti: " + err);
    }
  }

  var id = Utilities.getUuid();
  var nomor = maxNomor + 1;
  sh.appendRow([nomor, id, transaksi, masuk, keluar, sumber, tanggal, bukti, reqId]);
  var out = { status: "ok", id: id, nomor: nomor };
  if (bukti) out.drive_url = bukti;
  return jsonOut(out);
}

function getBuktiFolder() {
  var props = PropertiesService.getScriptProperties();
  var folderId = props.getProperty("BUKTI_FOLDER_ID");
  if (folderId) {
    try {
      return DriveApp.getFolderById(folderId);
    } catch (e) { /* buat ulang jika error */ }
  }
  var folder = DriveApp.createFolder(FOLDER_NAME);
  props.setProperty("BUKTI_FOLDER_ID", folder.getId());
  return folder;
}

function rowToObj(v) {
  return {
    nomor: v[0], id: String(v[1]), transaksi: String(v[2]),
    masuk: v[3] === "" ? null : v[3], keluar: v[4] === "" ? null : v[4],
    sumber: normSumberSel(v[5]), tanggal: sheetTanggalStr(v[6]), bukti: String(v[7])
  };
}

// normSumberSel menormalkan ISI SEL sumber (audit C5): alias ("Cash ") jadi
// kanonis; ejaan tak dikenal dipertahankan apa adanya (agar rekap total
// tetap menghitungnya walau per_sumber melewatkannya); kosong tetap "".
function normSumberSel(v) {
  var raw = String(v == null ? "" : v).trim();
  if (raw === "") return "";
  var c = normSumber(raw);
  return c !== "" ? c : raw;
}

// isEmptyRow true bila baris sampah (sisa hapus manual): tanpa id,
// transaksi, dan tanggal sekaligus. Baris seperti ini dilewati read/rekap
// supaya tidak menggelembungkan count (audit C5).
function isEmptyRow(o) {
  return o.id === "" && o.transaksi === "" && o.tanggal === "";
}

// sheetTanggalStr menormalkan sel tanggal jadi string YYYY-MM-DD (zona file
// spreadsheet). Tanpa ini, sel Date hasil auto-parse Sheets gagal semua
// perbandingan string di read/rekap (baca kosong padahal data ada).
var _sheetTzCache = null;
function sheetTanggalStr(v) {
  if (v instanceof Date) {
    if (!_sheetTzCache) {
      try {
        _sheetTzCache = SpreadsheetApp.getActiveSpreadsheet().getSpreadsheetTimeZone();
      } catch (e) {
        // Fallback Asia/Jakarta (audit D6) — bukan GMT+8 mentah — plus
        // panduan: samakan zona file (File → Settings) dengan BOT_TIMEZONE.
        _sheetTzCache = "Asia/Jakarta";
      }
    }
    return Utilities.formatDate(v, _sheetTzCache, "yyyy-MM-dd");
  }
  // Teks manual non-ISO yang umum ("5/10/2026", "2026-1-5") dinormalisasi
  // ke ISO supaya tidak merusak perbandingan leksikografis (audit R17).
  // Format tak dikenal = apa adanya (ditolak filter, bukan ditebak).
  var t = String(v == null ? "" : v).trim();
  var dm = /^(\d{1,2})\/(\d{1,2})\/(\d{4})$/.exec(t);
  if (dm && validTanggal(pad4(dm[3]) + "-" + pad2(dm[2]) + "-" + pad2(dm[1]))) {
    return pad4(dm[3]) + "-" + pad2(dm[2]) + "-" + pad2(dm[1]);
  }
  var ym = /^(\d{4})-(\d{1,2})-(\d{1,2})$/.exec(t);
  if (ym && validTanggal(pad4(ym[1]) + "-" + pad2(ym[2]) + "-" + pad2(ym[3]))) {
    return pad4(ym[1]) + "-" + pad2(ym[2]) + "-" + pad2(ym[3]);
  }
  return t;
}

function pad2(n) {
  n = String(n);
  return n.length >= 2 ? n : "0" + n;
}

function pad4(n) {
  n = String(n);
  while (n.length < 4) n = "0" + n;
  return n;
}

// matchRow memusatkan filter read/rekap (tanggal, kategori, sumber).
function matchRow(o, from, to, kat, sumber) {
  if (isEmptyRow(o)) return false;
  if (from && o.tanggal < from) return false;
  if (to && o.tanggal > to) return false;
  if (kat && String(o.transaksi).toLowerCase().indexOf(kat) < 0) return false;
  if (sumber && o.sumber !== sumber) return false;
  return true;
}

function readRows(sh, req) {
  var last = sh.getLastRow();
  if (last < 2) return jsonOut({ status: "ok", rows: [], truncated: false });
  var from = String(req.dari || "");
  var to = String(req.sampai || "");
  var kat = String(req.kategori || "").toLowerCase();
  var sumber = normSumber(req.sumber || "");
  if (req.sumber && !sumber) return jsonErr("VALIDATION", "sumber harus kas/rekening");
  var limit = parseInt(req.limit, 10);
  if (isNaN(limit) || limit <= 0) limit = 50;
  if (limit > 500) limit = 500;
  // Baca per chunk 1000 baris DARI BAWAH (terbaru dulu): tab besar tidak
  // perlu fetch seluruh isi bila limit sudah terpenuhi (audit D3). Katup 20
  // detik seperti rekap: filter selektif yang memaksa full-scan tidak boleh
  // melewati batas eksekusi — berhenti rapi dengan truncated:true.
  var rows = [];
  var truncated = false;
  var bottom = last;
  var t0 = Date.now();
  while (bottom > 1 && rows.length < limit) {
    var top = bottom - 999;
    if (top < 2) top = 2;
    var vals = sh.getRange(top, 1, bottom - top + 1, 8).getValues();
    for (var i = vals.length - 1; i >= 0 && rows.length < limit; i--) {
      var o = rowToObj(vals[i]);
      if (matchRow(o, from, to, kat, sumber)) rows.push(o);
    }
    bottom = top - 1;
    if (bottom > 1 && Date.now() - t0 > 20000) {
      truncated = true;
      break;
    }
  }
  // Masih ada baris lebih tua yang belum diperiksa = hasil terpotong.
  truncated = bottom > 1;
  return jsonOut({ status: "ok", rows: rows, truncated: truncated });
}

function rekapRows(sh, req) {
  var last = sh.getLastRow();
  var agg = emptyRekap();
  if (last < 2) return jsonOut({ status: "ok", rekap: agg, partial: false, examined: 0 });
  var from = String(req.dari || "");
  var to = String(req.sampai || "");
  var kat = String(req.kategori || "").toLowerCase();
  var sumber = normSumber(req.sumber || "");
  if (req.sumber && !sumber) return jsonErr("VALIDATION", "sumber harus kas/rekening");
  // Baca per chunk 1000 baris + katup waktu 20 detik (audit D5): tab puluhan
  // ribu baris tidak boleh melewati batas eksekusi Web App (~30 dtk) dengan
  // respons rusak — berhenti rapi dengan partial:true + jumlah diperiksa.
  var t0 = Date.now();
  var examined = 0;
  var partial = false;
  var top = 2;
  while (top <= last) {
    var bottom = top + 999;
    if (bottom > last) bottom = last;
    var vals = sh.getRange(top, 1, bottom - top + 1, 8).getValues();
    for (var i = 0; i < vals.length; i++) {
      examined++;
      var o = rowToObj(vals[i]);
      if (!matchRow(o, from, to, kat, sumber)) continue;
      var m = numSel(o.masuk);
      var k = numSel(o.keluar);
      agg.count++;
      agg.total_masuk += m;
      agg.total_keluar += k;
      if (agg.per_sumber[o.sumber]) {
        agg.per_sumber[o.sumber].count++;
        agg.per_sumber[o.sumber].masuk += m;
        agg.per_sumber[o.sumber].keluar += k;
      }
    }
    top = bottom + 1;
    if (top <= last && Date.now() - t0 > 20000) {
      partial = true;
      break;
    }
  }
  agg.saldo = agg.total_masuk - agg.total_keluar;
  return jsonOut({ status: "ok", rekap: agg, partial: partial, examined: examined });
}

// numSel mengubah sel nominal jadi angka (audit C3 sisi server): number
// asli dipakai; string digit ("50000", hasil ketik tangan) di-parse ketat;
// selain itu 0 (bukan error — baris kotor tidak boleh menggagalkan rekap).
function numSel(v) {
  if (typeof v === "number") return v;
  var s = String(v == null ? "" : v).trim().replace(/\s/g, "");
  if (!/^\d{1,15}$/.test(s)) return 0;
  return parseInt(s, 10);
}

function updateRow(sh, req) {
  var r = findRowById(sh, req.id);
  if (r < 0) return jsonErr("NOT_FOUND", "id tidak ditemukan");
  var cur = sh.getRange(r, 1, 1, 8).getValues()[0];
  var transaksi = cur[2], masuk = cur[3], keluar = cur[4], sumber = cur[5], tanggal = cur[6];
  var changed = false;

  if (req.transaksi !== undefined && req.transaksi !== null && String(req.transaksi).trim() !== "") {
    transaksi = String(req.transaksi).trim();
    changed = true;
  }
  var hasNominal = req.nominal !== undefined && req.nominal !== null && String(req.nominal).trim() !== "";
  var hasTipe = req.tipe !== undefined && req.tipe !== null && String(req.tipe).trim() !== "";
  if (hasNominal || hasTipe) {
    // Audit R5: inferensi dari sel existing hanya bila selnya VALID. Sel
    // kotor ("50rb", dua kolom terisi) dulu meracuni hasil: NaN ditolak
    // walau user benar, atau tipe ditebak lalu nominal lawan dikosongkan
    // diam-diam. Sekarang: korup = error actionable (sertakan nominal).
    var existing = null;
    if (!hasNominal) {
      var bothFilled = masuk !== "" && keluar !== "";
      var oneValid = parseNominalStrict(masuk !== "" ? masuk : keluar);
      if (bothFilled || isNaN(oneValid)) {
        return jsonErr("VALIDATION", "data nominal existing korup — sertakan nominal eksplisit saat update");
      }
      existing = oneValid;
    }
    var nominal = hasNominal ? parseNominalStrict(req.nominal) : existing;
    var tipe = hasTipe ? String(req.tipe).toLowerCase() : (masuk !== "" ? "masuk" : "keluar");
    if (isNaN(nominal)) return jsonErr("VALIDATION", "nominal harus digit bulat > 0 (tanpa titik/koma/suffix)");
    if (tipe !== "masuk" && tipe !== "keluar") return jsonErr("VALIDATION", "tipe harus masuk/keluar");
    masuk = tipe === "masuk" ? nominal : "";
    keluar = tipe === "keluar" ? nominal : "";
    changed = true;
  }
  if (req.sumber !== undefined && req.sumber !== null && String(req.sumber).trim() !== "") {
    var ns = normSumber(req.sumber);
    if (!ns) return jsonErr("VALIDATION", "sumber harus kas/rekening");
    sumber = ns;
    changed = true;
  }
  if (req.tanggal !== undefined && req.tanggal !== null && String(req.tanggal).trim() !== "") {
    if (!validTanggal(req.tanggal)) return jsonErr("VALIDATION", "tanggal harus YYYY-MM-DD yang valid");
    tanggal = String(req.tanggal).trim();
    changed = true;
  }
  if (!changed) return jsonErr("VALIDATION", "tidak ada field yang diubah");
  sh.getRange(r, 3, 1, 6).setValues([[transaksi, masuk, keluar, sumber, tanggal, cur[7]]]);
  return jsonOut({ status: "ok", id: String(cur[1]) });
}

function deleteRow(sh, req) {
  var r = findRowById(sh, req.id);
  if (r < 0) return jsonErr("NOT_FOUND", "id tidak ditemukan");
  // Audit D4/R15: file bukti di Drive ikut dibuang (trash) supaya tidak yatim
  // selamanya. ID dikenali dari 3 format URL Drive (file/d/, open?id=,
  // uc?id=); format lain/edit manual = dilewati + dicatat (bukan diam).
  // Sharing ANYONE_WITH_LINK dipertahankan karena bot & owner membukanya
  // tanpa login Google. Trash ≠ hapus permanen (kuota Drive tidak kembali).
  try {
    var buktiUrl = String(sh.getRange(r, 8, 1, 1).getValue() || "");
    var m = /\/d\/([A-Za-z0-9_-]+)/.exec(buktiUrl)
      || /[?&]id=([A-Za-z0-9_-]+)/.exec(buktiUrl);
    if (m) {
      DriveApp.getFileById(m[1]).setTrashed(true);
    } else if (buktiUrl.trim() !== "") {
      console.log("deleteRow: format URL bukti tak dikenal, file tidak di-trash: " + buktiUrl);
    }
  } catch (driveErr) { console.log("deleteRow: gagal trash bukti: " + driveErr); }
  sh.deleteRow(r);
  renumberCol(sh);
  return jsonOut({ status: "ok", id: String(req.id) });
}
