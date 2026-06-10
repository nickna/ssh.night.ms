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
