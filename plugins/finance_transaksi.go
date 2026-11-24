package plugins

import (
	"context"
	"fmt"
	"strings"

	"wabot-go/internal/store"
	"wabot-go/internal/timex"
)

func init() {
	registerPlugin(RegisterTransaksi)
}

func RegisterTransaksi(r *Registry) {
	r.Register(Command{
		Name:        "transaksi",
		Aliases:     []string{"trx", "trans"},
		Description: "Daftar transaksi",
		Category:    CategoryOwner,
		Handler:     handleTransaksi,
	})
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

// financeTransaksiUsage adalah blok petunjuk .transaksi.
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
// isi, lalu (bila ada) baris kosong + footer `> *N Transaksi*`.
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

// financeNominalLine merakit baris `last`/`date`.
func financeNominalLine(r store.FinanceRow) string {
	return fmt.Sprintf("- *%s* • [%s] %s",
		financeSignedNominal(r), financeSumberLabel(r.Sumber), r.Transaksi)
}

// financeAmountLine merakit baris `amount`.
func financeAmountLine(r store.FinanceRow) string {
	sign := "-"
	if r.Masuk != nil {
		sign = "+"
	}
	return fmt.Sprintf("- *%s* • [%s%s] %s",
		financeDayMonthLabel(r.Tanggal), sign, financeSumberLabel(r.Sumber), r.Transaksi)
}

// financeSignedNominal mengembalikan nominal bertanda: "+Rp20.000" untuk masuk,
// "-Rp5.000" untuk keluar.
func financeSignedNominal(r store.FinanceRow) string {
	if r.Masuk != nil {
		return "+" + financeRupiahFull(*r.Masuk)
	}
	if r.Keluar != nil {
		return "-" + financeRupiahFull(*r.Keluar)
	}
	return "tanpa nominal"
}

// financeSumberLabel menampilkan sumber dana dengan huruf besar di awal.
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
// kelompok berurutan.
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

// financeReverseRows membalik hasil SearchKeuangan menjadi urut waktu naik.
func financeReverseRows(rows []store.FinanceRow) {
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
}
