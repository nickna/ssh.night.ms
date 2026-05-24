package netlimit

import (
	"net"
	"sync/atomic"
)

// Listener wraps a net.Listener and enforces per-IP gates via Tracker on every
// Accept. Connections that pass the gates are returned wrapped so Close
// releases the per-IP counter. Connections that fail the gates are closed
// immediately and Accept loops to the next one — the caller never sees them.
//
// The global unauthenticated-handshake cap is NOT enforced here because it
// needs ssh.Context to coordinate release on auth-success; the SSH transport
// applies it in ConnCallback.
type Listener struct {
	net.Listener
	tracker *Tracker
}

// NewListener wraps inner with the Tracker's per-IP gating.
func NewListener(inner net.Listener, t *Tracker) *Listener {
	return &Listener{Listener: inner, tracker: t}
}

// Accept blocks until a connection arrives and passes the per-IP gates, or
// the underlying listener returns an error. Rejected connections are closed
// silently from the caller's perspective; observability lives in the
// Tracker's logger + onReject callback.
func (l *Listener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		release, _, ok := l.tracker.AcquireConn(conn.RemoteAddr())
		if !ok {
			_ = conn.Close()
			continue
		}
		return &trackedConn{Conn: conn, release: release}, nil
	}
}

// trackedConn wraps an accepted net.Conn so the per-IP counter decrements
// exactly once when Close is called — even if the SSH layer calls Close more
// than once (it doesn't today, but defending the invariant is cheap).
type trackedConn struct {
	net.Conn
	release  func()
	released atomic.Bool
}

func (c *trackedConn) Close() error {
	if c.released.CompareAndSwap(false, true) {
		c.release()
	}
	return c.Conn.Close()
}
