package plugins

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ---- parser hari & jam ----

var absensiDayNames = map[string]int{
	"minggu": 0, "senin": 1, "selasa": 2, "rabu": 3,
	"kamis": 4, "jumat": 5, "sabtu": 6,
}

// parseAbsenDays menerima NAMA ("senin-jumat", "setiap-hari") MAUPUN
// ANGKA ("8", "1-3", "1,3,5") — case-insensitive → CSV "1,2,3,4,5" urut.
// Angka: 1–7 = Senin–Minggu, 8 = Senin–Jumat, 9 = Senin–Sabtu,
// 10 = Setiap hari. Preset 8/9/10 harus tunggal (tak bisa digabung).
// Nama dan angka tak boleh campur. false bila tak ada hari valid.
func parseAbsenDays(raw string) (string, bool) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "setiap-hari" || s == "tiap-hari" || s == "harian" || s == "semua" || s == "10" {
		return "0,1,2,3,4,5,6", true
	}
	if s == "8" {
		return "1,2,3,4,5", true
	}
	if s == "9" {
		return "1,2,3,4,5,6", true
	}
	got := map[int]bool{}
	addRange := func(a, b int) {
		if a > b {
			a, b = b, a
		}
		for d := a; d <= b; d++ {
			got[d] = true
		}
	}
	isNumericPart := func(p string) bool {
		if p == "" {
			return false
		}
		for _, r := range p {
			if (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
		return true
	}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		// Preset hanya sah tunggal (sudah ditangani di atas bila utuh).
		if part == "8" || part == "9" || part == "10" {
			return "", false
		}
		if isNumericPart(part) {
			if bounds := strings.SplitN(part, "-", 2); len(bounds) == 2 && bounds[0] != "" && bounds[1] != "" {
				a, err1 := strconv.Atoi(bounds[0])
				b, err2 := strconv.Atoi(bounds[1])
				na, ok1 := numDay(a)
				nb, ok2 := numDay(b)
				if err1 != nil || err2 != nil || !ok1 || !ok2 {
					return "", false
				}
				if na <= nb {
					addRange(na, nb)
				} else {
					// Rentang melingkar (mis. 5-7 = Jum–Min): pecah dua.
					addRange(na, 6)
					addRange(0, nb)
				}
				continue
			}
			if v, err := strconv.Atoi(part); err == nil {
				if nd, ok := numDay(v); ok {
					got[nd] = true
					continue
				}
			}
			return "", false
		}
		if bounds := strings.SplitN(part, "-", 2); len(bounds) == 2 {
			a, ok1 := absensiDayNames[strings.TrimSpace(bounds[0])]
			b, ok2 := absensiDayNames[strings.TrimSpace(bounds[1])]
			if !ok1 || !ok2 {
				return "", false
			}
			addRange(a, b)
			continue
		}
		if d, ok := absensiDayNames[part]; ok {
			got[d] = true
			continue
		}
		return "", false
	}
	if len(got) == 0 {
		return "", false
	}
	var days []int
	for d := 0; d <= 6; d++ {
		if got[d] {
			days = append(days, d)
		}
	}
	var sb strings.Builder
	for i, d := range days {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, "%d", d)
	}
	return sb.String(), true
}

// numDay memetakan angka user 1–7 (1=Senin..7=Minggu) ke weekday Go 0–6.
func numDay(v int) (int, bool) {
	if v >= 1 && v <= 6 {
		return v, true
	}
	if v == 7 {
		return 0, true
	}
	return 0, false
}

// addNumCheck true bila 1–7 (Senin–Minggu).
func addNumCheck(v int) bool {
	_, ok := numDay(v)
	return ok
}

// parseAbsenClock menerima "07.00" / "07:00" → menit sejak 00:00.
func parseAbsenClock(raw string) (int, bool) {
	s := strings.TrimSpace(strings.Replace(raw, ".", ":", 1))
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, false
	}
	h, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	m, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// formatAbsenDays "1,2,3,4,5" → "Senin–Jumat" (rentang kompak bila
// berurutan, daftar bila tidak).
func formatAbsenDays(daysCSV string) string {
	names := []string{"Min", "Sen", "Sel", "Rab", "Kam", "Jum", "Sab"}
	var days []int
	for _, part := range strings.Split(daysCSV, ",") {
		if v, err := strconv.Atoi(strings.TrimSpace(part)); err == nil && v >= 0 && v <= 6 {
			days = append(days, v)
		}
	}
	if len(days) == 0 {
		return "-"
	}
	sort.Ints(days)
	if len(days) == 7 {
		return "Setiap hari"
	}
	contiguous := true
	for i := 1; i < len(days); i++ {
		if days[i] != days[i-1]+1 {
			contiguous = false
			break
		}
	}
	if contiguous {
		return names[days[0]] + "–" + names[days[len(days)-1]]
	}
	var parts []string
	for _, d := range days {
		parts = append(parts, names[d])
	}
	return strings.Join(parts, ",")
}

// formatAbsenMin menit → "07.00".
func formatAbsenMin(min int) string {
	return fmt.Sprintf("%02d.%02d", min/60, min%60)
}
