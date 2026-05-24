package session

import (
	"context"
	"expvar"
	"sync/atomic"
)

// activeSessions counts the in-flight per-user sessions across both surfaces
// (SSH transport + web wsbridge). Incremented by Acquire and decremented
// either via the returned release closure or automatically when the per-
// session context fires (AcquireForContext). Exported via expvar so a sysop
// can graph it from the /debug/vars mount when NIGHTMS_DEBUG_ADDR is set.
var activeSessions atomic.Int64

func init() {
	expvar.Publish("session_active_total", expvar.Func(func() any {
		return activeSessions.Load()
	}))
}

// Acquire reserves a slot in the global session counter. limit == 0 disables
// the cap entirely (caller's responsibility to read it from settings). When
// the post-increment count exceeds limit, Acquire returns ok=false and the
// slot is rolled back — the caller is responsible for rejecting the new
// session with an operator-visible reason.
//
// The returned release function is single-shot; subsequent calls are no-ops.
// Pairs with AcquireForContext for the common SSH/WS pattern where release
// should fire on disconnect rather than via defer.
func Acquire(limit int) (release func(), ok bool) {
	n := activeSessions.Add(1)
	if limit > 0 && n > int64(limit) {
		activeSessions.Add(-1)
		return func() {}, false
	}
	var released atomic.Bool
	return func() {
		if released.CompareAndSwap(false, true) {
			activeSessions.Add(-1)
		}
	}, true
}

// AcquireForContext wraps Acquire and arranges for the slot to be released
// when ctx.Done() fires. Returns false when the slot was refused (caller
// must reject the session). On success, spawns a goroutine that waits on
// ctx — one goroutine per session, negligible cost.
func AcquireForContext(ctx context.Context, limit int) bool {
	release, ok := Acquire(limit)
	if !ok {
		return false
	}
	go func() {
		<-ctx.Done()
		release()
	}()
	return true
}

// ActiveCount returns the current count of in-flight sessions. Useful for
// the sysop console footer and for tests.
func ActiveCount() int64 {
	return activeSessions.Load()
}
