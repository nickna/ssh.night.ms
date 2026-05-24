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
)

// RejectCallback is invoked when a connection is denied at acquire time.
// Optional — when nil, the Tracker logs a warn line and nothing else. Phase C
// wires this to the security audit recorder.
type RejectCallback func(addr net.Addr, reason RejectReason)

// Tracker holds the per-IP state and global counter. One per server.
type Tracker struct {
	cfg      Config
	logger   *slog.Logger
	onReject RejectCallback

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
	return &Tracker{
		cfg:      cfg,
		logger:   logger,
		onReject: onReject,
		perIP:    make(map[string]*ipState),
	}
}

// Run drives the periodic GC loop that removes idle per-IP entries. Exits
// when ctx is done.
func (t *Tracker) Run(ctx interface {
	Done() <-chan struct{}
}) {
	tick := time.NewTicker(t.cfg.IdleEvict / 2)
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
	cutoff := time.Now().Add(-t.cfg.IdleEvict).UnixNano()
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

	st := t.getOrCreate(key)

	// Token bucket first — a flood from one IP shouldn't even pay the
	// concurrent-cap check cost.
	if st.bucket != nil && !st.bucket.Allow() {
		t.reject(addr, RejectIPRate)
		return nil, RejectIPRate, false
	}

	// Concurrent cap.
	if t.cfg.MaxConnPerIP > 0 {
		// CAS-style: optimistically increment, decrement on overflow.
		// Cheaper than holding a lock around the compare.
		n := st.concurrent.Add(1)
		if n > int64(t.cfg.MaxConnPerIP) {
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
		if t.cfg.MaxConnPerIP > 0 {
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
	if t.cfg.MaxUnauthHandshakes <= 0 {
		return func() {}, "", true
	}
	n := t.global.Add(1)
	if n > int64(t.cfg.MaxUnauthHandshakes) {
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

func (t *Tracker) getOrCreate(key string) *ipState {
	t.mu.Lock()
	defer t.mu.Unlock()
	if st, ok := t.perIP[key]; ok {
		return st
	}
	st := &ipState{}
	if t.cfg.PerIPRate > 0 && t.cfg.PerIPBurst > 0 {
		st.bucket = rate.NewLimiter(t.cfg.PerIPRate, t.cfg.PerIPBurst)
	}
	t.perIP[key] = st
	return st
}
