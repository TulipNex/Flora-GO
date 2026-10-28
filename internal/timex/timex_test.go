package timex

import (
	"strings"
	"testing"
	"time"
)

// TestParseZone memastikan nilai BOT_TIMEZONE diterjemahkan dengan benar:
// nama IANA (format baru), alias lama WIB/WITA/WIT, default saat kosong, dan
// fallback aman saat nilainya tidak dikenal.
func TestParseZone(t *testing.T) {
	cases := []struct {
		raw       string
		wantLabel string
		wantZone  string
		offsetHrs int
	}{
		{"", labelWITA, "Asia/Makassar", 8},
		{"Asia/Makassar", labelWITA, "Asia/Makassar", 8},
		{"Asia/Jakarta", labelWIB, "Asia/Jakarta", 7},
		{"Asia/Pontianak", labelWIB, "Asia/Pontianak", 7},
		{"Asia/Jayapura", labelWIT, "Asia/Jayapura", 9},
		{"WIB", labelWIB, "Asia/Jakarta", 7},
		{"wita", labelWITA, "Asia/Makassar", 8},
		{"WIT", labelWIT, "Asia/Jayapura", 9},
		{"  Asia/Jakarta  ", labelWIB, "Asia/Jakarta", 7},
		{"Bukan/Zona", labelWITA, "Asia/Makassar", 8},
		{"", labelWITA, "Asia/Makassar", 8},
	}

	for _, tc := range cases {
		label, loc := parseZone(tc.raw)
		if label != tc.wantLabel {
			t.Errorf("parseZone(%q) label = %q, mau %q", tc.raw, label, tc.wantLabel)
		}
		if got := loc.String(); got != tc.wantZone {
			t.Errorf("parseZone(%q) zona = %q, mau %q", tc.raw, got, tc.wantZone)
		}
		// Offset diperiksa lewat tanggal tetap supaya tidak bergantung waktu uji.
		sample := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC).In(loc)
		_, off := sample.Zone()
		if off != tc.offsetHrs*3600 {
			t.Errorf("parseZone(%q) offset = %d detik, mau %d", tc.raw, off, tc.offsetHrs*3600)
		}
	}
}

// TestParseZoneForeign: zona di luar Indonesia memakai singkatan saat ini
// (JST untuk Asia/Tokyo) supaya label tetap pendek di pesan owner.
func TestParseZoneForeign(t *testing.T) {
	label, loc := parseZone("Asia/Tokyo")
	if loc.String() != "Asia/Tokyo" {
		t.Fatalf("zona = %q", loc.String())
	}
	if !strings.Contains(label, "JST") && label != "Asia/Tokyo" {
		t.Errorf("label = %q, mau singkatan JST atau nama IANA", label)
	}
}

// TestZoneAndNameKonsisten memastikan jalur publik (Zone/Name/Now) memakai
// hasil parser yang sama dan Now berada di lokasi aktif.
func TestZoneAndNameKonsisten(t *testing.T) {
	if got := Now().Location(); got != Zone() {
		t.Errorf("Now() lokasi = %v, Zone() = %v", got, Zone())
	}
	if Name() == "" {
		t.Error("Name() kosong")
	}
	if Zone() == nil {
		t.Fatal("Zone() nil")
	}
}
