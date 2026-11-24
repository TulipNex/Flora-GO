package store

import (
	"sync"
	"time"

	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types/events"
	"google.golang.org/protobuf/proto"
)

// In-Memory Media Descriptor Cache:
// Menyimpan HANYA deskriptor metadata kunci (~200 byte per entri, BUKAN byte media utuh
// dan BUKAN objek protobuf utuh yang membawa JPEGThumbnail).
// Digunakan untuk merekonstruksi DirectPath & MediaKey saat pengguna me-reply media
// dengan command bot (seperti .tomp3, .shazam, .sticker), karena WhatsApp sering
// memangkas DirectPath pada pesan balasan (quote).
//
// Karakteristik:
// - L1 Cache (RAM): Sangat cepat, TTL 15 menit, kapasitas 2000 item.
// - Batas ukuran file yang dicache: 50 MB (50 << 20 bytes). FileLength = 0 tetap diterima
//   karena pesan audio PTT sering kali tidak menyertakan FileLength di protobuf.

const (
	// MaxCachedMediaBytes = batas maksimum file media yang dicache (50 MB)
	MaxCachedMediaBytes uint64 = 50 << 20

	// MediaCacheTTL = durasi kedaluwarsa cache media di RAM (15 menit)
	MediaCacheTTL = 15 * time.Minute

	// MaxMediaCacheEntries = batas jumlah item tersimpan di memory map
	MaxMediaCacheEntries = 2000
)

// MediaDescriptor adalah deskriptor ringan (~200 byte) yang hanya memegang
// metadata esensial untuk mengunduh media dari WhatsApp CDN.
type MediaDescriptor struct {
	Type          string // "image", "video", "audio", "document", "sticker"
	URL           string
	DirectPath    string
	MediaKey      []byte
	FileEncSHA256 []byte
	FileSHA256    []byte
	FileLength    uint64
	Mimetype      string
	Seconds       uint32
	FileName      string
	IsAnimated    bool
}

// ToMessage merekonstruksi pesan *waE2E.Message minimal yang valid dan siap
// diunduh oleh whatsmeow/hypermeow Client.Download().
func (d *MediaDescriptor) ToMessage() *waE2E.Message {
	if d == nil {
		return nil
	}
	switch d.Type {
	case "video":
		return &waE2E.Message{
			VideoMessage: &waE2E.VideoMessage{
				URL:           proto.String(d.URL),
				DirectPath:    proto.String(d.DirectPath),
				MediaKey:      d.MediaKey,
				FileEncSHA256: d.FileEncSHA256,
				FileSHA256:    d.FileSHA256,
				FileLength:    proto.Uint64(d.FileLength),
				Mimetype:      proto.String(d.Mimetype),
				Seconds:       proto.Uint32(d.Seconds),
			},
		}
	case "image":
		return &waE2E.Message{
			ImageMessage: &waE2E.ImageMessage{
				URL:           proto.String(d.URL),
				DirectPath:    proto.String(d.DirectPath),
				MediaKey:      d.MediaKey,
				FileEncSHA256: d.FileEncSHA256,
				FileSHA256:    d.FileSHA256,
				FileLength:    proto.Uint64(d.FileLength),
				Mimetype:      proto.String(d.Mimetype),
			},
		}
	case "audio":
		return &waE2E.Message{
			AudioMessage: &waE2E.AudioMessage{
				URL:           proto.String(d.URL),
				DirectPath:    proto.String(d.DirectPath),
				MediaKey:      d.MediaKey,
				FileEncSHA256: d.FileEncSHA256,
				FileSHA256:    d.FileSHA256,
				FileLength:    proto.Uint64(d.FileLength),
				Mimetype:      proto.String(d.Mimetype),
				Seconds:       proto.Uint32(d.Seconds),
			},
		}
	case "document":
		return &waE2E.Message{
			DocumentMessage: &waE2E.DocumentMessage{
				URL:           proto.String(d.URL),
				DirectPath:    proto.String(d.DirectPath),
				MediaKey:      d.MediaKey,
				FileEncSHA256: d.FileEncSHA256,
				FileSHA256:    d.FileSHA256,
				FileLength:    proto.Uint64(d.FileLength),
				Mimetype:      proto.String(d.Mimetype),
				FileName:      proto.String(d.FileName),
			},
		}
	case "sticker":
		return &waE2E.Message{
			StickerMessage: &waE2E.StickerMessage{
				URL:           proto.String(d.URL),
				DirectPath:    proto.String(d.DirectPath),
				MediaKey:      d.MediaKey,
				FileEncSHA256: d.FileEncSHA256,
				FileSHA256:    d.FileSHA256,
				FileLength:    proto.Uint64(d.FileLength),
				Mimetype:      proto.String(d.Mimetype),
				IsAnimated:    proto.Bool(d.IsAnimated),
			},
		}
	}
	return nil
}

type cachedMediaItem struct {
	desc      *MediaDescriptor
	expiresAt time.Time
}

var (
	mediaCacheMu sync.RWMutex
	mediaCache   = make(map[string]*cachedMediaItem)
	mediaOrder   []string
)

func init() {
	// Background periodic cleanup setiap 1 menit untuk membuang entri RAM kadaluarsa
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		for range ticker.C {
			purgeExpiredMediaCache()
		}
	}()
}

// ExtractMediaDescriptor mengekstrak deskriptor kunci dari pesan WhatsApp jika memiliki DirectPath/URL valid.
func ExtractMediaDescriptor(msg *waE2E.Message) *MediaDescriptor {
	if msg == nil {
		return nil
	}
	if vid := msg.GetVideoMessage(); vid != nil && (len(vid.GetDirectPath()) > 0 || len(vid.GetURL()) > 0) {
		return &MediaDescriptor{
			Type:          "video",
			URL:           vid.GetURL(),
			DirectPath:    vid.GetDirectPath(),
			MediaKey:      vid.GetMediaKey(),
			FileEncSHA256: vid.GetFileEncSHA256(),
			FileSHA256:    vid.GetFileSHA256(),
			FileLength:    vid.GetFileLength(),
			Mimetype:      vid.GetMimetype(),
			Seconds:       vid.GetSeconds(),
		}
	}
	if img := msg.GetImageMessage(); img != nil && (len(img.GetDirectPath()) > 0 || len(img.GetURL()) > 0) {
		return &MediaDescriptor{
			Type:          "image",
			URL:           img.GetURL(),
			DirectPath:    img.GetDirectPath(),
			MediaKey:      img.GetMediaKey(),
			FileEncSHA256: img.GetFileEncSHA256(),
			FileSHA256:    img.GetFileSHA256(),
			FileLength:    img.GetFileLength(),
			Mimetype:      img.GetMimetype(),
		}
	}
	if aud := msg.GetAudioMessage(); aud != nil && (len(aud.GetDirectPath()) > 0 || len(aud.GetURL()) > 0) {
		return &MediaDescriptor{
			Type:          "audio",
			URL:           aud.GetURL(),
			DirectPath:    aud.GetDirectPath(),
			MediaKey:      aud.GetMediaKey(),
			FileEncSHA256: aud.GetFileEncSHA256(),
			FileSHA256:    aud.GetFileSHA256(),
			FileLength:    aud.GetFileLength(),
			Mimetype:      aud.GetMimetype(),
			Seconds:       aud.GetSeconds(),
		}
	}
	if doc := msg.GetDocumentMessage(); doc != nil && (len(doc.GetDirectPath()) > 0 || len(doc.GetURL()) > 0) {
		return &MediaDescriptor{
			Type:          "document",
			URL:           doc.GetURL(),
			DirectPath:    doc.GetDirectPath(),
			MediaKey:      doc.GetMediaKey(),
			FileEncSHA256: doc.GetFileEncSHA256(),
			FileSHA256:    doc.GetFileSHA256(),
			FileLength:    doc.GetFileLength(),
			Mimetype:      doc.GetMimetype(),
			FileName:      doc.GetFileName(),
		}
	}
	if stk := msg.GetStickerMessage(); stk != nil && (len(stk.GetDirectPath()) > 0 || len(stk.GetURL()) > 0) {
		return &MediaDescriptor{
			Type:          "sticker",
			URL:           stk.GetURL(),
			DirectPath:    stk.GetDirectPath(),
			MediaKey:      stk.GetMediaKey(),
			FileEncSHA256: stk.GetFileEncSHA256(),
			FileSHA256:    stk.GetFileSHA256(),
			FileLength:    stk.GetFileLength(),
			Mimetype:      stk.GetMimetype(),
			IsAnimated:    stk.GetIsAnimated(),
		}
	}
	if im := msg.GetInteractiveMessage(); im != nil && im.GetHeader() != nil {
		h := im.GetHeader()
		if img := h.GetImageMessage(); img != nil && (len(img.GetDirectPath()) > 0 || len(img.GetURL()) > 0) {
			return &MediaDescriptor{
				Type:          "image",
				URL:           img.GetURL(),
				DirectPath:    img.GetDirectPath(),
				MediaKey:      img.GetMediaKey(),
				FileEncSHA256: img.GetFileEncSHA256(),
				FileSHA256:    img.GetFileSHA256(),
				FileLength:    img.GetFileLength(),
				Mimetype:      img.GetMimetype(),
			}
		}
		if vid := h.GetVideoMessage(); vid != nil && (len(vid.GetDirectPath()) > 0 || len(vid.GetURL()) > 0) {
			return &MediaDescriptor{
				Type:          "video",
				URL:           vid.GetURL(),
				DirectPath:    vid.GetDirectPath(),
				MediaKey:      vid.GetMediaKey(),
				FileEncSHA256: vid.GetFileEncSHA256(),
				FileSHA256:    vid.GetFileSHA256(),
				FileLength:    vid.GetFileLength(),
				Mimetype:      vid.GetMimetype(),
				Seconds:       vid.GetSeconds(),
			}
		}
		if doc := h.GetDocumentMessage(); doc != nil && (len(doc.GetDirectPath()) > 0 || len(doc.GetURL()) > 0) {
			return &MediaDescriptor{
				Type:          "document",
				URL:           doc.GetURL(),
				DirectPath:    doc.GetDirectPath(),
				MediaKey:      doc.GetMediaKey(),
				FileEncSHA256: doc.GetFileEncSHA256(),
				FileSHA256:    doc.GetFileSHA256(),
				FileLength:    doc.GetFileLength(),
				Mimetype:      doc.GetMimetype(),
				FileName:      doc.GetFileName(),
			}
		}
	}
	return nil
}

// InspectMedia memeriksa apakah suatu pesan memiliki lampiran media (video, gambar, audio, dokumen, stiker),
// serta mengembalikan ukuran file (dalam byte) dan apakah membawa DirectPath/URL valid.
func InspectMedia(msg *waE2E.Message) (size uint64, hasDirectPath bool) {
	if d := ExtractMediaDescriptor(msg); d != nil {
		return d.FileLength, true
	}
	if msg == nil {
		return 0, false
	}
	if vid := msg.GetVideoMessage(); vid != nil {
		return vid.GetFileLength(), len(vid.GetDirectPath()) > 0 || len(vid.GetURL()) > 0
	}
	if img := msg.GetImageMessage(); img != nil {
		return img.GetFileLength(), len(img.GetDirectPath()) > 0 || len(img.GetURL()) > 0
	}
	if aud := msg.GetAudioMessage(); aud != nil {
		return aud.GetFileLength(), len(aud.GetDirectPath()) > 0 || len(aud.GetURL()) > 0
	}
	if doc := msg.GetDocumentMessage(); doc != nil {
		return doc.GetFileLength(), len(doc.GetDirectPath()) > 0 || len(doc.GetURL()) > 0
	}
	if stk := msg.GetStickerMessage(); stk != nil {
		return stk.GetFileLength(), len(stk.GetDirectPath()) > 0 || len(stk.GetURL()) > 0
	}
	return 0, false
}

// CacheIncomingMedia merekam deskriptor media asli yang masuk ke dalam In-Memory Media Cache.
// Pesan dari saluran (newsletter) dan status broadcast otomatis diabaikan.
func CacheIncomingMedia(evt *events.Message) {
	if evt == nil || evt.Message == nil || evt.Info.ID == "" {
		return
	}

	// Abaikan update status atau channel WhatsApp
	if evt.Info.Chat.Server == "newsletter" || evt.Info.Chat.Server == "broadcast" {
		return
	}

	msg := UnwrapMessage(evt.Message)
	if msg == nil {
		return
	}

	desc := ExtractMediaDescriptor(msg)
	if desc == nil || (desc.FileLength > 0 && desc.FileLength > MaxCachedMediaBytes) {
		return
	}

	id := evt.Info.ID

	mediaCacheMu.Lock()
	defer mediaCacheMu.Unlock()

	if item, exists := mediaCache[id]; exists {
		item.desc = desc
		item.expiresAt = time.Now().Add(MediaCacheTTL)
		return
	}

	mediaCache[id] = &cachedMediaItem{
		desc:      desc,
		expiresAt: time.Now().Add(MediaCacheTTL),
	}
	mediaOrder = append(mediaOrder, id)
	if len(mediaOrder) > MaxMediaCacheEntries {
		oldest := mediaOrder[0]
		mediaOrder = mediaOrder[1:]
		delete(mediaCache, oldest)
	}
}

// CacheOutgoingMedia merekam deskriptor pesan media yang dikirim oleh bot ke dalam MediaCache.
func CacheOutgoingMedia(id string, msg *waE2E.Message) {
	if id == "" || msg == nil {
		return
	}

	desc := ExtractMediaDescriptor(msg)
	if desc == nil || (desc.FileLength > 0 && desc.FileLength > MaxCachedMediaBytes) {
		return
	}

	mediaCacheMu.Lock()
	defer mediaCacheMu.Unlock()

	if item, exists := mediaCache[id]; exists {
		item.desc = desc
		item.expiresAt = time.Now().Add(MediaCacheTTL)
		return
	}

	mediaCache[id] = &cachedMediaItem{
		desc:      desc,
		expiresAt: time.Now().Add(MediaCacheTTL),
	}
	mediaOrder = append(mediaOrder, id)
	if len(mediaOrder) > MaxMediaCacheEntries {
		oldest := mediaOrder[0]
		mediaOrder = mediaOrder[1:]
		delete(mediaCache, oldest)
	}
}

// GetCachedMediaDescriptor mengambil deskriptor media berdasarkan StanzaID pesan asli.
func GetCachedMediaDescriptor(stanzaID string) *MediaDescriptor {
	if stanzaID == "" {
		return nil
	}

	mediaCacheMu.RLock()
	item, ok := mediaCache[stanzaID]
	mediaCacheMu.RUnlock()

	if !ok {
		return nil
	}

	if time.Now().After(item.expiresAt) {
		mediaCacheMu.Lock()
		delete(mediaCache, stanzaID)
		mediaCacheMu.Unlock()
		return nil
	}

	return item.desc
}

// GetCachedMediaMessage merekonstruksi objek *waE2E.Message asli yang memiliki DirectPath lengkap
// berdasarkan StanzaID pesan asli.
func GetCachedMediaMessage(stanzaID string) *waE2E.Message {
	desc := GetCachedMediaDescriptor(stanzaID)
	if desc == nil {
		return nil
	}
	return desc.ToMessage()
}

// purgeExpiredMediaCache membersihkan item RAM yang telah melewati batas TTL
func purgeExpiredMediaCache() {
	mediaCacheMu.Lock()
	defer mediaCacheMu.Unlock()

	now := time.Now()
	newOrder := make([]string, 0, len(mediaOrder))

	for _, id := range mediaOrder {
		if item, ok := mediaCache[id]; ok {
			if now.After(item.expiresAt) {
				delete(mediaCache, id)
			} else {
				newOrder = append(newOrder, id)
			}
		}
	}
	mediaOrder = newOrder
}
