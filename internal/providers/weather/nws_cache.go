package weather

import (
	"context"
	"fmt"
	"time"

	"github.com/nickna/ssh.night.ms/internal/providers/ttlcache"
)

// NWSCache wraps an upstream AlertProvider with a per-coordinate TTL cache.
// NWS recommends consumers not poll faster than a few minutes for the same
// point; this cache enforces that and coalesces concurrent screen mounts
// onto a single in-flight fetch via the shared ttlcache.Cache.
//
// Errors propagate directly (no stale-on-error fallback) — alerts that are
// stale are worse than no alerts; the UI surfaces the failure plainly.
type NWSCache struct {
	upstream AlertProvider
	inner    *ttlcache.Cache[coord, []Alert]
}

type coord struct {
	lat, lon float64
}

func coordKey(c coord) string { return fmt.Sprintf("%.4f,%.4f", c.lat, c.lon) }

// NewNWSCache returns a cache with the given TTL. A 5-minute TTL is the
// sweet spot for NWS — fresh enough that imminent storms surface within a
// session, cool enough not to look like a polling bot.
func NewNWSCache(upstream AlertProvider, ttl time.Duration) *NWSCache {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &NWSCache{
		upstream: upstream,
		inner:    ttlcache.New[coord, []Alert](ttl, coordKey),
	}
}

func (c *NWSCache) Alerts(ctx context.Context, lat, lon float64) ([]Alert, error) {
	return c.inner.Get(ctx, coord{lat, lon}, func(ctx context.Context) ([]Alert, error) {
		return c.upstream.Alerts(ctx, lat, lon)
	})
}
