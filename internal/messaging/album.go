package messaging

import (
	"context"
	"fmt"

	"wabot-go/internal/store"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// AlbumAssociation merakit asosiasi MEDIA_ALBUM untuk satu pesan ANAK album.
// parentKey adalah kunci pesan INDUK (AlbumMessage) yang ID-nya di-generate
// duluan — meniru Baileys (generateWAMessageContent: associationType
// MEDIA_ALBUM + parentMessageKey, tanpa messageIndex).
func AlbumAssociation(parentKey *waCommon.MessageKey) *waE2E.MessageAssociation {
	return &waE2E.MessageAssociation{
		AssociationType:  waE2E.MessageAssociation_MEDIA_ALBUM.Enum(),
		ParentMessageKey: parentKey,
	}
}

// AlbumParentKey membuat kunci parent album di chat ini: RemoteJID = chat,
// FromMe = true, ID = parentID yang sudah di-generate duluan (lihat
// whatsmeow.GenerateMessageID). Participant sengaja kosong seperti kunci
// pesan fromMe di grup.
func AlbumParentKey(chat types.JID, parentID types.MessageID) *waCommon.MessageKey {
	return &waCommon.MessageKey{
		RemoteJID: proto.String(chat.String()),
		FromMe:    proto.Bool(true),
		ID:        proto.String(string(parentID)),
	}
}

// SendAlbumParent mengirim pesan INDUK album (hanya jumlah expected) dengan
// ID yang sudah ditentukan. Anak-anak dikirim terpisah, masing-masing
// membawa association ke kunci ini (lihat AlbumAssociation). Urutan induk
// dulu baru anak mengikuti tebakan terbaik dari perilaku klien — kalau uji
// lapangan menunjukkan tidak mengelompok, coba urutan sebaliknya.
func SendAlbumParent(ctx context.Context, mctx *store.MessageContext, parentID types.MessageID, nImage, nVideo int) error {
	_, err := mctx.Client.SendMessage(ctx, mctx.Chat, &waE2E.Message{
		AlbumMessage: &waE2E.AlbumMessage{
			ExpectedImageCount: proto.Uint32(uint32(nImage)),
			ExpectedVideoCount: proto.Uint32(uint32(nVideo)),
		},
	}, whatsmeow.SendRequestExtra{ID: parentID})
	if err != nil {
		return fmt.Errorf("gagal kirim induk album: %w", err)
	}
	return nil
}
