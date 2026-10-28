package userdir

import (
	"context"
	"fmt"
	"time"

	"wabot-go/internal/external"
	"wabot-go/internal/messaging"
	"wabot-go/internal/store"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/types"
)

// RefreshPhoto memperbarui foto satu user (abaikan TTL bila force).
// Mengembalikan status akhir ("ok","none","hidden","skipped","error").
func RefreshPhoto(ctx context.Context, client *whatsmeow.Client, users *store.UsersDB, u *store.UserRecord, jid types.JID, force bool) (string, error) {
	if users == nil || u == nil {
		return "error", fmt.Errorf("users.db mati")
	}
	now := time.Now().Unix()
	if !force && u.PPStatus == "ok" && u.PPAt > 0 && now-u.PPAt < int64((7*24*time.Hour).Seconds()) {
		return "skipped", nil
	}
	if jid.IsEmpty() {
		// coba dari record
		if u.LID != "" {
			jid = types.NewJID(u.LID, types.HiddenUserServer)
		} else if u.PN != "" {
			jid = types.NewJID(u.PN, types.DefaultUserServer)
		}
	}
	if jid.IsEmpty() {
		return "error", fmt.Errorf("jid kosong")
	}
	info, err := client.GetProfilePictureInfo(ctx, jid, &whatsmeow.GetProfilePictureParams{ExistingID: u.PPID})
	if err != nil {
		if err == whatsmeow.ErrProfilePictureNotSet {
			_ = users.SetUserPhoto(ctx, u.ID, "none", "", "", "", 0, now)
			if u.PPPath != "" {
				_ = removeFile(u.PPPath)
			}
			return "none", nil
		}
		if err == whatsmeow.ErrProfilePictureUnauthorized {
			_ = users.SetUserPhoto(ctx, u.ID, "hidden", "", "", "", 0, now)
			return "hidden", nil
		}
		_ = users.SetUserPhoto(ctx, u.ID, "error", u.PPID, u.PPSha, u.PPPath, u.PPBytes, now)
		return "error", err
	}
	if info == nil {
		_ = users.SetUserPhoto(ctx, u.ID, u.PPStatus, u.PPID, u.PPSha, u.PPPath, u.PPBytes, now)
		return "skipped", nil
	}
	if info.URL == "" {
		_ = users.SetUserPhoto(ctx, u.ID, "none", info.ID, "", "", 0, now)
		if u.PPPath != "" {
			_ = removeFile(u.PPPath)
		}
		return "none", nil
	}
	data, err := external.DownloadBytes(ctx, info.URL)
	if err != nil {
		data, err = external.DownloadBytesIPv4(ctx, info.URL)
	}
	if err != nil || len(data) == 0 || len(data) > 8<<20 {
		_ = users.SetUserPhoto(ctx, u.ID, "error", info.ID, u.PPSha, u.PPPath, u.PPBytes, now)
		return "error", err
	}
	normalized := messaging.HeaderJPEG(data, 640, 85)
	if len(normalized) == 0 {
		_ = users.SetUserPhoto(ctx, u.ID, "error", info.ID, u.PPSha, u.PPPath, u.PPBytes, now)
		return "error", fmt.Errorf("normalisasi gagal")
	}
	sha := sha256Hex(normalized)
	path := "data/pp/" + sha + ".jpg"
	if err := writeFileAtomic(path, normalized); err != nil {
		return "error", err
	}
	if u.PPSha != "" && u.PPSha != sha && u.PPPath != "" {
		_ = removeFile(u.PPPath)
	}
	if err := users.SetUserPhoto(ctx, u.ID, "ok", info.ID, sha, path, len(normalized), now); err != nil {
		return "error", err
	}
	return "ok", nil
}
