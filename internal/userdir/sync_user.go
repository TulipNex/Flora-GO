package userdir

import (
	"context"
	"fmt"
	"time"

	"wabot-go/internal/identity"
	"wabot-go/internal/logfmt"
	"wabot-go/internal/store"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/types"
)

// UserSyncOptions mengatur sync satu user.
type UserSyncOptions struct {
	Chat       types.JID // grup konteks (opsional) untuk roster
	Target     types.JID
	WithPhoto  bool
	ForcePhoto bool
}

// UserSyncResult adalah ringkasan sync satu user.
type UserSyncResult struct {
	UserID   int64
	LID      string
	PN       string
	JID      string
	PushName string
	FullName string
	Username string
	PPStatus string
	IsMember bool
	IsAdmin  bool
	IsSuper  bool
}

// SyncUser menarik identitas satu user (GetUserInfo + contacts) lalu upsert.
func SyncUser(ctx context.Context, client *whatsmeow.Client, users *store.UsersDB, gdb *store.GroupDB, opt UserSyncOptions) (UserSyncResult, error) {
	res := UserSyncResult{}
	if users == nil {
		return res, fmt.Errorf("users.db mati")
	}
	if client == nil {
		return res, fmt.Errorf("client nil")
	}
	if opt.Target.IsEmpty() {
		return res, fmt.Errorf("target kosong")
	}

	// Tentukan LID/PN awal dari target.
	lid := ""
	pn := ""
	switch opt.Target.Server {
	case types.HiddenUserServer:
		lid = opt.Target.User
	case types.DefaultUserServer:
		pn = opt.Target.User
	default:
		// JID eksplisit non-user → tolak.
		return res, fmt.Errorf("target bukan user")
	}

	// Lengkapi pasangan format.
	if lid != "" && pn == "" {
		if j, ok := identity.ResolvePN(ctx, client, types.NewJID(lid, types.HiddenUserServer), types.JID{}); ok {
			pn = j.User
		}
	}
	if pn != "" && lid == "" {
		if j, ok := identity.ResolveLID(ctx, client, types.NewJID(pn, types.DefaultUserServer), types.JID{}); ok {
			lid = j.User
		}
	}

	// GetUserInfo (best-effort).
	username := ""
	ppID := ""
	if pn != "" || lid != "" {
		jids := []types.JID{}
		if pn != "" {
			jids = append(jids, types.NewJID(pn, types.DefaultUserServer))
		}
		if lid != "" {
			jids = append(jids, types.NewJID(lid, types.HiddenUserServer))
		}
		if info, err := client.GetUserInfo(ctx, jids); err == nil {
			for _, ui := range info {
				if ui.LID.User != "" {
					lid = ui.LID.User
				}
				if ui.Username != "" {
					username = ui.Username
				}
				if ui.PictureID != "" {
					ppID = ui.PictureID
				}
			}
		}
	}

	// Contacts (pushname/nama) — best-effort.
	pushname := ""
	fullName := ""
	firstName := ""
	redacted := ""
	candidates := []types.JID{}
	if pn != "" {
		candidates = append(candidates, types.NewJID(pn, types.DefaultUserServer))
	}
	if lid != "" {
		candidates = append(candidates, types.NewJID(lid, types.HiddenUserServer))
	}
	for _, j := range candidates {
		if client.Store == nil || client.Store.Contacts == nil {
			break
		}
		c, err := client.Store.Contacts.GetContact(ctx, j)
		if err != nil || !c.Found {
			continue
		}
		if pushname == "" {
			switch {
			case c.PushName != "":
				pushname = c.PushName
			case c.FullName != "":
				pushname = c.FullName
			case c.FirstName != "":
				pushname = c.FirstName
			}
		}
		if fullName == "" {
			fullName = c.FullName
		}
		if firstName == "" {
			firstName = c.FirstName
		}
		if redacted == "" {
			redacted = c.RedactedPhone
		}
		if username == "" {
			username = c.Username
		}
	}

	jidStr := opt.Target.String()
	if lid != "" {
		jidStr = types.NewJID(lid, types.HiddenUserServer).String()
	} else if pn != "" {
		jidStr = types.NewJID(pn, types.DefaultUserServer).String()
	}

	id, err := users.UpsertUserIdentity(ctx, store.UserIdentity{
		LID:           lid,
		PN:            pn,
		JID:           jidStr,
		PushName:      pushname,
		FullName:      fullName,
		FirstName:     firstName,
		Username:      username,
		RedactedPhone: redacted,
		Client:        client,
	})
	if err != nil {
		return res, err
	}

	// Set pp_id dari GetUserInfo bila ada (membantu ExistingID), tapi jangan
	// menimpa status yang sudah ada.
	if ppID != "" {
		u, _ := users.GetUser(ctx, id)
		if u != nil && u.PPID == "" {
			_ = users.SetUserPhoto(ctx, id, u.PPStatus, ppID, u.PPSha, u.PPPath, u.PPBytes, u.PPAt)
		}
	}

	// Roster di grup konteks (jika target member).
	isMember := false
	isAdmin := false
	isSuper := false
	if !opt.Chat.IsEmpty() && gdb != nil {
		info, err := client.GetGroupInfo(ctx, opt.Chat)
		if err == nil {
			for _, pt := range info.Participants {
				match := false
				if lid != "" && (pt.LID.User == lid) {
					match = true
				}
				if !match && pn != "" && (pt.PhoneNumber.User == pn || pt.JID.User == pn) {
					match = true
				}
				if !match && pt.JID.User == opt.Target.User {
					match = true
				}
				if match {
					isMember = true
					isAdmin = pt.IsAdmin
					isSuper = pt.IsSuperAdmin
					_ = gdb.UpsertGroupMember(ctx, store.GroupMember{
						ChatJID:      opt.Chat.String(),
						UserID:       id,
						JID:          pt.JID.String(),
						LID:          lid,
						PN:           pn,
						PushName:     pushname,
						IsAdmin:      isAdmin,
						IsSuperAdmin: isSuper,
						Present:      true,
						LastSeen:     time.Now().Unix(),
					})
					break
				}
			}
		}
	}

	res.UserID = id
	res.LID = lid
	res.PN = pn
	res.JID = jidStr
	res.PushName = pushname
	res.FullName = fullName
	res.Username = username
	res.PPStatus = ""
	if u, err := users.GetUser(ctx, id); err == nil && u != nil {
		res.PPStatus = u.PPStatus
	}
	res.IsMember = isMember
	res.IsAdmin = isAdmin
	res.IsSuper = isSuper

	if opt.WithPhoto {
		u, err := users.GetUser(ctx, id)
		if err == nil && u != nil {
			jid := opt.Target
			if jid.IsEmpty() {
				if lid != "" {
					jid = types.NewJID(lid, types.HiddenUserServer)
				} else if pn != "" {
					jid = types.NewJID(pn, types.DefaultUserServer)
				}
			}
			status, err := RefreshPhoto(ctx, client, users, u, jid, opt.ForcePhoto)
			if err != nil {
				logfmt.Warn("userdir: refresh foto %s gagal: %v\n", jid.String(), err)
			}
			res.PPStatus = status
		}
	}

	return res, nil
}
