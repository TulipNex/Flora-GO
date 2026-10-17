package plugins

// finance_monthly.go — reset + laporan keuangan bulanan otomatis.
//
// Alur: DB `keuangan` hanya menyimpan transaksi bulan berjalan. Saat bulan
// berganti, untuk tiap owner bot:
//   1. hitung saldo akhir bulan (saldo awal periode + mutasi),
//   2. rakit Excel laporan (ringkasan + daftar transaksi lengkap waktu input),
//   3. tulis arsip ke data/arsip-keuangan/<YYYY>/ (bila file kiriman hilang),
//   4. kirim file ke DM owner,
//   5. bawa saldo akhir jadi saldo awal bulan berikutnya,
//   6. HAPUS transaksi bulan itu (reset).
//
// Saat fitur PERTAMA aktif untuk seorang owner (belum punya baris periode):
// seluruh transaksi lama diarsipkan + dikirim ke DM owner, lalu buku dimulai
// ulang dari tanggal aktivasi dengan saldo awal = net transaksi lama.
//
// Pemicu: timer yang di-arm ulang ke awal bulan berikutnya (bukan polling),
// plus catch-up saat startup supaya bulan yang terlewat saat bot mati tetap
// dilaporkan. Kalau gagal (belum tersambung), percobaan diulang tiap 30 menit.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"wabot-go/internal/config"
	financex "wabot-go/internal/finance"
	"wabot-go/internal/logfmt"
	"wabot-go/internal/store"
	"wabot-go/internal/timex"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/types"
)

const (
	// financeArsipDir adalah folder arsip laporan di dalam data/ (gitignored,
	// terpisah dari glob data/*.db milik backup).
	financeArsipDir = "data/arsip-keuangan"

	// financeRetryInterval: jeda percobaan ulang bila sweep belum tuntas.
	financeRetryInterval = 30 * time.Minute
)

var (
	financeClient *whatsmeow.Client
	financeOnce   sync.Once
)

// InitFinanceScheduler menyimpan client pengirim laporan. Dipanggil sekali di
// main.go sebelum Connect (pola InitBackupScheduler).
func InitFinanceScheduler(client *whatsmeow.Client) {
	financeClient = client
}

// RestoreFinanceMonthly mengejar laporan bulan yang terlewat lalu menyalakan
// loop timer bulanan. Dipanggil sekali setelah Connect (pola
// RestoreAbsensiSessions).
func RestoreFinanceMonthly() {
	financeOnce.Do(func() {
		go func() {
			// Catch-up: jangan lanjut sebelum semua bulan lampau tuntas.
			for !runFinanceMonthlySweep() {
				time.Sleep(time.Minute)
			}
			financeMonthlyLoop()
		}()
	})
}

// financeMonthlyLoop meng-arm timer ke awal bulan berikutnya, menjalankan
// sweep, lalu mengulang. Bila sweep belum tuntas, ulangi tiap 30 menit.
func financeMonthlyLoop() {
	for {
		if d := time.Until(financex.NextMonthStart(timex.Now())); d > 0 {
			time.Sleep(d)
		}
		for !runFinanceMonthlySweep() {
			time.Sleep(financeRetryInterval)
		}
	}
}

// runFinanceMonthlySweep menutup semua periode lampau yang belum dilaporkan
// untuk tiap owner. Mengembalikan false bila ada yang masih gagal (mis. bot
// belum tersambung) supaya pemanggil menunda & mencoba lagi.
func runFinanceMonthlySweep() bool {
	client := financeClient
	if client == nil || !client.IsConnected() {
		return false
	}
	ctx := context.Background()
	cur := financex.Periode(timex.Now())
	ok := true
	for _, owner := range config.OwnerNumbers {
		owner = strings.TrimSpace(owner)
		if owner == "" {
			continue
		}
		// Inisialisasi sekali: belum ada baris periode = owner baru bagi
		// fitur ini → arsipkan buku lama lalu mulai ulang dengan saldo awal.
		inited, err := store.Finance().HasFinancePeriode(ctx, owner)
		if err != nil {
			logfmt.Warn("finance: gagal cek periode %s: %v\n", owner, err)
			ok = false
			continue
		}
		if !inited {
			if err := initializeFinanceOwner(ctx, client, owner, cur); err != nil {
				logfmt.Warn("finance: gagal inisialisasi %s: %v\n", owner, err)
				ok = false
				continue
			}
		}
		// Pastikan periode berjalan punya baris.
		if err := store.Finance().EnsureFinancePeriode(ctx, owner, cur); err != nil {
			logfmt.Warn("finance: gagal siapkan periode %s/%s: %v\n", owner, cur, err)
			ok = false
			continue
		}
		periodes, err := store.Finance().OpenFinancePeriodes(ctx, owner, cur)
		if err != nil {
			logfmt.Warn("finance: gagal baca periode %s: %v\n", owner, err)
			ok = false
			continue
		}
		for _, p := range periodes {
			if err := closeFinancePeriode(ctx, client, owner, p); err != nil {
				logfmt.Warn("finance: gagal tutup %s/%s: %v\n", owner, p.Periode, err)
				ok = false
				break
			}
		}
	}
	return ok
}

// closeFinancePeriode merakit + mengirim laporan satu bulan, mengarsipkannya,
// membawa saldo akhir ke bulan berikutnya, lalu menghapus transaksinya.
func closeFinancePeriode(ctx context.Context, client *whatsmeow.Client, owner string, p store.FinancePeriode) error {
	dari, sampai, valid := financex.PeriodeBounds(p.Periode)
	if !valid {
		return fmt.Errorf("periode tidak valid: %q", p.Periode)
	}
	rows, err := store.Finance().ListKeuanganRange(ctx, owner, dari, sampai)
	if err != nil {
		return err
	}
	tx := toFinanceTransaksi(rows)
	awal := financex.Saldo{Kas: p.SaldoAwalKas, Rekening: p.SaldoAwalRekening}
	data, err := financex.BuildLaporan(financex.Laporan{
		Owner:     owner,
		Periode:   p.Periode,
		Awal:      awal,
		Transaksi: tx,
		Dibuat:    timex.Now(),
	})
	if err != nil {
		return fmt.Errorf("rakit laporan: %w", err)
	}
	_, _, akhir := financex.Ringkas(awal, tx)

	arsipRel, err := writeFinanceArsip(owner, p.Periode, data)
	if err != nil {
		return fmt.Errorf("tulis arsip: %w", err)
	}

	jid, err := types.ParseJID(owner + "@" + types.DefaultUserServer)
	if err != nil || jid.IsEmpty() {
		return fmt.Errorf("JID owner tidak valid: %q", owner)
	}
	caption := fmt.Sprintf(
		"*Laporan Keuangan %s*\n%d transaksi bulan ini.\nSaldo akhir — kas: %d · rekening: %d.\nTransaksi bulan lalu diarsipkan lalu dikosongkan; saldo akhir dibawa jadi saldo awal bulan ini.",
		p.Periode, len(tx), akhir.Kas, akhir.Rekening)
	if err := sendDocBytes(ctx, client, jid, filepath.Base(arsipRel), xlsxMIME, data, caption); err != nil {
		return fmt.Errorf("kirim laporan: %w", err)
	}

	// Kirim sukses → tandai, bawa saldo, reset. Urutan penting: jangan reset
	// sebelum laporan benar-benar terkirim & tercatat.
	if err := store.Finance().MarkFinancePeriodeReported(ctx, owner, p.Periode, timex.Now().Unix(), arsipRel); err != nil {
		return err
	}
	if next := financex.NextPeriode(p.Periode); next != "" {
		if err := store.Finance().SetFinancePeriodeAwal(ctx, owner, next, akhir.Kas, akhir.Rekening); err != nil {
			return err
		}
	}
	if _, err := store.Finance().DeleteKeuanganPeriode(ctx, owner, dari, sampai); err != nil {
		return err
	}
	logfmt.Plain("Laporan keuangan %s (%s) terkirim · kas=%d rekening=%d\n", p.Periode, owner, akhir.Kas, akhir.Rekening)
	return nil
}

// initializeFinanceOwner menyiapkan owner saat fitur baru aktif: arsipkan
// SEMUA transaksi lama → kirim ke DM owner → mulai ulang dengan saldo awal
// (net transaksi lama). Dijalankan sekali per owner.
func initializeFinanceOwner(ctx context.Context, client *whatsmeow.Client, owner, periode string) error {
	rows, err := store.Finance().ListAllKeuangan(ctx, owner)
	if err != nil {
		return err
	}
	rekap, err := store.Finance().RekapKeuangan(ctx, store.FinanceFilter{Owner: owner})
	if err != nil {
		return err
	}
	kas := rekap.PerSumber["kas"].Saldo
	rekening := rekap.PerSumber["rekening"].Saldo

	if len(rows) > 0 {
		data, err := financex.BuildLaporan(financex.Laporan{
			Owner:     owner,
			Periode:   "pra-fitur",
			Transaksi: toFinanceTransaksi(rows),
			Dibuat:    timex.Now(),
		})
		if err != nil {
			return fmt.Errorf("rakit arsip pra-fitur: %w", err)
		}
		label := "pra-fitur-" + timex.Now().Format("20060102-150405")
		rel, err := writeFinanceArsip(owner, label, data)
		if err != nil {
			return fmt.Errorf("tulis arsip pra-fitur: %w", err)
		}
		if jid, perr := types.ParseJID(owner + "@" + types.DefaultUserServer); perr == nil && !jid.IsEmpty() {
			caption := fmt.Sprintf(
				"*Arsip keuangan pra-fitur*\n%d transaksi lama diarsipkan.\nBuku baru dimulai %s dengan saldo awal — kas: %d · rekening: %d.",
				len(rows), timex.Now().Format("02 Jan 2006 15:04"), kas, rekening)
			if serr := sendDocBytes(ctx, client, jid, filepath.Base(rel), xlsxMIME, data, caption); serr != nil {
				// Arsip sudah tersimpan di panel — jangan gantung inisialisasi.
				logfmt.Warn("finance: gagal kirim arsip pra-fitur %s: %v\n", owner, serr)
			}
		}
	}

	if err := store.Finance().ResetFinanceOwnerInitial(ctx, owner, periode, kas, rekening); err != nil {
		return err
	}
	logfmt.Plain("finance: %s diinisialisasi — arsip %d transaksi, saldo awal kas=%d rekening=%d\n", owner, len(rows), kas, rekening)
	return nil
}

// toFinanceTransaksi memetakan baris store ke tipe laporan.
func toFinanceTransaksi(rows []store.FinanceRow) []financex.Transaksi {
	out := make([]financex.Transaksi, 0, len(rows))
	for _, r := range rows {
		out = append(out, financex.Transaksi{
			Tanggal: r.Tanggal,
			InputAt: r.InputAt,
			Teks:    r.Transaksi,
			Sumber:  r.Sumber,
			Masuk:   r.Masuk,
			Keluar:  r.Keluar,
		})
	}
	return out
}

// writeFinanceArsip menulis file laporan ke data/arsip-keuangan/<tahun>/.
// label = "YYYY-MM" (bulanan) atau "pra-fitur-<stamp>" (arsip awal).
// Mengembalikan path relatif (disimpan di finance_periode.arsip_file/log).
func writeFinanceArsip(owner, label string, data []byte) (string, error) {
	year := timex.Now().Format("2006")
	if len(label) >= 4 && label[4] == '-' {
		year = label[:4]
	}
	dir := filepath.Join(financeArsipDir, year)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("keuangan-%s-%s.xlsx", owner, label)
	rel := filepath.Join(dir, name)
	if err := os.WriteFile(rel, data, 0o644); err != nil {
		return "", err
	}
	return rel, nil
}
