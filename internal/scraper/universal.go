package scraper

import (
	"context"
)

// FetchUniversal mengambil media APAPUN via Omnify AIO (45+ platform).
// Kontrak fungsi TETAP (fallback universal tiap platform + sumber utama
// Facebook) — yang diganti hanya isi engine: j2download SUDAH DIHAPUS
// (butuh cookies+CSRF yang tak bisa diverifikasi dari sini) diganti
// omnify (satu POST JSON, tanpa key — lihat omnify.go).
func FetchUniversal(ctx context.Context, pageURL string) (*ScrapeResult, error) {
	return FetchOmnify(ctx, pageURL)
}
