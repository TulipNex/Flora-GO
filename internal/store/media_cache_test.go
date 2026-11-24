package store

import (
	"testing"

	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types"
	"github.com/polymorfa/hypermeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestMediaCache(t *testing.T) {
	// 1. Uji media standar di bawah batas 50 MB (15 MB video)
	validSize := uint64(15 << 20)
	evtValid := &events.Message{
		Info: types.MessageInfo{
			ID: "STANZA_TEST_1",
		},
		Message: &waE2E.Message{
			VideoMessage: &waE2E.VideoMessage{
				URL:           proto.String("https://mmg.whatsapp.net/v/123"),
				DirectPath:    proto.String("/v/t62.7114-24/123_n.enc"),
				MediaKey:      []byte("test_key_32bytes_sample12345678"),
				FileLength:    proto.Uint64(validSize),
				Mimetype:      proto.String("video/mp4"),
				Seconds:       proto.Uint32(45),
				JPEGThumbnail: []byte("large_thumbnail_data_should_be_stripped"),
			},
		},
	}

	CacheIncomingMedia(evtValid)

	// Verifikasi MediaDescriptor
	desc := GetCachedMediaDescriptor("STANZA_TEST_1")
	if desc == nil {
		t.Fatalf("ekspektasi MediaDescriptor tersimpan, tapi nil")
	}
	if desc.Type != "video" {
		t.Fatalf("tipe media salah: %s", desc.Type)
	}
	if desc.DirectPath != "/v/t62.7114-24/123_n.enc" {
		t.Fatalf("direct path descriptor salah: %s", desc.DirectPath)
	}
	if desc.Seconds != 45 {
		t.Fatalf("seconds salah: %d", desc.Seconds)
	}

	// Verifikasi rekonstruksi waE2E.Message
	cached := GetCachedMediaMessage("STANZA_TEST_1")
	if cached == nil {
		t.Fatalf("ekspektasi media tersimpan di cache, tapi nil")
	}
	vid := cached.GetVideoMessage()
	if vid == nil {
		t.Fatalf("ekspektasi VideoMessage, tapi nil")
	}
	if vid.GetDirectPath() != "/v/t62.7114-24/123_n.enc" {
		t.Fatalf("direct path tidak cocok: %s", vid.GetDirectPath())
	}
	if vid.GetSeconds() != 45 {
		t.Fatalf("seconds tidak cocok: %d", vid.GetSeconds())
	}
	// Pastikan JPEGThumbnail TIDAK tersimpan (telah distrip)
	if len(vid.GetJPEGThumbnail()) > 0 {
		t.Fatalf("JPEGThumbnail seharusnya tidak dicache")
	}

	// 2. Uji media di atas batas 50 MB (misal 55 MB video) -> Harus ditolak
	oversize := uint64(55 << 20)
	evtOversize := &events.Message{
		Info: types.MessageInfo{
			ID: "STANZA_OVERSIZE",
		},
		Message: &waE2E.Message{
			VideoMessage: &waE2E.VideoMessage{
				DirectPath: proto.String("/v/oversize.enc"),
				FileLength: proto.Uint64(oversize),
				Mimetype:   proto.String("video/mp4"),
			},
		},
	}

	CacheIncomingMedia(evtOversize)
	if GetCachedMediaMessage("STANZA_OVERSIZE") != nil {
		t.Fatalf("media > 50 MB seharusnya tidak dicache")
	}

	// 3. Uji media tanpa DirectPath dan tanpa URL -> Harus ditolak dari cache
	evtNoPath := &events.Message{
		Info: types.MessageInfo{
			ID: "STANZA_NO_PATH",
		},
		Message: &waE2E.Message{
			VideoMessage: &waE2E.VideoMessage{
				FileLength: proto.Uint64(validSize),
				Mimetype:   proto.String("video/mp4"),
			},
		},
	}

	CacheIncomingMedia(evtNoPath)
	if GetCachedMediaMessage("STANZA_NO_PATH") != nil {
		t.Fatalf("media tanpa direct path seharusnya tidak dicache")
	}

	// 4. Uji media dari channel (newsletter) -> Harus ditolak
	evtNewsletter := &events.Message{
		Info: types.MessageInfo{
			ID: "STANZA_NEWSLETTER",
			Chat: types.JID{
				Server: "newsletter",
			},
		},
		Message: &waE2E.Message{
			ImageMessage: &waE2E.ImageMessage{
				DirectPath: proto.String("/mms/newsletter.enc"),
				FileLength: proto.Uint64(1 << 20),
				Mimetype:   proto.String("image/jpeg"),
			},
		},
	}
	CacheIncomingMedia(evtNewsletter)
	if GetCachedMediaMessage("STANZA_NEWSLETTER") != nil {
		t.Fatalf("media dari newsletter seharusnya tidak dicache")
	}
}

func TestResolveQuotedWithMediaCache(t *testing.T) {
	// Simpan pesan asli ke cache
	originalEvt := &events.Message{
		Info: types.MessageInfo{
			ID: "MSG_ORIGINAL_VIDEO",
		},
		Message: &waE2E.Message{
			VideoMessage: &waE2E.VideoMessage{
				DirectPath: proto.String("/mms/video/valid_path.enc"),
				MediaKey:   []byte("secret_key_12345678"),
				FileLength: proto.Uint64(5 << 20), // 5MB
				Mimetype:   proto.String("video/mp4"),
				Seconds:    proto.Uint32(12),
			},
		},
	}
	CacheIncomingMedia(originalEvt)

	// Buat pesan reply (quoted) di mana WhatsApp memangkas DirectPath-nya
	replyEvt := &events.Message{
		Info: types.MessageInfo{
			ID: "REPLY_MSG_1",
		},
		Message: &waE2E.Message{
			ExtendedTextMessage: &waE2E.ExtendedTextMessage{
				Text: proto.String(".tomp3"),
				ContextInfo: &waE2E.ContextInfo{
					StanzaID: proto.String("MSG_ORIGINAL_VIDEO"),
					QuotedMessage: &waE2E.Message{
						VideoMessage: &waE2E.VideoMessage{
							// DirectPath kosong seperti yang dikirim WhatsApp
							DirectPath: proto.String(""),
							Mimetype:   proto.String("video/mp4"),
							Seconds:    proto.Uint32(10),
						},
					},
				},
			},
		},
	}

	extracted := ExtractQuotedMessage(replyEvt)
	if extracted == nil {
		t.Fatalf("ExtractQuotedMessage mengembalikan nil")
	}

	vid := extracted.GetVideoMessage()
	if vid == nil {
		t.Fatalf("seharusnya mengembalikan VideoMessage")
	}
	if vid.GetDirectPath() != "/mms/video/valid_path.enc" {
		t.Fatalf("DirectPath gagal diresolve dari MediaCache: %s", vid.GetDirectPath())
	}
	if vid.GetSeconds() != 12 {
		t.Fatalf("Seconds gagal diresolve: %d", vid.GetSeconds())
	}
}

func TestTrackBotMedia(t *testing.T) {
	botMsgID := "BOT_SENT_IMAGE_123"
	botMedia := &waE2E.Message{
		ImageMessage: &waE2E.ImageMessage{
			DirectPath: proto.String("/mms/image/bot_generated.enc"),
			MediaKey:   []byte("secret_key_bot_image_1234567890"),
			FileLength: proto.Uint64(1 << 20), // 1MB
			Mimetype:   proto.String("image/jpeg"),
		},
	}

	// Simpan pesan media keluar bot via TrackBotMedia
	TrackBotMedia(botMsgID, botMedia, "hasil seedream")

	// 1. Verifikasi terdaftar di bot message tracker
	if !IsBotMessageID(botMsgID) {
		t.Fatalf("seharusnya terdaftar sebagai BotMessageID")
	}
	if GetBotMessageText(botMsgID) != "hasil seedream" {
		t.Fatalf("teks bot message tidak cocok: %s", GetBotMessageText(botMsgID))
	}

	// 2. Verifikasi tersimpan di MediaCache
	desc := GetCachedMediaDescriptor(botMsgID)
	if desc == nil {
		t.Fatalf("MediaDescriptor bot outgoing seharusnya tersimpan di MediaCache")
	}
	if desc.DirectPath != "/mms/image/bot_generated.enc" {
		t.Fatalf("DirectPath media bot tidak cocok: %s", desc.DirectPath)
	}

	// 3. Verifikasi saat user me-reply gambar bot dengan .sticker (DirectPath kosong di quote)
	userReply := &events.Message{
		Info: types.MessageInfo{
			ID: "USER_REPLY_STICKER",
		},
		Message: &waE2E.Message{
			ExtendedTextMessage: &waE2E.ExtendedTextMessage{
				Text: proto.String(".sticker"),
				ContextInfo: &waE2E.ContextInfo{
					StanzaID: proto.String(botMsgID),
					QuotedMessage: &waE2E.Message{
						ImageMessage: &waE2E.ImageMessage{
							// DirectPath kosong seperti yang dikirim WhatsApp
							DirectPath: proto.String(""),
							Mimetype:   proto.String("image/jpeg"),
						},
					},
				},
			},
		},
	}

	extracted := ExtractQuotedMessage(userReply)
	if extracted == nil {
		t.Fatalf("ExtractQuotedMessage mengembalikan nil")
	}
	img := extracted.GetImageMessage()
	if img == nil {
		t.Fatalf("seharusnya mengembalikan ImageMessage")
	}
	if img.GetDirectPath() != "/mms/image/bot_generated.enc" {
		t.Fatalf("DirectPath media bot gagal diresolve: %s", img.GetDirectPath())
	}
}
