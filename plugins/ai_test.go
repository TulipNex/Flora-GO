package plugins

import (
	"strings"
	"testing"

	"wabot-go/internal/ai"
)

// TestSplitModelRefs memastikan ".agent model del 1,2,4" dan "1 2 4" dibaca
// sama, karena nomor urut adalah fitur utama perintah ini.
func TestSplitModelRefs(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"1", []string{"1"}},
		{"1,2,4", []string{"1", "2", "4"}},
		{"1, 2, 4", []string{"1", "2", "4"}},
		{"1 2 4", []string{"1", "2", "4"}},
		{"ciora-vision-free, 3", []string{"ciora-vision-free", "3"}},
		{" 1 ,, 2 , ", []string{"1", "2"}},
		{" , ", nil},
		{"", nil},
	}
	for _, c := range cases {
		got := splitModelRefs(c.in)
		if len(got) != len(c.want) {
			t.Fatalf("splitModelRefs(%q) = %v, ingin %v", c.in, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("splitModelRefs(%q) = %v, ingin %v", c.in, got, c.want)
			}
		}
	}
}

// TestModelDeleteSummary memverifikasi ringkasan hapus model tetap satu bubble
// dan jujur: yang terhapus, yang dilewati, dan tidak mengklaim sukses saat
// tidak ada yang dihapus.
func TestModelDeleteSummary(t *testing.T) {
	single := modelDeleteSummary(ai.DelModelsResult{
		Provider: "ciora",
		Deleted:  []string{"ciora-ai-free"},
	})
	if !strings.Contains(single, "ciora-ai-free") || !strings.Contains(single, "ciora") {
		t.Fatalf("ringkasan satu model harus menyebut model + provider: %q", single)
	}
	if strings.Contains(single, "Dilewati") {
		t.Fatalf("tanpa skip tidak boleh ada blok Dilewati: %q", single)
	}

	batch := modelDeleteSummary(ai.DelModelsResult{
		Provider: "ciora",
		Deleted:  []string{"a", "b"},
		Skipped: []ai.SkippedModelRef{
			{Ref: "3", Reason: "sedang aktif — ganti dulu baru hapus"},
			{Ref: "99", Reason: "di luar rentang 1-6"},
		},
	})
	for _, want := range []string{"2 model", "a", "b", "Dilewati", "3 (sedang aktif", "99 (di luar rentang"} {
		if !strings.Contains(batch, want) {
			t.Fatalf("ringkasan batch kurang %q: %q", want, batch)
		}
	}

	none := modelDeleteSummary(ai.DelModelsResult{
		Provider: "ciora",
		Skipped:  []ai.SkippedModelRef{{Ref: "1", Reason: "satu-satunya model provider"}},
	})
	if !strings.Contains(none, "Tidak ada model yang dihapus") {
		t.Fatalf("tanpa hasil harus jujur, bukan klaim sukses: %q", none)
	}

	empty := modelDeleteSummary(ai.DelModelsResult{Provider: "kosong"})
	if !strings.Contains(empty, "Belum ada model terdaftar") {
		t.Fatalf("provider tanpa model harus dijelaskan: %q", empty)
	}
}
