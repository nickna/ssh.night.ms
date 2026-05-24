package news

import (
	"context"
	"time"

	"github.com/nickna/ssh.night.ms/internal/providers/ttlcache"
)

// Cache wraps a Provider with a single in-process TTL cache. Per-instance,
// not Redis-backed — each nightms process maintains its own copy so the
// HN API never sees more than one request per TTL window from us, regardless
// of how many SSH/web sessions are browsing /news.
//
// Implementation is the shared ttlcache.Cache with one logical key. We pass
// the requested limit through to the key so two screens asking for different
// list lengths don't fight (the bigger one fetches; the smaller serves its
// prefix from the bigger entry only when the cache already holds enough
// rows — see TopStories below).
type Cache struct {
	Upstream Provider
	TTL      time.Duration
	inner    *ttlcache.Cache[int, []Story]
}

// NewCache wraps upstream with a TTL-keyed cache. Reasonable default TTL is
// 5 minutes — HN's ranks drift but not so fast that minute-level freshness
// matters.
func NewCache(upstream Provider, ttl time.Duration) *Cache {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &Cache{
		Upstream: upstream,
		TTL:      ttl,
		inner:    ttlcache.New[int, []Story](ttl, nil, ttlcache.StaleOnError()),
	}
}

// TopStories returns cached results when fresh; otherwise fans out to the
// upstream provider while singleflight-coalescing concurrent requests.
//
// A previously cached fetch with limit >= requested can satisfy the request
// directly — no need to spin up a new fetch just to trim rows.
func (c *Cache) TopStories(ctx context.Context, limit int) ([]Story, error) {
	if cached, ok := c.inner.Peek(limit); ok && len(cached) >= limit {
		return cached[:limit], nil
	}
	stories, err := c.inner.Get(ctx, limit, func(ctx context.Context) ([]Story, error) {
		return c.Upstream.TopStories(ctx, limit)
	})
	if err != nil {
		return nil, err
	}
	if len(stories) > limit {
		stories = stories[:limit]
	}
	return stories, nil
}

// Compile-time guard that Cache satisfies Provider so screens can hold
// either without code change.
var _ Provider = (*Cache)(nil)
