package plugins

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/polymorfa/hypermeow/types"

	"wabot-go/internal/ai"
	"wabot-go/internal/identity"
	"wabot-go/internal/store"
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
	return identity.Strings(ctx, mctx.Client, target, types.JID{})
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
