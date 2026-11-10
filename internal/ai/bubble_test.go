package ai

import (
	"strings"
	"testing"
)

func TestSanitizeForWhatsAppIndentedList(t *testing.T) {
	input := `Halo Mitra! Berikut adalah beberapa pilihan penjual es pisang ijo di Kendari yang bisa kamu kunjungi:

* **Kedai Es Pisang Ijo Daun**
 * **Alamat:** Jalan Komjen Dr.H.M Jasin, Mokoau, Kota Kendari
* **Pisang Ijo AMANDA Wua-wua**
  * **Alamat:** Jalan Jenderal M.T. Haryono, Bende, Kota Kendari
* **Pisang Ijo Amanda Bypass**
   - **Alamat:** Jalan Brigjen M. Yoenoes, Bende, Kota Kendari
* **Pisang Ijo Zahra**
	• **Alamat:** Lorong H. Lamambo, Baruga, Kota Kendari
* **Es Teler Koni - Pisang Ijo**
 1. **Alamat:** Jalan Jendral Ahmad Yani No.51, Bende, Kota Kendari`

	got := SanitizeForWhatsApp(input)

	// Pastikan tidak ada baris yang diawali spasi lalu marker (*, -, •, 1.)
	lines := strings.Split(got, "\n")
	for _, l := range lines {
		if strings.HasPrefix(l, " *") || strings.HasPrefix(l, "  *") ||
			strings.HasPrefix(l, " -") || strings.HasPrefix(l, "\t•") ||
			strings.HasPrefix(l, " 1.") {
			t.Fatalf("masih ada indentasi di depan list marker: %q", l)
		}
	}

	// Pastikan baris alamat diawali langsung dengan "* *Alamat:*"
	if !strings.Contains(got, "\n* *Alamat:* Jalan Komjen") {
		t.Fatalf("baris alamat harus diawali '* *Alamat:*', dapat:\n%s", got)
	}
	if !strings.Contains(got, "\n* *Alamat:* Jalan Jenderal") {
		t.Fatalf("baris alamat Amanda Wua-wua harus diawali '* *Alamat:*', dapat:\n%s", got)
	}
	if !strings.Contains(got, "\n- *Alamat:* Jalan Brigjen") {
		t.Fatalf("baris alamat Amanda Bypass harus diawali '- *Alamat:*', dapat:\n%s", got)
	}
	if !strings.Contains(got, "\n• *Alamat:* Lorong H. Lamambo") {
		t.Fatalf("baris alamat Zahra harus diawali '• *Alamat:*', dapat:\n%s", got)
	}
	if !strings.Contains(got, "\n1. *Alamat:* Jalan Jendral Ahmad Yani") {
		t.Fatalf("baris alamat Koni harus diawali '1. *Alamat:*', dapat:\n%s", got)
	}
}

func TestSanitizeForWhatsAppAsteriskInParentheses(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{
			in:   "*1. Integrasi AI yang Lebih Dalam (*Artificial Intelligence*)*",
			want: "*1. Integrasi AI yang Lebih Dalam (Artificial Intelligence)*",
		},
		{
			in:   "**1. Integrasi AI yang Lebih Dalam (*Artificial Intelligence*)**",
			want: "*1. Integrasi AI yang Lebih Dalam (Artificial Intelligence)*",
		},
		{
			in:   "* *Evolusi Menuju Web Spasial (*Metaverse* dan Realitas Imersif)*",
			want: "* *Evolusi Menuju Web Spasial (Metaverse dan Realitas Imersif)*",
		},
		{
			in:   "* *Perkembangan *Internet of Things* (*IoT*) dan Rumah Pintar*",
			want: "* *Perkembangan *Internet of Things* (IoT) dan Rumah Pintar*",
		},
	}

	for _, c := range cases {
		got := SanitizeForWhatsApp(c.in)
		if got != c.want {
			t.Fatalf("SanitizeForWhatsApp(%q) =\n%q, mau:\n%q", c.in, got, c.want)
		}
	}
}
