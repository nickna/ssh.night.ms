package finance

import (
	"context"
	"time"

	"github.com/nickna/ssh.night.ms/internal/providers/ttlcache"
)

// Cache wraps an AssetProvider with per-(canonical, calltype) TTL caching and
// singleflight coalescing — concurrent screen mounts that hit the same symbol
// share a single upstream fetch. StaleOnError lets a transient rate-limit
// keep the table populated with the last-known values rather than blanking.
//
// One Cache is sized for one asset class. Multi composes three Caches (one
// per leg) so a Yahoo outage doesn't poison CoinGecko results.
type Cache struct {
	Upstream AssetProvider
	TTL      time.Duration

	quotes  *ttlcache.Cache[string, *Quote]
	sparks  *ttlcache.Cache[string, []float64]
	details *ttlcache.Cache[string, *Detail]
}

func NewCache(upstream AssetProvider, ttl time.Duration) *Cache {
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	return &Cache{
		Upstream: upstream,
		TTL:      ttl,
		quotes:   ttlcache.New[string, *Quote](ttl, nil, ttlcache.StaleOnError()),
		sparks:   ttlcache.New[string, []float64](ttl, nil, ttlcache.StaleOnError()),
		details:  ttlcache.New[string, *Detail](ttl, nil, ttlcache.StaleOnError()),
	}
}

func (c *Cache) GetQuote(ctx context.Context, canonical string) (*Quote, error) {
	return c.quotes.Get(ctx, canonical, func(ctx context.Context) (*Quote, error) {
		return c.Upstream.GetQuote(ctx, canonical)
	})
}

func (c *Cache) GetSparkline(ctx context.Context, canonical string) ([]float64, error) {
	return c.sparks.Get(ctx, canonical, func(ctx context.Context) ([]float64, error) {
		return c.Upstream.GetSparkline(ctx, canonical)
	})
}

func (c *Cache) GetDetail(ctx context.Context, canonical string) (*Detail, error) {
	return c.details.Get(ctx, canonical, func(ctx context.Context) (*Detail, error) {
		return c.Upstream.GetDetail(ctx, canonical)
	})
}

var _ AssetProvider = (*Cache)(nil)
