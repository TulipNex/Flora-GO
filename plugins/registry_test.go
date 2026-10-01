package plugins

import "testing"

// TestNoCommandAliasCollision memastikan tidak ada dua command (nama maupun
// alias) yang menempati key registry yang sama. Registry.Register menimpa
// diam-diam (pendaftar terakhir menang) — tanpa test ini, tabrakan seperti
// ".tt" (tiktok vs tictactoe, Sep 2026: download TikTok via .tt rusak tanpa
// error) hanya ketahuan saat user komplain.
//
// Test jalan per-registrar (bukan hasil akhir gabungan) supaya penimpaan
// terdeteksi — di map gabungan, yang kalah sudah hilang tak berbekas.
func TestNoCommandAliasCollision(t *testing.T) {
	used := map[string]string{} // key registry -> nama command pemilik pertama
	for _, fn := range registrars {
		r := NewRegistry()
		fn(r)
		for key, cmd := range r.commands {
			if owner, dup := used[key]; dup {
				t.Errorf("key %q dipakai ganda: command %q vs %q", key, owner, cmd.Name)
				continue
			}
			used[key] = cmd.Name
		}
	}
}

// TestTikTokKeepsTTAlias adalah regresi spesifik insiden Sep 2026: alias
// ".tt" adalah milik downloader TikTok; tictactoe memakai ".ttt"/".tc".
func TestTikTokKeepsTTAlias(t *testing.T) {
	r := NewRegistry()
	RegisterAll(r)

	cmd, ok := r.Get("tt")
	if !ok || cmd.Name != "tiktok" {
		name := ""
		if ok {
			name = cmd.Name
		}
		t.Fatalf(".tt harus milik tiktok, dapat %q", name)
	}
	for _, alias := range []string{"ttt", "tc", "tictactoe"} {
		g, ok := r.Get(alias)
		if !ok || g.Name != "tictactoe" {
			t.Fatalf("%q harus milik tictactoe", alias)
		}
	}
}
