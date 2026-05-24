package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"

	"github.com/nickna/ssh.night.ms/internal/data/gen"
	"github.com/nickna/ssh.night.ms/internal/security/audit"
)

// banInvalidateChannel is the Redis pub/sub channel used to push-invalidate
// BanCache instances across replicas. The payload is the affected ip key
// (or "*" for "refresh all" on bulk operations); both producer and consumer
// always re-read the active-bans table on receipt.
const banInvalidateChannel = "security:ban-invalidate"

// BanCache holds the currently-active persistent IP bans in-process so the
// auth hot path doesn't pay a Postgres RTT on every login. Refreshed every
// RefreshInterval from security_ip_bans (where expires_at > now()), plus
// push-invalidated via Redis pub/sub when AddBan/RemoveBan fires anywhere
// in the fleet.
//
// Lifecycle: construct via NewBanCache → call Load(ctx) once before serving
// any auth requests → spawn a goroutine running Run(ctx) for the periodic
// refresh + pub/sub listener. main wires both.
type BanCache struct {
	queries         *gen.Queries
	redisClient     *redis.Client
	logger          *slog.Logger
	refreshInterval time.Duration

	// Audit is optional. When set, AddBan emits PersistentBanAuto /
	// PersistentBanManual depending on createdBy, and RemoveBan emits
	// PersistentBanRevoke. Nil-safe.
	Audit audit.Recorder

	mu   sync.RWMutex
	bans map[string]time.Time // collapsed ip key → expiry
}

// NewBanCache constructs a BanCache. RefreshInterval defaults to 30s when
// the passed value is zero — long enough that periodic Postgres scans are
// cheap, short enough that a missed pub/sub message converges fast.
func NewBanCache(queries *gen.Queries, redisClient *redis.Client, logger *slog.Logger, refreshInterval time.Duration) *BanCache {
	if refreshInterval <= 0 {
		refreshInterval = 30 * time.Second
	}
	return &BanCache{
		queries:         queries,
		redisClient:     redisClient,
		logger:          logger,
		refreshInterval: refreshInterval,
		bans:            make(map[string]time.Time),
	}
}

// Load performs a synchronous initial refresh. main calls this before the
// SSH listener starts so the first auth attempt sees the persisted bans.
func (c *BanCache) Load(ctx context.Context) error {
	return c.refresh(ctx)
}

// Run drives the periodic refresh ticker and the pub/sub invalidation
// listener. Returns when ctx is cancelled. Both inner loops fail-warn rather
// than fail-stop — a Redis or Postgres blip shouldn't kill the cache
// goroutine and leave the in-memory state forever frozen.
func (c *BanCache) Run(ctx context.Context) {
	go c.listenInvalidations(ctx)
	ticker := time.NewTicker(c.refreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.refresh(ctx); err != nil {
				c.logger.Warn("ban cache refresh", "err", err)
			}
		}
	}
}

// IsBanned reports whether ip is currently subject to a persistent ban and,
// if so, when the ban expires. ip must be the collapsed-IP key form
// (netlimit.CollapseIP / netlimit.CollapseIPString). Returns (false, zero)
// for unknown or expired entries.
func (c *BanCache) IsBanned(ip string) (bool, time.Time) {
	if ip == "" {
		return false, time.Time{}
	}
	c.mu.RLock()
	exp, ok := c.bans[ip]
	c.mu.RUnlock()
	if !ok {
		return false, time.Time{}
	}
	if time.Now().After(exp) {
		// Stale entry — drop on read so the cache shrinks without waiting
		// for the next refresh. Cheap: the write lock is taken only when
		// we actually have something to evict.
		c.mu.Lock()
		if e, ok := c.bans[ip]; ok && time.Now().After(e) {
			delete(c.bans, ip)
		}
		c.mu.Unlock()
		return false, time.Time{}
	}
	return true, exp
}

// AddBan upserts the ban row and broadcasts pub/sub invalidation so other
// replicas (and our own cache) re-read promptly. The local cache is also
// updated synchronously so the very next IsBanned call sees the new entry
// regardless of pub/sub latency. createdBy is "auto" for limiter-driven
// bans or the sysop handle for manual ones.
func (c *BanCache) AddBan(ctx context.Context, ip string, duration time.Duration, reason, createdBy string) error {
	if ip == "" {
		return errors.New("BanCache: empty ip")
	}
	expiry := time.Now().Add(duration)
	if err := c.queries.UpsertIPBan(ctx, gen.UpsertIPBanParams{
		IpAddr:    ip,
		ExpiresAt: pgtype.Timestamptz{Time: expiry, Valid: true},
		Reason:    reason,
		CreatedBy: createdBy,
	}); err != nil {
		return fmt.Errorf("BanCache: upsert: %w", err)
	}
	c.mu.Lock()
	c.bans[ip] = expiry
	c.mu.Unlock()
	c.publish(ctx, ip)
	if c.Audit != nil {
		if createdBy == "auto" {
			c.Audit.Record(ctx, audit.PersistentBanAuto{
				IP: ip, ExpiresAt: expiry,
				// Lockcount is unknown at this layer (the rate limiter
				// hook knows it); leaving it zero is fine — the slog
				// line still carries the reason string.
			})
		} else {
			c.Audit.Record(ctx, audit.PersistentBanManual{
				IP: ip, ByHandle: createdBy, Reason: reason, ExpiresAt: expiry,
			})
		}
	}
	return nil
}

// RemoveBan deletes the ban row, drops the entry from the local cache, and
// broadcasts invalidation. byHandle identifies the sysop responsible (empty
// for the future cleanup-goroutine path). Returns the number of rows
// removed (0 if there was no active ban for that ip).
func (c *BanCache) RemoveBan(ctx context.Context, ip, byHandle string) (int64, error) {
	if ip == "" {
		return 0, errors.New("BanCache: empty ip")
	}
	n, err := c.queries.DeleteIPBan(ctx, ip)
	if err != nil {
		return 0, fmt.Errorf("BanCache: delete: %w", err)
	}
	c.mu.Lock()
	delete(c.bans, ip)
	c.mu.Unlock()
	c.publish(ctx, ip)
	if c.Audit != nil && n > 0 {
		c.Audit.Record(ctx, audit.PersistentBanRevoke{IP: ip, ByHandle: byHandle})
	}
	return n, nil
}

// Snapshot returns a copy of the currently-cached active bans. Used by the
// sysop UI to render the active-bans pane without forcing a Postgres scan.
// The map is a fresh allocation; safe for the caller to mutate.
func (c *BanCache) Snapshot() map[string]time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]time.Time, len(c.bans))
	for k, v := range c.bans {
		out[k] = v
	}
	return out
}

func (c *BanCache) refresh(ctx context.Context) error {
	rows, err := c.queries.ListActiveIPBans(ctx)
	if err != nil {
		return fmt.Errorf("BanCache: list active: %w", err)
	}
	next := make(map[string]time.Time, len(rows))
	for _, r := range rows {
		if r.ExpiresAt.Valid {
			next[r.IpAddr] = r.ExpiresAt.Time
		}
	}
	c.mu.Lock()
	c.bans = next
	c.mu.Unlock()
	return nil
}

func (c *BanCache) publish(ctx context.Context, ip string) {
	if c.redisClient == nil {
		return
	}
	if err := c.redisClient.Publish(ctx, banInvalidateChannel, ip).Err(); err != nil {
		c.logger.Warn("ban cache publish invalidate", "ip", ip, "err", err)
	}
}

func (c *BanCache) listenInvalidations(ctx context.Context) {
	if c.redisClient == nil {
		return
	}
	pubsub := c.redisClient.Subscribe(ctx, banInvalidateChannel)
	defer pubsub.Close()
	ch := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			// Don't react to messages we published ourselves any
			// differently — refresh is idempotent and reading the
			// (just-written) row again is the cheapest correctness
			// guarantee across replicas.
			if err := c.refresh(ctx); err != nil {
				c.logger.Warn("ban cache invalidate refresh", "trigger", msg.Payload, "err", err)
			}
		}
	}
}
