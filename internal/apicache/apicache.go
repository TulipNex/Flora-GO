// Package apicache menyediakan cache TTL + singleflight untuk hasil API
// eksternal (Tahap 3). Leaf package: hanya bergantung pada ttlcache dan
// golang.org/x/sync/singleflight.
package apicache

import (
	"context"
	"fmt"
	"time"

	"github.com/jellydator/ttlcache/v3"
	"golang.org/x/sync/singleflight"
)

// Cache adalah pembungkus ttlcache + singleflight untuk satu jenis nilai.
type Cache[T any] struct {
	c   *ttlcache.Cache[string, T]
	sf  singleflight.Group
	ttl time.Duration
}

// New membuat cache dengan TTL dan kapasitas maksimum (0 = tanpa batas
// kapasitas, hanya TTL). Start() dipanggil otomatis.
func New[T any](ttl time.Duration, maxEntries int) *Cache[T] {
	opts := []ttlcache.Option[string, T]{ttlcache.WithTTL[string, T](ttl)}
	if maxEntries > 0 {
		opts = append(opts, ttlcache.WithCapacity[string, T](uint64(maxEntries)))
	}
	c := ttlcache.New[string, T](opts...)
	go c.Start()
	return &Cache[T]{c: c, ttl: ttl}
}

// GetOrLoad mengambil nilai dari cache; bila tidak ada, memanggil load
// (dengan singleflight agar request identik bersamaan tidak memanggil API
// dua kali). Hanya hasil sukses yang disimpan (err != nil tidak di-cache).
func (c *Cache[T]) GetOrLoad(ctx context.Context, key string, load func(context.Context) (T, error)) (T, error) {
	if item := c.c.Get(key); item != nil {
		return item.Value(), nil
	}
	v, err, _ := c.sf.Do(key, func() (any, error) {
		if item := c.c.Get(key); item != nil {
			return item.Value(), nil
		}
		val, lerr := load(ctx)
		if lerr != nil {
			return nil, lerr
		}
		c.c.Set(key, val, c.ttl)
		return val, nil
	})
	if err != nil {
		var zero T
		return zero, err
	}
	val, ok := v.(T)
	if !ok {
		var zero T
		return zero, fmt.Errorf("apicache: tipe tidak cocok")
	}
	return val, nil
}

// Stop menghentikan goroutine internal cache (panggil saat shutdown bila perlu).
func (c *Cache[T]) Stop() {
	if c.c != nil {
		c.c.Stop()
	}
}