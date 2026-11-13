package plugins

// finance_saldo.go — jalur cepat owner untuk keuangan TANPA lewat agent AI:
//   - .saldo        → saldo kas, rekening, total bulan berjalan
//   - .transaksi    → petunjuk subcommand
//   - .transaksi last <n>       → n transaksi terakhir (1–50)
//   - .transaksi date <tanggal> → semua transaksi tanggal itu (1–31)
//   - .transaksi amount <nominal> → transaksi dengan nominal tertentu
//
// Kenapa ada command ini: jawaban lewat agent bergantung pada model (bisa lupa
// memanggil tool / salah menjumlah). Command ini membaca DB langsung, jadi
// angkanya deterministik dan tidak butuh kuota provider.
//
// Sumber angka: store.Finance().SaldoBerjalan — saldo awal bulan
// (finance_periode) + mutasi bulan berjalan. Tidak ada tabel saldo; DB hanya
// menyimpan transaksi bulan berjalan (bulan lalu diarsipkan ke Excel).
//
// Kategori Owner: menu otomatis menyembunyikannya dari non-owner dan bot clone
// ikut memblokir (cloneBlockedCategories di registry.go).

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"wabot-go/internal/identity"
	"wabot-go/internal/logfmt"
	"wabot-go/internal/store"
	"wabot-go/internal/timex"

	"github.com/polymorfa/hypermeow/types"
)

const (
	// financeMaxLast membatasi .transaksi last supaya balasan tidak melebihi
	// batas praktis satu bubble WhatsApp.
	financeMaxLast = 50
	// financeMaxRows adalah katup tampilan daftar transaksi (sama dengan
	// batas SearchKeuangan); lebih dari ini diberi peringatan terpotong.
	financeMaxRows = 500
	// financeMaxNominal membatasi nominal yang bisa dicari (15 digit, sama
	// dengan batas tool keuangan di internal/ai/tools/native).
	financeMaxNominal = 999_999_999_999_999
)

func init() {
	registerPlugin(RegisterFinanceSaldo)
}

// RegisterFinanceSaldo mendaftarkan `.saldo` dan `.transaksi` (khusus owner,
// kategori Owner — tidak ada kategori baru).
func RegisterFinanceSaldo(r *Registry) {
	r.Register(Command{
		Name:        "saldo",
		Description: "Saldo kas, rekening, total bulan ini",
		Category:    CategoryOwner,
		Handler:     handleSaldo,
	})
	r.Register(Command{
		Name:        "transaksi",
		Aliases:     []string{"trx", "trans"},
		Description: "Daftar transaksi (last/date/amount)",
		Category:    CategoryOwner,
		Handler:     handleTransaksi,
	})
}

// financeSaldoPromptBlock merangkai blok saldo untuk system prompt agent.
// Khusus owner (DB keuangan privat per nomor owner) dan fail-soft: bukan
// owner / DB bermasalah = string kosong, chat tetap jalan tanpa blok.
//
// Angka ditulis mentah (1250000, bukan 1.250.000) supaya model tidak salah
// membaca pemisah ribuan Indonesia. Ini potret AWAL giliran; setelah model
// mencatat transaksi, angka terbaru harus diambil lewat tool cek_saldo.
func financeSaldoPromptBlock(ctx context.Context, mctx *store.MessageContext) string {
	if mctx == nil || !IsOwner(mctx) {
		return ""
	}
	owner := financeOwnerNumber(ctx, mctx)
	if owner == "" {
		return ""
	}
	s, err := store.Finance().SaldoBerjalan(ctx, owner)
	if err != nil {
		logfmt.Warn("finance: gagal baca saldo untuk prompt: %v\n", err)
		return ""
	}
	return fmt.Sprintf(
		"[Keuangan owner — data lokal per %s %s]\n"+
			"Saldo bulan berjalan (%s): kas %d · rekening %d · total %d. "+
			"Saldo awal bulan: kas %d · rekening %d. %d transaksi bulan ini (masuk %d, keluar %d).\n"+
			"Aturan: untuk pertanyaan saldo/total pakai angka ini atau tool cek_saldo — JANGAN menjumlah manual dari baca_keuangan. "+
			"Kalau baru mencatat/mengubah/menghapus transaksi, panggil cek_saldo untuk angka terbaru.",
		timex.Now().Format("2 Jan 2006 15:04"), timex.Name(),
		s.Periode,
		s.Kas, s.Rekening, s.Total,
		s.AwalKas, s.AwalRekening,
		s.Count, s.Masuk, s.Keluar,
	)
}

// financeOwnerNumber memetakan pengirim ke nomor owner (kunci DB keuangan).
// Command sudah lewat requireOwner; ini hanya menyeragamkan bentuk JID ke
// digit yang dipakai DB, termasuk fallback LID→PN.
func financeOwnerNumber(ctx context.Context, mctx *store.MessageContext) string {
	var alt types.JID
	if mctx.RawEvt != nil {
		alt = mctx.RawEvt.Info.SenderAlt
	}
	num, _ := identity.OwnerNumber(ctx, mctx.Client, mctx.Sender, alt)
	return num
}

func handleSaldo(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	owner := financeOwnerNumber(ctx, mctx)
	if owner == "" {
		return store.ReplyText(ctx, mctx, ErrMsg("Saldo", "nomor owner tidak dikenali dari pesan ini."))
	}
	s, err := store.Finance().SaldoBerjalan(ctx, owner)
	if err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Saldo", fmt.Sprintf("gagal membaca database keuangan (%v).", err)))
	}
	return store.ReplyText(ctx, mctx, financeSaldoText(s))
}

// handleTransaksi menangani `.transaksi` beserta subcommand last/date/amount.
// Salah format selalu dibalas petunjuk singkat, bukan error teknis.
func handleTransaksi(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	if len(mctx.Args) == 0 {
		return store.ReplyText(ctx, mctx, financeTransaksiUsage())
	}
	owner := financeOwnerNumber(ctx, mctx)
	if owner == "" {
		return store.ReplyText(ctx, mctx, ErrMsg("Transaksi", "nomor owner tidak dikenali dari pesan ini."))
	}
	switch strings.ToLower(strings.TrimSpace(mctx.Args[0])) {
	case "last":
		return financeTransaksiLast(ctx, mctx, owner, mctx.Args[1:])
	case "date":
		return financeTransaksiDate(ctx, mctx, owner, mctx.Args[1:])
	case "amount":
		return financeTransaksiAmount(ctx, mctx, owner, mctx.Args[1:])
	default:
		return store.ReplyText(ctx, mctx, financeTransaksiUsage())
	}
}

// financeTransaksiLast menampilkan n transaksi terbaru bulan berjalan.
func financeTransaksiLast(ctx context.Context, mctx *store.MessageContext, owner string, args []string) error {
	if len(args) == 0 {
		return store.ReplyText(ctx, mctx, ErrMsg("Transaksi", "sebutkan jumlahnya, mis. .transaksi last 5 (1–50)."))
	}
	n, err := parseFinanceLast(args[0])
	if err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Transaksi", err.Error()))
	}
	dari, sampai := financeMonthBounds(timex.Now())
	rows, _, err := store.Finance().SearchKeuangan(ctx, store.FinanceFilter{
		Owner:  owner,
		Dari:   dari,
		Sampai: sampai,
		Limit:  n,
	})
	if err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Transaksi", fmt.Sprintf("gagal membaca database keuangan (%v).", err)))
	}
	if len(rows) == 0 {
		return store.ReplyText(ctx, mctx, financeListBlock("Transaksi terakhir",
			[]string{Bullet("Belum ada transaksi bulan ini.")}, ""))
	}
	// Urut tanggal naik (lama → baru) sesuai keputusan owner; SearchKeuangan
	// mengembalikan terbaru dulu, jadi dibalik lalu dikelompokkan per tanggal.
	financeReverseRows(rows)
	lines := make([]string, 0, len(rows)+4)
	for _, g := range financeGroupByDate(rows) {
		lines = append(lines, "`"+financeDateLabel(g.Tanggal)+"`")
		for _, r := range g.Rows {
			lines = append(lines, financeNominalLine(r))
		}
	}
	return store.ReplyText(ctx, mctx, financeListBlock(fmt.Sprintf("%d Transaksi terakhir", len(rows)), lines, ""))
}

// financeTransaksiDate menampilkan semua transaksi pada satu tanggal bulan
// berjalan. Isian hanya angka tanggal (1–31).
func financeTransaksiDate(ctx context.Context, mctx *store.MessageContext, owner string, args []string) error {
	if len(args) == 0 {
		return store.ReplyText(ctx, mctx, ErrMsg("Transaksi", "sebutkan tanggalnya, mis. .transaksi date 25 (1–31)."))
	}
	now := timex.Now()
	day, err := parseFinanceDay(args[0], now)
	if err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Transaksi", err.Error()))
	}
	tanggal := fmt.Sprintf("%s-%02d", now.Format("2006-01"), day)
	rows, truncated, err := store.Finance().SearchKeuangan(ctx, store.FinanceFilter{
		Owner:  owner,
		Dari:   tanggal,
		Sampai: tanggal,
		Limit:  financeMaxRows,
	})
	if err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Transaksi", fmt.Sprintf("gagal membaca database keuangan (%v).", err)))
	}
	if len(rows) == 0 {
		return store.ReplyText(ctx, mctx, financeListBlock("Transaksi "+financeDateLabel(tanggal),
			[]string{Bullet("Tidak ada transaksi tanggal ini.")}, ""))
	}
	// Urutan waktu input (pagi → malam) supaya terbaca seperti catatan harian.
	financeReverseRows(rows)
	lines := make([]string, 0, len(rows))
	for _, r := range rows {
		lines = append(lines, financeNominalLine(r))
	}
	return store.ReplyText(ctx, mctx, financeListBlock(
		"Transaksi "+financeDateLabel(tanggal), lines, financeCountLabel(len(rows)), truncated))
}

// financeTransaksiAmount menampilkan semua transaksi dengan nominal tertentu
// (masuk maupun keluar) di bulan berjalan.
func financeTransaksiAmount(ctx context.Context, mctx *store.MessageContext, owner string, args []string) error {
	if len(args) == 0 {
		return store.ReplyText(ctx, mctx, ErrMsg("Transaksi", "sebutkan nominalnya, mis. .transaksi amount 20000."))
	}
	nominal, err := parseFinanceNominal(args[0])
	if err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Transaksi", err.Error()))
	}
	dari, sampai := financeMonthBounds(timex.Now())
	rows, truncated, err := store.Finance().SearchKeuangan(ctx, store.FinanceFilter{
		Owner:   owner,
		Dari:    dari,
		Sampai:  sampai,
		Nominal: &nominal,
		Limit:   financeMaxRows,
	})
	if err != nil {
		return store.ReplyText(ctx, mctx, ErrMsg("Transaksi", fmt.Sprintf("gagal membaca database keuangan (%v).", err)))
	}
	if len(rows) == 0 {
		return store.ReplyText(ctx, mctx, financeListBlock("Transaksi "+financeRupiahFull(nominal),
			[]string{Bullet("Tidak ada transaksi dengan nominal ini bulan ini.")}, ""))
	}
	// Urut tanggal naik, konsisten dengan daftar lain.
	financeReverseRows(rows)
	lines := make([]string, 0, len(rows))
	for _, r := range rows {
		lines = append(lines, financeAmountLine(r))
	}
	return store.ReplyText(ctx, mctx, financeListBlock(
		"Transaksi "+financeRupiahFull(nominal), lines, financeCountLabel(len(rows)), truncated))
}

// ---- Format balasan ----

// financeSaldoText merangkai balasan .saldo: saldo berjalan (kas, rekening,
// total) lalu ringkasan mutasi bulan berjalan sebagai blok kedua. Angka
// mutasi dipakai apa adanya dari SaldoBerjalan — tidak ada query tambahan.
func financeSaldoText(s store.FinanceSaldo) string {
	text := Block("Saldo Keuangan",
		Row("Kas", financeRupiahFull(s.Kas)),
		Row("Rekening", financeRupiahFull(s.Rekening)),
		Row("Total", financeRupiahFull(s.Total)),
	)
	return text + Section("Bulan Ini ("+financePeriodeLabel(s.Periode)+")",
		Row("Masuk", financeRupiahFull(s.Masuk)),
		Row("Keluar", financeRupiahFull(s.Keluar)),
		Row("Transaksi", strconv.FormatInt(s.Count, 10)),
	)
}

// financeTransaksiUsage adalah blok petunjuk .transaksi — gaya referensi
// seperti .videocall/.voicecall (seksi monospace, baris `Format :`, lalu blok
// kode berisi daftar nilai), bukan daftar bullet panjang. Nama command tidak
// diulang di dalam blok kode karena `Format :` sudah menyebutnya.
func financeTransaksiUsage() string {
	return "*Transaksi Keuangan*\n\n" +
		"`Saldo`\n" +
		"Format : .saldo\n\n" +
		"`Daftar`\n" +
		"Format : .transaksi <type> <nilai>\n\n" +
		"```type : last • date • amount\n" +
		"last   : 1–50\n" +
		"date   : tanggal\n" +
		"amount : nominal```"
}

// financeListBlock merangkai daftar transaksi: judul bold, baris kosong,
// isi, lalu (bila ada) baris kosong + footer `> *N Transaksi*`. Baris kosong
// setelah judul disengaja supaya daftar tidak menempel ke judul.
func financeListBlock(title string, lines []string, footer string, truncated ...bool) string {
	var sb strings.Builder
	sb.WriteString(Title(title))
	if len(lines) > 0 {
		sb.WriteString("\n\n")
		sb.WriteString(strings.Join(lines, "\n"))
	}
	if footer != "" {
		sb.WriteString("\n\n" + Hint("*"+footer+"*"))
	}
	if len(truncated) > 0 && truncated[0] {
		sb.WriteString("\n" + Hint(fmt.Sprintf("Dibatasi %d baris — masih ada transaksi lain.", financeMaxRows)))
	}
	return sb.String()
}

// financeNominalLine merakit baris `last`/`date`:
// "- *-Rp5.000* • [Kas] Minuman" (nominal bertanda + sumber dana).
func financeNominalLine(r store.FinanceRow) string {
	return fmt.Sprintf("- *%s* • [%s] %s",
		financeSignedNominal(r), financeSumberLabel(r.Sumber), r.Transaksi)
}

// financeAmountLine merakit baris `amount`:
// "- *08 Nov* • [-Kas] Minuman" (nominal sudah jadi judul, jadi tanggal yang
// tampil dan tanda masuk/keluar pindah ke dalam bracket sumber).
func financeAmountLine(r store.FinanceRow) string {
	sign := "-"
	if r.Masuk != nil {
		sign = "+"
	}
	return fmt.Sprintf("- *%s* • [%s%s] %s",
		financeDayMonthLabel(r.Tanggal), sign, financeSumberLabel(r.Sumber), r.Transaksi)
}

// financeSignedNominal mengembalikan nominal bertanda: "+Rp20.000" untuk masuk,
// "-Rp5.000" untuk keluar. Baris tanpa nominal (data lama/korup) ditandai apa
// adanya, bukan disembunyikan.
func financeSignedNominal(r store.FinanceRow) string {
	if r.Masuk != nil {
		return "+" + financeRupiahFull(*r.Masuk)
	}
	if r.Keluar != nil {
		return "-" + financeRupiahFull(*r.Keluar)
	}
	return "tanpa nominal"
}

// financeSumberLabel menampilkan sumber dana dengan huruf besar di awal
// ("kas" → "Kas"); nilai asing dikembalikan apa adanya.
func financeSumberLabel(sumber string) string {
	switch strings.ToLower(strings.TrimSpace(sumber)) {
	case "kas":
		return "Kas"
	case "rekening":
		return "Rekening"
	}
	return sumber
}

// financeGroupByDate mengelompokkan baris yang SUDAH urut tanggal menjadi
// kelompok berurutan (dipakai header `11 Nov 2026` di .transaksi last).
func financeGroupByDate(rows []store.FinanceRow) []financeDateGroup {
	groups := make([]financeDateGroup, 0, len(rows))
	for _, r := range rows {
		if n := len(groups); n > 0 && groups[n-1].Tanggal == r.Tanggal {
			groups[n-1].Rows = append(groups[n-1].Rows, r)
			continue
		}
		groups = append(groups, financeDateGroup{Tanggal: r.Tanggal, Rows: []store.FinanceRow{r}})
	}
	return groups
}

// financeDateGroup adalah satu tanggal + baris-barisnya (urut input).
type financeDateGroup struct {
	Tanggal string
	Rows    []store.FinanceRow
}

// financeReverseRows membalik hasil SearchKeuangan (terbaru dulu) menjadi
// urut tanggal/waktu input naik, tanpa mengubah urutan di dalam satu waktu.
func financeReverseRows(rows []store.FinanceRow) {
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
}

// ---- Parsing & helper murni (diuji di finance_saldo_test.go) ----

// parseFinanceLast memvalidasi jumlah transaksi untuk .transaksi last.
func parseFinanceLast(raw string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("jumlah harus angka 1–%d, mis. .transaksi last 5.", financeMaxLast)
	}
	if n < 1 || n > financeMaxLast {
		return 0, fmt.Errorf("jumlah harus 1–%d.", financeMaxLast)
	}
	return n, nil
}

// parseFinanceDay memvalidasi tanggal (hanya angka) terhadap bulan berjalan —
// buku keuangan per bulan, jadi bulan & tahun tidak perlu diketik.
func parseFinanceDay(raw string, now time.Time) (int, error) {
	day, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || day < 1 || day > 31 {
		return 0, fmt.Errorf("tanggal harus angka 1–31, mis. .transaksi date 25.")
	}
	// Hari terakhir bulan berjalan: tanggal 1 bulan berikutnya dikurangi 1 hari.
	last := time.Date(now.Year(), now.Month()+1, 0, 0, 0, 0, 0, now.Location()).Day()
	if day > last {
		return 0, fmt.Errorf("bulan ini hanya sampai tanggal %d.", last)
	}
	return day, nil
}

// parseFinanceNominal menerima nominal dengan atau tanpa pemisah ribuan
// (20000, 20.000, 20,000) dan awalan Rp.
func parseFinanceNominal(raw string) (int64, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.TrimPrefix(s, "rp")
	s = strings.NewReplacer(".", "", ",", "", " ", "").Replace(s)
	nominal, err := strconv.ParseInt(s, 10, 64)
	if err != nil || nominal <= 0 {
		return 0, fmt.Errorf("nominal harus angka lebih dari 0, mis. .transaksi amount 20000.")
	}
	if nominal > financeMaxNominal {
		return 0, fmt.Errorf("nominal terlalu besar (maks 15 digit).")
	}
	return nominal, nil
}

// financeRupiah memformat angka dengan pemisah ribuan Indonesia (1.250.000).
func financeRupiah(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteByte(s[i])
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// financeRupiahFull menambahkan awalan Rp (dipakai baris saldo).
func financeRupiahFull(n int64) string {
	if n < 0 {
		return "-Rp" + financeRupiah(-n)
	}
	return "Rp" + financeRupiah(n)
}

// financeDateLabel mengubah 'YYYY-MM-DD' jadi '09 Nov 2026' (tanggal dua
// digit, sama seperti contoh desain). Nilai aneh dikembalikan apa adanya
// supaya baris tidak hilang.
func financeDateLabel(tanggal string) string {
	t, err := time.ParseInLocation("2006-01-02", tanggal, timex.Zone())
	if err != nil {
		return tanggal
	}
	return t.Format("02 Jan 2006")
}

// financeDayMonthLabel mengubah 'YYYY-MM-DD' jadi '08 Nov' (tanpa tahun) —
// dipakai .transaksi amount karena semua baris masih bulan berjalan.
func financeDayMonthLabel(tanggal string) string {
	t, err := time.ParseInLocation("2006-01-02", tanggal, timex.Zone())
	if err != nil {
		return tanggal
	}
	return t.Format("02 Jan")
}

// financePeriodeLabel mengubah periode 'YYYY-MM' jadi 'Nov 2026' (judul blok
// "Bulan Ini"). Nilai aneh dikembalikan apa adanya.
func financePeriodeLabel(periode string) string {
	t, err := time.ParseInLocation("2006-01", periode, timex.Zone())
	if err != nil {
		return periode
	}
	return t.Format("Jan 2006")
}

// financeCountLabel merangkai keterangan jumlah baris untuk footer daftar
// transaksi ("5 Transaksi"), bukan subtotal nominal.
func financeCountLabel(n int) string {
	return strconv.Itoa(n) + " Transaksi"
}

// financeMonthBounds mengembalikan tanggal awal & akhir bulan berjalan.
func financeMonthBounds(now time.Time) (dari, sampai string) {
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	last := first.AddDate(0, 1, -1)
	return first.Format("2006-01-02"), last.Format("2006-01-02")
}
