package store

import (
	"context"
	"fmt"
	"sync"
	"time"

	"wabot-go/internal/logfmt"

	"github.com/polymorfa/hypermeow/types"
)

// IsGroup mengecek apakah chat tujuan pesan ini adalah grup (bukan chat pribadi).
func (mctx *MessageContext) IsGroup() bool {
	return mctx.Chat.Server == types.GroupServer
}

// groupInfoCache menyimpan hasil GetGroupInfo per grup dengan TTL SINGKAT
// (10 detik). Satu command admin (kick/promote/tagall/antilink) memanggil
// GroupInfo 2–3x berurutan — di grup 1000 member tiap panggilan mengunduh
// daftar penuh. Cache ini memangkasnya jadi 1x per jendela, dan semua
// pemanggil (IsSenderAdmin, IsBotAdmin, TagAll, dsb) otomatis hemat tanpa
// diubah. TTL 10 detik dipilih supaya status admin tetap segar (perubahan
// dari perangkat lain terlihat max 10 detik kemudian) — jauh di bawah risiko
// basi, jauh di atas durasi satu command. Hanya read; pemanggil tidak boleh
// memutasi pointer hasil.
var (
	groupInfoCacheMu sync.Mutex
	groupInfoCache   = map[string]groupInfoCacheEntry{}
)

type groupInfoCacheEntry struct {
	info *types.GroupInfo
	at   time.Time
}

const groupInfoCacheTTL = 10 * time.Second

// GroupInfo mengambil metadata grup terbaru langsung dari WhatsApp
// (nama, deskripsi, daftar participant + status admin masing-masing, dll).
// Hasil di-cache 10 detik per grup (lihat groupInfoCache) — pemanggil yang
// butuh data dijamin-segar (mis. setelah promote) harus menunggu jendela
// lewat atau memakai GetGroupInfo langsung, tapi tidak ada pemanggil seperti
// itu saat ini.
func (mctx *MessageContext) GroupInfo(ctx context.Context) (*types.GroupInfo, error) {
	if !mctx.IsGroup() {
		return nil, fmt.Errorf("chat ini bukan grup")
	}
	key := mctx.Chat.String()
	groupInfoCacheMu.Lock()
	if e, ok := groupInfoCache[key]; ok && time.Since(e.at) < groupInfoCacheTTL {
		groupInfoCacheMu.Unlock()
		return e.info, nil
	}
	groupInfoCacheMu.Unlock()
	info, err := mctx.Client.GetGroupInfo(ctx, mctx.Chat)
	if err != nil {
		return nil, err
	}
	groupInfoCacheMu.Lock()
	groupInfoCache[key] = groupInfoCacheEntry{info: info, at: time.Now()}
	// Bersihkan entri basi oportunistik supaya map tidak tumbuh tanpa batas
	// (jumlah grup yang dilayani bot memang terbatas, ini jaring pengaman).
	for k, e := range groupInfoCache {
		if time.Since(e.at) > time.Minute {
			delete(groupInfoCache, k)
		}
	}
	groupInfoCacheMu.Unlock()
	return info, nil
}

// ParticipantMatchesJID membandingkan satu participant grup dengan sebuah JID
// target, TANPA terjebak masalah dual-format WhatsApp (LID vs nomor telepon/PN).
// Sebuah participant dan sebuah JID dianggap "orang yang sama" kalau salah satu
// dari: .User mentah cocok, ATAU field PhoneNumber cocok, ATAU field LID cocok.
// Ini perlu karena di grup dengan AddressingMode "lid", p.JID bisa berupa
// @lid sementara JID yang mau dibandingkan (misal dari Store.ID atau mention)
// berupa @s.whatsapp.net, atau sebaliknya.
//
// Di-export (sebelumnya private) karena audit duplikasi menemukan salinan
// logika ini yang identik baris per baris di plugins/groupmanage.go
// (participantMatchesJIDLocal). Dua salinan logika admin-check kritis begini
// rawan menyimpang diam-diam kalau cuma salah satu yang diperbaiki — sekarang
// ada SATU sumber kebenaran, dipakai oleh store maupun plugins.
func ParticipantMatchesJID(p types.GroupParticipant, target types.JID) bool {
	if p.JID.User == target.User {
		return true
	}
	if !p.PhoneNumber.IsEmpty() && p.PhoneNumber.User == target.User {
		return true
	}
	if !p.LID.IsEmpty() && p.LID.User == target.User {
		return true
	}
	return false
}

// IsSenderAdmin mengecek apakah pengirim pesan (mctx.Sender) adalah admin
// atau superadmin di grup ini. Dipakai untuk gerbang akses command seperti
// .kick, .promote, .setname, dll.
func (mctx *MessageContext) IsSenderAdmin(ctx context.Context) (bool, error) {
	info, err := mctx.GroupInfo(ctx)
	if err != nil {
		return false, err
	}
	for _, p := range info.Participants {
		if ParticipantMatchesJID(p, mctx.Sender) {
			return p.IsAdmin || p.IsSuperAdmin, nil
		}
	}
	return false, nil
}

// IsBotAdmin mengecek apakah bot sendiri adalah admin di grup ini — dibutuhkan
// sebelum melakukan aksi seperti kick/promote/demote/setname, karena WhatsApp
// menolak aksi tersebut kalau bot bukan admin.
//
// Dibandingkan dengan DUA identitas bot sekaligus (Store.ID dan Store.LID kalau
// ada), karena WhatsApp bisa mendaftarkan bot di daftar participant grup memakai
// salah satu dari dua format itu tergantung AddressingMode grup — membandingkan
// cuma satu bisa false-negative walau bot sungguhan admin.
func (mctx *MessageContext) IsBotAdmin(ctx context.Context) (bool, error) {
	info, err := mctx.GroupInfo(ctx)
	if err != nil {
		return false, err
	}

	botID := mctx.Client.Store.ID
	botLID := mctx.Client.Store.LID // types.JID kosong (User == "") kalau belum ada LID

	for _, p := range info.Participants {
		matched := false
		if botID != nil && ParticipantMatchesJID(p, *botID) {
			matched = true
		}
		if !matched && !botLID.IsEmpty() && ParticipantMatchesJID(p, botLID) {
			matched = true
		}
		if matched {
			return p.IsAdmin || p.IsSuperAdmin, nil
		}
	}

	// Bot tidak ditemukan sama sekali di daftar participant — biasanya berarti
	// cache participant grup basi. Bukan error fatal, tapi dicatat supaya
	// kelihatan di log kalau ini yang jadi penyebab false-negative.
	logfmt.Warn("IsBotAdmin: bot (ID=%v, LID=%v) tidak ditemukan di participant grup %s\n", botID, botLID, mctx.Chat)
	return false, nil
}

// MentionedJIDs mengambil daftar JID yang di-mention (@tag) di pesan ini,
// dipakai oleh command seperti .kick @user / .promote @user.
// Delegasi ke ExtractMentionedJIDs (trigger_extract.go) supaya pemrosesan
// unwrap + cakupan tipe pesan cuma ada di satu tempat (audit A2).
func (mctx *MessageContext) MentionedJIDs() []string {
	return ExtractMentionedJIDs(mctx.RawEvt)
}

// QuotedParticipant mengambil JID pengirim pesan yang di-reply (kalau command
// dipakai lewat reply, bukan mention langsung), misal ".kick" sambil reply
// pesan target. Mengembalikan string kosong kalau tidak ada pesan yang di-reply.
// Delegasi ke ExtractQuotedParticipant — lihat catatan di MentionedJIDs.
func (mctx *MessageContext) QuotedParticipant() string {
	return ExtractQuotedParticipant(mctx.RawEvt)
}
