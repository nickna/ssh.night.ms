package netlimit

import (
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

// Config bounds the three connection-level controls. A zero or negative value
// in any of the three caps disables that particular control — useful for
// tests and for letting operators turn off a control without removing the
// surrounding wiring.
type Config struct {
	// MaxConnPerIP caps the number of concurrently open TCP connections from
	// one source (IPv6 /64-collapsed). Decremented on conn.Close, not on auth
	// success — the threat we're bounding is slowloris-style socket hoarding,
	// which is measured at the TCP layer not the SSH-auth layer.
	MaxConnPerIP int

	// PerIPRate is the new-connection token-bucket rate per source IP
	// (connections per second). PerIPBurst is the bucket depth. The bucket is
	// independent of MaxConnPerIP — the cap bounds steady state, the bucket
	// bounds arrival rate.
	PerIPRate  rate.Limit
	PerIPBurst int

	// MaxUnauthHandshakes caps in-flight unauthenticated handshakes process-wide,
	// the in-process equivalent of OpenSSH's MaxStartups. A botnet that spreads
	// load across IPs to evade MaxConnPerIP still trips this.
	MaxUnauthHandshakes int

	// IdleEvict is how long an empty per-IP entry sits in the map before GC.
	// Zero defaults to 5 minutes — long enough that a returning user isn't
	// burst-recreated, short enough that a scanner's entries don't accumulate.
	IdleEvict time.Duration
}

// RejectReason identifies which gate caused a rejection. Used by the audit
// callback to emit the right event type in Phase C.
type RejectReason string

const (
	RejectIPConcurrent RejectReason = "ip_concurrent_cap"
	RejectIPRate       RejectReason = "ip_rate_cap"
	RejectGlobalUnauth RejectReason = "global_unauth_cap"
	RejectIPBanned     RejectReason = "ip_banned"
)

// BanChecker reports whether a source IP is on the persistent-ban list. It is
// satisfied by *auth.BanCache, but lives here as a minimal interface so the
// netlimit package never imports auth — that would form an import cycle,
// since auth already imports netlimit for CollapseIP. ipKey is the
// collapsed-IP key form produced by CollapseIP; the second return (ban
// expiry) is unused by the gate but kept so *auth.BanCache satisfies the
// interface without an adapter.
type BanChecker interface {
	IsBanned(ipKey string) (bool, time.Time)
}

// RejectCallback is invoked when a connection is denied at acquire time.
// Optional — when nil, the Tracker logs a warn line and nothing else. Phase C
// wires this to the security audit recorder.
type RejectCallback func(addr net.Addr, reason RejectReason)

// Tracker holds the per-IP state and global counter. One per server.
//
// cfg is stored as atomic.Pointer[Config] so the sysop console (via
// settings.Cache) can rotate the active config without restarting the
// listener. Hot-path reads do `t.cfg.Load()` once at the top of each
// AcquireConn / AcquireHandshake call — single atomic load, no locks.
//
// Caveat: per-IP rate.Limiter instances are constructed once when a source
// IP first appears, bound to the rate/burst snapshot at that moment.
// Subsequent config rotations don't update existing buckets — they only
// affect newly-created ones (or buckets the idle-evict GC has reaped and
// the next conn re-creates). For a true hot-rotate of the per-IP rate we'd
// need to walk perIP and call SetLimit/SetBurst, but that's a rare-op
// foot­gun the current sysop UI doesn't expose; idle eviction (default
// 5min) catches up on its own.
type Tracker struct {
	cfg      atomic.Pointer[Config]
	logger   *slog.Logger
	onReject RejectCallback

	// banChecker, when non-nil, is consulted at the top of AcquireConn so a
	// persistently-banned IP is dropped at TCP-accept time, before any gate
	// allocation or SSH handshake. Set once via SetBanChecker during startup
	// wiring (before the listener serves); not designed for concurrent
	// rotation, matching the plain logger/onReject fields beside it.
	banChecker BanChecker

	mu    sync.Mutex
	perIP map[string]*ipState

	global atomic.Int64
}

type ipState struct {
	concurrent atomic.Int64
	lastSeenNs atomic.Int64
	bucket     *rate.Limiter // nil when PerIPRate == 0 (per-IP rate disabled)
}

// NewTracker constructs a Tracker with the given config. Callers must invoke
// Run(ctx) in a goroutine to drive the idle-entry GC loop; otherwise the
// per-IP map grows unboundedly across the process lifetime.
func NewTracker(cfg Config, logger *slog.Logger, onReject RejectCallback) *Tracker {
	if cfg.IdleEvict <= 0 {
		cfg.IdleEvict = 5 * time.Minute
	}
	t := &Tracker{
		logger:   logger,
		onReject: onReject,
		perIP:    make(map[string]*ipState),
	}
	t.cfg.Store(&cfg)
	return t
}

// SetBanChecker installs the persistent-ban lookup consulted by AcquireConn.
// Call once during startup wiring, before the listener begins serving — the
// field is read on the accept hot path without synchronization, so rotating
// it under live traffic is not supported. Passing nil leaves the gate
// disabled (the zero value), which keeps existing call sites and tests that
// never set a checker working unchanged.
func (t *Tracker) SetBanChecker(bc BanChecker) {
	t.banChecker = bc
}

// UpdateConfig swaps the active Config. Called by the sysop settings hook
// (see cmd/nightms/main.go) when an operator changes max_conn_per_ip /
// max_unauth_handshakes etc. IdleEvict is preserved from the existing config
// if the new one passes zero — we never want to disable GC by accident.
func (t *Tracker) UpdateConfig(next Config) {
	if next.IdleEvict <= 0 {
		if cur := t.cfg.Load(); cur != nil {
			next.IdleEvict = cur.IdleEvict
		} else {
			next.IdleEvict = 5 * time.Minute
		}
	}
	t.cfg.Store(&next)
}

// Run drives the periodic GC loop that removes idle per-IP entries. Exits
// when ctx is done.
func (t *Tracker) Run(ctx interface {
	Done() <-chan struct{}
}) {
	// IdleEvict cadence is read once at start — operators don't change it at
	// runtime (it's not in the settings catalog), and re-reading per tick
	// would make a future change to IdleEvict racy with the ticker.
	tick := time.NewTicker(t.cfg.Load().IdleEvict / 2)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			t.gc()
		}
	}
}

// gc removes per-IP entries that have no in-flight conns and haven't been
// touched in IdleEvict. Holds the map lock for the iteration — fine because
// GC runs every IdleEvict/2 and the map is small in any realistic deployment.
func (t *Tracker) gc() {
	cutoff := time.Now().Add(-t.cfg.Load().IdleEvict).UnixNano()
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, st := range t.perIP {
		if st.concurrent.Load() == 0 && st.lastSeenNs.Load() < cutoff {
			delete(t.perIP, k)
		}
	}
}

// AcquireConn applies the per-IP gates (concurrent cap + token bucket) and
// returns a release function that must be called when the connection closes.
// On rejection returns (nil, reason, false); the caller is responsible for
// closing the underlying conn.
//
// The release function is safe to call exactly once; subsequent calls are
// no-ops. Wrap your conn.Close with sync.Once if the caller can't guarantee
// single-shot release.
func (t *Tracker) AcquireConn(addr net.Addr) (release func(), reason RejectReason, ok bool) {
	key := CollapseIP(addr)
	if key == "" {
		// Unknown / unparseable addr — let it through. Rare edge case
		// (Unix sockets in tests); production listener always has a real
		// TCP addr.
		return func() {}, "", true
	}

	// Persistent-ban gate runs first — before the token bucket, the
	// concurrent cap, and the per-IP state allocation. A banned offender is
	// the same IP the auth pipeline already refuses (checkPersistentBan);
	// dropping it here means it never completes a handshake to be refused at
	// the auth callback, which kills the reconnect churn (and the per-attempt
	// auth_failure + handshake_failed event pair) those bots generate. Logged
	// at Debug, not Warn: dropping an already-banned IP is the system working
	// as intended and is the highest-volume reject path, so it must not flood
	// the operational warn stream. Observability lives in the onReject audit
	// event (ConnRejectedBanned, info severity).
	if t.banChecker != nil {
		if banned, _ := t.banChecker.IsBanned(key); banned {
			if t.logger != nil {
				t.logger.Debug("netlimit: dropped banned ip", "ip", key)
			}
			if t.onReject != nil {
				t.onReject(addr, RejectIPBanned)
			}
			return nil, RejectIPBanned, false
		}
	}

	cfg := t.cfg.Load()
	st := t.getOrCreate(key, cfg)

	// Token bucket first — a flood from one IP shouldn't even pay the
	// concurrent-cap check cost.
	if st.bucket != nil && !st.bucket.Allow() {
		t.reject(addr, RejectIPRate)
		return nil, RejectIPRate, false
	}

	// Concurrent cap.
	maxPerIP := cfg.MaxConnPerIP
	if maxPerIP > 0 {
		// CAS-style: optimistically increment, decrement on overflow.
		// Cheaper than holding a lock around the compare.
		n := st.concurrent.Add(1)
		if n > int64(maxPerIP) {
			st.concurrent.Add(-1)
			t.reject(addr, RejectIPConcurrent)
			return nil, RejectIPConcurrent, false
		}
	}
	st.lastSeenNs.Store(time.Now().UnixNano())

	var released atomic.Bool
	return func() {
		if !released.CompareAndSwap(false, true) {
			return
		}
		if maxPerIP > 0 {
			st.concurrent.Add(-1)
		}
		st.lastSeenNs.Store(time.Now().UnixNano())
	}, "", true
}

// AcquireHandshake applies the global unauthenticated-handshake cap and
// returns a release function that should be called when either auth completes
// (success or failure) or the conn closes — whichever happens first. The
// release function is single-shot; subsequent calls are no-ops.
func (t *Tracker) AcquireHandshake(addr net.Addr) (release func(), reason RejectReason, ok bool) {
	maxUnauth := t.cfg.Load().MaxUnauthHandshakes
	if maxUnauth <= 0 {
		return func() {}, "", true
	}
	n := t.global.Add(1)
	if n > int64(maxUnauth) {
		t.global.Add(-1)
		t.reject(addr, RejectGlobalUnauth)
		return nil, RejectGlobalUnauth, false
	}
	var released atomic.Bool
	return func() {
		if released.CompareAndSwap(false, true) {
			t.global.Add(-1)
		}
	}, "", true
}

func (t *Tracker) reject(addr net.Addr, reason RejectReason) {
	if t.logger != nil {
		t.logger.Warn("netlimit: connection rejected",
			"ip", CollapseIP(addr),
			"reason", string(reason))
	}
	if t.onReject != nil {
		t.onReject(addr, reason)
	}
}

func (t *Tracker) getOrCreate(key string, cfg *Config) *ipState {
	t.mu.Lock()
	defer t.mu.Unlock()
	if st, ok := t.perIP[key]; ok {
		return st
	}
	st := &ipState{}
	if cfg.PerIPRate > 0 && cfg.PerIPBurst > 0 {
		st.bucket = rate.NewLimiter(cfg.PerIPRate, cfg.PerIPBurst)
	}
	t.perIP[key] = st
	return st
}
