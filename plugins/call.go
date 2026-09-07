package plugins

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/purpshell/meowcaller"
	"go.mau.fi/whatsmeow"

	"wabot-go/internal/store"
)

// witaLocation adalah zona waktu WITA (UTC+8), dipakai untuk menjadwalkan
// panggilan dengan format ".call <nomor>|<jam>" (mis. ".call 628xxx|12.50").
var witaLocation = time.FixedZone("WITA", 8*60*60)

// callClient membungkus whatsmeow.Client untuk fitur panggilan WhatsApp.
// Diisi oleh InitCallClient, dipanggil dari main.go SEBELUM waClient.Connect().
var callClient *meowcaller.Client

func init() {
	registerPlugin(RegisterCall)
}

// InitCallClient memasang meowcaller di atas whatsmeow client.
// PENTING: harus dipanggil SEBELUM waClient.Connect(), karena meowcaller perlu
// memasang interceptor <call>/<ack> sebelum receive loop whatsmeow mulai jalan.
func InitCallClient(wa *whatsmeow.Client) {
	callClient = meowcaller.NewClient(wa)

	callClient.OnIncomingCall(func(call *meowcaller.Call) {
		fmt.Printf("📞 Panggilan masuk dari %s\n", call.Peer().String())

		if err := call.Answer(); err != nil {
			fmt.Printf("❌ Gagal menjawab panggilan: %v\n", err)
			return
		}

		if mp3, err := meowcaller.MP3File("greeting.mp3"); err != nil {
			fmt.Printf("❌ Gagal membuka/decode greeting.mp3: %v\n", err)
		} else if err := call.Play(mp3); err != nil {
			fmt.Printf("❌ Gagal memutar greeting.mp3: %v\n", err)
		}

		call.OnEnd(func(reason string) {
			fmt.Printf("📴 Panggilan berakhir: %s\n", reason)
		})
	})
}

// RegisterCall mendaftarkan command ".call <nomor>" untuk melakukan panggilan
// WhatsApp ke nomor tersebut, atau ".call <nomor>|<jam>" (mis. "12.50", WITA)
// untuk menjadwalkan panggilan pada jam tersebut hari ini (atau besok kalau
// jam itu sudah lewat). Jadwal disimpan in-memory saja — hilang kalau bot restart.
func RegisterCall(r *Registry) {
	r.Register(Command{
		Name:        "call",
		Description: "Panggil WhatsApp. .call 628xxx (langsung) atau .call 628xxx|12.50 (jadwal jam WITA)",
		Handler: func(ctx context.Context, mctx *store.MessageContext) error {
			if callClient == nil {
				return store.ReplyText(ctx, mctx, "Fitur call belum aktif.")
			}
			if len(mctx.Args) == 0 {
				return store.ReplyText(ctx, mctx, "Format salah. Contoh: .call 628123456789 atau .call 628123456789|12.50")
			}

			raw := mctx.Args[0]
			parts := strings.SplitN(raw, "|", 2)
			target := parts[0]

			// Tidak ada "|" -> panggil langsung seperti biasa
			if len(parts) == 1 {
				return doCall(ctx, mctx, target)
			}

			// Ada "|<jam>" -> jadwalkan
			at, err := parseJamWITA(parts[1])
			if err != nil {
				return store.ReplyText(ctx, mctx, fmt.Sprintf("Format jam salah (%v). Contoh: .call 628123456789|12.50", err))
			}

			delay := time.Until(at)
			go func() {
				timer := time.NewTimer(delay)
				defer timer.Stop()
				<-timer.C
				// ctx dari command sudah bisa selesai sebelum jadwal tiba,
				// jadi pakai context.Background() untuk pelaksanaan panggilan terjadwal.
				_ = doCall(context.Background(), mctx, target)
			}()

			return store.ReplyText(ctx, mctx, fmt.Sprintf(
				"📅 Panggilan ke %s dijadwalkan pukul %s WITA (%s lagi).",
				target, at.Format("15:04"), delay.Round(time.Second),
			))
		},
	})
}

// parseJamWITA mem-parsing format "HH.MM" (mis. "12.50") menjadi waktu absolut
// dalam zona WITA. Kalau jam itu sudah lewat hari ini, dijadwalkan untuk besok.
func parseJamWITA(jamStr string) (time.Time, error) {
	hm := strings.SplitN(jamStr, ".", 2)
	if len(hm) != 2 {
		return time.Time{}, fmt.Errorf("gunakan format HH.MM, contoh 12.50")
	}

	hour, err := strconv.Atoi(hm[0])
	if err != nil || hour < 0 || hour > 23 {
		return time.Time{}, fmt.Errorf("jam tidak valid: %q", hm[0])
	}
	minute, err := strconv.Atoi(hm[1])
	if err != nil || minute < 0 || minute > 59 {
		return time.Time{}, fmt.Errorf("menit tidak valid: %q", hm[1])
	}

	now := time.Now().In(witaLocation)
	target := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, witaLocation)

	if !target.After(now) {
		target = target.AddDate(0, 0, 1) // sudah lewat -> jadwalkan besok
	}

	return target, nil
}

// doCall melakukan panggilan WhatsApp ke target dan memasang callback
// OnPeerAccept (memutar greeting.mp3) & OnEnd (logging).
func doCall(ctx context.Context, mctx *store.MessageContext, target string) error {
	call, err := callClient.Call(ctx, target)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal melakukan panggilan: %v", err))
	}

	call.OnPeerAccept(func() {
		fmt.Printf("✅ Panggilan ke %s diterima\n", target)

		mp3, err := meowcaller.MP3File("greeting.mp3")
		if err != nil {
			fmt.Printf("❌ Gagal membuka/decode greeting.mp3: %v\n", err)
			return
		}
		if err := call.Play(mp3); err != nil {
			fmt.Printf("❌ Gagal memutar greeting.mp3: %v\n", err)
		}
	})
	call.OnEnd(func(reason string) {
		fmt.Printf("📴 Panggilan ke %s berakhir: %s\n", target, reason)
	})

	return store.ReplyText(ctx, mctx, "📞 Memanggil "+target+"...")
}