package netlimit

import (
	"net"
	"sync"
	"time"
)

// DeadlineConn wraps a net.Conn with a deadline that can be cleared once
// authentication completes. Used to enforce a LoginGraceTime — the deadline
// is set when the conn is first wrapped (before any handshake bytes flow)
// and cleared by the auth callback on a successful Known / SignupRequired
// decision. If auth never completes, the deadline fires and the SSH
// handshake reads error out.
//
// DeadlineConn also carries an optional onClose hook which runs exactly once
// at Close time. Transport code uses this to release the global
// unauthenticated-handshake slot acquired in ConnCallback — releasing on
// auth-success and again on conn-close, with the underlying release closure's
// own atomic guard collapsing the duplicate into a single decrement.
type DeadlineConn struct {
	net.Conn

	mu       sync.Mutex
	cleared  bool
	onClose  func()
	fired    bool
}

// WrapWithDeadline returns a *DeadlineConn that has SetDeadline(now+grace)
// already applied. A zero grace skips the deadline (caller wants the
// onClose plumbing without timeout enforcement). onClose may be nil.
func WrapWithDeadline(conn net.Conn, grace time.Duration, onClose func()) *DeadlineConn {
	dc := &DeadlineConn{Conn: conn, onClose: onClose}
	if grace > 0 {
		_ = conn.SetDeadline(time.Now().Add(grace))
	}
	return dc
}

// ClearDeadline removes the handshake deadline. Safe to call concurrently;
// idempotent — repeated calls after the first are no-ops.
func (c *DeadlineConn) ClearDeadline() {
	c.mu.Lock()
	already := c.cleared
	c.cleared = true
	c.mu.Unlock()
	if already {
		return
	}
	_ = c.Conn.SetDeadline(time.Time{})
}

// Close fires the onClose hook (if any, and not already fired) and then
// closes the underlying conn. Single-shot: a second Close still propagates
// to the underlying conn but doesn't re-fire onClose.
func (c *DeadlineConn) Close() error {
	c.fireOnce()
	return c.Conn.Close()
}

// FireOnClose runs the onClose hook now if it hasn't run yet. Used by the
// auth-success path to release the global handshake slot before the conn
// closes, so the slot is freed for the next handshake immediately rather
// than at session end.
func (c *DeadlineConn) FireOnClose() {
	c.fireOnce()
}

func (c *DeadlineConn) fireOnce() {
	c.mu.Lock()
	if c.fired || c.onClose == nil {
		c.mu.Unlock()
		return
	}
	c.fired = true
	fn := c.onClose
	c.mu.Unlock()
	fn()
}
