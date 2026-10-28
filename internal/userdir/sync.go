package userdir

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"sort"
	"time"

	"wabot-go/internal/external"
	"wabot-go/internal/identity"
	"wabot-go/internal/logfmt"
	"wabot-go/internal/messaging"
	"wabot-go/internal/store"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/types"
)

// SyncOptions mengatur satu proses sync per grup.
type SyncOptions struct {
	Chat       types.JID
	WithPhotos bool
	MaxPhotos  int
	ForcePhoto bool
	PhotoTTL   time.Duration
	Delay      time.Duration
}

// SyncResult adalah ringkasan hasil sync.
type SyncResult struct {
	Members       int
	NewUsers      int
	UpdatedUsers  int
	Absent        int
	PhotosFetched int
	PhotosSkipped int
	PhotosFailed  int
	Duration      time.Duration
}

// Sync menarik metadata grup + identitas user + (opsional) foto profil.
// Tidak menyentuh pesan; hanya baca GroupInfo + contacts + (opt) PP.
func Sync(ctx context.Context, client *whatsmeow.Client, users *store.UsersDB, gdb *store.GroupDB, opt SyncOptions) (SyncResult, error) {
	start := time.Now()
	res := SyncResult{}
	if users == nil {
		return res, fmt.Errorf("users.db mati")
	}
	if gdb == nil {
		return res, fmt.Errorf("group.db mati")
	}
	if client == nil {
		return res, fmt.Errorf("client nil")
	}
	if opt.Chat.IsEmpty() {
		return res, fmt.Errorf("chat kosong")
	}
	if opt.MaxPhotos <= 0 {
		opt.MaxPhotos = 300
	}
	if opt.PhotoTTL <= 0 {
		opt.PhotoTTL = 7 * 24 * time.Hour
	}
	if opt.Delay <= 0 {
		opt.Delay = 350 * time.Millisecond
	}

	info, err := client.GetGroupInfo(ctx, opt.Chat)
	if err != nil {
		return res, fmt.Errorf("gagal ambil info grup: %w", err)
	}

	now := time.Now().Unix()
	presentIDs := make([]int64, 0, len(info.Participants))

	for _, pt := range info.Participants {
		ident := participantIdentity(pt)
		// Lengkapi pushname dari contacts (best-effort).
		if push := contactPushName(client, ctx, ident); push != "" {
			ident.PushName = push
		}
		if ident.PushName == "" && pt.DisplayName != "" {
			ident.PushName = pt.DisplayName
		}

		// Cek apakah user sudah ada (untuk hitung baru/diperbarui).
		existing := findExistingUser(users, ctx, ident)
		id, err := users.UpsertUserIdentity(ctx, ident)
		if err != nil {
			logfmt.Warn("userdir: gagal upsert user %s: %v\n", pt.JID.String(), err)
			continue
		}
		if existing == nil {
			res.NewUsers++
		} else {
			res.UpdatedUsers++
		}

		// Snapshot roster.
		member := store.GroupMember{
			ChatJID:      opt.Chat.String(),
			UserID:       id,
			JID:          pt.JID.String(),
			LID:          ident.LID,
			PN:           ident.PN,
			PushName:     ident.PushName,
			IsAdmin:      pt.IsAdmin,
			IsSuperAdmin: pt.IsSuperAdmin,
			Present:      true,
			LastSeen:     now,
		}
		if err := gdb.UpsertGroupMember(ctx, member); err != nil {
			logfmt.Warn("userdir: gagal upsert member %s: %v\n", pt.JID.String(), err)
		}
		presentIDs = append(presentIDs, id)
		res.Members++
	}

	// Tandai yang keluar.
	before, _ := gdb.CountGroupMembers(ctx, opt.Chat.String(), true)
	if err := gdb.MarkGroupMembersAbsent(ctx, opt.Chat.String(), presentIDs, now); err != nil {
		logfmt.Warn("userdir: gagal tandai absent: %v\n", err)
	}
	after, _ := gdb.CountGroupMembers(ctx, opt.Chat.String(), true)
	if before > after {
		res.Absent = before - after
	}

	// Pass foto (opsional).
	if opt.WithPhotos {
		fetched, skipped, failed := syncPhotos(ctx, client, users, gdb, opt, now)
		res.PhotosFetched = fetched
		res.PhotosSkipped = skipped
		res.PhotosFailed = failed
	}

	res.Duration = time.Since(start)
	return res, nil
}

func participantIdentity(pt types.GroupParticipant) store.UserIdentity {
	ident := store.UserIdentity{Username: pt.Username}
	lid := ""
	pn := ""
	if !pt.LID.IsEmpty() {
		lid = pt.LID.User
	}
	if !pt.PhoneNumber.IsEmpty() {
		pn = pt.PhoneNumber.User
	}
	if lid == "" && pn == "" {
		n := identity.Normalize(pt.JID)
		switch n.Server {
		case types.HiddenUserServer:
			lid = n.User
		case types.DefaultUserServer:
			pn = n.User
		}
	}
	ident.LID = lid
	ident.PN = pn
	ident.JID = pt.JID.String()
	return ident
}

func contactPushName(client *whatsmeow.Client, ctx context.Context, ident store.UserIdentity) string {
	candidates := []types.JID{}
	if ident.PN != "" {
		candidates = append(candidates, types.NewJID(ident.PN, types.DefaultUserServer))
	}
	if ident.LID != "" {
		candidates = append(candidates, types.NewJID(ident.LID, types.HiddenUserServer))
	}
	for _, j := range candidates {
		if client.Store == nil || client.Store.Contacts == nil {
			break
		}
		c, err := client.Store.Contacts.GetContact(ctx, j)
		if err != nil || !c.Found {
			continue
		}
		switch {
		case c.PushName != "":
			return c.PushName
		case c.FullName != "":
			return c.FullName
		case c.FirstName != "":
			return c.FirstName
		}
	}
	return ""
}

func findExistingUser(users *store.UsersDB, ctx context.Context, ident store.UserIdentity) *store.UserRecord {
	if ident.LID != "" {
		if r, err := users.FindUserByLID(ctx, ident.LID); err == nil && r != nil {
			return r
		}
	}
	if ident.PN != "" {
		if r, err := users.FindUserByPN(ctx, ident.PN); err == nil && r != nil {
			return r
		}
	}
	if ident.JID != "" {
		if r, err := users.FindUserByJID(ctx, ident.JID); err == nil && r != nil {
			return r
		}
	}
	return nil
}

func syncPhotos(ctx context.Context, client *whatsmeow.Client, users *store.UsersDB, gdb *store.GroupDB, opt SyncOptions, now int64) (int, int, int) {
	fetched, skipped, failed := 0, 0, 0
	members, err := gdb.ListGroupMembers(ctx, opt.Chat.String(), true, 0, 0)
	if err != nil {
		logfmt.Warn("userdir: gagal list member untuk foto: %v\n", err)
		return fetched, skipped, failed
	}
	// Urutkan agar deterministik (PN lalu LID).
	sort.Slice(members, func(i, j int) bool {
		if members[i].PN != members[j].PN {
			return members[i].PN < members[j].PN
		}
		return members[i].LID < members[j].LID
	})

	count := 0
	for _, m := range members {
		if count >= opt.MaxPhotos {
			break
		}
		u, err := users.GetUser(ctx, m.UserID)
		if err != nil {
			continue
		}
		// TTL skip.
		if !opt.ForcePhoto && u.PPStatus == "ok" && u.PPAt > 0 && now-u.PPAt < int64(opt.PhotoTTL.Seconds()) {
			skipped++
			continue
		}

		// Tentukan JID untuk fetch.
		jid := types.JID{}
		if m.LID != "" {
			jid = types.NewJID(m.LID, types.HiddenUserServer)
		} else if m.PN != "" {
			jid = types.NewJID(m.PN, types.DefaultUserServer)
		} else if m.JID != "" {
			if p, err := types.ParseJID(m.JID); err == nil {
				jid = p
			}
		}
		if jid.IsEmpty() {
			continue
		}

		count++
		info, err := client.GetProfilePictureInfo(ctx, jid, &whatsmeow.GetProfilePictureParams{ExistingID: u.PPID})
		if err != nil {
			if err == whatsmeow.ErrProfilePictureNotSet {
				_ = users.SetUserPhoto(ctx, u.ID, "none", "", "", "", 0, now)
				if u.PPPath != "" {
					_ = removeFile(u.PPPath)
				}
			} else if err == whatsmeow.ErrProfilePictureUnauthorized {
				_ = users.SetUserPhoto(ctx, u.ID, "hidden", "", "", "", 0, now)
			} else {
				_ = users.SetUserPhoto(ctx, u.ID, "error", u.PPID, u.PPSha, u.PPPath, u.PPBytes, now)
				logfmt.Warn("userdir: pp %s gagal: %v\n", jid.String(), err)
			}
			failed++
			time.Sleep(opt.Delay)
			continue
		}
		if info == nil {
			// Tidak berubah.
			_ = users.SetUserPhoto(ctx, u.ID, u.PPStatus, u.PPID, u.PPSha, u.PPPath, u.PPBytes, now)
			skipped++
			time.Sleep(opt.Delay)
			continue
		}
		if info.URL == "" {
			_ = users.SetUserPhoto(ctx, u.ID, "none", info.ID, "", "", 0, now)
			if u.PPPath != "" {
				_ = removeFile(u.PPPath)
			}
			skipped++
			time.Sleep(opt.Delay)
			continue
		}

		data, err := external.DownloadBytes(ctx, info.URL)
		if err != nil {
			data, err = external.DownloadBytesIPv4(ctx, info.URL)
		}
		if err != nil || len(data) == 0 || len(data) > 8<<20 {
			_ = users.SetUserPhoto(ctx, u.ID, "error", info.ID, u.PPSha, u.PPPath, u.PPBytes, now)
			failed++
			logfmt.Warn("userdir: unduh pp %s gagal: %v\n", jid.String(), err)
			time.Sleep(opt.Delay)
			continue
		}
		normalized := messaging.HeaderJPEG(data, 640, 85)
		if len(normalized) == 0 {
			_ = users.SetUserPhoto(ctx, u.ID, "error", info.ID, u.PPSha, u.PPPath, u.PPBytes, now)
			failed++
			time.Sleep(opt.Delay)
			continue
		}
		sha := sha256Hex(normalized)
		path := "data/pp/" + sha + ".jpg"
		if err := writeFileAtomic(path, normalized); err != nil {
			failed++
			logfmt.Warn("userdir: tulis pp %s gagal: %v\n", jid.String(), err)
			time.Sleep(opt.Delay)
			continue
		}
		if u.PPSha != "" && u.PPSha != sha && u.PPPath != "" {
			_ = removeFile(u.PPPath)
		}
		if err := users.SetUserPhoto(ctx, u.ID, "ok", info.ID, sha, path, len(normalized), now); err != nil {
			failed++
			logfmt.Warn("userdir: simpan metadata pp %s gagal: %v\n", jid.String(), err)
		} else {
			fetched++
		}
		time.Sleep(opt.Delay)
	}
	return fetched, skipped, failed
}

// --- helpers kecil ---

func sha256Hex(b []byte) string {
	// gunakan crypto/sha256
	h := sha256.Sum256(b)
	return fmt.Sprintf("%x", h[:])
}

func writeFileAtomic(path string, data []byte) error {
	if err := ensureDir("data/pp"); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func removeFile(path string) error {
	return os.Remove(path)
}

func ensureDir(dir string) error {
	return os.MkdirAll(dir, 0755)
}
