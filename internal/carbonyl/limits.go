package carbonyl

import (
	"sync"
	"sync/atomic"
)

// Limits bounds concurrent rich-mode sessions across three axes. A zero in any
// field disables that axis. Stored as an atomic.Pointer on the Runner so a
// sysop tightening caps mid-incident takes effect on the next Acquire without
// a restart.
type Limits struct {
	Global    int
	PerIP     int
	PerHandle int
}

// RejectReason names which gate refused an Acquire. Surfaced to the user as
// the toast message so they know which knob to ask the sysop to loosen.
type RejectReason string

const (
	RejectGlobal   RejectReason = "global"
	RejectIP       RejectReason = "per_ip"
	RejectHandle   RejectReason = "per_handle"
	RejectDisabled RejectReason = "disabled"
	RejectNoBinary RejectReason = "no_binary"
	RejectBadURL   RejectReason = "bad_url"
	RejectInternal RejectReason = "internal_error"
)

// tokens holds the three counter sets behind one mutex. Compared to the
// netlimit.Tracker pattern this uses a single lock for all three increments
// rather than per-axis atomics — Carbonyl launches are rare (humans pressing a
// key) so the small contention cost is fine, and it makes the all-or-nothing
// semantics trivial (if any axis fails, no axis was bumped).
type tokens struct {
	cfg       atomic.Pointer[Limits]
	mu        sync.Mutex
	global    int
	perIP     map[string]int
	perHandle map[int64]int
}

func newTokens(initial Limits) *tokens {
	t := &tokens{
		perIP:     make(map[string]int),
		perHandle: make(map[int64]int),
	}
	t.cfg.Store(&initial)
	return t
}

// updateLimits hot-swaps the active Limits. Existing acquired counters are
// untouched — a tightened cap only affects the next Acquire.
func (t *tokens) updateLimits(next Limits) {
	t.cfg.Store(&next)
}

// Acquire either reserves a slot under all three caps and returns a
// single-shot release, or returns the reason it couldn't. ip and handle keys
// of "" / 0 still count against the global cap but skip their own per-axis
// map — useful for tests that don't have realistic identities.
func (t *tokens) Acquire(ip string, handle int64) (release func(), reason RejectReason, ok bool) {
	cfg := t.cfg.Load()
	t.mu.Lock()
	defer t.mu.Unlock()

	if cfg.Global > 0 && t.global >= cfg.Global {
		return nil, RejectGlobal, false
	}
	if cfg.PerIP > 0 && ip != "" && t.perIP[ip] >= cfg.PerIP {
		return nil, RejectIP, false
	}
	if cfg.PerHandle > 0 && handle > 0 && t.perHandle[handle] >= cfg.PerHandle {
		return nil, RejectHandle, false
	}

	t.global++
	if ip != "" {
		t.perIP[ip]++
	}
	if handle > 0 {
		t.perHandle[handle]++
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			defer t.mu.Unlock()
			t.global--
			if ip != "" {
				if t.perIP[ip] <= 1 {
					delete(t.perIP, ip)
				} else {
					t.perIP[ip]--
				}
			}
			if handle > 0 {
				if t.perHandle[handle] <= 1 {
					delete(t.perHandle, handle)
				} else {
					t.perHandle[handle]--
				}
			}
		})
	}, "", true
}

// snapshot is used by tests + sysop debug to report current usage. Lock-free
// callers are not safe; the public API is via Runner.Stats.
func (t *tokens) snapshot() (global int, perIP map[string]int, perHandle map[int64]int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	ip := make(map[string]int, len(t.perIP))
	for k, v := range t.perIP {
		ip[k] = v
	}
	h := make(map[int64]int, len(t.perHandle))
	for k, v := range t.perHandle {
		h[k] = v
	}
	return t.global, ip, h
}
