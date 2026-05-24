// Package ttlcache is a tiny generic in-process TTL cache with
// singleflight coalescing of concurrent fetches. It exists to collapse the
// four near-identical mutex-+-inflight-channel implementations that grew up
// across the provider packages (news, finance, weather/nws, maptile) into
// one well-tested shape.
//
// Behaviour:
//   - Fresh entries (within TTL) return immediately.
//   - Stale or missing entries trigger a fetch; concurrent callers for the
//     same key share the single in-flight upstream call via singleflight.
//   - TTL == 0 means "no expiry" — entries live for the process lifetime
//     (used by the maptile cache: OSM tiles never need to be re-fetched).
//   - When constructed with StaleOnError, a transient upstream failure
//     returns the last-known value (if any) with err == nil. Read-only
//     screens (news, finance) prefer "1 minute old" to "blank with error";
//     screens that surface alerts (NWS) leave it off and let errors flow.
package ttlcache

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// Cache is a per-key TTL cache. Use New to construct.
//
// K must be comparable so it can key the internal map; for non-string keys
// supply a KeyFn so singleflight (which uses string keys internally) can
// group concurrent fetches correctly.
type Cache[K comparable, V any] struct {
	ttl   time.Duration
	keyFn func(K) string
	stale bool

	sf singleflight.Group

	mu    sync.Mutex
	items map[K]entry[V]
}

type entry[V any] struct {
	value     V
	fetchedAt time.Time
}

// Option configures Cache behaviour at construction time.
type Option func(*config)

type config struct {
	stale bool
}

// StaleOnError lets Get return the last-known value (if any) when the
// upstream fetch errors. Default is to propagate the error.
func StaleOnError() Option { return func(c *config) { c.stale = true } }

// New builds a Cache with the given TTL and optional configuration. If
// keyFn is nil a default fmt-based stringification is used.
func New[K comparable, V any](ttl time.Duration, keyFn func(K) string, opts ...Option) *Cache[K, V] {
	var c config
	for _, o := range opts {
		o(&c)
	}
	if keyFn == nil {
		keyFn = func(k K) string { return fmt.Sprintf("%v", k) }
	}
	return &Cache[K, V]{
		ttl:   ttl,
		keyFn: keyFn,
		stale: c.stale,
		items: map[K]entry[V]{},
	}
}

// Get returns the cached value if fresh; otherwise it calls fetch (sharing a
// single in-flight call across concurrent callers for the same key) and
// caches the result. The fetch closure receives the caller's context; the
// singleflight winner's context is the one observed by the upstream call.
func (c *Cache[K, V]) Get(ctx context.Context, key K, fetch func(context.Context) (V, error)) (V, error) {
	if v, ok := c.fresh(key); ok {
		return v, nil
	}

	sfKey := c.keyFn(key)
	v, err, _ := c.sf.Do(sfKey, func() (any, error) {
		fresh, ferr := fetch(ctx)
		if ferr == nil {
			c.mu.Lock()
			c.items[key] = entry[V]{value: fresh, fetchedAt: time.Now()}
			c.mu.Unlock()
			return fresh, nil
		}
		if c.stale {
			c.mu.Lock()
			e, ok := c.items[key]
			c.mu.Unlock()
			if ok {
				return e.value, nil
			}
		}
		var zero V
		return zero, ferr
	})
	if err != nil {
		var zero V
		return zero, err
	}
	return v.(V), nil
}

// Peek returns the cached value without consulting freshness or fetching.
// Useful when a caller wants to look at the last-known value without
// triggering an upstream call (e.g., a status indicator).
func (c *Cache[K, V]) Peek(key K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[key]
	if !ok {
		var zero V
		return zero, false
	}
	return e.value, true
}

// Invalidate drops the cached entry for key, forcing the next Get to fetch.
func (c *Cache[K, V]) Invalidate(key K) {
	c.mu.Lock()
	delete(c.items, key)
	c.mu.Unlock()
}

// fresh returns the cached value if it exists and (ttl == 0 || age < ttl).
func (c *Cache[K, V]) fresh(key K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[key]
	if !ok {
		var zero V
		return zero, false
	}
	if c.ttl > 0 && time.Since(e.fetchedAt) >= c.ttl {
		var zero V
		return zero, false
	}
	return e.value, true
}
