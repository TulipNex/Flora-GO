package identity

import (
	"context"
	"testing"

	"github.com/polymorfa/hypermeow/types"
)

// BenchmarkNormalize mengukur biaya normalisasi JID (dipanggil sangat sering
// di jalur pesan masuk).
func BenchmarkNormalize(b *testing.B) {
	j := types.JID{User: "628123456789", Server: "s.whatsapp.net"}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = Normalize(j)
	}
}

// BenchmarkSameUser mengukur perbandingan user-part.
func BenchmarkSameUser(b *testing.B) {
	a := types.JID{User: "628123456789", Server: "s.whatsapp.net"}
	c := types.JID{User: "628123456789", Server: "s.whatsapp.net"}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = SameUser(a, c)
	}
}

// BenchmarkKeys mengukur pembuatan daftar kunci identitas (tanpa resolve
// DB; client nil → fail-closed, tetapi tetap mengukur alokasi map/slice).
func BenchmarkKeys(b *testing.B) {
	j := types.JID{User: "628123456789", Server: "s.whatsapp.net"}
	alt := types.JID{User: "123456789", Server: types.HiddenUserServer}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = Keys(context.Background(), nil, j, alt)
	}
}
