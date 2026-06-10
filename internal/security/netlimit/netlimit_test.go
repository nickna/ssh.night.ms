package netlimit

import (
	"context"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func mustAddr(t *testing.T, s string) net.Addr {
	t.Helper()
	a, err := net.ResolveTCPAddr("tcp", s)
	if err != nil {
		t.Fatalf("resolve %s: %v", s, err)
	}
	return a
}

func TestCollapseIP_IPv4_PreservesAddress(t *testing.T) {
	got := CollapseIP(mustAddr(t, "203.0.113.45:12345"))
	if got != "203.0.113.45" {
		t.Fatalf("want 203.0.113.45, got %q", got)
	}
}

func TestCollapseIP_IPv6_CollapsesTo64(t *testing.T) {
	// Two addresses inside the same /64 collapse to the same key.
	a := CollapseIP(mustAddr(t, "[2001:db8:1:2::1]:22"))
	b := CollapseIP(mustAddr(t, "[2001:db8:1:2:ffff:ffff:ffff:fffe]:22"))
	if a != b {
		t.Fatalf("expected same /64 key, got a=%q b=%q", a, b)
	}
	// A different /64 must produce a different key.
	c := CollapseIP(mustAddr(t, "[2001:db8:1:3::1]:22"))
	if a == c {
		t.Fatalf("expected different /64 keys, got %q == %q", a, c)
	}
}

func TestCollapseIP_NilReturnsEmpty(t *testing.T) {
	if CollapseIP(nil) != "" {
		t.Fatalf("nil addr must return empty key")
	}
}

func TestTracker_PerIPConcurrentCap(t *testing.T) {
	tr := NewTracker(Config{MaxConnPerIP: 3}, discardLogger(), nil)
	addr := mustAddr(t, "198.51.100.1:1000")

	var releases []func()
	for i := 0; i < 3; i++ {
		rel, _, ok := tr.AcquireConn(addr)
		if !ok {
			t.Fatalf("conn %d should be admitted", i)
		}
		releases = append(releases, rel)
	}
	// 4th must be rejected with the right reason.
	if _, reason, ok := tr.AcquireConn(addr); ok || reason != RejectIPConcurrent {
		t.Fatalf("4th conn should reject with RejectIPConcurrent; got ok=%v reason=%q", ok, reason)
	}
	// Release one — next acquire succeeds.
	releases[0]()
	if _, _, ok := tr.AcquireConn(addr); !ok {
		t.Fatalf("after release one slot should be free")
	}
}

func TestTracker_ReleaseIsIdempotent(t *testing.T) {
	tr := NewTracker(Config{MaxConnPerIP: 1}, discardLogger(), nil)
	addr := mustAddr(t, "198.51.100.2:1000")
	rel, _, ok := tr.AcquireConn(addr)
	if !ok {
		t.Fatal("first conn should be admitted")
	}
	rel()
	rel() // double-release must not over-decrement
	// Should still be able to take MaxConnPerIP=1 more.
	if _, _, ok := tr.AcquireConn(addr); !ok {
		t.Fatal("after double-release one slot should be free, not two")
	}
	if _, reason, ok := tr.AcquireConn(addr); ok || reason != RejectIPConcurrent {
		t.Fatalf("over-release would let a 2nd through; got ok=%v reason=%q", ok, reason)
	}
}

func TestTracker_PerIPTokenBucket(t *testing.T) {
	// Rate=10/s, burst=2 → first two pass fast, third gets rejected.
	tr := NewTracker(Config{PerIPRate: rate.Limit(10), PerIPBurst: 2}, discardLogger(), nil)
	addr := mustAddr(t, "198.51.100.3:1000")

	for i := 0; i < 2; i++ {
		if _, _, ok := tr.AcquireConn(addr); !ok {
			t.Fatalf("conn %d within burst should pass", i)
		}
	}
	if _, reason, ok := tr.AcquireConn(addr); ok || reason != RejectIPRate {
		t.Fatalf("burst exhaustion should reject with RejectIPRate; got ok=%v reason=%q", ok, reason)
	}
}

func TestTracker_GlobalUnauthCap(t *testing.T) {
	tr := NewTracker(Config{MaxUnauthHandshakes: 2}, discardLogger(), nil)
	addr := mustAddr(t, "198.51.100.4:1000")

	rel1, _, ok := tr.AcquireHandshake(addr)
	if !ok {
		t.Fatal("first handshake should be admitted")
	}
	rel2, _, ok := tr.AcquireHandshake(addr)
	if !ok {
		t.Fatal("second handshake should be admitted")
	}
	if _, reason, ok := tr.AcquireHandshake(addr); ok || reason != RejectGlobalUnauth {
		t.Fatalf("third handshake should reject with RejectGlobalUnauth; got ok=%v reason=%q", ok, reason)
	}
	rel1()
	if _, _, ok := tr.AcquireHandshake(addr); !ok {
		t.Fatal("after release, next handshake should succeed")
	}
	rel2()
}

func TestTracker_RejectCallbackFires(t *testing.T) {
	var hits atomic.Int64
	var seenReason RejectReason
	var mu sync.Mutex
	cb := func(_ net.Addr, r RejectReason) {
		hits.Add(1)
		mu.Lock()
		seenReason = r
		mu.Unlock()
	}
	tr := NewTracker(Config{MaxConnPerIP: 1}, discardLogger(), cb)
	addr := mustAddr(t, "198.51.100.5:1000")

	if _, _, ok := tr.AcquireConn(addr); !ok {
		t.Fatal("first should pass")
	}
	if _, _, ok := tr.AcquireConn(addr); ok {
		t.Fatal("second should reject")
	}
	if hits.Load() != 1 {
		t.Fatalf("expected 1 reject callback, got %d", hits.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	if seenReason != RejectIPConcurrent {
		t.Fatalf("expected RejectIPConcurrent, got %q", seenReason)
	}
}

// fakeBanChecker is a BanChecker stub whose verdict is driven by a set of
// collapsed-IP keys. Expiry is always zero — the gate ignores it.
type fakeBanChecker struct {
	banned map[string]bool
}

func (f fakeBanChecker) IsBanned(ipKey string) (bool, time.Time) {
	return f.banned[ipKey], time.Time{}
}

func TestTracker_BannedIP_DroppedBeforeGates(t *testing.T) {
	var hits atomic.Int64
	var seen RejectReason
	var mu sync.Mutex
	cb := func(_ net.Addr, r RejectReason) {
		hits.Add(1)
		mu.Lock()
		seen = r
		mu.Unlock()
	}
	// Generous caps + a live token bucket: if the ban gate didn't run first,
	// this IP would sail through. It must be rejected anyway.
	tr := NewTracker(Config{MaxConnPerIP: 5, PerIPRate: rate.Limit(100), PerIPBurst: 10}, discardLogger(), cb)
	tr.SetBanChecker(fakeBanChecker{banned: map[string]bool{"203.0.113.9": true}})

	addr := mustAddr(t, "203.0.113.9:5000")
	if _, reason, ok := tr.AcquireConn(addr); ok || reason != RejectIPBanned {
		t.Fatalf("banned IP must reject with RejectIPBanned; got ok=%v reason=%q", ok, reason)
	}
	if hits.Load() != 1 {
		t.Fatalf("expected 1 reject callback, got %d", hits.Load())
	}
	mu.Lock()
	got := seen
	mu.Unlock()
	if got != RejectIPBanned {
		t.Fatalf("callback should see RejectIPBanned, got %q", got)
	}
	// The ban gate runs before getOrCreate, so no per-IP state is allocated
	// for a banned offender — it can't consume map memory or a token slot.
	tr.mu.Lock()
	_, present := tr.perIP[CollapseIP(addr)]
	tr.mu.Unlock()
	if present {
		t.Fatal("banned IP must not allocate a per-IP state entry")
	}
	// A different, non-banned IP is wholly unaffected.
	if _, _, ok := tr.AcquireConn(mustAddr(t, "203.0.113.10:5000")); !ok {
		t.Fatal("non-banned IP should still be admitted")
	}
}

func TestTracker_BanCheckerNil_AdmitsNormally(t *testing.T) {
	// No SetBanChecker call → the nil checker must be a no-op, not a panic,
	// and must not change the admit decision.
	tr := NewTracker(Config{MaxConnPerIP: 1}, discardLogger(), nil)
	if _, _, ok := tr.AcquireConn(mustAddr(t, "203.0.113.11:1")); !ok {
		t.Fatal("nil ban checker must admit normally")
	}
}

func TestTracker_BanChecker_OnlyBannedKeysRejected(t *testing.T) {
	// A checker that bans one key must leave every other key alone — guards
	// against an inverted-condition regression in the gate.
	tr := NewTracker(Config{MaxConnPerIP: 2}, discardLogger(), nil)
	tr.SetBanChecker(fakeBanChecker{banned: map[string]bool{"198.51.100.50": true}})

	if _, reason, ok := tr.AcquireConn(mustAddr(t, "198.51.100.50:1")); ok || reason != RejectIPBanned {
		t.Fatalf("listed key must reject; got ok=%v reason=%q", ok, reason)
	}
	if _, _, ok := tr.AcquireConn(mustAddr(t, "198.51.100.51:1")); !ok {
		t.Fatal("unlisted key must be admitted")
	}
}

func TestTracker_IPv6Collapsing_AppliesToCap(t *testing.T) {
	// Two distinct addresses in the same /64 must share the cap.
	tr := NewTracker(Config{MaxConnPerIP: 2}, discardLogger(), nil)
	a := mustAddr(t, "[2001:db8:1:2::1]:1000")
	b := mustAddr(t, "[2001:db8:1:2::2]:1000")
	c := mustAddr(t, "[2001:db8:1:2::3]:1000")

	if _, _, ok := tr.AcquireConn(a); !ok {
		t.Fatal("a should pass")
	}
	if _, _, ok := tr.AcquireConn(b); !ok {
		t.Fatal("b should pass")
	}
	// c is in the same /64 as a+b, so it must trip the per-IP cap.
	if _, _, ok := tr.AcquireConn(c); ok {
		t.Fatal("c should reject — same /64 as a+b")
	}
}

func TestTracker_GC_RemovesIdleEntries(t *testing.T) {
	tr := NewTracker(Config{MaxConnPerIP: 5, IdleEvict: 20 * time.Millisecond}, discardLogger(), nil)
	addr := mustAddr(t, "198.51.100.6:1000")

	rel, _, ok := tr.AcquireConn(addr)
	if !ok {
		t.Fatal("first should pass")
	}
	rel()

	// Wait past the idle window then trigger gc.
	time.Sleep(30 * time.Millisecond)
	tr.gc()

	tr.mu.Lock()
	_, present := tr.perIP[CollapseIP(addr)]
	tr.mu.Unlock()
	if present {
		t.Fatal("idle entry should have been GC'd")
	}
}

// fakeListener is a net.Listener whose Accept returns a queued conn or blocks
// on a channel. Used to verify Listener.Accept's rejection-and-loop behavior
// without a real network stack.
type fakeListener struct {
	in chan net.Conn
}

func newFakeListener() *fakeListener { return &fakeListener{in: make(chan net.Conn, 16)} }
func (l *fakeListener) Accept() (net.Conn, error) {
	c, ok := <-l.in
	if !ok {
		return nil, net.ErrClosed
	}
	return c, nil
}
func (l *fakeListener) Close() error   { close(l.in); return nil }
func (l *fakeListener) Addr() net.Addr { return mustAddrPlain("127.0.0.1:0") }

func mustAddrPlain(s string) net.Addr {
	a, _ := net.ResolveTCPAddr("tcp", s)
	return a
}

// fakeConn is a net.Conn stub that records Close calls.
type fakeConn struct {
	remote net.Addr
	closed atomic.Bool
}

func (c *fakeConn) Read(b []byte) (int, error)       { return 0, io.EOF }
func (c *fakeConn) Write(b []byte) (int, error)      { return len(b), nil }
func (c *fakeConn) Close() error                     { c.closed.Store(true); return nil }
func (c *fakeConn) LocalAddr() net.Addr              { return mustAddrPlain("127.0.0.1:0") }
func (c *fakeConn) RemoteAddr() net.Addr             { return c.remote }
func (c *fakeConn) SetDeadline(time.Time) error      { return nil }
func (c *fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (c *fakeConn) SetWriteDeadline(time.Time) error { return nil }

func TestListener_AcceptDropsRejected(t *testing.T) {
	tr := NewTracker(Config{MaxConnPerIP: 1}, discardLogger(), nil)
	fl := newFakeListener()
	l := NewListener(fl, tr)

	good := &fakeConn{remote: mustAddrPlain("198.51.100.7:1")}
	rejected := &fakeConn{remote: mustAddrPlain("198.51.100.7:2")}
	good2 := &fakeConn{remote: mustAddrPlain("198.51.100.8:1")}

	fl.in <- good
	fl.in <- rejected
	fl.in <- good2

	c1, err := l.Accept()
	if err != nil {
		t.Fatalf("accept 1: %v", err)
	}
	if c1.(*trackedConn).Conn.(*fakeConn) != good {
		t.Fatal("accept 1 should return the good conn")
	}
	// The 2nd should be rejected (cap=1, same IP). Listener loops past it
	// silently and returns good2 next.
	c2, err := l.Accept()
	if err != nil {
		t.Fatalf("accept 2: %v", err)
	}
	if c2.(*trackedConn).Conn.(*fakeConn) != good2 {
		t.Fatal("accept 2 should skip rejected and return good2")
	}
	if !rejected.closed.Load() {
		t.Fatal("rejected conn must be closed by the listener")
	}
}

func TestTrackedConn_CloseReleases(t *testing.T) {
	tr := NewTracker(Config{MaxConnPerIP: 1}, discardLogger(), nil)
	fl := newFakeListener()
	l := NewListener(fl, tr)

	fl.in <- &fakeConn{remote: mustAddrPlain("198.51.100.9:1")}
	c, err := l.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	// Closing must free the per-IP slot.
	_ = c.Close()
	// Double-close must not over-decrement.
	_ = c.Close()

	fl.in <- &fakeConn{remote: mustAddrPlain("198.51.100.9:2")}
	if _, err := l.Accept(); err != nil {
		t.Fatalf("after close, next should succeed: %v", err)
	}
}

func TestDeadlineConn_Clear(t *testing.T) {
	fc := &fakeConn{remote: mustAddrPlain("198.51.100.10:1")}
	dc := WrapWithDeadline(fc, 1*time.Second, nil)
	dc.ClearDeadline()
	dc.ClearDeadline() // idempotent
}

func TestDeadlineConn_FireOnce(t *testing.T) {
	var fired atomic.Int64
	fc := &fakeConn{remote: mustAddrPlain("198.51.100.11:1")}
	dc := WrapWithDeadline(fc, 0, func() { fired.Add(1) })
	dc.FireOnClose()
	dc.FireOnClose()
	_ = dc.Close()
	_ = dc.Close()
	if got := fired.Load(); got != 1 {
		t.Fatalf("onClose should fire exactly once across FireOnClose+Close calls; got %d", got)
	}
	if !fc.closed.Load() {
		t.Fatal("underlying conn must be closed")
	}
}

func TestTracker_Run_StopsOnContextDone(t *testing.T) {
	tr := NewTracker(Config{IdleEvict: 50 * time.Millisecond}, discardLogger(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		tr.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Run did not exit on ctx cancel")
	}
}
