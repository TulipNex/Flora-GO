package plugins

import (
	"context"
	"fmt"
	"strings"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/types"

	"wabot-go/internal/store"
)

// ---------------------------------------------------------------------------
// Helper bersama SPR/SBPR: resolve target, kanonikalisasi, ownID
// ---------------------------------------------------------------------------

// sprParseMentions memetakan @user → JID beneran dari metadata mention.
func sprParseMentions(mctx *store.MessageContext) []types.JID {
	var mentions []types.JID
	for _, m := range mctx.MentionedJIDs() {
		if jid, err := types.ParseJID(m); err == nil {
			mentions = append(mentions, jid)
		}
	}
	return mentions
}

// sprResolveTokenList mengubah token-token target mentah jadi JID.
// Token yang tak dikenal dikembalikan di fails (bukan error fatal).
func sprResolveTokenList(mctx *store.MessageContext, mentions []types.JID, toks []string) ([]types.JID, []string) {
	var jids []types.JID
	var fails []string
	for _, t := range toks {
		jid, err := sprResolveToken(mctx, mentions, t)
		if err != nil {
			fails = append(fails, t)
			continue
		}
		jids = append(jids, jid)
	}
	return jids, fails
}

// sprIsBotJID true bila JID adalah akun bot sendiri — delegasi ke
// store.IsBotJID (toleran dual-format LID/PN).
func sprIsBotJID(client *whatsmeow.Client, jid types.JID) bool {
	return store.IsBotJID(client, jid)
}

// sprCanonicalJID memetakan JID ke bentuk participant grup (@lid/@pn sesuai
// mode grup). False bila bukan member.
func sprCanonicalJID(info *types.GroupInfo, jid types.JID) (types.JID, bool) {
	for _, pt := range info.Participants {
		if store.ParticipantMatchesJID(pt, jid) {
			return pt.JID, true
		}
	}
	return types.JID{}, false
}

// sprOwnID memilih identitas pengirim mengikuti addressing mode grup
// (cermin logika SendMessage publik). False bila identitas belum siap.
func sprOwnID(client *whatsmeow.Client, info *types.GroupInfo) (types.JID, bool) {
	var ownID types.JID
	if client.Store.ID != nil {
		ownID = *client.Store.ID
	}
	if info.AddressingMode == types.AddressingModeLID && !client.Store.LID.IsEmpty() {
		ownID = client.Store.LID
	}
	return ownID, !ownID.IsEmpty()
}

// sprBuildTargets membangun daftar target final dari hasil parse + resolve:
//   - hasExclude: basis = includes (bila hasInclude) atau semua member,
//     minus excludes.
//   - selain itu: includes apa adanya.
//
// Tiap target dikanonikalisasi ke JID participant, dideduplikasi, dan bot
// sendiri dibuang. Balikan kedua = jumlah token yang dilewati
// (bukan member/duplikat/bot); token tak dikenal (fails) dihitung pemanggil.
func sprBuildTargets(client *whatsmeow.Client, info *types.GroupInfo, p sprParsed, includes, excludes []types.JID) ([]types.JID, int) {
	var targets []types.JID
	seen := map[string]bool{}
	skipped := 0
	add := func(jid types.JID) {
		if jid.IsEmpty() || sprIsBotJID(client, jid) {
			skipped++
			return
		}
		c, ok := sprCanonicalJID(info, jid)
		if !ok {
			skipped++
			return
		}
		key := c.ToNonAD().User + "/" + string(c.ToNonAD().Server)
		if seen[key] {
			skipped++
			return
		}
		seen[key] = true
		targets = append(targets, c)
	}
	if p.hasExclude {
		isExcluded := func(pt types.GroupParticipant) bool {
			for _, ex := range excludes {
				if store.ParticipantMatchesJID(pt, ex) {
					return true
				}
			}
			return false
		}
		if p.hasInclude {
			for _, jid := range includes {
				add(jid)
			}
		} else {
			for _, pt := range info.Participants {
				if sprIsBotJID(client, pt.JID) || isExcluded(pt) {
					continue
				}
				add(pt.JID)
			}
		}
	} else {
		for _, jid := range includes {
			add(jid)
		}
	}
	return targets, skipped
}

// ---------------------------------------------------------------------------
// Parsing argumen (murni, diuji unit di spr_test.go)
// ---------------------------------------------------------------------------

// sprParsed hasil parse RawArgs .spr.
type sprParsed struct {
	isDebug    bool
	hasInclude bool
	hasExclude bool
	includes   []string // token target mentah (sesudah -i / posisi default)
	excludes   []string // token target mentah (sesudah -e)
	message    string   // pesan asli (sudah tanpa segmen decoy)
	decoy      string
	hasDecoy   bool
	quotedOnly bool // tak ada token target sama sekali → pakai reply
}

// sprIsTargetToken true bila token terlihat seperti target: @mention,
// @me, nomor/LID digit, atau JID eksplisit.
func sprIsTargetToken(tok string) bool {
	t := strings.Trim(tok, ",;")
	if t == "" {
		return false
	}
	if strings.HasPrefix(t, "@") {
		return len(t) > 1
	}
	if strings.Contains(t, "@") {
		return true // mis. 123@lid / 123@s.whatsapp.net
	}
	digits := strings.TrimPrefix(t, "+")
	if len(digits) >= 8 {
		allDigit := true
		for _, r := range digits {
			if r < '0' || r > '9' {
				allDigit = false
				break
			}
		}
		if allDigit {
			return true
		}
	}
	return false
}

// parseSPRArgs memecah RawArgs menjadi flag, target, dan pesan.
// Aturan: -i menampung token-target sampai flag berikutnya / token biasa;
// -e sama; tanpa flag, token-target di awal jadi target default; sisanya
// pesan (diambil dari raw untuk menjaga whitespace/baris baru asli).
// Decoy dipisah di karakter '|' PERTAMA pada pesan.
func parseSPRArgs(raw string) sprParsed {
	tokens := strings.Fields(raw)
	if len(tokens) == 1 && strings.EqualFold(tokens[0], "debug") {
		return sprParsed{isDebug: true}
	}
	var p sprParsed
	consumed := 0 // jumlah token terpakai (flag + target)
	i := 0
	inInclude, inExclude := false, false // posisi flag terakhir terlihat
	sawFlag := false
	for i < len(tokens) {
		t := tokens[i]
		if t == "-i" {
			p.hasInclude, inInclude, inExclude, sawFlag = true, true, false, true
			consumed++
			i++
			continue
		}
		if t == "-e" {
			p.hasExclude, inInclude, inExclude, sawFlag = true, false, true, true
			consumed++
			i++
			continue
		}
		if !sprIsTargetToken(t) {
			break // mulai pesan
		}
		// Token target tanpa flag -i/-e yang sedang aktif HANYA sah di
		// posisi awal (target default). Kalau sudah ada flag tapi...
		// (inInclude/inExclude menampungnya di bawah).
		if !sawFlag && (inInclude || inExclude) {
			break
		}
		switch {
		case inExclude:
			p.excludes = append(p.excludes, t)
		case inInclude:
			p.includes = append(p.includes, t)
		default:
			// Tanpa flag: hanya deretan awal yang jadi target default.
			p.includes = append(p.includes, t)
		}
		consumed++
		i++
	}
	if len(p.includes) == 0 && len(p.excludes) == 0 && !p.hasInclude && !p.hasExclude {
		p.quotedOnly = true
	}
	// Iris pesan dari raw mentah mulai sesudah token terpakai, supaya
	// baris baru/spasi ganda di dalam pesan tidak hancur.
	msgRaw := raw
	off := 0
	for k := 0; k < consumed; k++ {
		pos := strings.Index(msgRaw[off:], tokens[k])
		if pos < 0 {
			break
		}
		off += pos + len(tokens[k])
	}
	msgRaw = strings.TrimSpace(msgRaw[off:])
	if idx := strings.Index(msgRaw, "|"); idx >= 0 {
		p.message = strings.TrimSpace(msgRaw[:idx])
		p.decoy = strings.TrimSpace(msgRaw[idx+1:])
		p.hasDecoy = true
	} else {
		p.message = msgRaw
	}
	return p
}

// sprResolveToken mengubah satu token target jadi JID.
func sprResolveToken(mctx *store.MessageContext, mentions []types.JID, tok string) (types.JID, error) {
	t := strings.Trim(tok, ",;")
	if strings.EqualFold(t, "@me") {
		return mctx.Sender, nil
	}
	if strings.HasPrefix(t, "@") {
		body := t[1:]
		if strings.Contains(body, "@") {
			return types.ParseJID(body) // JID eksplisit, mis. 123@lid
		}
		for _, mj := range mentions {
			if mj.User == body {
				return mj, nil
			}
		}
		if sprIsDigits(body) {
			// Mention mentah tanpa metadata (ketik manual): anggap PN.
			// Grup @lid wajib pakai @mention asli (lihat bantuan).
			return types.NewJID(body, types.DefaultUserServer), nil
		}
		return types.JID{}, fmt.Errorf("target %q tak dikenal (pakai @mention dari daftar member)", tok)
	}
	if strings.Contains(t, "@") {
		return types.ParseJID(t)
	}
	digits := strings.TrimPrefix(t, "+")
	if sprIsDigits(digits) {
		return types.NewJID(digits, types.DefaultUserServer), nil
	}
	return types.JID{}, fmt.Errorf("target %q tak dikenal", tok)
}

func sprIsDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// sprExtractGroupFlag memindai pasangan "-g <JID grup>" di argumen mentah,
// melepasnya, dan mengembalikan sisa argumen + JID grup (kosong = tanpa
// flag, jalan di chat asal). "-g" tanpa pasangan JID grup valid → error
// (lebih baik menolak daripada salah sasaran diam-diam). Posisi bebas;
// pemotongan dari raw mentah supaya whitespace pesan tetap utuh.
func sprExtractGroupFlag(raw string) (string, types.JID, error) {
	tokens := strings.Fields(raw)
	idx := -1
	for i, t := range tokens {
		if t == "-g" {
			idx = i
			break
		}
	}
	if idx < 0 {
		return raw, types.JID{}, nil
	}
	if idx+1 >= len(tokens) {
		return "", types.JID{}, fmt.Errorf("flag -g butuh JID grup, contoh: -g 1203...@g.us (lihat .listgrup)")
	}
	jid, perr := types.ParseJID(tokens[idx+1])
	if perr != nil || jid.Server != types.GroupServer || jid.User == "" {
		return "", types.JID{}, fmt.Errorf("nilai -g harus JID grup (@g.us), contoh: -g 1203...@g.us (lihat .listgrup)")
	}
	// Potong pasangan dari raw mentah (pindai sekuensial agar token
	// yang teksnya kembar tetap sejajar).
	off, start := 0, -1
	for k := 0; k <= idx+1; k++ {
		pos := strings.Index(raw[off:], tokens[k])
		if pos < 0 {
			kept := append(append([]string{}, tokens[:idx]...), tokens[idx+2:]...)
			return strings.Join(kept, " "), jid, nil
		}
		if k == idx {
			start = off + pos
		}
		off += pos + len(tokens[k])
	}
	return strings.TrimSpace(strings.TrimRight(raw[:start], " \t\r\n") + " " + strings.TrimLeft(raw[off:], " \t\r\n")), jid, nil
}

// sprGroupInfo mengambil metadata grup tujuan: cache 10 detik bila chat
// asal (hemat fetch), langsung bila -g (cache milik chat lain tak berlaku).
func sprGroupInfo(ctx context.Context, mctx *store.MessageContext, chat types.JID) (*types.GroupInfo, error) {
	if chat == mctx.Chat {
		return mctx.GroupInfo(ctx)
	}
	return mctx.Client.GetGroupInfo(ctx, chat)
}
