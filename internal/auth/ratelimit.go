package auth

import (
	"context"
	"net"
	"time"
)

// RateLimitCheck reports whether a handle/IP pair is currently locked out.
type RateLimitCheck struct {
	LockedOut  bool
	RetryAfter time.Duration
}

// RateLimiter is the interface the lookup service depends on. The production
// implementation is RedisRateLimiter (ratelimit_redis.go).
type RateLimiter interface {
	Check(ctx context.Context, handle string, sourceIP net.Addr) (RateLimitCheck, error)
	RecordFailure(ctx context.Context, handle string, sourceIP net.Addr) error
	Clear(ctx context.Context, handle string) error
}

// NoopRateLimiter never locks out. Used by tests that don't need to exercise
// the real limiter.
type NoopRateLimiter struct{}

func (NoopRateLimiter) Check(_ context.Context, _ string, _ net.Addr) (RateLimitCheck, error) {
	return RateLimitCheck{}, nil
}
func (NoopRateLimiter) RecordFailure(_ context.Context, _ string, _ net.Addr) error { return nil }
func (NoopRateLimiter) Clear(_ context.Context, _ string) error                     { return nil }
