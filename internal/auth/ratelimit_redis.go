package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/nickna/ssh.night.ms/internal/security/audit"
	"github.com/nickna/ssh.night.ms/internal/security/netlimit"
	"github.com/nickna/ssh.night.ms/internal/settings"
)

// RateLimitParams carries the lockout knobs (NIGHTMS_LOCKOUT_*) plus the
// exponential-backoff + persistent-ban additions from the Phase B hardening
// pass. Defaults: 5 per-handle fails / 20 per-IP fails inside a 15-minute
// window trigger a 15-minute lockout. Each successive lockout within 24h
// doubles the lock duration up to BackoffMax. After PersistentBanThreshold
// lockouts within 24h the IP becomes eligible for a sysop-visible persistent
// ban (handled by the OnPersistentBanEligible callback wired in main).
type RateLimitParams struct {
	HandleThreshold int
	IPThreshold     int
	WindowDuration  time.Duration // how long failures stay counted (sliding)
	LockDuration    time.Duration // base lock duration; multiplied by 1<<min(n-1, BackoffMax)

	// BackoffMax caps the exponential multiplier shift: 0 = flat, 5 = up
	// to ×32. With LockDuration=15m, BackoffMax=5 caps the longest
	// auto-lock at 8h.
	BackoffMax int

	// PersistentBanThreshold is the per-IP lockcount within LockcountWindow
	// at which the limiter flags the IP for persistent ban (typically 3).
	// Zero disables the persistent-ban escalation entirely; the limiter
	// still applies exponential lockouts.
	PersistentBanThreshold int

	// LockcountWindow is the sliding TTL on the lockcount counter (typically
	// 24h). Refreshed on every INCR so a patient attacker doesn't drop out
	// of the window by spacing their attempts just-so.
	LockcountWindow time.Duration
}

// RedisRateLimiter is the production limiter, backed by Redis INCR/EXPIRE
// counters and TTL'd lock keys. Implements the RateLimiter interface.
//
// OnPersistentBanEligible is an optional hook fired when a per-IP lockcount
// crosses PersistentBanThreshold. The implementation in
// internal/auth/persistban.go upserts a row into security_ip_bans and
// broadcasts a Redis pub/sub invalidation so the BanCache picks up the new
// ban on every replica.
//
// Audit, when non-nil, receives LockoutHandle / LockoutIP events at the
// moment a lock is set. Nil-safe — tests can skip it.
type RedisRateLimiter struct {
	Client *redis.Client
	Params RateLimitParams
	Logger *slog.Logger

	// Settings is the runtime-tunable settings cache. When non-nil, the
	// thresholds HandleThreshold / IPThreshold / WindowDuration are read from
	// the current snapshot on every check, falling back to Params for any
	// snapshot value that's zero. The other Params fields (LockDuration,
	// BackoffMax, PersistentBanThreshold, LockcountWindow) aren't exposed in
	// the catalog — they remain env-driven via Params.
	Settings *settings.Cache

	OnPersistentBanEligible func(ip string, lockcount int64)
	Audit                   audit.Recorder
}

func NewRedisRateLimiter(client *redis.Client, params RateLimitParams, logger *slog.Logger) *RedisRateLimiter {
	return &RedisRateLimiter{Client: client, Params: params, Logger: logger}
}

// effective returns the params struct to use for the current check, layering
// Settings-cache overrides over the base Params. Called on every Check /
// RecordFailure so live tuning takes effect without restart. Cheap: one
// atomic.Pointer load + a value copy.
func (r *RedisRateLimiter) effective() RateLimitParams {
	p := r.Params
	if r.Settings == nil {
		return p
	}
	snap := r.Settings.Get()
	if snap.LockoutHandleThreshold > 0 {
		p.HandleThreshold = snap.LockoutHandleThreshold
	}
	if snap.LockoutIPThreshold > 0 {
		p.IPThreshold = snap.LockoutIPThreshold
	}
	if snap.LockoutWindowSeconds > 0 {
		p.WindowDuration = time.Duration(snap.LockoutWindowSeconds) * time.Second
	}
	return p
}

func failHandleKey(handle string) string      { return "auth:fail:handle:" + strings.ToLower(handle) }
func failIPKey(ip string) string              { return "auth:fail:ip:" + ip }
func lockHandleKey(handle string) string      { return "auth:lock:handle:" + strings.ToLower(handle) }
func lockIPKey(ip string) string              { return "auth:lock:ip:" + ip }
func lockcountHandleKey(handle string) string { return "auth:lockcount:handle:" + strings.ToLower(handle) }
func lockcountIPKey(ip string) string         { return "auth:lockcount:ip:" + ip }

// normalizeIP strips the port from a net.Addr.String() so the lockout key is
// stable across multiple connection attempts from the same address.
func normalizeIP(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	return host
}

// Check reports whether the (handle, IP) pair is currently locked out and,
// if so, how long until the lock expires.
func (r *RedisRateLimiter) Check(ctx context.Context, handle string, sourceIP net.Addr) (RateLimitCheck, error) {
	ip := normalizeIP(sourceIP)
	if d, locked, err := r.lockTTL(ctx, lockHandleKey(handle)); err != nil {
		return RateLimitCheck{}, err
	} else if locked {
		return RateLimitCheck{LockedOut: true, RetryAfter: d}, nil
	}
	if ip != "" {
		if d, locked, err := r.lockTTL(ctx, lockIPKey(ip)); err != nil {
			return RateLimitCheck{}, err
		} else if locked {
			return RateLimitCheck{LockedOut: true, RetryAfter: d}, nil
		}
	}
	return RateLimitCheck{}, nil
}

// lockTTL returns the duration remaining on a lock key, plus a bool for
// "lock is currently active". A missing key returns (0, false, nil).
//
// Note on the comparison: go-redis v9's DurationCmd only multiplies positive
// integer replies by the precision (time.Second for TTL). Negative sentinels
// (-2 = missing, -1 = no expire) come back as raw nanoseconds — so
// `d == -2*time.Second` would always be false. Compare against the raw
// int64 ns count instead.
func (r *RedisRateLimiter) lockTTL(ctx context.Context, key string) (time.Duration, bool, error) {
	d, err := r.Client.TTL(ctx, key).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("rate: TTL %s: %w", key, err)
	}
	switch int64(d) {
	case -2:
		// Missing key — not locked.
		return 0, false, nil
	case -1:
		// Lock without TTL is an unexpected state; treat as the full
		// LockDuration so the operator notices and the user isn't
		// permanently shut out by our own bug.
		return r.Params.LockDuration, true, nil
	default:
		// Positive remainder — already multiplied by time.Second by go-redis.
		return d, true, nil
	}
}

// RecordFailure bumps the failure counters and may flip a lock on. The
// counters' TTLs are reset to the window each time we INCR so the window is
// effectively sliding.
//
// Phase B: when a threshold trips, the lock duration is computed by INCRing
// a sliding-TTL lockcount counter and applying the exponential backoff
// 1 << min(lockcount-1, BackoffMax). The fail counter is cleared after the
// lock is set so the NEXT lockout starts a fresh fail-count window — the
// escalation lives entirely in the lockcount key (24h sliding TTL). On the
// IP path, if the lockcount crosses PersistentBanThreshold, the
// OnPersistentBanEligible hook fires so persistban.go can write a
// security_ip_bans row and broadcast pub/sub invalidation.
func (r *RedisRateLimiter) RecordFailure(ctx context.Context, handle string, sourceIP net.Addr) error {
	eff := r.effective()
	if handle != "" {
		n, err := r.incrWithExpire(ctx, failHandleKey(handle), eff.WindowDuration)
		if err != nil {
			return err
		}
		if n >= int64(eff.HandleThreshold) {
			lockcount, err := r.incrWithExpire(ctx, lockcountHandleKey(handle), r.lockcountWindow())
			if err != nil {
				return err
			}
			dur := r.computeLockDuration(lockcount)
			if err := r.Client.Set(ctx, lockHandleKey(handle), "1", dur).Err(); err != nil {
				return fmt.Errorf("rate: set handle lock: %w", err)
			}
			r.Logger.Info("rate limit: handle locked",
				"handle", handle, "fails", n, "lockcount", lockcount, "duration", dur)
			if r.Audit != nil {
				r.Audit.Record(ctx, audit.LockoutHandle{
					Handle: handle, IP: netlimit.CollapseIP(sourceIP),
					Fails: int(n), Lockcount: lockcount, Duration: dur,
				})
			}
			// Reset fail counter so the next lockout window is independent.
			// The escalation state lives in lockcount; failHandleKey is just
			// the per-window threshold trigger.
			_ = r.Client.Del(ctx, failHandleKey(handle)).Err()
		}
	}
	if ip := normalizeIP(sourceIP); ip != "" {
		n, err := r.incrWithExpire(ctx, failIPKey(ip), eff.WindowDuration)
		if err != nil {
			return err
		}
		if n >= int64(eff.IPThreshold) {
			lockcount, err := r.incrWithExpire(ctx, lockcountIPKey(ip), r.lockcountWindow())
			if err != nil {
				return err
			}
			dur := r.computeLockDuration(lockcount)
			if err := r.Client.Set(ctx, lockIPKey(ip), "1", dur).Err(); err != nil {
				return fmt.Errorf("rate: set ip lock: %w", err)
			}
			r.Logger.Info("rate limit: ip locked",
				"ip", ip, "fails", n, "lockcount", lockcount, "duration", dur)
			if r.Audit != nil {
				r.Audit.Record(ctx, audit.LockoutIP{
					IP: ip, Fails: int(n), Lockcount: lockcount, Duration: dur,
				})
			}
			_ = r.Client.Del(ctx, failIPKey(ip)).Err()

			if r.Params.PersistentBanThreshold > 0 &&
				lockcount >= int64(r.Params.PersistentBanThreshold) &&
				r.OnPersistentBanEligible != nil {
				r.OnPersistentBanEligible(ip, lockcount)
			}
		}
	}
	return nil
}

// computeLockDuration applies the exponential-backoff multiplier 1 << min(n-1, BackoffMax)
// to the base LockDuration. lockcount=1 → ×1 (base); =2 → ×2; =6+ → ×32 (when
// BackoffMax=5). Returns LockDuration as-is when lockcount < 1 (defensive).
func (r *RedisRateLimiter) computeLockDuration(lockcount int64) time.Duration {
	if lockcount < 1 {
		return r.Params.LockDuration
	}
	shift := int(lockcount) - 1
	if shift > r.Params.BackoffMax {
		shift = r.Params.BackoffMax
	}
	return r.Params.LockDuration * time.Duration(int64(1)<<shift)
}

func (r *RedisRateLimiter) lockcountWindow() time.Duration {
	if r.Params.LockcountWindow > 0 {
		return r.Params.LockcountWindow
	}
	return 24 * time.Hour
}

// incrWithExpire INCRs the counter and (re)applies the window TTL. Returns
// the post-increment value so the caller can decide whether to lock.
func (r *RedisRateLimiter) incrWithExpire(ctx context.Context, key string, window time.Duration) (int64, error) {
	n, err := r.Client.Incr(ctx, key).Result()
	if err != nil {
		return 0, fmt.Errorf("rate: INCR %s: %w", key, err)
	}
	if err := r.Client.Expire(ctx, key, window).Err(); err != nil {
		return n, fmt.Errorf("rate: EXPIRE %s: %w", key, err)
	}
	return n, nil
}

// Clear is called on successful auth. Drops the handle-side counter and lock
// so a user who finally typed their password right doesn't carry the
// lockout-pending state into the next session. The per-IP counters stay so
// an attacker scanning many handles from one address still trips the IP
// threshold.
func (r *RedisRateLimiter) Clear(ctx context.Context, handle string) error {
	if handle == "" {
		return nil
	}
	if err := r.Client.Del(ctx, failHandleKey(handle), lockHandleKey(handle)).Err(); err != nil {
		return fmt.Errorf("rate: clear handle: %w", err)
	}
	return nil
}
