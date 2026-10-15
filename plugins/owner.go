package plugins

import (
	"context"
	"fmt"

	"wabot-go/internal/config"
	"wabot-go/internal/store"

	"github.com/polymorfa/hypermeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func init() {
	registerPlugin(RegisterOwner)
}

// RegisterOwner mendaftarkan command ".owner" -> balas dengan contact card (vCard)
// owner UTAMA (entri pertama OWNER_NUMBERS), diambil dari
// wabot-go/internal/config (config.OwnerName, config.PrimaryOwnerNumber).
func RegisterOwner(r *Registry) {
	r.Register(Command{
		Name:        "owner",
		Description: "Kirim kontak owner bot",
		Category:    CategoryUmum,
		Handler: func(ctx context.Context, mctx *store.MessageContext) error {
			ownerNum := config.PrimaryOwnerNumber()
			vcard := fmt.Sprintf(
				"BEGIN:VCARD\nVERSION:3.0\nN:;%s;;;\nFN:%s\nTEL;type=CELL;type=VOICE;waid=%s:+%s\nEND:VCARD",
				config.OwnerName, config.OwnerName, ownerNum, ownerNum,
			)
			_, err := mctx.Client.SendMessage(ctx, mctx.Chat, &waE2E.Message{
				ContactMessage: &waE2E.ContactMessage{
					DisplayName: proto.String(config.OwnerName),
					Vcard:       proto.String(vcard),
				},
			})
			if err == nil {
				store.LogOutgoing(mctx, "contact", config.OwnerName)
			}
			return err
		},
	})
}
