package ai

import (
	"context"
	"fmt"
	"sync"

	"wabot-go/internal/store"
)

// FactExtractionInterval adalah tiap berapa giliran (satu giliran = satu
// balasan AI terkirim) fact extractor dipicu untuk seorang sender.
// Dihitung per-sender (bukan global) supaya sender yang jarang chat tidak
// "menunggu" giliran sender lain.
const FactExtractionInterval = 8

var (
	turnCountMu sync.Mutex
	turnCount   = make(map[string]int)
)

// TriggerFactExtraction dipanggil SETIAP giliran percakapan selesai
// (setelah balasan AI berhasil dikirim). Fungsi ini sendiri tidak pernah
// blocking dan tidak pernah mengembalikan error ke pemanggil — hitungan
// giliran & keputusan "waktunya ekstrak atau belum" dilakukan sinkron
// (murah, cuma increment int), tapi panggilan ke provider (mahal, network)
// selalu dilempar ke goroutine terpisah (fire-and-forget).
//
// senderJID: dipakai sebagai key fakta (per sender, lintas chat/grup —
// sesuai keputusan desain Fase 5), BUKAN sessionKey (yang bisa saja nanti
// beda format untuk grup).
// history: potongan histori percakapan TERBARU (cukup histori sesi saat
// ini, tidak perlu re-fetch dari DB) yang akan dijadikan bahan ekstraksi.
func TriggerFactExtraction(provider ProviderAdapter, senderJID string, history []Content) {
	turnCountMu.Lock()
	turnCount[senderJID]++
	shouldRun := turnCount[senderJID] >= FactExtractionInterval
	if shouldRun {
		turnCount[senderJID] = 0
	}
	turnCountMu.Unlock()

	if !shouldRun {
		return
	}

	// Salin history supaya goroutine tidak berbagi slice dengan pemanggil
	// (pemanggil mungkin masih memodifikasi slice aslinya setelah return).
	historyCopy := append([]Content{}, history...)

	go runFactExtraction(provider, senderJID, historyCopy)
}

// runFactExtraction adalah isi goroutine fire-and-forget: ambil fakta
// lama, panggil provider untuk gabungkan dengan histori baru, simpan
// hasilnya. Semua kegagalan di sini fail-soft — hanya dicatat ke console,
// tidak pernah mengganggu alur chat utama (yang sudah lama selesai saat
// goroutine ini masih jalan).
func runFactExtraction(provider ProviderAdapter, senderJID string, history []Content) {
	ctx := context.Background()

	oldFacts, err := store.AI().GetFacts(ctx, senderJID)
	if err != nil {
		fmt.Printf("⚠️  fact-extractor: gagal ambil fakta lama untuk %s: %v\n", senderJID, err)
		oldFacts = ""
	}

	transcript := FormatTranscript(history)
	if transcript == "" {
		return // tidak ada isi berarti untuk diekstrak
	}

	newFacts, err := ExtractFacts(ctx, provider, oldFacts, transcript)
	if err != nil {
		fmt.Printf("⚠️  fact-extractor: gagal ekstrak fakta untuk %s: %v\n", senderJID, err)
		return
	}

	if err := store.AI().UpsertFacts(ctx, senderJID, newFacts); err != nil {
		fmt.Printf("⚠️  fact-extractor: gagal simpan fakta untuk %s: %v\n", senderJID, err)
	}
}
