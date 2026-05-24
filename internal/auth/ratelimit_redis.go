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
)

// RateLimitParams matches the .NET PasswordHashingOptions / lockout env keys
// (NIGHTMS_LOCKOUT_*). Defaults: 5 per-handle fails / 20 per-IP fails inside a
// 15-minute window trigger a 15-minute lockout.
type RateLimitParams struct {
	HandleThreshold int
	IPThreshold     int
	WindowDuration  time.Duration // how long failures stay counted
	LockDuration    time.Duration // how long a lockout persists once tripped
}

// DefaultRateLimitParams returns the same numbers the .NET stack defaults to,
// so two stacks pointed at the same Redis would lock the same accounts at
// the same thresholds during cutover.
func DefaultRateLimitParams() RateLimitParams {
	return RateLimitParams{
		HandleThreshold: 5,
		IPThreshold:     20,
		WindowDuration:  15 * time.Minute,
		LockDuration:    15 * time.Minute,
	}
}

// RedisRateLimiter is the production limiter, backed by Redis INCR/EXPIRE
// counters and TTL'd lock keys. Implements the RateLimiter interface.
type RedisRateLimiter struct {
	Client *redis.Client
	Params RateLimitParams
	Logger *slog.Logger
}

func NewRedisRateLimiter(client *redis.Client, params RateLimitParams, logger *slog.Logger) *RedisRateLimiter {
	return &RedisRateLimiter{Client: client, Params: params, Logger: logger}
}

func failHandleKey(handle string) string { return "auth:fail:handle:" + strings.ToLower(handle) }
func failIPKey(ip string) string         { return "auth:fail:ip:" + ip }
func lockHandleKey(handle string) string { return "auth:lock:handle:" + strings.ToLower(handle) }
func lockIPKey(ip string) string         { return "auth:lock:ip:" + ip }

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
// effectively sliding (which matches the .NET behavior).
func (r *RedisRateLimiter) RecordFailure(ctx context.Context, handle string, sourceIP net.Addr) error {
	if handle != "" {
		n, err := r.incrWithExpire(ctx, failHandleKey(handle), r.Params.WindowDuration)
		if err != nil {
			return err
		}
		if n >= int64(r.Params.HandleThreshold) {
			if err := r.Client.Set(ctx, lockHandleKey(handle), "1", r.Params.LockDuration).Err(); err != nil {
				return fmt.Errorf("rate: set handle lock: %w", err)
			}
			r.Logger.Info("rate limit: handle locked", "handle", handle, "fails", n)
		}
	}
	if ip := normalizeIP(sourceIP); ip != "" {
		n, err := r.incrWithExpire(ctx, failIPKey(ip), r.Params.WindowDuration)
		if err != nil {
			return err
		}
		if n >= int64(r.Params.IPThreshold) {
			if err := r.Client.Set(ctx, lockIPKey(ip), "1", r.Params.LockDuration).Err(); err != nil {
				return fmt.Errorf("rate: set ip lock: %w", err)
			}
			r.Logger.Info("rate limit: ip locked", "ip", ip, "fails", n)
		}
	}
	return nil
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
