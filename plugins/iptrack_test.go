package plugins

import (
	"strings"
	"testing"
)

func TestBuildIPTrackText_SingleLine(t *testing.T) {
	info := ipInfo{
		IP:                 "93.184.216.34",
		City:               "Los Angeles",
		State:              "California",
		Country:            "United States",
		CountryCode:        "US",
		Continent:          "North America",
		IsEU:               false,
		Zip:                "90012",
		Latitude:           34.05220,
		Longitude:          -118.24370,
		Accuracy:           "100km",
		Timezone:           "America/Los_Angeles",
		UTCOffset:          "-07:00",
		CurrencyCode:       "USD",
		CallingCode:        "+1",
		RIR:                "ARIN",
		ASN:                15133,
		ASNOrg:             "EDGECAST",
		ASNType:            "hosting",
		ASNRoute:           "93.184.216.0/24",
		ASNCountry:         "US",
		ASNActive:          true,
		CompanyName:        "EDGECAST, Inc.",
		CompanyType:        "hosting",
		CompanyDomain:      "edgecast.com",
		CompanyNetwork:     "93.184.216.0/24",
		IsDatacenter:       true,
		DatacenterName:     "EdgeCast DC",
		IsVPN:              false,
		IsProxy:            false,
		IsTor:              false,
		IsMobile:           false,
		AbuseName:          "Abuse Dept",
		AbuseEmail:         "abuse@edgecast.com",
		ReverseDNS:         "custom.edgecast.com",
	}

	text := buildIPTrackText("93.184.216.34", "example.com", info)

	// 1. Pastikan tautan openstreetmap telah dihapus
	if strings.Contains(text, "openstreetmap.org") {
		t.Errorf("output tidak boleh memuat tautan openstreetmap, didapat:\n%s", text)
	}

	// 2. Pastikan tidak ada penggabungan baris dengan pemisah pipa '|'
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "•") {
			if strings.Contains(line, "|") {
				t.Errorf("baris bullet tidak boleh menggabungkan beberapa field dengan '|': %s", line)
			}
		}
	}

	// 3. Pastikan header section dan field kunci ada
	expectedFields := []string{
		"🌐 *IP TRACKER*",
		"📌 *Target*",
		"• Host : example.com",
		"• IP : 93.184.216.34",
		"• Reverse DNS : custom.edgecast.com",
		"• Registri (RIR) : ARIN",
		"📍 *Geolokasi*",
		"• Kota : Los Angeles",
		"• Provinsi / Wilayah : California",
		"• Negara : United States",
		"• Kode Negara : US",
		"• Benua : North America",
		"• Anggota EU : ❌ Tidak",
		"• Kode Pos : 90012",
		"• Garis Lintang : 34.05220",
		"• Garis Bujur : -118.24370",
		"• Akurasi : 100km",
		"• Zona Waktu : America/Los_Angeles",
		"• UTC Offset : -07:00",
		"• Mata Uang : USD",
		"• Kode Telepon : +1",
		"🏢 *Organisasi & ASN*",
		"• Perusahaan : EDGECAST, Inc.",
		"• Nomor ASN : AS15133",
		"• Status ASN : Ya",
		"🖥️ *Datacenter*",
		"• Nama Datacenter : EdgeCast DC",
		"🛡️ *Analisis Keamanan*",
		"• VPN : ❌ Tidak",
		"• Datacenter / Hosting : ✅ Ya",
		"⚠️ *Kontak Abuse*",
		"• Email : abuse@edgecast.com",
	}

	for _, expected := range expectedFields {
		if !strings.Contains(text, expected) {
			t.Errorf("output wajib memuat baris %q, didapat:\n%s", expected, text)
		}
	}
}

func TestResolveIPTrackTarget(t *testing.T) {
	// Raw IP -> host kosong
	ip, host, err := resolveIPTrackTarget("8.8.8.8")
	if err != nil || ip != "8.8.8.8" || host != "" {
		t.Errorf("gagal resolve raw IP: got ip=%q host=%q err=%v", ip, host, err)
	}

	// IP dengan URL path -> tetap IP mentah, host kosong
	ip, host, err = resolveIPTrackTarget("https://1.1.1.1/dns-query")
	if err != nil || ip != "1.1.1.1" || host != "" {
		t.Errorf("gagal resolve IP url: got ip=%q host=%q err=%v", ip, host, err)
	}

	// IP dengan path biasa -> tetap IP mentah, host kosong
	ip, host, err = resolveIPTrackTarget("1.1.1.1/path")
	if err != nil || ip != "1.1.1.1" || host != "" {
		t.Errorf("gagal resolve IP host with path: got ip=%q host=%q err=%v", ip, host, err)
	}
}
