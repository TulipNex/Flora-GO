package userdir

import (
	"context"
	"fmt"
	"sort"
	"time"

	"wabot-go/internal/store"
)

// PrunePhotos menghapus file cache foto yang kedaluwarsa (pp_at < cutoff)
// dan/atau melebihi pagar LRU (maxFiles / maxBytes). Mengembalikan jumlah
// file yang dihapus + byte yang dibebaskan.
func PrunePhotos(ctx context.Context, users *store.UsersDB, maxFiles, maxBytes int) (int, int64, error) {
	if users == nil {
		return 0, 0, fmt.Errorf("users.db mati")
	}
	if maxFiles <= 0 {
		maxFiles = 5000
	}
	if maxBytes <= 0 {
		maxBytes = 300 << 20
	}
	// Ambil semua user yang punya foto, urut paling lama dulu.
	cutoff := time.Now().Add(-7 * 24 * time.Hour).Unix()
	old, err := users.ListUsersByPhotoOlderThan(ctx, cutoff, 100000)
	if err != nil {
		return 0, 0, err
	}
	// Tambahkan yang belum kedaluwarsa untuk LRU (ambil seluruh).
	// Kita ambil ulang: semua user dengan pp_path <> ''.
	all, err := users.ListUsers(ctx, 100000, 0)
	if err != nil {
		return 0, 0, err
	}
	var withPhoto []store.UserRecord
	for _, r := range all {
		if r.PPPath != "" {
			withPhoto = append(withPhoto, r)
		}
	}
	// Set kedaluwarsa dulu.
	toDelete := map[int64]bool{}
	for _, r := range old {
		toDelete[r.ID] = true
	}
	// LRU: urutkan by pp_at asc, hapus sampai di bawah pagar.
	sort.Slice(withPhoto, func(i, j int) bool { return withPhoto[i].PPAt < withPhoto[j].PPAt })
	totalBytes := int64(0)
	for _, r := range withPhoto {
		totalBytes += int64(r.PPBytes)
	}
	// Hapus kedaluwarsa.
	deleted := 0
	freed := int64(0)
	for _, r := range withPhoto {
		if !toDelete[r.ID] {
			continue
		}
		if err := removeFile(r.PPPath); err == nil {
			freed += int64(r.PPBytes)
		}
		_ = users.ClearUserPhoto(ctx, r.ID)
		deleted++
		totalBytes -= int64(r.PPBytes)
	}
	// LRU pagar file/byte.
	for _, r := range withPhoto {
		if toDelete[r.ID] {
			continue
		}
		if len(withPhoto)-deleted <= maxFiles && totalBytes <= int64(maxBytes) {
			break
		}
		if err := removeFile(r.PPPath); err == nil {
			freed += int64(r.PPBytes)
		}
		_ = users.ClearUserPhoto(ctx, r.ID)
		deleted++
		totalBytes -= int64(r.PPBytes)
	}
	return deleted, freed, nil
}
