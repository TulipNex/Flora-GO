// Package heavy menampung pengendali beban untuk pekerjaan berat
// (transcode ffmpeg, Chrome launch) di panel kecil (1 core / ~1,6GB).
//
// Paket ini nyaris leaf: satu-satunya impor repo adalah internal/memlog
// (yang juga leaf) untuk mencatat berapa byte yang dipakai satu transcode.
// Itu tidak melahirkan import cycle, tapi jangan tambah impor lain ke sini.
package heavy

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"wabot-go/internal/memlog"
)

// maxTranscodePar default 1: transcode ffmpeg sangat boros CPU/RAM di panel
// 1 core; jalankan serial supaya tidak saling berebut.
func maxTranscodePar() int {
	raw := strings.TrimSpace(os.Getenv("HEAVY_MAX_PAR"))
	if raw == "" {
		return 1
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 1
	}
	if n > 4 {
		return 4
	}
	return n
}

var transcodeSem = make(chan struct{}, maxTranscodePar())

// AcquireTranscode mengambil satu slot transcode. Gagal ramah bila antrean
// melewati batas tunggu atau context dibatalkan. Pemanggil WAJIB memanggil
// release() setelah selesai (biasanya via defer).
func AcquireTranscode(ctx context.Context) (func(), error) {
	queueTimeout := 30 * time.Second
	if raw := strings.TrimSpace(os.Getenv("HEAVY_QUEUE_TIMEOUT")); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			queueTimeout = d
		}
	}
	timer := time.NewTimer(queueTimeout)
	defer timer.Stop()
	select {
	case transcodeSem <- struct{}{}:
		// Satu baris riwayat memori per pemakaian slot: ffmpeg adalah
		// penyumbang RSS terbesar di panel ini, dan durasi slot = durasi
		// proses ffmpeg berjalan (Op.End idempoten, jadi release ganda aman).
		op := memlog.Begin("ffmpeg", "transcode")
		return func() {
			op.End(true, "")
			<-transcodeSem
		}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, fmt.Errorf("heavy: antrean transcode penuh — coba lagi sebentar")
	}
}
