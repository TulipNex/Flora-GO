package plugins

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"wabot-go/internal/ai"
	"wabot-go/internal/config"
	"wabot-go/internal/logfmt"
	"wabot-go/internal/store"
	"wabot-go/internal/timex"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types"
	"google.golang.org/protobuf/proto"
)

func init() {
	registerPlugin(RegisterAI)
}

func RegisterAI(r *Registry) {
	r.Register(Command{
		Name:        "agent",
		Aliases:     []string{"ai"},
		Description: "Kelola Flora (.agent model/provider/status/tools/trust/trustlist/reset/voice)",
		Category:    CategoryOwner,
		Handler:     handleAgentCommand,
	})
}

// handleAgentCommand menangani command ADMIN Flora — tanya-jawab via
// command SUDAH DIHAPUS, ngobrol hanya lewat trigger natural (sebut nama
// Flora / reply bubble bot / kirim stiker — lihat handleAITrigger).
// Gerbang pertama: hanya owner + user terpercaya yang dilayani.
//
// Batas owner vs trusted: model/provider/status/trust/untrust/trustlist/
// reset -memory KHUSUS owner; user terpercaya hanya boleh ".agent reset"
// biasa (sesi sendiri). Pengaturan provider/model ini GLOBAL (semua user, persist ke
// SQLite — lihat internal/ai/providers.go), jadi wajar hanya owner yang
// boleh mengubahnya.
func handleAgentCommand(ctx context.Context, mctx *store.MessageContext) error {
	if !IsOwnerOrTrusted(ctx, mctx) {
		return store.ReplyText(ctx, mctx, "Flora hanya melayani owner dan user yang dipercaya.")
	}
	if len(mctx.Args) == 0 {
		return store.ReplyText(ctx, mctx, agentUsage())
	}
	switch strings.ToLower(mctx.Args[0]) {
	case "provider":
		return handleProviderSubcommand(ctx, mctx)
	case "model":
		return handleModelSubcommand(ctx, mctx)
	case "status":
		return handleStatusSubcommand(ctx, mctx)
	case "trust":
		return handleTrustSubcommand(ctx, mctx, true)
	case "untrust":
		return handleTrustSubcommand(ctx, mctx, false)
	case "trustlist":
		return handleTrustListSubcommand(ctx, mctx)
	case "reset":
		return handleResetSubcommand(ctx, mctx)
	case "voice":
		return handleVoiceSubcommand(ctx, mctx)
	case "tools":
		return handleToolsSubcommand(ctx, mctx)
	default:
		return store.ReplyText(ctx, mctx, agentUsage())
	}
}

// agentUsage adalah teks bantuan untuk ".agent" polos / subcommand salah.
func agentUsage() string {
	return "Flora Agent:\n" +
		".agent model [nomor/nama]\n" +
		".agent model add <nama> | del <nama>\n" +
		".agent provider [nomor/nama]\n" +
		".agent provider add <nama> <endpoint> | del <nama>\n" +
		".agent status\n" +
		".agent tools\n" +
		".agent trust @user\n" +
		".agent untrust @user\n" +
		".agent trustlist\n" +
		".agent reset\n" +
		".agent reset -memory [@user/nomor]\n" +
		".agent voice [on/off]\n" +
		"Ngobrol: sebut nama Flora / reply pesannya."
}

// handleResetSubcommand menangani ".agent reset" (reset histori sesi
// sendiri — boleh untuk user terpercaya) dan ".agent reset -memory"
// (KHUSUS owner: hapus ingatan jangka panjang + sesi pemanggil).
//
// Kunci sesi dibedakan private/grup (lihat ai.BuildSessionKey) — reset yang
// diketik di dalam grup HANYA mereset histori sender itu di grup tersebut,
// tidak menyentuh histori private-nya atau histori dia di grup lain.
func handleResetSubcommand(ctx context.Context, mctx *store.MessageContext) error {
	sessionKey := ai.BuildSessionKey(mctx.IsGroup(), mctx.Chat.String(), mctx.Sender.String())
	if len(mctx.Args) > 1 && strings.EqualFold(mctx.Args[1], "-memory") {
		if msg := requireOwner(mctx); msg != "" {
			return store.ReplyText(ctx, mctx, msg)
		}
		return handleResetMemory(ctx, mctx)
	}
	ai.GlobalSession.Reset(sessionKey)
	return store.ReplyText(ctx, mctx, "Oke, sesi percakapan sudah direset.")
}

// handleResetMemory menangani ".agent reset -memory [target]" (KHUSUS
// owner). Tanpa target = milik sendiri (kunci persis seperti saat disimpan).
// Dengan target (mention / reply / nomor di Args[2:], gabungan untuk nomor
// ber-spasi) = milik target: fakta dihapus via beberapa kandidat kunci +
// sesi target di chat ini direset. Jumlah baris fakta yang terhapus
// dilaporkan jujur (0 = tidak ditemukan).
func handleResetMemory(ctx context.Context, mctx *store.MessageContext) error {
	target, hasTarget, err := resolveResetMemoryTarget(mctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("%v — contoh: .agent reset -memory @user", err))
	}
	label := "mu"
	factKeys := []string{mctx.Sender.String()}
	sessionKeys := []string{ai.BuildSessionKey(mctx.IsGroup(), mctx.Chat.String(), mctx.Sender.String())}
	if hasTarget {
		label = target.User
		factKeys = nil
		sessionKeys = nil
		for _, ck := range memoryCandidateKeys(ctx, mctx, target) {
			factKeys = append(factKeys, ck)
			sessionKeys = append(sessionKeys, ai.BuildSessionKey(mctx.IsGroup(), mctx.Chat.String(), ck))
		}
	}
	n, err := store.AI().DeleteFactsForKeys(ctx, factKeys)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal hapus ingatan: %v", err))
	}
	for _, sk := range sessionKeys {
		ai.GlobalSession.Reset(sk)
	}
	if n > 0 {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Ingatan jangka panjang (%d baris fakta) + sesi %s di chat ini sudah dihapus total.", n, label))
	}
	return store.ReplyText(ctx, mctx, fmt.Sprintf("Tidak ada ingatan tersimpan untuk %s; sesinya di chat ini sudah direset.", label))
}

// resolveResetMemoryTarget mengambil target opsional reset -memory dari
// mention, pesan reply, atau nomor HP di argumen ketiga dst. Tanpa ketiganya
// = reset milik sendiri (hasTarget false, bukan error).
func resolveResetMemoryTarget(mctx *store.MessageContext) (types.JID, bool, error) {
	if mentioned := mctx.MentionedJIDs(); len(mentioned) > 0 {
		jid, err := types.ParseJID(mentioned[0])
		if err != nil {
			return types.JID{}, false, fmt.Errorf("JID mention tidak valid")
		}
		return jid.ToNonAD(), true, nil
	}
	if quoted := mctx.QuotedParticipant(); quoted != "" {
		jid, err := types.ParseJID(quoted)
		if err != nil {
			return types.JID{}, false, fmt.Errorf("JID reply tidak valid")
		}
		return jid.ToNonAD(), true, nil
	}
	if len(mctx.Args) > 2 {
		jid, err := ParsePhoneToJID(strings.Join(mctx.Args[2:], " "))
		if err != nil {
			return types.JID{}, false, err
		}
		return jid.ToNonAD(), true, nil
	}
	return types.JID{}, false, nil
}

// memoryCandidateKeys menyusun kandidat kunci fakta untuk satu target: JID
// target (bare) + hasil resolve alt-JID whatsmeow (pemetaan LID↔PN, pola yang
// sama dipakai displayID di store/log.go). Best-effort: fakta tersimpan apa
// adanya dalam bentuk PN atau LID, jadi dicoba semua bentuk yang bisa
// diturunkan; kalau tetap meleset, pemanggil melaporkan 0 baris.
func memoryCandidateKeys(ctx context.Context, mctx *store.MessageContext, target types.JID) []string {
	keys := []string{target.String()}
	if mctx.Client != nil && mctx.Client.Store != nil {
		if alt, err := mctx.Client.Store.GetAltJID(ctx, target); err == nil && !alt.IsEmpty() {
			if s := alt.ToNonAD().String(); s != keys[0] {
				keys = append(keys, s)
			}
		}
	}
	return keys
}

// handleTrustSubcommand menangani ".agent trust @user" /
// ".agent untrust @user" (KHUSUS owner). Target diambil dari mention,
// pesan yang di-reply, atau nomor HP di argumen kedua — cermin
// resolveBanTarget, tapi nomor dibaca dari Args[1] (Args[0] = "trust").
func handleTrustSubcommand(ctx context.Context, mctx *store.MessageContext, trust bool) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	action := "trust"
	if !trust {
		action = "untrust"
	}
	target, err := resolveTrustTarget(mctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("%v — contoh: .agent %s @user", err, action))
	}
	if trust {
		if err := store.Groups().TrustUser(ctx, target); err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal memberi kepercayaan: %v", err))
		}
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Oke, %s sekarang dipercaya mengakses Flora.", target.User))
	}
	removed, err := store.Groups().UntrustUser(ctx, target)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal mencabut kepercayaan: %v", err))
	}
	if !removed {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("%s tidak ada di daftar trust.", target.User))
	}
	return store.ReplyText(ctx, mctx, fmt.Sprintf("Kepercayaan %s sudah dicabut.", target.User))
}

// handleTrustListSubcommand menangani ".agent trustlist" (KHUSUS owner):
// menampilkan seluruh user yang dipercaya + waktu ditambahkan.
func handleTrustListSubcommand(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	list, err := store.Groups().ListTrustedUsers(ctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal baca daftar trust: %v", err))
	}
	if len(list) == 0 {
		return store.ReplyText(ctx, mctx, "Daftar trust kosong — belum ada user yang dipercaya.")
	}
	var sb strings.Builder
	sb.WriteString("Daftar trust Flora:\n")
	mentions := make([]types.JID, 0, len(list))
	for i, e := range list {
		// Tag biru butuh pairing "@<User>" di teks + MentionedJID (aturan
		// yang sama dengan tagall/ReplyTextWithMentions). JID yang gagal
		// di-parse tampil polos supaya satu baris rusak tidak menggagalkan
		// seluruh daftar.
		if jid, perr := types.ParseJID(e.JID); perr == nil && jid.User != "" {
			bare := jid.ToNonAD()
			sb.WriteString(fmt.Sprintf("%d. @%s\n", i+1, bare.User))
			mentions = append(mentions, bare)
		} else {
			name := e.Part
			if name == "" {
				name = e.JID
			}
			sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, name))
		}
	}
	if len(mentions) == 0 {
		return store.ReplyText(ctx, mctx, sb.String())
	}
	return store.ReplyTextWithMentions(ctx, mctx, sb.String(), mentions)
}

// resolveTrustTarget mengambil target trust/untrust dari mention, pesan
// reply, atau nomor HP di argumen kedua. Hasil dinormalisasi ToNonAD
// supaya format penyimpanannya konsisten (lihat store.TrustUser).
func resolveTrustTarget(mctx *store.MessageContext) (types.JID, error) {
	if mentioned := mctx.MentionedJIDs(); len(mentioned) > 0 {
		jid, err := types.ParseJID(mentioned[0])
		if err != nil {
			return types.JID{}, fmt.Errorf("JID mention tidak valid")
		}
		return jid.ToNonAD(), nil
	}
	if quoted := mctx.QuotedParticipant(); quoted != "" {
		jid, err := types.ParseJID(quoted)
		if err != nil {
			return types.JID{}, fmt.Errorf("JID reply tidak valid")
		}
		return jid.ToNonAD(), nil
	}
	if len(mctx.Args) > 1 {
		// Gabung SEMUA sisa argumen: nomor ber-spasi ("+62 822-1234-5678")
		// terpecah jadi beberapa Args oleh strings.Fields — cleaner di
		// ParsePhoneToJID membuang spasi/strip/plus-nya kembali.
		jid, err := ParsePhoneToJID(strings.Join(mctx.Args[1:], " "))
		if err != nil {
			return types.JID{}, err
		}
		return jid.ToNonAD(), nil
	}
	return types.JID{}, fmt.Errorf("tag (@user), reply pesan target, atau sertakan nomor")
}

// handleProviderSubcommand menangani ".agent provider" (tampilkan daftar),
// ".agent provider <nama>" dan ".agent provider <nomor>" (ganti provider
// aktif, nomor sesuai urutan di daftar). KHUSUS owner (pengaturan global).
func handleProviderSubcommand(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	arg := strings.TrimSpace(strings.Join(mctx.Args[1:], " "))

	if arg == "" {
		return store.ReplyText(ctx, mctx, ai.FormatProviderList(ctx))
	}

	// Subcommand registry: "add <nama> <endpoint>" / "del <nama>".
	// Key provider dibaca dari env <NAMA>_API_KEY, jadi tambah provider
	// hanya butuh nama + endpoint (tanpa secret di chat).
	fields := strings.Fields(arg)
	if len(fields) >= 1 && strings.EqualFold(fields[0], "add") {
		if len(fields) != 3 {
			return store.ReplyText(ctx, mctx, "Format: .agent provider add <nama> <endpoint-chat-completions>\nContoh: .agent provider add openagentic https://openagentic.id/api/v1/chat/completions")
		}
		if err := ai.AddProvider(ctx, fields[1], fields[2]); err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal tambah provider: %v", err))
		}
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Oke, provider *%s* ditambahkan. Isi API key di env %s lalu pilih modelnya.",
			strings.ToLower(fields[1]), strings.ToUpper(strings.ReplaceAll(strings.ToLower(fields[1]), "-", "_"))+"_API_KEY"))
	}
	if len(fields) == 2 && strings.EqualFold(fields[0], "del") {
		if err := ai.DelProvider(ctx, fields[1]); err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal hapus provider: %v", err))
		}
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Oke, provider *%s* dihapus.", strings.ToLower(fields[1])))
	}

	name := ai.ResolveProviderRef(ctx, arg)
	if err := ai.SetActiveProvider(ctx, name); err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal ganti provider: %v\n\n%s", err, ai.FormatProviderList(ctx)))
	}

	return store.ReplyText(ctx, mctx, fmt.Sprintf("Oke, provider AI sekarang: *%s*", name))
}

// handleModelSubcommand menangani ".agent model" (tampilkan daftar model utk
// provider aktif), ".agent model <nama>" dan ".agent model <nomor>" (ganti
// model aktif, nomor sesuai urutan di daftar). KHUSUS owner.
func handleModelSubcommand(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	arg := strings.TrimSpace(strings.Join(mctx.Args[1:], " "))

	if arg == "" {
		return store.ReplyText(ctx, mctx, ai.FormatModelList(ctx))
	}

	// Subcommand registry (provider yang SEDANG aktif): "add <nama>" /
	// "del <nama>".
	fields := strings.Fields(arg)
	if len(fields) == 2 && strings.EqualFold(fields[0], "add") {
		if err := ai.AddModel(ctx, fields[1]); err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal tambah model: %v", err))
		}
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Oke, model *%s* ditambahkan.", fields[1]))
	}
	if len(fields) == 2 && strings.EqualFold(fields[0], "del") {
		if err := ai.DelModel(ctx, fields[1]); err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal hapus model: %v", err))
		}
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Oke, model *%s* dihapus.", fields[1]))
	}

	name := ai.ResolveModelRef(ctx, ai.GetActiveProviderName(ctx), arg)
	if err := ai.SetActiveModel(ctx, name); err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal ganti model: %v\n\n%s", err, ai.FormatModelList(ctx)))
	}

	return store.ReplyText(ctx, mctx, fmt.Sprintf("Oke, model AI sekarang: *%s*", name))
}

// handleVoiceSubcommand menangani ".agent voice on/off" (mode suara Flora:
// balasan AI dikirim sebagai SATU voice note, bukan teks) dan
// ".agent voice" polos (tampilkan status). KHUSUS owner — tiap balasan
// memakan panggilan TTS + convert, jadi bukan keputusan user biasa.
func handleVoiceSubcommand(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	arg := strings.ToLower(strings.TrimSpace(strings.Join(mctx.Args[1:], " ")))

	if arg == "" {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Mode suara Flora: *%s*\n\nPakai `.agent voice on` / `.agent voice off` untuk mengubah.", voiceOnOffText(ctx)))
	}
	switch arg {
	case "on", "aktif", "enable", "nyala":
		if err := ai.SetVoiceReply(ctx, true); err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal menyalakan mode suara: %v", err))
		}
		return store.ReplyText(ctx, mctx, "Mode suara Flora: *ON* — balasan Flora dikirim sebagai voice note.")
	case "off", "mati", "disable", "padam":
		if err := ai.SetVoiceReply(ctx, false); err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal mematikan mode suara: %v", err))
		}
		return store.ReplyText(ctx, mctx, "Mode suara Flora: *OFF* — balasan Flora dikirim sebagai teks seperti biasa.")
	default:
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Argumen tidak dikenal. Pakai `.agent voice on` / `.agent voice off`.\nStatus sekarang: *%s*", voiceOnOffText(ctx)))
	}
}

// voiceOnOffText mengembalikan "ON"/"OFF" untuk status mode suara Flora.
func voiceOnOffText(ctx context.Context) string {
	if ai.IsVoiceReplyOn(ctx) {
		return "ON"
	}
	return "OFF"
}

// handleStatusSubcommand menangani ".agent status": menampilkan status lock
// trigger natural (global), provider + model aktif, dan status sesi chat
// ini (aktif kalau masih dalam idle timeout 10 menit, idle kalau sudah
// lama tidak chat). KHUSUS owner. Sengaja jalan walau sedang terkunci —
// gunanya justru untuk mengecek status lock.
func handleStatusSubcommand(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	locked, err := store.Groups().IsAILocked(ctx)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal baca status lock: %v", err))
	}
	lockText := "aktif (trigger natural menyala)"
	if locked {
		lockText = "TERKUNCI (trigger natural mati)"
	}

	sessionKey := ai.BuildSessionKey(mctx.IsGroup(), mctx.Chat.String(), mctx.Sender.String())
	sessionText := "idle (sapa Flora untuk mulai)"
	if ai.GlobalSession.HasActive(sessionKey) {
		sessionText = "aktif"
	}

	return store.ReplyText(ctx, mctx, fmt.Sprintf(
		"Status Flora:\n- Lock: %s\n- Provider: %s\n- Model: %s\n- Voice: %s\n- Sesi chat ini: %s",
		lockText, ai.GetActiveProviderName(ctx), ai.GetActiveModelName(ctx), voiceOnOffText(ctx), sessionText,
	))
}

// agentToolOrder adalah urutan kurasi tampil ".agent tools", dikelompokkan
// per kategori. Nama tool diambil DINAMIS dari registry (lihat
// handleToolsSubcommand) — daftar ini hanya mengatur urutan + kategori,
// jadi tool baru tetap muncul (di kelompok "Lainnya", urutan abjad) walau
// belum ditambahkan ke sini.
var agentToolOrder = []string{
	"catat_keuangan", "baca_keuangan", "rekap_keuangan", "edit_keuangan", "hapus_keuangan", "export_keuangan",
	"reminder",
	"kick_member", "promote_member", "demote_member", "set_group_name", "tag_all", "group_link", "group_close", "group_open",
	"make_call", "make_sticker", "buat_excel",
	"bot_mode", "system_info",
}

// agentToolCategory memetakan nama tool ke judul kelompok di daftar.
var agentToolCategory = map[string]string{
	"catat_keuangan": "Keuangan", "baca_keuangan": "Keuangan",
	"rekap_keuangan": "Keuangan", "edit_keuangan": "Keuangan",
	"hapus_keuangan": "Keuangan", "export_keuangan": "Keuangan",
	"reminder":    "Pengingat",
	"kick_member": "Grup (di dalam grup)", "promote_member": "Grup (di dalam grup)",
	"demote_member": "Grup (di dalam grup)", "set_group_name": "Grup (di dalam grup)",
	"tag_all": "Grup (di dalam grup)", "group_link": "Grup (di dalam grup)",
	"group_close": "Grup (di dalam grup)", "group_open": "Grup (di dalam grup)",
	"make_call": "Telepon & Media", "make_sticker": "Telepon & Media", "buat_excel": "Telepon & Media",
	"bot_mode": "Sistem", "system_info": "Sistem",
}

// agentToolTrusted menandai tool yang bisa dipakai user terpercaya
// (simbol Ⓣ setelah nama). Tanpa tanda = khusus owner. Untuk kelompok
// grup, trusted tetap perlu jadi admin (lihat catatan kaki render).
var agentToolTrusted = map[string]bool{
	"kick_member": true, "promote_member": true, "demote_member": true,
	"set_group_name": true, "tag_all": true, "group_link": true,
	"group_close": true, "group_open": true,
	"make_sticker": true, "buat_excel": true, "system_info": true,
}

// agentToolExample adalah contoh kalimat natural pemicu tiap tool (tool
// dipanggil model dari obrolan biasa, bukan dari command). Tool yang belum
// punya entri tampil dengan teks fallback — lihat handleToolsSubcommand.
var agentToolExample = map[string]string{
	"catat_keuangan":  "Flora, catat bensin 50rb dari rekening",
	"baca_keuangan":   "Flora, tampilkan 10 pengeluaran terakhirku",
	"rekap_keuangan":  "Flora, saldo kas berapa?",
	"edit_keuangan":   "Flora, ubah parkir kemarin jadi 15rb",
	"hapus_keuangan":  "Flora, hapus catatan kopi tadi pagi (Flora konfirmasi dulu)",
	"export_keuangan": "Flora, export keuanganku bulan ini ke Excel",
	"buat_excel":      "Flora, jadikan data ini Excel",
	"reminder":        "Flora, ingatkan aku jam 5 sore bayar listrik",
	"kick_member":     "Flora, tendang @user",
	"promote_member":  "Flora, jadikan @user admin",
	"demote_member":   "Flora, turunkan @user dari admin",
	"set_group_name":  "Flora, ganti nama grup jadi Keluarga",
	"tag_all":         "Flora, tag semua member",
	"group_link":      "Flora, minta link grup ini",
	"group_close":     "Flora, tutup grup",
	"group_open":      "Flora, buka grup",
	"make_call":       "Flora, telepon saya / telepon 081234567890",
	"make_sticker":    "Flora, jadiin stiker (kirim/quote gambar)",
	"bot_mode":        "Flora, ganti mode ke public",
	"system_info":     "Flora, info server",
}

// handleToolsSubcommand menangani ".agent tools": menampilkan SEMUA tool
// Flora dari registry (dinamis — tool baru otomatis ikut tanpa edit daftar)
// dengan contoh quote per tool. Gerbang owner+trusted (tanpa requireOwner
// tambahan): user terpercaya memang boleh memakai Flora, jadi boleh tahu
// kemampuannya. Format per tool: bullet + nama (+ Ⓣ bila trusted boleh)
// + baris quote contoh.
func handleToolsSubcommand(ctx context.Context, mctx *store.MessageContext) error {
	decls := ai.GetToolDeclarations()
	if len(decls) == 0 {
		return store.ReplyText(ctx, mctx, "Belum ada tool yang terdaftar.")
	}
	byName := make(map[string]bool, len(decls))
	for _, d := range decls {
		byName[d.Name] = true
	}
	// Susun urutan: kurasi dulu, sisa tool (baru) abjad di belakang.
	ordered := make([]string, 0, len(decls))
	for _, name := range agentToolOrder {
		if byName[name] {
			ordered = append(ordered, name)
			delete(byName, name)
		}
	}
	rest := make([]string, 0, len(byName))
	for name := range byName {
		rest = append(rest, name)
	}
	sort.Strings(rest)
	ordered = append(ordered, rest...)

	var sb strings.Builder
	sb.WriteString("Kemampuan Flora via ngobrol (bukan diketik sebagai command).\n" +
		"Bisa dipakai owner; bertanda Ⓣ juga bisa dipakai user terpercaya.")
	lastCat := ""
	for _, name := range ordered {
		cat, ok := agentToolCategory[name]
		if !ok {
			cat = "Lainnya"
		}
		if cat != lastCat {
			sb.WriteString("\n\n" + cat + ":")
			if strings.HasPrefix(cat, "Grup") {
				sb.WriteString(" (Ⓣ perlu jadi admin)")
			}
			lastCat = cat
		}
		display := name
		if agentToolTrusted[name] {
			display += " Ⓣ"
		}
		example, ok := agentToolExample[name]
		if !ok {
			example = "(contoh menyusul)"
		}
		fmt.Fprintf(&sb, "\n• %s\n> %s", display, example)
	}
	return store.ReplyText(ctx, mctx, strings.TrimRight(sb.String(), "\n"))
}

// HandleAIMessage adalah logic inti pemrosesan AI — sekarang HANYA dipanggil
// dari trigger natural (kata kunci/reply/sticker, lihat
// internal/handler/handler.go); tanya-jawab via command sudah dihapus,
// command .agent murni admin.
//
// Sejak Fase 4: dibungkus spam gate + lock per-sender, mood user disuntik
// sebagai tambahan system prompt, dan pemanggilan provider memakai retry
// otomatis + notifikasi owner saat gagal total (lihat internal/ai/reliability.go).
//
// Sejak Fase 5: balasan final dipecah jadi multi-bubble (lihat sendBubbles)
// alih-alih satu ReplyText tunggal, dan fact extractor dipicu fire-and-forget
// setiap beberapa giliran (lihat processAITurn).
//
// Kunci sesi PERCAKAPAN (histori chat) dibangun lewat ai.BuildSessionKey,
// yang membedakan private vs grup: sender yang sama akan punya histori
// TERPISAH antara chat private-nya dan chat dia di sebuah grup (dan
// terpisah lagi per grup) — sebelumnya semua konteks ini keliru berbagi
// satu histori yang sama karena kunci sesi hanya memakai sender JID.
func HandleAIMessage(ctx context.Context, mctx *store.MessageContext, text string) error {
	isGroup := mctx.IsGroup()
	sessionKey := ai.BuildSessionKey(isGroup, mctx.Chat.String(), mctx.Sender.String())

	// Anti-spam: request yang datang terlalu cepat dari sender yang sama
	// diabaikan diam-diam, tidak diproses dan tidak membalas apa-apa —
	// mencegah AI "nyasar" merespons pesan beruntun yang sebenarnya satu
	// maksud yang sama.
	if ai.IsSpam(sessionKey) {
		return nil
	}

	// Lock per-sesi (non-blocking): pesan yang datang saat sesi yang sama
	// masih diproses ditolak diam-diam — tidak antre serial, tidak ada LLM
	// call tambahan, tidak ada bubble. Mencegah burst menumpuk giliran +
	// bubble berurutan yang terlihat seperti spam.
	resultText, ok, err := ai.TrySenderLock(sessionKey, func() (string, error) {
		return processAITurn(ctx, mctx, sessionKey, text)
	})
	if !ok {
		logfmt.Skip("Giliran AI sesi %s ditolak: sesi sibuk.\n", sessionKey)
		return nil
	}
	if err != nil {
		// Fase 4: pesan ramah ke user, detail teknis sudah dikirim ke owner
		// oleh notifyOwner di dalam processAITurn. Balasan ramah ini
		// di-cooldown per chat (lihat sendFriendlyAIError): saat provider
		// down + chat ramai, tiap giliran gagal tidak boleh membanjiri chat
		// dengan bubble error berulang.
		return sendFriendlyAIError(ctx, mctx, err)
	}

	return sendFloraReply(ctx, mctx, resultText)
}

// sendBubbles memecah balasan final jadi beberapa bubble (Fase 5) dan
// mengirimkannya berurutan dengan delay ketik simulasi di antaranya.
// Bubble pertama dikirim sebagai reply ke pesan user (ReplyText, sama
// seperti perilaku sebelum Fase 5); bubble ke-2 dst dikirim sebagai
// pesan lepas (SendText) supaya terasa natural, bukan reply bertumpuk.
//
// Delay ketik disimulasikan SEBELUM tiap bubble dikirim (termasuk bubble
// pertama), dan selama delay itu status "mengetik..." (chat presence
// composing) ditampilkan ke user lewat SendChatPresence — supaya jeda
// antar-bubble bukan cuma diam, tapi terlihat seperti orang benar-benar
// mengetik. Kegagalan kirim presence sengaja diabaikan (fail-soft): status
// mengetik cuma kosmetik, tidak boleh sampai menggagalkan pengiriman pesan
// utamanya kalau WhatsApp menolak/timeout presence-nya.
func sendBubbles(ctx context.Context, mctx *store.MessageContext, text string) error {
	bubbles := ai.SplitIntoBubbles(text)
	if len(bubbles) == 0 {
		return nil
	}

	for i, bubble := range bubbles {
		_ = mctx.Client.SendChatPresence(ctx, mctx.Chat, types.ChatPresenceComposing, types.ChatPresenceMediaText)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(ai.TypingDelayFor(bubble)):
		}

		var err error
		if i == 0 {
			err = store.ReplyTextAgent(ctx, mctx, bubble)
		} else {
			err = store.SendTextAgent(ctx, mctx, bubble)
		}
		if err != nil {
			return err
		}
	}

	// Setelah bubble terakhir terkirim, pastikan status "mengetik" tidak
	// nyangkut di layar user — WhatsApp biasanya membersihkannya otomatis
	// begitu pesan diterima, tapi dikirim eksplisit sebagai jaring pengaman.
	_ = mctx.Client.SendChatPresence(ctx, mctx.Chat, types.ChatPresencePaused, types.ChatPresenceMediaText)

	return nil
}

// sendFloraReply mengirim balasan final Flora lewat jalur yang sesuai
// mode: voice note UTUH bila `.agent voice on`, teks bubble bila OFF.
// Satu-satunya pintu keluar balasan AI (HandleAIMessage) lewat sini.
func sendFloraReply(ctx context.Context, mctx *store.MessageContext, text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if ai.IsVoiceReplyOn(ctx) {
		if err := sendVoiceReply(ctx, mctx, text); err != nil {
			// Fail-soft: TTS gagal = kirim sebagai teks biasa. Balasan
			// user tidak boleh hilang gara-gara layanan suara bermasalah.
			logfmt.Warn("voice reply gagal (%v), fallback teks.\n", err)
		} else {
			return nil
		}
	}
	return sendBubbles(ctx, mctx, text)
}

// sendVoiceReply mengirim SELURUH teks balasan sebagai SATU voice note
// (tanpa pecah bubble, tanpa batas 500 char): teks dipecah jadi chunk di
// dalam SynthesizeLong, MP3 digabung, sekali convert Opus, sekali kirim.
// Suara fixed Gadis (identitas suara Flora). Presence composing dipakai
// selama menyiapkan (whatsmeow tidak punya presence "recording").
func sendVoiceReply(ctx context.Context, mctx *store.MessageContext, text string) error {
	_ = mctx.Client.SendChatPresence(ctx, mctx.Chat, types.ChatPresenceComposing, types.ChatPresenceMediaText)
	defer func() {
		_ = mctx.Client.SendChatPresence(ctx, mctx.Chat, types.ChatPresencePaused, types.ChatPresenceMediaText)
	}()

	mp3Bytes, err := SynthesizeLong(ctx, strings.TrimSpace(text), ttsDefaultVoice)
	if err != nil {
		return err
	}
	if len(mp3Bytes) == 0 {
		return fmt.Errorf("hasil sintesis kosong")
	}
	opusBytes, err := runFfmpegConvert(ctx, mp3Bytes, ".mp3", "out.opus", nil,
		"-c:a", "libopus", "-b:a", "48k")
	if err != nil || len(opusBytes) == 0 {
		return fmt.Errorf("gagal siapkan voice note: %v", err)
	}
	return sendVoiceNote(ctx, mctx, opusBytes, true)
}

// processAITurn adalah isi satu giliran percakapan AI, dipisah dari
// HandleAIMessage supaya bisa dibungkus ai.WithSenderLock dengan bersih
// (closure murni menerima/mengembalikan (string, error), tanpa langsung
// mengirim balasan dari dalam lock).
//
// sessionKey di sini sudah berupa kunci gabungan private/grup (lihat
// ai.BuildSessionKey di HandleAIMessage) — dipakai untuk histori
// percakapan (ai.GlobalSession). Fakta jangka panjang (fact extractor)
// SENGAJA TETAP memakai mctx.Sender.String() murni, bukan sessionKey —
// lihat catatan di pemanggilan ai.TriggerFactExtraction di bawah.
func processAITurn(ctx context.Context, mctx *store.MessageContext, sessionKey, text string) (string, error) {
	provider, err := ai.GetActiveProvider(ctx)
	if err != nil {
		return "", err
	}

	isGroup := mctx.IsGroup()

	// Vision: kalau pesan pemicu membawa gambar (lampiran langsung atau
	// yang di-reply), unduh dan selipkan ke pesan user giliran ini supaya
	// model yang mendukung vision bisa "melihat"-nya. Byte gambar HANYA
	// hidup di slice history lokal ini (ephemeral) — baris yang disimpan
	// ke sesi/DB di bawah tetap teks murni, jadi tidak membengkakkan
	// memori maupun database. Provider non-vision mengabaikan
	// field gambar secara diam-diam.
	userPart := ai.Part{Text: text}
	if imgBytes, imgMime, imgNote := extractTriggerImage(ctx, mctx); len(imgBytes) > 0 {
		userPart.ImageBytes = imgBytes
		userPart.ImageMime = imgMime
	} else if imgNote != "" {
		text += "\n" + imgNote
		userPart.Text = text
	}

	// Dokumen: kalau pesan pemicu membawa file (PDF/DOCX/XLSX/teks —
	// lampiran langsung atau yang di-reply), ekstrak teksnya dan tempel ke
	// pesan user. Tidak seperti gambar vision, ini murni teks sehingga
	// SEMUA provider bisa membacanya dan aman disimpan
	// ke histori. Dokumen pendek (<= maxDocChars) ditempel utuh;
	// dokumen panjang dibaca bertahap via summarizeLongDocument supaya
	// tidak ada isi yang hilang (bukan dipotong seperti dulu).
	if docName, docText, docNote := readTriggerDocument(ctx, mctx); docNote != "" {
		text += "\n" + docNote
		userPart.Text = text
	} else if docText != "" {
		if len([]rune(docText)) <= maxDocChars {
			text += "\n" + formatDocBlock(docName, strings.TrimSpace(docText), false)
		} else {
			text += "\n" + summarizeLongDocument(ctx, provider, docName, docText)
		}
		userPart.Text = text
	}

	history := ai.GlobalSession.Get(sessionKey)
	// Backstop hemat-token: kirim maks 40 giliran terakhir. Penyimpanan
	// (memori + SQLite) tetap utuh — yang dipangkas hanya kiriman.
	history = ai.CapHistoryForSend(history, 40)
	history = append(history, ai.Content{
		Role:  "user",
		Parts: []ai.Part{userPart},
	})

	actx := ai.NewAgentContext(ctx, mctx.Sender.String(), mctx.Chat.String(), isGroup)
	// Extra dipakai tool bridge yang butuh akses whatsmeow client/mctx asli
	// tanpa membuat package internal/ai import whatsmeow secara langsung.
	actx.Extra["client"] = mctx.Client
	actx.Extra["mctx"] = mctx

	// Mood user disuntik sebagai tambahan system prompt (versi sederhana:
	// hanya mood user, tanpa mood bot/crush detector, sesuai blueprint).
	mood := ai.DetectUserMood(text)
	systemPrompt := config.AISystemPromptBase
	// Acuan tanggal: model tidak punya jam internal — tanpa baris ini,
	// kata relatif seperti "kemarin"/"minggu ini" ditebak dari pengetahuan
	// model yang bisa basi. Zona aktif timex (WIB/WITA/WIT via env).
	nowLocal := timex.Now()
	systemPrompt = systemPrompt + "\n\nHari ini (zona " + timex.Name() + "): " +
		nowLocal.Format("Monday, 2 January 2006") +
		" — format tanggal tool YYYY-MM-DD: " + nowLocal.Format("2006-01-02") + ". " +
		"Hitung tanggal relatif (kemarin, minggu/bulan ini) dari tanggal ini."
	if modifier := ai.MoodModifier(mood); modifier != "" {
		systemPrompt = systemPrompt + "\n\n" + modifier
	}

	// Fase 5: fakta jangka panjang tentang user (per sender JID, lintas
	// chat/grup) disuntik ke system prompt kalau ada. Kegagalan baca
	// fail-soft — chat tetap jalan tanpa fakta kalau DB bermasalah.
	if factsJSON, err := store.AI().GetFacts(ctx, mctx.Sender.String()); err == nil {
		if factsPrompt := ai.FormatFactsForPrompt(factsJSON); factsPrompt != "" {
			systemPrompt = systemPrompt + "\n\n" + factsPrompt
		}
	}

	// Ingatan hemat: ringkasan FIFO sesi-sesi sebelumnya (maks 4) disuntik
	// sebagai konteks. Fail-soft seperti fakta — chat tetap jalan tanpanya.
	if summaries, err := store.AI().ListSessionSummaries(ctx, sessionKey); err == nil {
		if sp := ai.FormatSummariesForPrompt(summaries); sp != "" {
			systemPrompt = systemPrompt + "\n\n" + sp
		}
	}

	notifyOwner := func(err error) {
		notifyOwnerOfAIError(mctx, err)
	}

	logAITurnStart(ctx, mctx, sessionKey, history, text)
	turnStart := time.Now()
	resultText, err := ai.RunAgentLoopWithRetry(actx, provider, history, systemPrompt, notifyOwner)
	logAITurnDone(sessionKey, resultText, err, time.Since(turnStart))
	if err != nil {
		return "", err
	}

	newTurn := []ai.Content{
		{Role: "user", Parts: []ai.Part{{Text: text}}},
		{Role: "model", Parts: []ai.Part{{Text: resultText}}},
	}
	ai.GlobalSession.Append(sessionKey, newTurn[0])
	ai.GlobalSession.Append(sessionKey, newTurn[1])

	// Fase 5: pemicu fact extractor fire-and-forget, memakai PROVIDER AKTIF
	// yang sama — ekstraksi fakta ikut lewat provider yang sedang aktif,
	// supaya tidak diam-diam tetap memanggil provider lain di belakang layar
	// meski user sudah pindah provider.
	//
	// SENGAJA memakai mctx.Sender.String() murni (bukan sessionKey yang
	// sudah digabung dengan chatJID untuk grup) — fakta jangka panjang
	// tentang seseorang memang didesain lintas-chat/grup (lihat keputusan
	// Fase 5: "fakta user disimpan per sender, lintas semua chat/grup"),
	// jadi harus TETAP satu kunci per orang meski histori PERCAKAPANNYA
	// sekarang sudah dipisah per private/grup lewat sessionKey.
	ai.TriggerFactExtraction(provider, mctx.Sender.String(), append(history, newTurn[1]))

	return resultText, nil
}

// maxVisionImageBytes adalah batas ukuran gambar untuk vision (4MB). Foto
// dari WhatsApp umumnya sudah dikompres (<1MB); di atas batas dilewati
// supaya payload base64 tidak meledak dan provider tidak menolak request
// karena kebesaran.
const maxVisionImageBytes = 4 << 20

// extractTriggerImage mengambil gambar dari pesan pemicu AI untuk vision:
// prioritas lampiran langsung di pesan itu, fallback ke pesan yang
// di-reply; tiap jalur mencoba ImageMessage dulu lalu StickerMessage.
// Stiker diperlakukan SAMA seperti foto (diunduh + dikirim sebagai
// image_url) supaya Flora "melihat" stiker yang diterimanya — stiker
// animasi dikirim apa adanya, backend vision umumnya memakai frame
// pertama. Mengembalikan byte+mime kalau berhasil; kalau pesan memang
// mengandung gambar/stiker tapi gagal diunduh/kebesaran, kembalikan note
// jujur untuk ditempel ke teks (supaya AI tidak mengarang isinya). Kalau
// tidak ada gambar sama sekali, kembalikan kosong. Semua kegagalan
// fail-soft: chat teks tetap jalan normal.
func extractTriggerImage(ctx context.Context, mctx *store.MessageContext) (imgBytes []byte, imgMime, note string) {
	// Getter protobuf aman dipanggil pada pesan nil (mengembalikan nil),
	// jadi tidak perlu cek nil hasil UnwrapMessage satu per satu.
	direct := store.UnwrapMessage(mctx.RawEvt.Message)
	quoted := store.UnwrapMessage(store.ExtractQuotedMessage(mctx.RawEvt))

	// Kumpulkan kandidat berurutan: gambar langsung, gambar reply, stiker
	// langsung, stiker reply. Yang pertama ketemu dipakai.
	type candidate struct {
		msg         whatsmeow.DownloadableMessage
		mime        string
		label       string
		defaultMime string
	}
	var candidates []candidate
	if img := direct.GetImageMessage(); img != nil {
		candidates = append(candidates, candidate{img, img.GetMimetype(), "gambar", "image/jpeg"})
	}
	if img := quoted.GetImageMessage(); img != nil {
		candidates = append(candidates, candidate{img, img.GetMimetype(), "gambar", "image/jpeg"})
	}
	if stk := direct.GetStickerMessage(); stk != nil {
		candidates = append(candidates, candidate{stk, stk.GetMimetype(), "stiker", "image/webp"})
	}
	if stk := quoted.GetStickerMessage(); stk != nil {
		candidates = append(candidates, candidate{stk, stk.GetMimetype(), "stiker", "image/webp"})
	}
	if len(candidates) == 0 {
		return nil, "", ""
	}

	c := candidates[0]
	data, err := mctx.Client.Download(ctx, c.msg)
	if err != nil {
		logfmt.Warn("vision: gagal download %s: %v\n", c.label, err)
		return nil, "", fmt.Sprintf("[Catatan: ada %s terlampir tapi tidak bisa dibaca.]", c.label)
	}
	if len(data) > maxVisionImageBytes {
		logfmt.Warn("vision: %s terlalu besar (%d byte), dilewati.\n", c.label, len(data))
		return nil, "", fmt.Sprintf("[Catatan: ada %s terlampir tapi terlalu besar untuk dibaca.]", c.label)
	}
	mime := c.mime
	if mime == "" {
		mime = c.defaultMime
	}
	return data, mime, ""
}

// ownerNotifyCooldown: notifikasi error AI ke owner dibatasi max 1 per
// jendela ini — saat provider down + grup ramai, ratusan giliran gagal
// tidak boleh membanjiri DM owner. Error yang ditahan dihitung (suppressed)
// dan dilaporkan pada notifikasi berikutnya. Mutex karena dipanggil dari
// banyak goroutine trigger konkuren.
var (
	ownerNotifyMu         sync.Mutex
	ownerNotifyLast       time.Time
	ownerNotifySuppressed int
)

const ownerNotifyCooldown = 5 * time.Minute

// userErrorCooldown: balasan ramah error AI ke chat dibatasi max 1 per
// jendela ini PER CHAT — saat provider down + chat ramai, tiap giliran gagal
// tidak boleh membanjiri chat dengan bubble "Maaf, lagi ada gangguan"
// berulang. Error yang ditahan dihitung (suppressed) dan dilaporkan pada
// balasan berikutnya. Mutex karena dipanggil dari banyak goroutine trigger
// konkuren. Return nil saat ditahan supaya fallback di handler (yang juga
// mengirim pesan error bila HandleAIMessage gagal) tidak ikut mengirim ganda.
var (
	userErrMu         sync.Mutex
	userErrLast       = make(map[string]time.Time)
	userErrSuppressed = make(map[string]int)
)

const userErrorCooldown = 5 * time.Minute

// sendFriendlyAIError mengirim bubble ramah error AI dengan cooldown per chat.
// Saat masih dalam jendela cooldown: tidak kirim apa-apa ke chat (diam),
// cukup catat di console + hitung suppressed, return nil.
// Penyebab antrean provider (ai.ProviderBusyMarker) dibalas dengan pesan
// "lagi antre" yang jujur, bukan "gangguan".
func sendFriendlyAIError(ctx context.Context, mctx *store.MessageContext, err error) error {
	chat := mctx.Chat.String()

	userErrMu.Lock()
	if last, ok := userErrLast[chat]; ok && time.Since(last) < userErrorCooldown {
		userErrSuppressed[chat]++
		n := userErrSuppressed[chat]
		userErrMu.Unlock()
		logfmt.Skip("Balasan error AI ke %s ditahan (cooldown, +%d ditahan).\n", chat, n)
		return nil
	}
	userErrLast[chat] = time.Now()
	suppressed := userErrSuppressed[chat]
	userErrSuppressed[chat] = 0
	userErrMu.Unlock()

	msg := "Maaf, lagi ada gangguan nih. Coba lagi sebentar ya~"
	if err != nil && strings.Contains(err.Error(), ai.ProviderBusyMarker) {
		msg = "Flora lagi antre menjawab chat lain nih, coba lagi sebentar ya~"
	}
	if suppressed > 0 {
		msg += fmt.Sprintf("\n(+%d error serupa ditahan dalam 5 menit terakhir.)", suppressed)
	}
	return store.ReplyText(ctx, mctx, msg)
}

// logAITurnStart mencatat pembuka giliran AI UTUH (format blok MESSAGE).
// Mode ringkas (AI_LOG_FULL=0) tidak mencetak apa pun di sini.
func logAITurnStart(ctx context.Context, mctx *store.MessageContext, sessionKey string, history []ai.Content, text string) {
	if !ai.AILogFullMode() {
		return
	}
	at := "private " + mctx.Chat.User
	if mctx.IsGroup() {
		at = "group " + mctx.Chat.User
	}
	ai.LogTurnStart(
		mctx.Sender.String(), at,
		ai.GetActiveProviderName(ctx), ai.GetActiveModelName(ctx),
		sessionKey, len(history), text,
	)
}

// logAITurnDone mencatat penutup giliran (format blok MESSAGE).
func logAITurnDone(sessionKey, resultText string, err error, elapsed time.Duration) {
	if !ai.AILogFullMode() {
		return
	}
	if err != nil {
		ai.LogTurnDone("GAGAL "+elapsed.String(), err.Error())
		return
	}
	ai.LogTurnDone("selesai "+elapsed.String(), resultText)
}

// notifyOwnerOfAIError mengirim laporan error teknis ke nomor owner UTAMA
// (entri pertama OWNER_NUMBERS), terpisah dari pesan ramah yang dilihat user biasa.
// Kegagalan kirim notifikasi ini sendiri sengaja diabaikan (fail-soft) —
// tidak boleh sampai bikin proses utama gagal gara-gara notifikasi gagal.
func notifyOwnerOfAIError(mctx *store.MessageContext, err error) {
	ownerNotifyMu.Lock()
	if time.Since(ownerNotifyLast) < ownerNotifyCooldown {
		ownerNotifySuppressed++
		n := ownerNotifySuppressed
		ownerNotifyMu.Unlock()
		logfmt.Skip("Notifikasi error AI ke owner ditahan (cooldown, +%d ditahan).\n", n)
		return
	}
	ownerNotifyLast = time.Now()
	suppressed := ownerNotifySuppressed
	ownerNotifySuppressed = 0
	ownerNotifyMu.Unlock()

	ownerJID := types.NewJID(config.PrimaryOwnerNumber(), types.DefaultUserServer)
	report := ai.FormatOwnerErrorReport(mctx.Sender.String(), mctx.Chat.String(), err)
	if suppressed > 0 {
		report += fmt.Sprintf("\n(+%d error serupa ditahan dalam 5 menit terakhir.)", suppressed)
	}

	_, sendErr := mctx.Client.SendMessage(context.Background(), ownerJID, &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String(report),
		},
	})
	if sendErr != nil {
		logfmt.Error("Gagal kirim notifikasi error AI ke owner: %v\n", sendErr)
	}
}
