package carbonyl

import "io"

// WinSize is the terminal dimensions passed to ioctl(TIOCSWINSZ) on the PTY
// master whenever the SSH client sends a window-change request.
type WinSize struct {
	Rows int
	Cols int
}

// SessionIO is the surface a transport (SSH, ...) implements so this package
// stays free of imports from internal/transport or the wish/ssh modules.
//
// The Runner reads keystrokes from Stdin and pumps them into the PTY master;
// it copies PTY master output into Stdout; carbonyl's own stderr is captured
// separately by the Runner and sent to slog, NOT to this Stderr (which is
// almost always the SSH channel and would garble the screen).
//
// WindowChanges fires once per SSH SIGWINCH; the bridge re-ioctls on each
// value. The channel must NOT be closed until Done is fired (callers can
// safely range over it).
//
// Done fires when the underlying transport disconnects; the bridge tears
// down all goroutines and the child process when this fires.
type SessionIO interface {
	Stdin() io.Reader
	Stdout() io.Writer
	Stderr() io.Writer
	WindowChanges() <-chan WinSize
	Done() <-chan struct{}
}
