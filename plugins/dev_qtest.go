package plugins

import (
	"context"
	"strings"
	"time"

	"wabot-go/internal/store"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waCommon"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func init() {
	registerPlugin(RegisterQtest)
}

// RegisterQtest mendaftarkan command `.qtest` kategori Owner: uji kirim
// tipe keluarga Question (Q&A Status/Channel). Bentuk: `.qtest
// response|status`.
// FAKTA UJI live (Okt 2026): kedua varian TERKIRIM tapi TAK TAMPIL di klien
// (key dummy, tak ada pertanyaan asli untuk ditempeli). Kesimpulan:
// questionMessage/questionReplyMessage masih FutureProof (skema dalam
// opaque, tak bisa dirakit bermakna); respons butuh key pertanyaan asli;
// Q&A tidak bisa dibuat dari bot (komposisi klien + jalur Status). JANGAN
// uji ulang tanpa temuan skema baru.
func RegisterQtest(r *Registry) {
	r.Register(Command{
		Name:        "qtest",
		Description: "Uji kirim tipe Question (owner)",
		Category:    CategoryOwner,
		Handler:     handleQtest,
	})
}

func handleQtest(ctx context.Context, mctx *store.MessageContext) error {
	sub := ""
	if len(mctx.Args) > 0 {
		sub = strings.ToLower(strings.TrimSpace(mctx.Args[0]))
	}

	// Key DUMMY yang jujur: menunjuk chat sendiri dengan ID acak. Bukan
	// pertanyaan sungguhan — section ini hanya menguji penerimaan server.
	// ID acak + timestamp supaya tiap uji unik di log.
	dummyKey := &waCommon.MessageKey{
		RemoteJID: proto.String(mctx.Chat.String()),
		FromMe:    proto.Bool(true),
		ID:        proto.String("QTEST" + time.Now().UTC().Format("150405")),
	}
	text := "Jawaban uji QnA (kunci dummy, abaikan)."

	var msg *waE2E.Message
	label := ""
	switch sub {
	case "response":
		msg = &waE2E.Message{
			QuestionResponseMessage: &waE2E.QuestionResponseMessage{
				Key:  dummyKey,
				Text: proto.String(text),
			},
		}
		label = "question-response"
	case "status":
		msg = &waE2E.Message{
			StatusQuestionAnswerMessage: &waE2E.StatusQuestionAnswerMessage{
				Key:  dummyKey,
				Text: proto.String(text),
			},
		}
		label = "status-question-answer"
	default:
		return store.ReplyText(ctx, mctx, "Varian: .qtest [response|status]")
	}

	// Bukan native_flow: tanpa node `biz` manual maupun tambahan apa pun.
	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, msg,
		whatsmeow.SendRequestExtra{ID: whatsmeow.GenerateMessageID()})
	if err != nil {
		return store.ReplyText(ctx, mctx, "Gagal kirim "+label+": "+err.Error())
	}
	store.LogOutgoing(mctx, "qtest", label)
	store.TrackBotMessageID(resp.ID, "qtest "+label)
	return store.ReplyText(ctx, mctx, "Terkirim ("+label+"). Cek apakah tampil/diabaikan di HP.")
}
