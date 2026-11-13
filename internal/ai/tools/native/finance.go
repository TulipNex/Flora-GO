package native

// Tools keuangan Flora — database SQLite LOKAL (store.Finance),
// pengganti Google Sheet + Apps Script + Drive (keputusan owner: mulai nol,
// tanpa mirror, tanpa kolom bukti, tanpa nomor tersimpan).
//
// Pembagian tugas: TOTAL/SALDO via rekap_keuangan (agregasi DB, tanpa
// limit); RINCIAN via baca_keuangan (multi-patokan, limit 500 katup).
// Model DILARANG menjumlahkan manual dari hasil baca.

import (
	"fmt"
	"strconv"
	"strings"

	"wabot-go/internal/ai"
	"wabot-go/internal/identity"
	"wabot-go/internal/store"
	"wabot-go/internal/timex"
)

func init() {
	ai.RegisterTool(ai.ToolDefinition{
		Name: "baca_keuangan",
		Description: "Mencari RINCIAN catatan keuangan OWNER di database lokal (privat per nomor owner). " +
			"Untuk TOTAL/SALDO pakai rekap_keuangan — JANGAN menjumlahkan manual dari hasil baca. " +
			"Hanya untuk OWNER (ditolak untuk non-owner). " +
			"Semua filter opsional dan digabung AND; makin banyak patokan makin sedikit hasil. " +
			"Kosongkan dari+sampai untuk 30 hari terakhir; limit default 50 (maks 500, terbaru dulu). " +
			"Hasil berupa baris {id,transaksi,masuk,keluar,sumber,tanggal,saldo(kas,rek,total),input} — rujuk baris via id.",
		Parameters: ai.ParameterSchema{
			Type: "OBJECT",
			Properties: map[string]ai.PropertySchema{
				"dari": {
					Type:        "STRING",
					Description: "Tanggal mulai YYYY-MM-DD (opsional, kosong = 30 hari lalu bila sampai juga kosong).",
				},
				"sampai": {
					Type:        "STRING",
					Description: "Tanggal akhir YYYY-MM-DD (opsional, kosong = hari ini bila dari juga kosong).",
				},
				"kategori": {
					Type:        "STRING",
					Description: "Teks yang harus ada di transaksi (opsional).",
				},
				"sumber": {
					Type:        "STRING",
					Description: "Filter sumber dana: kas/rekening (opsional, untuk rincian; total via rekap_keuangan).",
				},
				"nominal": {
					Type:        "NUMBER",
					Description: "Nominal tepat rupiah (opsional, cocok di sisi masuk ATAU keluar).",
				},
				"min_nominal": {
					Type:        "NUMBER",
					Description: "Nominal minimum rupiah (opsional).",
				},
				"maks_nominal": {
					Type:        "NUMBER",
					Description: "Nominal maksimum rupiah (opsional).",
				},
				"limit": {
					Type:        "NUMBER",
					Description: "Maks baris, default 50, maks 500.",
				},
			},
		},
		Execute: func(actx *ai.AgentContext, args map[string]interface{}) (string, error) {
			owner, err := requireOwnerNative(actx)
			if err != nil {
				return "", err
			}
			dari, sampai := defaultRange30(argString(args, "dari"), argString(args, "sampai"))
			if err := CheckFilterDates(dari, sampai); err != nil {
				return "", err
			}
			sumberRaw := argString(args, "sumber")
			if err := CheckFilterSumber(sumberRaw); err != nil {
				return "", err
			}
			filter := store.FinanceFilter{
				Owner:  owner,
				Teks:   argString(args, "kategori"),
				Dari:   dari,
				Sampai: sampai,
				Sumber: NormSumber(sumberRaw),
				Limit:  int(argNumber(args, "limit", 50)),
			}
			if n, ok := argNumberOk(args, "nominal"); ok {
				v, err := checkNominalArg(n)
				if err != nil {
					return "", err
				}
				filter.Nominal = &v
			}
			if n, ok := argNumberOk(args, "min_nominal"); ok {
				v, err := checkNominalArg(n)
				if err != nil {
					return "", err
				}
				filter.MinNominal = &v
			}
			if n, ok := argNumberOk(args, "maks_nominal"); ok {
				v, err := checkNominalArg(n)
				if err != nil {
					return "", err
				}
				filter.MaksNominal = &v
			}
			rows, truncated, err := store.Finance().SearchKeuangan(actx.Context, filter)
			if err != nil {
				return "", fmt.Errorf("gagal baca keuangan: %v", err)
			}
			if len(rows) == 0 {
				return "Tidak ada catatan yang cocok dengan patokan itu.", nil
			}
			var sb strings.Builder
			fmt.Fprintf(&sb, "Ditemukan %d baris (terbaru dulu):", len(rows))
			if truncated {
				sb.WriteString(" PERINGATAN: hasil terpotong limit — masih ada baris lebih tua. Tambah patokan (tanggal/nominal/teks) untuk sisanya.")
			}
			for _, r := range rows {
				fmt.Fprintf(&sb, "\n%v | %v | masuk:%v keluar:%v | sumber:%v | %v",
					r.ID, r.Transaksi, numOrDashInt(r.Masuk), numOrDashInt(r.Keluar), r.Sumber, r.Tanggal)
				if r.SaldoKas != nil && r.SaldoRekening != nil {
					if r.Saldo != nil {
						fmt.Fprintf(&sb, " | saldo(kas:%v, rek:%v, total:%v)", *r.SaldoKas, *r.SaldoRekening, *r.Saldo)
					} else {
						fmt.Fprintf(&sb, " | saldo(kas:%v, rek:%v)", *r.SaldoKas, *r.SaldoRekening)
					}
				}
				if r.InputAt != "" {
					fmt.Fprintf(&sb, " | input:%v", r.InputAt)
				}
			}
			return sb.String(), nil
		},
	})

	ai.RegisterTool(ai.ToolDefinition{
		Name: "rekap_keuangan",
		Description: "Agregasi TOTAL/SALDO keuangan OWNER dihitung database (TANPA limit baris). " +
			"Untuk pertanyaan saldo saat ini tanpa rentang tanggal, pakai cek_saldo (lebih ringkas); " +
			"pakai tool ini untuk rekap bersumber dana atau rentang tanggal tertentu ('pengeluaran bulan ini berapa?'). " +
			"JANGAN menjumlahkan manual dari baca_keuangan. Hanya untuk OWNER. " +
			"Tanggal YYYY-MM-DD; kosongkan dari+sampai untuk saldo BULAN BERJALAN (sudah termasuk saldo awal bulan ini); " +
			"isi dari/sampai untuk mutasi rentang itu saja (tanpa saldo awal). Filter kategori/sumber opsional.",
		Parameters: ai.ParameterSchema{
			Type: "OBJECT",
			Properties: map[string]ai.PropertySchema{
				"dari": {
					Type:        "STRING",
					Description: "Tanggal mulai YYYY-MM-DD (opsional, kosong = sejak awal).",
				},
				"sampai": {
					Type:        "STRING",
					Description: "Tanggal akhir YYYY-MM-DD (opsional, kosong = sampai terakhir).",
				},
				"kategori": {
					Type:        "STRING",
					Description: "Filter teks transaksi (opsional).",
				},
				"sumber": {
					Type:        "STRING",
					Description: "Filter sumber dana: kas/rekening (opsional).",
				},
			},
		},
		Execute: func(actx *ai.AgentContext, args map[string]interface{}) (string, error) {
			owner, err := requireOwnerNative(actx)
			if err != nil {
				return "", err
			}
			if err := CheckFilterDates(argString(args, "dari"), argString(args, "sampai")); err != nil {
				return "", err
			}
			sumberRaw := argString(args, "sumber")
			if err := CheckFilterSumber(sumberRaw); err != nil {
				return "", err
			}
			dari := argString(args, "dari")
			sampai := argString(args, "sampai")
			sumber := NormSumber(sumberRaw)
			r, err := store.Finance().RekapKeuangan(actx.Context, store.FinanceFilter{
				Owner:  owner,
				Teks:   argString(args, "kategori"),
				Dari:   dari,
				Sampai: sampai,
				Sumber: sumber,
			})
			if err != nil {
				return "", fmt.Errorf("gagal rekap keuangan: %v", err)
			}
			// Rentang terbuka = saldo bulan berjalan → sertakan saldo awal
			// periode (DB hanya menyimpan transaksi bulan berjalan).
			if dari == "" && sampai == "" {
				if p, found, perr := store.Finance().GetFinancePeriode(actx.Context, owner, timex.Now().Format("2006-01")); perr == nil && found {
					applySaldoAwal(&r, p, sumber)
				}
			}
			var sb strings.Builder
			fmt.Fprintf(&sb, "Rekap %d transaksi: masuk %d, keluar %d, saldo %d.",
				r.Count, r.TotalMasuk, r.TotalKeluar, r.Saldo)
			for _, s := range []string{"kas", "rekening"} {
				if m, ok := r.PerSumber[s]; ok {
					fmt.Fprintf(&sb, "\n- %s: %d transaksi, masuk %d, keluar %d, saldo %d.",
						s, m.Count, m.TotalMasuk, m.TotalKeluar, m.Saldo)
				}
			}
			return sb.String(), nil
		},
	})

	ai.RegisterTool(ai.ToolDefinition{
		Name: "cek_saldo",
		Description: "Saldo keuangan OWNER saat ini — TANPA parameter, tidak ada cara salah pakai. " +
			"Pakai tool ini setiap kali ditanya saldo/total uang bulan ini ('saldo kas berapa?', 'total uangku berapa?'). " +
			"Angka dihitung database: saldo awal bulan + mutasi bulan berjalan (kas, rekening, total). " +
			"JANGAN menjumlahkan manual dari baca_keuangan. Untuk rentang tanggal lain pakai rekap_keuangan. " +
			"Hanya untuk OWNER (ditolak untuk non-owner).",
		Parameters: ai.ParameterSchema{
			Type:       "OBJECT",
			Properties: map[string]ai.PropertySchema{},
		},
		Execute: func(actx *ai.AgentContext, args map[string]interface{}) (string, error) {
			owner, err := requireOwnerNative(actx)
			if err != nil {
				return "", err
			}
			s, err := store.Finance().SaldoBerjalan(actx.Context, owner)
			if err != nil {
				return "", fmt.Errorf("gagal baca saldo keuangan: %v", err)
			}
			return fmt.Sprintf("Saldo bulan berjalan (%s): kas %d, rekening %d, total %d. "+
				"Saldo awal bulan: kas %d, rekening %d. %d transaksi bulan ini (masuk %d, keluar %d).",
				s.Periode, s.Kas, s.Rekening, s.Total,
				s.AwalKas, s.AwalRekening, s.Count, s.Masuk, s.Keluar), nil
		},
	})

	ai.RegisterTool(ai.ToolDefinition{
		Name: "edit_keuangan",
		Description: "Mengubah baris keuangan OWNER berdasarkan ID (lihat dari baca_keuangan — hanya id stabil). " +
			"Hanya untuk OWNER. Isi hanya field yang mau diubah, kosongkan sisanya. " +
			"nominal integer rupiah >0; tanggal YYYY-MM-DD; tipe masuk/keluar (bila nominal/tipe diubah, kolom masuk/keluar dihitung ulang).",
		Parameters: ai.ParameterSchema{
			Type: "OBJECT",
			Properties: map[string]ai.PropertySchema{
				"id": {
					Type:        "STRING",
					Description: "Wajib: id baris (dari hasil baca_keuangan).",
				},
				"transaksi": {
					Type:        "STRING",
					Description: "Teks transaksi baru (opsional).",
				},
				"nominal": {
					Type:        "NUMBER",
					Description: "Nominal rupiah baru integer >0 (opsional).",
				},
				"tipe": {
					Type:        "STRING",
					Description: "masuk/keluar, bila tipe ikut berubah (opsional).",
				},
				"sumber": {
					Type:        "STRING",
					Description: "kas/rekening, bila sumber dana ikut berubah (opsional).",
				},
				"tanggal": {
					Type:        "STRING",
					Description: "Tanggal YYYY-MM-DD baru (opsional).",
				},
			},
			Required: []string{"id"},
		},
		Execute: func(actx *ai.AgentContext, args map[string]interface{}) (string, error) {
			owner, err := requireOwnerNative(actx)
			if err != nil {
				return "", err
			}
			id := argString(args, "id")
			if id == "" {
				return "", fmt.Errorf("id wajib diisi (lihat dari baca_keuangan dulu)")
			}
			var (
				transaksi     string
				ubahTrans     bool
				masuk, keluar *int64
				ubahNominal   bool
				sumber        string
				ubahSumber    bool
				tanggal       string
				ubahTanggal   bool
			)
			if s := argString(args, "transaksi"); s != "" {
				transaksi, ubahTrans = s, true
			}
			hasNominal, hasTipe := false, false
			var nominal int64
			if n, ok := argNumberOk(args, "nominal"); ok {
				v, err := checkNominalArg(n)
				if err != nil {
					return "", err
				}
				nominal, hasNominal = v, true
			}
			tipe := ""
			if s := argString(args, "tipe"); s != "" {
				t := strings.ToLower(strings.TrimSpace(s))
				if t != "masuk" && t != "keluar" {
					return "", fmt.Errorf("tipe harus masuk/keluar")
				}
				tipe, hasTipe = t, true
			}
			if hasNominal || hasTipe {
				// Tanpa nominal eksplisit, infer dari baris existing — tapi
				// hanya bila selnya VALID (audit R5): sel kotor ("50rb",
				// dua sisi terisi) = error actionable, bukan tebak/hapus.
				// Tanpa tipe eksplisit, pertahankan sisi semula.
				m := nominal
				side := tipe
				if !hasNominal || side == "" {
					cur, err := store.Finance().GetKeuangan(actx.Context, owner, id)
					if err != nil {
						return "", fmt.Errorf("gagal baca baris: %v", err)
					}
					if cur == nil {
						return "", fmt.Errorf("id tidak ditemukan")
					}
					if !hasNominal {
						bothFilled := cur.Masuk != nil && cur.Keluar != nil
						existing := cur.Masuk
						if existing == nil {
							existing = cur.Keluar
						}
						if bothFilled || existing == nil || *existing <= 0 {
							return "", fmt.Errorf("data nominal existing korup — sertakan nominal eksplisit saat update")
						}
						m = *existing
					}
					if side == "" {
						side = "keluar"
						if cur.Masuk != nil {
							side = "masuk"
						}
					}
				}
				if side == "masuk" {
					masuk, keluar = &m, nil
				} else {
					masuk, keluar = nil, &m
				}
				ubahNominal = true
			}
			if s := argString(args, "sumber"); s != "" {
				c := NormSumber(s)
				if c == "" {
					return "", fmt.Errorf("sumber harus kas/rekening")
				}
				sumber, ubahSumber = c, true
			}
			if s := argString(args, "tanggal"); s != "" {
				if !validTanggalNative(s) {
					return "", fmt.Errorf("tanggal harus YYYY-MM-DD yang valid")
				}
				tanggal, ubahTanggal = s, true
			}
			if !ubahTrans && !ubahNominal && !ubahSumber && !ubahTanggal {
				return "", fmt.Errorf("tidak ada field yang diubah")
			}
			ok, err := store.Finance().UpdateKeuangan(actx.Context, owner, id, transaksi, ubahTrans, masuk, keluar, ubahNominal, sumber, tanggal, ubahSumber, ubahTanggal)
			if err != nil {
				return "", fmt.Errorf("gagal update: %v", err)
			}
			if !ok {
				return "", fmt.Errorf("id tidak ditemukan")
			}
			return fmt.Sprintf("Berhasil: baris %s diperbarui.", id), nil
		},
	})

	ai.RegisterTool(ai.ToolDefinition{
		Name: "hapus_keuangan",
		Description: "Menghapus baris keuangan OWNER berdasarkan ID (lihat dari baca_keuangan — hanya id stabil). " +
			"Hanya untuk OWNER. PENTING: pastikan user sudah KONFIRMASI eksplisit baris mana yang dihapus " +
			"(sebutkan isi/transaksinya balik) SEBELUM memanggil tool ini — jangan hapus berdasarkan tebakan.",
		Parameters: ai.ParameterSchema{
			Type: "OBJECT",
			Properties: map[string]ai.PropertySchema{
				"id": {
					Type:        "STRING",
					Description: "Wajib: id baris yang sudah dikonfirmasi user.",
				},
			},
			Required: []string{"id"},
		},
		Execute: func(actx *ai.AgentContext, args map[string]interface{}) (string, error) {
			owner, err := requireOwnerNative(actx)
			if err != nil {
				return "", err
			}
			id := argString(args, "id")
			if id == "" {
				return "", fmt.Errorf("id wajib diisi")
			}
			ok, err := store.Finance().DeleteKeuangan(actx.Context, owner, id)
			if err != nil {
				return "", fmt.Errorf("gagal hapus: %v", err)
			}
			if !ok {
				return "", fmt.Errorf("id tidak ditemukan")
			}
			return fmt.Sprintf("Berhasil: baris %s dihapus.", id), nil
		},
	})
}

// checkNominalArg memvalidasi angka argumen tool: integer >0 dalam batas
// presisi (cermin parseNominalStrict lama di server). Satu sumber kebenaran
// untuk baca (filter), edit, dan catat via bridge.
func checkNominalArg(n float64) (int64, error) {
	if n <= 0 || n != float64(int64(n)) {
		return 0, fmt.Errorf("nominal harus integer rupiah >0")
	}
	if n > MaxNominal {
		return 0, fmt.Errorf("nominal terlalu besar (maks 15 digit)")
	}
	return int64(n), nil
}

// applySaldoAwal menambahkan saldo awal periode berjalan ke hasil rekap
// (dipakai saat rentang terbuka). sumberFilter "" = kas+rekening.
func applySaldoAwal(r *store.FinanceRekap, p store.FinancePeriode, sumberFilter string) {
	add := func(sumber string, awal int64) {
		ps, exists := r.PerSumber[sumber]
		if awal == 0 && !exists {
			return
		}
		ps.Saldo += awal
		r.PerSumber[sumber] = ps
		r.Saldo += awal
	}
	if sumberFilter == "" || sumberFilter == "kas" {
		add("kas", p.SaldoAwalKas)
	}
	if sumberFilter == "" || sumberFilter == "rekening" {
		add("rekening", p.SaldoAwalRekening)
	}
}

// ---- Helper ----

// requireOwnerNative memastikan pemanggil tool adalah salah satu owner
// (config.OwnerNumbers). Native tidak boleh import plugins (arah dependency
// terbalik + menarik whatsmeow), jadi pencocokan JID ditulis lokal di sini:
// bandingkan user-part SenderJID (+SenderAlt) dengan daftar digit owner.
func requireOwnerNative(actx *ai.AgentContext) (string, error) {
	phone, err := OwnerPhone(actx)
	if err != nil {
		return "", fmt.Errorf("fitur keuangan hanya untuk owner bot")
	}
	return phone, nil
}

// OwnerPhone mengembalikan nomor owner (digit) pemanggil, atau error bila
// bukan owner. Diekspor supaya tool bridge (catat/export) memakai resolusi
// yang SAMA dengan tool native.
func OwnerPhone(actx *ai.AgentContext) (string, error) {
	if actx == nil {
		return "", fmt.Errorf("konteks agent kosong")
	}
	candidates := []string{actx.SenderJID}
	if mctx := agentMctx(actx); mctx != nil && mctx.RawEvt != nil {
		candidates = append(candidates, mctx.RawEvt.Info.SenderAlt.String())
	}
	for _, c := range candidates {
		if phone := matchOwnerDigits(c); phone != "" {
			return phone, nil
		}
	}
	// Fallback LID: petakan via store bila mctx tersedia (grup mode lid).
	if mctx := agentMctx(actx); mctx != nil && mctx.Client != nil && mctx.RawEvt != nil {
		sender := mctx.RawEvt.Info.Sender
		if pn, err := mctx.Client.Store.GetAltJID(actx.Context, sender); err == nil && !pn.IsEmpty() {
			if phone := matchOwnerDigits(pn.String()); phone != "" {
				return phone, nil
			}
		}
	}
	return "", fmt.Errorf("fitur keuangan hanya untuk owner bot")
}

// agentMctx mengambil *store.MessageContext dari Extra tanpa import
// plugins (Extra diisi plugins/ai.go; type-assert butuh import store saja,
// dan store tidak import ai — aman dari cyclic).
func agentMctx(actx *ai.AgentContext) *store.MessageContext {
	if actx == nil {
		return nil
	}
	mctx, _ := actx.Extra["mctx"].(*store.MessageContext)
	return mctx
}

// matchOwnerDigits mencocokkan string JID ke salah satu nomor owner,
// mengembalikan nomor owner-nya (bukan inputnya). Implementasi tunggal ada di
// internal/identity supaya command owner (plugins) memakai kunci nomor yang
// SAMA PERSIS dengan tool keuangan ini.
func matchOwnerDigits(jidStr string) string {
	return identity.MatchOwnerNumber(jidStr)
}

// defaultRange30 mengisi dari/sampai yang kosong dengan jendela 30 hari
// terakhir zona aktif timex (sampai = hari ini). Bila salah satu diisi,
// yang lain dibiarkan (filter satu sisi).
func defaultRange30(dari, sampai string) (string, string) {
	if dari == "" && sampai == "" {
		today := timex.Now()
		sampai = today.Format("2006-01-02")
		dari = today.AddDate(0, 0, -30).Format("2006-01-02")
	}
	return dari, sampai
}

// sumberAlias memetakan alias sumber dana ke kanonis. Kanonis dipakai untuk
// validasi pre-flight di Go supaya penolakan jelas sebelum query.
var sumberAlias = map[string]string{
	"kas": "kas", "cash": "kas", "tunai": "kas",
	"rekening": "rekening", "bank": "rekening", "transfer": "rekening",
}

// NormSumber menormalkan input sumber ke kanonis kas/rekening ("" bila tak
// dikenal). Diekspor supaya bridge (catat) memakai aturan yang sama dengan
// native (edit) — satu sumber kebenaran.
func NormSumber(s string) string {
	if c, ok := sumberAlias[strings.ToLower(strings.TrimSpace(s))]; ok {
		return c
	}
	return ""
}

// MaxNominal adalah nominal maksimum (15 digit): float64 kehilangan presisi
// integer di atas 2^53, jadi tolak sebelum disimpan — bukan dibulatkan
// diam-diam. Diekspor untuk bridge.
const MaxNominal = 999999999999999

// validTanggalNative true bila string tanggal YYYY-MM-DD valid kalender
// (menolak rollover seperti 2026-02-30).
func validTanggalNative(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) != 10 || s[4] != '-' || s[7] != '-' {
		return false
	}
	digits := s[:4] + s[5:7] + s[8:]
	for _, r := range digits {
		if r < '0' || r > '9' {
			return false
		}
	}
	y, _ := strconv.Atoi(s[:4])
	mo, _ := strconv.Atoi(s[5:7])
	d, _ := strconv.Atoi(s[8:])
	if mo < 1 || mo > 12 || d < 1 {
		return false
	}
	dim := []int{31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}[mo-1]
	if mo == 2 && y%4 == 0 && (y%100 != 0 || y%400 == 0) {
		dim = 29
	}
	return d <= dim
}

// CheckFilterDates memvalidasi filter tanggal baca/rekap/export: string
// bebas ("kemarin") ditolak pre-flight dengan pesan jelas (bukan hasil
// kosong misterius). Kosong = filter terbuka (boleh).
func CheckFilterDates(dari, sampai string) error {
	for _, d := range []string{dari, sampai} {
		if strings.TrimSpace(d) != "" && !validTanggalNative(d) {
			return fmt.Errorf("filter tanggal harus YYYY-MM-DD yang valid (dapat %q)", d)
		}
	}
	return nil
}

// CheckFilterSumber memastikan filter sumber dikenal (kosong = terbuka).
func CheckFilterSumber(sumber string) error {
	if strings.TrimSpace(sumber) != "" && NormSumber(sumber) == "" {
		return fmt.Errorf("filter sumber harus kas/rekening (dapat %q)", sumber)
	}
	return nil
}

// numOrDashInt menampilkan pointer nominal int64 atau "-" bila kosong.
func numOrDashInt(v *int64) string {
	if v == nil {
		return "-"
	}
	return strconv.FormatInt(*v, 10)
}

// argString mengambil string argumen tool (aman bila bukan string).
func argString(args map[string]interface{}, key string) string {
	s, _ := args[key].(string)
	return strings.TrimSpace(s)
}

// argNumber mengambil angka argumen (JSON number float64 maupun string
// digit "50000" — model kadang mengirim string). Default bila kosong.
func argNumber(args map[string]interface{}, key string, def float64) float64 {
	if n, ok := argNumberOk(args, key); ok {
		return n
	}
	return def
}

// argNumberOk versi boolean: bedakan "tak diisi" vs "diisi 0".
func argNumberOk(args map[string]interface{}, key string) (float64, bool) {
	switch v := args[key].(type) {
	case float64:
		return v, true
	case int64:
		return float64(v), true
	case int:
		return float64(v), true
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return 0, false
		}
		s = strings.ReplaceAll(s, ",", "")
		if n, err := strconv.ParseFloat(s, 64); err == nil {
			return n, true
		}
		return 0, false
	case nil:
		return 0, false
	default:
		return 0, false
	}
}
