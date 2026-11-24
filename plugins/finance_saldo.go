package plugins

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

// RegisterFinanceSaldo mendaftarkan `.saldo` (khusus owner).
func RegisterFinanceSaldo(r *Registry) {
	r.Register(Command{
		Name:        "saldo",
		Description: "Saldo kas, rekening, total bulan ini",
		Category:    CategoryOwner,
		Handler:     handleSaldo,
	})
}

// financeSaldoPromptBlock merangkai blok saldo untuk system prompt agent.
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

// ---- Format balasan ----

// financeSaldoText merangkai balasan .saldo: saldo berjalan (kas, rekening,
// total) lalu ringkasan mutasi bulan berjalan sebagai blok kedua.
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

// parseFinanceDay memvalidasi tanggal (hanya angka) terhadap bulan berjalan.
func parseFinanceDay(raw string, now time.Time) (int, error) {
	day, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || day < 1 || day > 31 {
		return 0, fmt.Errorf("tanggal harus angka 1–31, mis. .transaksi date 25.")
	}
	last := time.Date(now.Year(), now.Month()+1, 0, 0, 0, 0, 0, now.Location()).Day()
	if day > last {
		return 0, fmt.Errorf("bulan ini hanya sampai tanggal %d.", last)
	}
	return day, nil
}

// parseFinanceNominal menerima nominal dengan atau tanpa pemisah ribuan.
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

// financeDateLabel mengubah 'YYYY-MM-DD' jadi '09 Nov 2026'.
func financeDateLabel(tanggal string) string {
	t, err := time.ParseInLocation("2006-01-02", tanggal, timex.Zone())
	if err != nil {
		return tanggal
	}
	return t.Format("02 Jan 2006")
}

// financeDayMonthLabel mengubah 'YYYY-MM-DD' jadi '08 Nov' (tanpa tahun).
func financeDayMonthLabel(tanggal string) string {
	t, err := time.ParseInLocation("2006-01-02", tanggal, timex.Zone())
	if err != nil {
		return tanggal
	}
	return t.Format("02 Jan")
}

// financePeriodeLabel mengubah periode 'YYYY-MM' jadi 'Nov 2026'.
func financePeriodeLabel(periode string) string {
	t, err := time.ParseInLocation("2006-01", periode, timex.Zone())
	if err != nil {
		return periode
	}
	return t.Format("Jan 2006")
}

// financeCountLabel merangkai keterangan jumlah baris untuk footer daftar transaksi.
func financeCountLabel(n int) string {
	return strconv.Itoa(n) + " Transaksi"
}

// financeMonthBounds mengembalikan tanggal awal & akhir bulan berjalan.
func financeMonthBounds(now time.Time) (dari, sampai string) {
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	last := first.AddDate(0, 1, -1)
	return first.Format("2006-01-02"), last.Format("2006-01-02")
}
