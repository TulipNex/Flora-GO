// Package identity memusatkan semua logika identitas WhatsApp yang rawan
// konflik: dual-format LID vs PN (nomor HP), perbedaan device/agent (AD-JID),
// server "hosted", dan pencocokan pengirim yang bisa flip-flop antar format.
//
// Sebelum paket ini, pola "resolve LID↔PN", "bikin kunci kanonik pengirim",
// dan "bandingkan identitas mentah" disalin ulang di belasan file — satu
// tempat lupa cek LID saja bisa membuat owner/admin tak dikenali. Semua kode
// baru WAJIB memakai helper di sini alih-alih membandingkan JID mentah.
//
// Prinsip:
//   - Semua fungsi nil-safe (client/ctx/Store nil tidak panik).
//   - Semua resolve fail-closed: gagal resolve = bukan kecocokan.
//   - Normalize() menyamakan server hosted/AD sebelum dibandingkan.
package identity

import (
	"context"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/types"
)

// Candidate adalah identitas satu pengirim/entitas: JID utama plus JID
// alternatif bawaan event (Info.SenderAlt), yang biasanya memuat format
// seberang (PN↔LID) sehingga pencocokan tak butuh DB.
type Candidate struct {
	JID types.JID
	Alt types.JID
}

// Normalize menyeragamkan JID sebelum dibandingkan/disimpan:
//   - server "hosted"     → s.whatsapp.net (PN)
//   - server "hosted.lid" → lid (LID)
//   - AD-JID dibuang jadi bentuk bare (ToNonAD) supaya beda device/agent
//     tidak menggagalkan pencocokan.
//
// Server lain (grup, broadcast, newsletter, bot) dibiarkan apa adanya.
func Normalize(j types.JID) types.JID {
	switch j.Server {
	case types.HostedServer:
		j.Server = types.DefaultUserServer
	case types.HostedLIDServer:
		j.Server = types.HiddenUserServer
	}
	return j.ToNonAD()
}

// UserPart mengembalikan bagian user (bare) dari sebuah JID, sudah
// dinormalisasi. String kosong bila JID kosong.
func UserPart(j types.JID) string {
	return Normalize(j).User
}

// SameUser true bila dua JID merujuk user yang sama DALAM FORMAT YANG SAMA
// (keduanya PN atau keduanya LID). Untuk mencocokkan lintas format (PN vs LID)
// pakai Candidate.Matches / ResolvePN / Keys karena butuh JID alt atau peta
// LID↔PN.
func SameUser(a, b types.JID) bool {
	ua, ub := UserPart(a), UserPart(b)
	return ua != "" && ua == ub
}

// Matches true bila kandidat merujuk target, baik lewat JID utamanya maupun
// JID alternatifnya (format seberang). Pencocokan user-part, bukan string JID
// penuh, supaya device/agent/hosted tidak menggagalkan.
func (c Candidate) Matches(target types.JID) bool {
	return SameUser(c.JID, target) || (!c.Alt.IsEmpty() && SameUser(c.Alt, target))
}

// MatchesAny true bila kandidat cocok dengan salah satu target.
func (c Candidate) MatchesAny(targets ...types.JID) bool {
	for _, t := range targets {
		if c.Matches(t) {
			return true
		}
	}
	return false
}

// ResolvePN mengembalikan bentuk PN (nomor HP) kanonik dari sebuah identitas.
// Urutan: JID sudah PN → pakai JID; JID alt sudah PN → pakai alt; kalau JID
// adalah LID → resolve lewat peta LID↔PN whatsmeow. ok=false bila nomor HP
// tidak diketahui (fail-closed).
func ResolvePN(ctx context.Context, client *whatsmeow.Client, jid, alt types.JID) (types.JID, bool) {
	if n := Normalize(jid); n.Server == types.DefaultUserServer && n.User != "" {
		return n, true
	}
	if a := Normalize(alt); a.Server == types.DefaultUserServer && a.User != "" {
		return a, true
	}
	return resolveFromMap(ctx, client, jid, types.DefaultUserServer)
}

// ResolveLID mengembalikan bentuk LID kanonik dari sebuah identitas (kebalikan
// ResolvePN). ok=false bila LID tidak diketahui.
func ResolveLID(ctx context.Context, client *whatsmeow.Client, jid, alt types.JID) (types.JID, bool) {
	if n := Normalize(jid); n.Server == types.HiddenUserServer && n.User != "" {
		return n, true
	}
	if a := Normalize(alt); a.Server == types.HiddenUserServer && a.User != "" {
		return a, true
	}
	return resolveFromMap(ctx, client, jid, types.HiddenUserServer)
}

// resolveFromMap me-resolve format seberang lewat peta whatsmeow, hanya untuk
// JID yang server-nya bisa dipetakan (PN/LID). Fail-closed.
func resolveFromMap(ctx context.Context, client *whatsmeow.Client, jid types.JID, wantServer string) (types.JID, bool) {
	n := Normalize(jid)
	if n.IsEmpty() || n.User == "" {
		return types.JID{}, false
	}
	if n.Server != types.DefaultUserServer && n.Server != types.HiddenUserServer {
		return types.JID{}, false
	}
	if client == nil || client.Store == nil || client.Store.LIDs == nil {
		return types.JID{}, false
	}
	alt, err := client.Store.GetAltJID(ctx, n)
	if err != nil {
		return types.JID{}, false
	}
	alt = Normalize(alt)
	if alt.Server != wantServer || alt.User == "" {
		return types.JID{}, false
	}
	return alt, true
}

// CanonicalUser mengembalikan kunci user-part STABIL untuk sebuah identitas:
// nomor HP (PN) bila bisa diketahui, jika tidak jatuh ke user JID apa adanya.
// Dipakai untuk key sesi/penyimpanan supaya orang yang sama tidak terpecah
// jadi dua entri hanya karena flip-flop PN/LID.
func CanonicalUser(ctx context.Context, client *whatsmeow.Client, jid, alt types.JID) string {
	if pn, ok := ResolvePN(ctx, client, jid, alt); ok {
		return pn.User
	}
	return UserPart(jid)
}

// Keys mengembalikan SEMUA bentuk user-part yang mungkin merujuk identitas
// yang sama: dari JID, dari alt, dan dari hasil resolve LID↔PN. Urut, tanpa
// duplikat. Dipakai untuk pencocokan/penyimpanan yang tahan flip-flop PN/LID
// (mis. ban/trust): cukup cek irisan Keys pengirim dengan Keys target.
func Keys(ctx context.Context, client *whatsmeow.Client, jid, alt types.JID) []string {
	seen := make(map[string]bool, 4)
	out := make([]string, 0, 4)
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	add(UserPart(jid))
	add(UserPart(alt))
	if pn, ok := ResolvePN(ctx, client, jid, alt); ok {
		add(pn.User)
	}
	if lid, ok := ResolveLID(ctx, client, jid, alt); ok {
		add(lid.User)
	}
	return out
}

// Strings mengembalikan bentuk JID bare (tanpa AD) dari jid, alternatifnya,
// dan hasil resolve LID↔PN (bila client tersedia) — sudah dinormalisasi dan
// tanpa duplikat. Dipakai untuk mencocokkan nilai tersimpan yang berbentuk
// JID penuh (mis. banned_users.user_jid) maupun key kanonik.
func Strings(ctx context.Context, client *whatsmeow.Client, jid, alt types.JID) []string {
	seen := make(map[string]bool, 4)
	out := make([]string, 0, 4)
	add := func(j types.JID) {
		n := Normalize(j)
		if n.IsEmpty() || n.User == "" {
			return
		}
		if s := n.String(); !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	add(jid)
	add(alt)
	if pn, ok := ResolvePN(ctx, client, jid, alt); ok {
		add(pn)
	}
	if lid, ok := ResolveLID(ctx, client, jid, alt); ok {
		add(lid)
	}
	return out
}

// IsBot true bila jid adalah akun bot sendiri. Dibandingkan dengan DUA
// identitas bot (Store.ID = PN, Store.LID = LID) supaya tak salah saat grup
// memakai salah satu format. Nil-safe (false).
func IsBot(client *whatsmeow.Client, jid types.JID) bool {
	if client == nil || client.Store == nil {
		return false
	}
	u := UserPart(jid)
	if u == "" {
		return false
	}
	if id := client.Store.ID; id != nil && UserPart(*id) == u {
		return true
	}
	if lid := client.Store.LID; !lid.IsEmpty() && UserPart(lid) == u {
		return true
	}
	return false
}
