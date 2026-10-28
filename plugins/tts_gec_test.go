package plugins

import (
	"regexp"
	"testing"
)

// Vektor di bawah dihasilkan langsung oleh edge-tts 7.3.1
// (edge_tts.drm.generate_sec_ms_gec_v2) supaya port Go ini tidak "benar
// sendiri". Kalau Microsoft mengganti skema token lagi, tes ini tetap hijau
// (algoritmanya memang belum berubah) — untuk itu ada smoke test live di
// tts_smoke_test.go.
func TestTTSGECV2MatchesEdgeTTS(t *testing.T) {
	cases := []struct {
		unix float64
		want string
	}{
		{1700000000, "2CD23A2CA0B2595A-F3C846C370E05F9CC83ACC5F995C5F5D87A4D0F14CE8936A0F902E64099E38A2"},
		{1700000100, "0919602B5750900A-EC9DD394F23CB5635CF62A0E8E24136DCD32B26D10C543B10082E482D7516397"},
		{0, "52BC7A17B50DFEC9-28C4394775C8751CD2F6859C030C06DB1489ACAB3B3A7651831FC26895177ED6"},
		{1759999999.7, "6D50DDDB91812C33-DD68EFB01898B0750C9797D51A5FF93471DFDAE6888A1039D532CBDCCE54545D"},
		{1761200000, "C18EDA1D9D6E9E1A-020D623AC0DF9B38882CE331BEBD65FD080406B574104C1D939CD3CC8DAA5662"},
	}
	for _, tc := range cases {
		if got := ttsGECv2(tc.unix); got != tc.want {
			t.Errorf("ttsGECv2(%v)\n got: %s\nwant: %s", tc.unix, got, tc.want)
		}
	}
}

// Token harus berbentuk <16 heks>-<64 heks> huruf besar; server menolak
// format lain (dan v1 = 64 heks tanpa CS64 = 403).
var ttsGECTokenRe = regexp.MustCompile(`^[0-9A-F]{16}-[0-9A-F]{64}$`)

func TestTTSGECTokenShape(t *testing.T) {
	if !ttsGECTokenRe.MatchString(ttsGenerateGEC()) {
		t.Fatalf("token tidak sesuai format v2: %q", ttsGenerateGEC())
	}
}

// Versi harus berawalan "2-": dengan "1-" server membalas 403 walau tokennya
// sudah v2.
func TestTTSGECVersionIsV2(t *testing.T) {
	if ttsGECVersion != "2-143.0.3650.75" {
		t.Fatalf("ttsGECVersion = %q, ingin 2-143.0.3650.75", ttsGECVersion)
	}
}

// Token berganti setiap 5 menit (jendela waktu server), dan stabil di dalam
// satu jendela — ini yang membuat dua percobaan beruntun tidak boros.
func TestTTSGECRoundsTo5Minutes(t *testing.T) {
	// 1700000100 habis dibagi 300 (awal jendela), jadi +299 masih jendela
	// yang sama dan +300 sudah jendela berikutnya.
	base := ttsGECv2(1700000100)
	if same := ttsGECv2(1700000100 + 299); same != base {
		t.Fatalf("token berubah dalam jendela yang sama: %s vs %s", base, same)
	}
	if next := ttsGECv2(1700000100 + 300); next == base {
		t.Fatalf("token tidak berubah setelah 5 menit: %s", base)
	}
}
