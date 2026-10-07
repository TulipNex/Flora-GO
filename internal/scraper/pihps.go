package scraper

// Scraper PIHPS BI (Pusat Informasi Harga Pangan Strategis Nasional):
// Tabel Harga Pasar Tradisional Berdasarkan Daerah untuk .het.
//
// Sumber: endpoint JSON DevExpress AspNet (tanpa login/token):
//   GetRefProvince                                   → daftar {id,name}
//   GetGridDataDaerah?price_type_id&tipe_laporan&
//     province_id&start_date&end_date                → {data:[{no,name,level,"DD/MM/YYYY":"15,800",...}]}
// Baris level 1 = kategori (no romawi), level 2 = komoditas. Angka format
// Indonesia ("15,800" = lima belas ribu delapan ratus) di-parse ke integer.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	pihpsBase        = "https://www.bi.go.id/hargapangan/WebSite/TabelHarga"
	pihpsTimeout     = 30 * time.Second
	pihpsMaxBody     = 4 << 20 // 4MB (grid ±30 baris, jauh di bawah ini)
	pihpsPriceTypeID = 1       // 1 = Pasar Tradisional (default situs)
	pihpsTipeHarian  = 1       // 1 = Laporan Harian (default situs)
)

// PihpsRow adalah satu baris grid: kategori/komoditas + harga per tanggal
// (key tanggal format DD/MM/YYYY seperti dari API).
type PihpsRow struct {
	No     string
	Nama   string
	Level  int
	Prices map[string]*int64 // tanggal DD/MM/YYYY → rupiah (nil = kosong)
	Dates  []string          // urutan kolom tanggal (DD/MM/YYYY)
}

// pihpsGet memanggil endpoint JSON PIHPS (GET, tanpa auth).
func pihpsGet(ctx context.Context, path string, params map[string]string) ([]byte, error) {
	u, err := url.Parse(pihpsBase + path)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	for k, v := range params {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", scraperUA)
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("BI tidak merespons (%v)", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("BI HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, pihpsMaxBody))
	if err != nil {
		return nil, fmt.Errorf("gagal baca respons BI")
	}
	return body, nil
}

// PihpsProvinceID mencari ID provinsi dari nama (case-insensitive,
// substring — "sultra"/"sulawesi tenggara" cocok).
func PihpsProvinceID(ctx context.Context, name string) (int, string, error) {
	body, err := pihpsGet(ctx, "/GetRefProvince", nil)
	if err != nil {
		return 0, "", err
	}
	var parsed struct {
		Data []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return 0, "", fmt.Errorf("respons provinsi BI bukan JSON valid")
	}
	want := strings.ToLower(strings.TrimSpace(name))
	for _, p := range parsed.Data {
		if strings.Contains(strings.ToLower(p.Name), want) {
			return p.ID, p.Name, nil
		}
	}
	return 0, "", fmt.Errorf("provinsi %q tidak ditemukan di BI", name)
}

// PihpsGridDaerah mengambil grid daerah untuk 1 provinsi + rentang tanggal.
// start/end format YYYY-MM-DD (zona pemanggil); kolom hasil DD/MM/YYYY.
func PihpsGridDaerah(ctx context.Context, provinceID int, startDate, endDate string) ([]PihpsRow, []string, error) {
	body, err := pihpsGet(ctx, "/GetGridDataDaerah", map[string]string{
		"price_type_id": strconv.Itoa(pihpsPriceTypeID),
		"tipe_laporan":  strconv.Itoa(pihpsTipeHarian),
		"province_id":   strconv.Itoa(provinceID),
		"start_date":    startDate + "T00:00:00",
		"end_date":      endDate + "T00:00:00",
	})
	if err != nil {
		return nil, nil, err
	}
	var parsed struct {
		Data []map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, nil, fmt.Errorf("respons grid BI bukan JSON valid")
	}
	rows, dates := parsePihpsGrid(parsed.Data)
	if len(rows) == 0 {
		return nil, nil, fmt.Errorf("data BI kosong untuk periode itu")
	}
	return rows, dates, nil
}

// parsePihpsGrid mengubah baris JSON grid jadi PihpsRow (dipisah supaya
// bisa dites unit tanpa HTTP). Key non-tanggal: no/name/level/id.
func parsePihpsGrid(data []map[string]interface{}) ([]PihpsRow, []string) {
	// Kumpulkan kolom tanggal (key selain no/name/level/id), urut kronologis.
	dateSet := map[string]bool{}
	for _, m := range data {
		for k := range m {
			if k != "no" && k != "name" && k != "level" && k != "id" {
				dateSet[k] = true
			}
		}
	}
	dates := make([]string, 0, len(dateSet))
	for d := range dateSet {
		dates = append(dates, d)
	}
	sortPihpsDates(dates)

	rows := make([]PihpsRow, 0, len(data))
	for _, m := range data {
		r := PihpsRow{Prices: map[string]*int64{}, Dates: dates}
		r.No, _ = m["no"].(string)
		if r.No == "" {
			if f, ok := m["no"].(float64); ok {
				r.No = strconv.Itoa(int(f))
			}
		}
		r.Nama, _ = m["name"].(string)
		if f, ok := m["level"].(float64); ok {
			r.Level = int(f)
		}
		for _, d := range dates {
			v, _ := m[d].(string)
			v = strings.TrimSpace(v)
			if v == "" || v == "-" {
				continue
			}
			// Format Indonesia: "15,800" = lima belas ribu (koma = ribuan
			// bila ekornya tepat 3 digit), desimal ("15,80") dipotong.
			// Titik selalu ribuan ("15.800"). Rupiah bulat.
			s := strings.ReplaceAll(v, ".", "")
			if i := strings.LastIndex(s, ","); i >= 0 {
				tail := s[i+1:]
				head := s[:i]
				if len(tail) == 3 && len(head) > 0 {
					s = head + tail
				} else {
					s = head
				}
			}
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil || n < 0 {
				continue
			}
			nv := n
			r.Prices[d] = &nv
		}
		rows = append(rows, r)
	}
	return rows, dates
}

// sortPihpsDates mengurutkan tanggal DD/MM/YYYY kronologis (insertion,
// kolom sedikit — tanpa import sort tambahan pun boleh, tapi sort jelas).
func sortPihpsDates(dates []string) {
	for i := 1; i < len(dates); i++ {
		for j := i; j > 0 && pihpsDateKey(dates[j]) < pihpsDateKey(dates[j-1]); j-- {
			dates[j], dates[j-1] = dates[j-1], dates[j]
		}
	}
}

// pihpsDateKey mengubah DD/MM/YYYY jadi YYYYMMDD untuk perbandingan.
func pihpsDateKey(d string) string {
	p := strings.Split(d, "/")
	if len(p) != 3 {
		return d
	}
	for len(p[0]) < 2 {
		p[0] = "0" + p[0]
	}
	for len(p[1]) < 2 {
		p[1] = "0" + p[1]
	}
	return p[2] + p[1] + p[0]
}
