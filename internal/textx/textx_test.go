package textx

import "testing"

func TestTruncateRunes(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		max       int
		trim      bool
		wantOut   string
		wantTotal int
		wantCut   bool
	}{
		{"tak terpotong", "halo", 10, false, "halo", 4, false},
		{"terpotong", "halo dunia", 4, false, "halo", 10, true},
		{"trim lalu potong", "  halo dunia  ", 4, true, "halo", 10, true},
		{"trim pas", "  halo  ", 10, true, "halo", 4, false},
		{"multibyte", "añébçdé", 3, false, "añé", 7, true},
		{"max nol", "abc", 0, false, "", 3, true},
	}
	for _, c := range cases {
		out, total, cut := TruncateRunes(c.in, c.max, c.trim)
		if out != c.wantOut || total != c.wantTotal || cut != c.wantCut {
			t.Errorf("%s: TruncateRunes(%q,%d,%v) = %q,%d,%v; mau %q,%d,%v",
				c.name, c.in, c.max, c.trim, out, total, cut, c.wantOut, c.wantTotal, c.wantCut)
		}
	}
}
