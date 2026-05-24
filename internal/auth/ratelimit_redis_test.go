package auth

import (
	"testing"
	"time"
)

// TestComputeLockDuration_ExponentialSequence pins the public escalation
// schedule: each lockout doubles the previous until BackoffMax caps the
// multiplier. With LockDuration=15m and BackoffMax=5 the sequence is
// 15m, 30m, 1h, 2h, 4h, 8h, 8h (capped), ...
func TestComputeLockDuration_ExponentialSequence(t *testing.T) {
	r := &RedisRateLimiter{Params: RateLimitParams{
		LockDuration: 15 * time.Minute,
		BackoffMax:   5,
	}}
	cases := []struct {
		lockcount int64
		want      time.Duration
	}{
		{1, 15 * time.Minute},
		{2, 30 * time.Minute},
		{3, 60 * time.Minute},
		{4, 2 * time.Hour},
		{5, 4 * time.Hour},
		{6, 8 * time.Hour},
		{7, 8 * time.Hour}, // capped
		{20, 8 * time.Hour}, // still capped, no overflow
	}
	for _, tc := range cases {
		if got := r.computeLockDuration(tc.lockcount); got != tc.want {
			t.Errorf("lockcount=%d: want %v, got %v", tc.lockcount, tc.want, got)
		}
	}
}

func TestComputeLockDuration_DefensiveBelowOne(t *testing.T) {
	r := &RedisRateLimiter{Params: RateLimitParams{
		LockDuration: 15 * time.Minute,
		BackoffMax:   5,
	}}
	if got := r.computeLockDuration(0); got != 15*time.Minute {
		t.Errorf("lockcount=0 should fall back to base; got %v", got)
	}
}

func TestComputeLockDuration_ZeroBackoffMax_NoEscalation(t *testing.T) {
	// BackoffMax=0 disables doubling — every lockout stays at the base
	// LockDuration, matching the pre-Phase-B flat behavior.
	r := &RedisRateLimiter{Params: RateLimitParams{
		LockDuration: 15 * time.Minute,
		BackoffMax:   0,
	}}
	for n := int64(1); n < 10; n++ {
		if got := r.computeLockDuration(n); got != 15*time.Minute {
			t.Errorf("BackoffMax=0 lockcount=%d: want flat 15m, got %v", n, got)
		}
	}
}
