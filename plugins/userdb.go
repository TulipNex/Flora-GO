package plugins

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"wabot-go/internal/logfmt"
	"wabot-go/internal/messaging"
	"wabot-go/internal/store"
	"wabot-go/internal/userdir"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types"
	"google.golang.org/protobuf/proto"
)

func init() {
	registerPlugin(RegisterUserDB)
}

// RegisterUserDB mendaftarkan command ".userdb" (alias: .udb) — owner-only.
func RegisterUserDB(r *Registry) {
	r.Register(Command{
		Name:        "userdb",
		Aliases:     []string{"udb"},
		Description: "Direktori user per grup (owner). .userdb sync|syncpp|status|info|stats|prune",
		Category:    CategoryOwner,
		Handler:     handleUserDB,
	})
}

const userdbUsage = "*UserDB — direktori identitas user* (khusus owner)\n" +
	"ID grup lihat di .listgrup.\n" +
	"User: @mention, reply, nomor HP, atau JID.\n" +
	"\n" +
	"*Sync*:\n" +
	"- .userdb sync <id grup|user>\n" +
	"- .userdb syncpp <id grup|user>   (sekaligus foto profil)\n" +
	"  tanpa argumen di grup = grup itu sendiri\n" +
	"\n" +
	"*Lainnya*:\n" +
	"- .userdb status\n" +
	"- .userdb info <user>\n" +
	"- .userdb stats\n" +
	"- .userdb prune\n"

type userdirSyncState struct {
	Label   string
	Started time.Time
	End     time.Time
	Done    bool
	Err     error
	Result  userdir.SyncResult
	mu      sync.Mutex
}

var (
	userdirSyncMu sync.Mutex
	userdirSyncs  = map[string]*userdirSyncState{}
)

func handleUserDB(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	raw := strings.TrimSpace(mctx.RawArgs)
	if raw == "" {
		return store.ReplyText(ctx, mctx, userdbUsage)
	}
	fields := strings.Fields(raw)
	sub := strings.ToLower(fields[0])
	rest := strings.TrimSpace(raw[len(fields[0]):])

	switch sub {
	case "sync":
		return userdbSync(ctx, mctx, rest, false)
	case "syncpp":
		return userdbSync(ctx, mctx, rest, true)
	case "status":
		return userdbStatus(ctx, mctx)
	case "info":
		return userdbInfo(ctx, mctx, rest)
	case "stats":
		return userdbStats(ctx, mctx)
	case "prune":
		return userdbPrune(ctx, mctx, rest)
	default:
		return store.ReplyText(ctx, mctx, userdbUsage)
	}
}

// --- sync ---

func userdbSync(ctx context.Context, mctx *store.MessageContext, rest string, withPhotos bool) error {
	users := store.Users()
	if users == nil {
		return store.ReplyText(ctx, mctx, "users.db mati.")
	}
	gdb := store.GroupsFor(mctx.Client)

	// Tanpa argumen: di grup = grup itu sendiri.
	if strings.TrimSpace(rest) == "" {
		if !mctx.IsGroup() {
			return store.ReplyText(ctx, mctx, "Sebutkan ID grup atau target user.\n\n"+userdbUsage)
		}
		rest = mctx.Chat.String()
	}

	// Coba resolve sebagai grup (1-4 digit / ID).
	if gid, chatJID, isGroup, err := userdbResolveGroupID(ctx, mctx, rest); err == nil && isGroup {
		// Verifikasi joined.
		entry, _ := gdb.GroupIDBySeq(ctx, gid)
		if entry == nil || !entry.Present {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("ID grup %03d tidak dikenal / bot sudah keluar. Jalankan .listgrup.", gid))
		}
		chat, err := types.ParseJID(chatJID)
		if err != nil {
			return store.ReplyText(ctx, mctx, "JID grup tidak valid.")
		}
		label := fmt.Sprintf("[%03d] %s", gid, entry.Name)

		if !withPhotos {
			res, err := userdir.Sync(ctx, mctx.Client, users, gdb, userdir.SyncOptions{
				Chat:       chat,
				WithPhotos: false,
				MaxPhotos:  300,
				PhotoTTL:   7 * 24 * time.Hour,
				Delay:      350 * time.Millisecond,
			})
			if err != nil {
				return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal sync %s: %v", label, err))
			}
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Sync %s selesai.\n%s", label, userdbSyncSummary(res)))
		}

		// Async dengan foto.
		origin := mctx.Chat
		state := &userdirSyncState{Label: label, Started: time.Now()}
		userdirSyncMu.Lock()
		userdirSyncs[chat.String()] = state
		userdirSyncMu.Unlock()

		_ = store.ReplyText(ctx, mctx, fmt.Sprintf("Sync foto %s dimulai (max 300). Progres: .userdb status", label))

		go userdbSyncAsyncGroup(mctx.Client, origin, chat, label, state)
		return nil
	}

	// Bukan grup → user target.
	jid, err := userdbResolveUserTarget(mctx, rest)
	if err != nil {
		return store.ReplyText(ctx, mctx, err.Error())
	}
	if withPhotos {
		res, err := userdir.SyncUser(ctx, mctx.Client, users, gdb, userdir.UserSyncOptions{
			Chat:       types.JID{},
			Target:     jid,
			WithPhoto:  true,
			ForcePhoto: true,
		})
		if err != nil {
			return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal syncpp %s: %v", jid.String(), err))
		}
		return store.ReplyText(ctx, mctx, userdbUserSummary(res))
	}
	res, err := userdir.SyncUser(ctx, mctx.Client, users, gdb, userdir.UserSyncOptions{
		Chat:      types.JID{},
		Target:    jid,
		WithPhoto: false,
	})
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal sync %s: %v", jid.String(), err))
	}
	return store.ReplyText(ctx, mctx, userdbUserSummary(res))
}

func userdbSyncSummary(res userdir.SyncResult) string {
	return fmt.Sprintf("Member: %d (baru %d, diperbarui %d, keluar %d)\nFoto: diambil %d, dilewati %d, gagal %d\nDurasi: %s",
		res.Members, res.NewUsers, res.UpdatedUsers, res.Absent,
		res.PhotosFetched, res.PhotosSkipped, res.PhotosFailed,
		res.Duration.Round(time.Second))
}

func userdbUserSummary(res userdir.UserSyncResult) string {
	pp := res.PPStatus
	if pp == "" {
		pp = "-"
	}
	member := "bukan member grup"
	if res.IsMember {
		member = "member"
		if res.IsSuper {
			member = "superadmin"
		} else if res.IsAdmin {
			member = "admin"
		}
	}
	return fmt.Sprintf("User %s\nLID: %s\nPN: %s\nPushname: %s\nNama: %s\nUsername: %s\nFoto: %s\nKeanggotaan: %s",
		res.JID, valueOrDash(res.LID), valueOrDash(res.PN), valueOrDash(res.PushName),
		valueOrDash(res.FullName), valueOrDash(res.Username), pp, member)
}

// --- resolve group id ---

func userdbResolveGroupID(ctx context.Context, mctx *store.MessageContext, rest string) (int, string, bool, error) {
	gdb := store.GroupsFor(mctx.Client)

	tok := strings.TrimSpace(rest)
	if flds := strings.Fields(tok); len(flds) > 0 {
		tok = flds[0]
	}
	tok = strings.Trim(tok, "[]")
	n, err := strconv.Atoi(tok)
	if err != nil || n < 1 || n > 9999 {
		return 0, "", false, fmt.Errorf("bukan id grup")
	}

	// pastikan indeks ada
	groups, err := mctx.Client.GetJoinedGroups(ctx)
	if err == nil && len(groups) > 0 {
		present := make([]string, 0, len(groups))
		for _, g := range groups {
			name := g.Name
			if name == "" {
				name = "(tanpa nama)"
			}
			if _, err := gdb.UpsertGroupID(ctx, g.JID.String(), name); err != nil {
				logfmt.Warn("userdb: gagal indeks %s: %v\n", g.JID.String(), err)
			}
			present = append(present, g.JID.String())
		}
		_ = gdb.MarkGroupIDsAbsent(ctx, present)
	}

	entry, err := gdb.GroupIDBySeq(ctx, n)
	if err != nil || entry == nil {
		return 0, "", false, fmt.Errorf("id tidak ditemukan")
	}
	return n, entry.ChatJID, true, nil
}

// --- resolve user target ---

func userdbResolveUserTarget(mctx *store.MessageContext, rest string) (types.JID, error) {
	mentions := sprParseMentions(mctx)
	// reply only
	if strings.TrimSpace(rest) == "" {
		if q := mctx.QuotedParticipant(); q != "" {
			jid, err := types.ParseJID(q)
			if err == nil {
				return jid.ToNonAD(), nil
			}
		}
		return types.JID{}, fmt.Errorf("Butuh target user.")
	}
	// mention
	if strings.HasPrefix(strings.TrimSpace(rest), "@") {
		tok := strings.Fields(rest)[0]
		jid, err := sprResolveToken(mctx, mentions, tok)
		if err != nil {
			return types.JID{}, err
		}
		return jid.ToNonAD(), nil
	}
	// JID eksplisit
	if strings.Contains(rest, "@") {
		// ambil token pertama yang mengandung @
		for _, t := range strings.Fields(rest) {
			if strings.Contains(t, "@") {
				jid, err := types.ParseJID(strings.Trim(t, ",;"))
				if err == nil {
					return jid.ToNonAD(), nil
				}
			}
		}
	}
	// nomor HP (gabung semua token)
	phone := userdbNormalizePhone(rest)
	if phone != "" {
		return types.NewJID(phone, types.DefaultUserServer), nil
	}
	return types.JID{}, fmt.Errorf("Target user tidak dikenal. Pakai @mention, reply, nomor HP, atau JID.")
}

func userdbNormalizePhone(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	s := b.String()
	if len(s) < 8 {
		return ""
	}
	if strings.HasPrefix(s, "0") {
		s = "62" + s[1:]
	}
	if len(s) < 8 || len(s) > 15 {
		return ""
	}
	return s
}

// --- info ---

func userdbInfo(ctx context.Context, mctx *store.MessageContext, rest string) error {
	jid, err := userdbResolveUserTarget(mctx, rest)
	if err != nil {
		// coba reply
		if strings.TrimSpace(rest) == "" {
			if q := mctx.QuotedParticipant(); q != "" {
				if pj, perr := types.ParseJID(q); perr == nil {
					jid = pj.ToNonAD()
					err = nil
				}
			}
		}
	}
	if err != nil {
		return store.ReplyText(ctx, mctx, err.Error())
	}

	users := store.Users()
	if users == nil {
		return store.ReplyText(ctx, mctx, "users.db mati.")
	}

	var rec *store.UserRecord
	if jid.Server == types.HiddenUserServer {
		rec, _ = users.FindUserByLID(ctx, jid.User)
	}
	if rec == nil && jid.Server == types.DefaultUserServer {
		rec, _ = users.FindUserByPN(ctx, jid.User)
	}
	if rec == nil {
		rec, _ = users.FindUserByJID(ctx, jid.String())
	}
	if rec == nil {
		return store.ReplyText(ctx, mctx, "User belum ada di database. Jalankan .userdb sync <user> dulu.")
	}

	// caption
	lid := rec.LID
	if lid == "" {
		lid = "-"
	}
	pn := rec.PN
	if pn == "" {
		pn = "-"
	}
	pp := rec.PPStatus
	if pp == "" {
		pp = "-"
	}
	memberLine := ""
	if mctx.IsGroup() {
		if gm, err := store.GroupsFor(mctx.Client).FindGroupMember(ctx, mctx.Chat.String(), jid.User); err == nil && gm != nil {
			status := "member"
			if !gm.Present {
				status = "keluar"
			}
			memberLine = fmt.Sprintf("\nKeanggotaan grup ini: %s (admin=%v super=%v)", status, gm.IsAdmin, gm.IsSuperAdmin)
		} else {
			memberLine = "\nKeanggotaan grup ini: tidak tercatat"
		}
	}
	caption := fmt.Sprintf(
		"User %s\nLID: %s\nPN: %s\nJID: %s\nPushname: %s\nNama: %s %s\nUsername: %s\nFoto: %s\nDilihat: %d kali (terakhir %s)%s",
		jid.String(), lid, pn, rec.JID, valueOrDash(rec.PushName),
		valueOrDash(rec.FullName), valueOrDash(rec.FirstName), valueOrDash(rec.Username),
		pp, rec.SeenCount, formatUnix(rec.LastSeen), memberLine)

	// foto
	data := []byte{}
	if rec.PPPath != "" {
		if b, err := os.ReadFile(rec.PPPath); err == nil && len(b) > 0 {
			data = b
		}
	}
	if len(data) == 0 {
		// coba refresh (best-effort, tidak force jika baru dicek)
		status, err := userdir.RefreshPhoto(ctx, mctx.Client, users, rec, jid, false)
		if err == nil && status == "ok" {
			if b, err := os.ReadFile(rec.PPPath); err == nil {
				// path mungkin berubah; baca dari record terbaru
				if r2, err := users.GetUser(ctx, rec.ID); err == nil && r2.PPPath != "" {
					if b2, err := os.ReadFile(r2.PPPath); err == nil {
						data = b2
					}
				}
				if len(data) == 0 {
					data = b
				}
			}
		}
	}

	if len(data) == 0 {
		return store.ReplyText(ctx, mctx, caption)
	}
	im, err := messaging.UploadImageMessage(ctx, mctx.Client, data)
	if err != nil {
		return store.ReplyText(ctx, mctx, caption)
	}
	im.Caption = proto.String(caption)
	_, err = mctx.Client.SendMessage(ctx, mctx.Chat, &waE2E.Message{ImageMessage: im})
	if err != nil {
		return store.ReplyText(ctx, mctx, caption)
	}
	store.LogOutgoing(mctx, "userdb-info", caption)
	return nil
}

// --- status ---

func userdbStatus(ctx context.Context, mctx *store.MessageContext) error {
	userdirSyncMu.Lock()
	defer userdirSyncMu.Unlock()
	if len(userdirSyncs) == 0 {
		return store.ReplyText(ctx, mctx, "Belum ada sync foto berjalan/selesai.")
	}
	keys := make([]string, 0, len(userdirSyncs))
	for k := range userdirSyncs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	sb.WriteString("Sync foto:")
	for _, k := range keys {
		st := userdirSyncs[k]
		st.mu.Lock()
		state := "berjalan"
		if st.Done {
			state = "selesai"
		}
		line := fmt.Sprintf("\n- %s: %s", st.Label, state)
		if st.Done {
			line += fmt.Sprintf(" | %s", userdbSyncSummary(st.Result))
		} else {
			line += fmt.Sprintf(" | mulai %s", st.Started.Format("15:04:05"))
		}
		if st.Err != nil {
			line += fmt.Sprintf(" | err: %v", st.Err)
		}
		st.mu.Unlock()
		sb.WriteString(line)
	}
	return store.ReplyText(ctx, mctx, sb.String())
}

// --- stats ---

func userdbStats(ctx context.Context, mctx *store.MessageContext) error {
	users := store.Users()
	if users == nil {
		return store.ReplyText(ctx, mctx, "users.db mati.")
	}
	totalUsers, _ := users.CountUsers(ctx)
	gdb := store.GroupsFor(mctx.Client)
	memberTotal, memberPresent := 0, 0
	if mctx.IsGroup() {
		memberTotal, _ = gdb.CountGroupMembers(ctx, mctx.Chat.String(), false)
		memberPresent, _ = gdb.CountGroupMembers(ctx, mctx.Chat.String(), true)
	}
	groupCount, _ := gdb.CountGroupIDs(ctx)

	cacheFiles := 0
	cacheBytes := int64(0)
	if entries, err := os.ReadDir("data/pp"); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			cacheFiles++
			if info, err := e.Info(); err == nil {
				cacheBytes += info.Size()
			}
		}
	}
	return store.ReplyText(ctx, mctx, fmt.Sprintf(
		"UserDB stats\nTotal user: %d\nGrup terindeks: %d\nGrup ini: %d anggota (%d hadir)\nCache foto: %d file (%d B)",
		totalUsers, groupCount, memberTotal, memberPresent, cacheFiles, cacheBytes))
}

// --- prune ---

func userdbPrune(ctx context.Context, mctx *store.MessageContext, rest string) error {
	deleted, freed, err := userdir.PrunePhotos(ctx, store.Users(), 5000, 300<<20)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Prune gagal: %v", err))
	}
	return store.ReplyText(ctx, mctx, fmt.Sprintf("Prune selesai: %d file dihapus, %d B dibebaskan.", deleted, freed))
}

// --- async syncpp grup (dipanggil dari userdbSync) ---

// userdbSyncAsyncGroup menjalankan sync foto grup di background dan mengirim
// ringkasan ke origin (bukan target). Dipisah supaya userdbSync tetap ringkas.
func userdbSyncAsyncGroup(client *whatsmeow.Client, origin, target types.JID, label string, state *userdirSyncState) {
	bg := context.Background()
	gdb := store.GroupsFor(client)
	res, err := userdir.Sync(bg, client, store.Users(), gdb, userdir.SyncOptions{
		Chat:       target,
		WithPhotos: true,
		MaxPhotos:  300,
		PhotoTTL:   7 * 24 * time.Hour,
		Delay:      350 * time.Millisecond,
	})
	state.mu.Lock()
	state.Result = res
	state.Err = err
	state.Done = true
	state.End = time.Now()
	state.mu.Unlock()

	msg := ""
	if err != nil {
		msg = fmt.Sprintf("Sync foto %s gagal: %v", label, err)
	} else {
		msg = fmt.Sprintf("Sync foto %s selesai.\n%s", label, userdbSyncSummary(res))
	}
	out := &store.MessageContext{Client: client, Chat: origin}
	_ = store.SendText(bg, out, msg)
}

func valueOrDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func formatUnix(ts int64) string {
	if ts <= 0 {
		return "-"
	}
	return time.Unix(ts, 0).Format("2006-01-02 15:04")
}
