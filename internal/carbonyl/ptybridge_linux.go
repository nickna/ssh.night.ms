//go:build linux

package carbonyl

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

// bridgePTY runs three goroutines wiring the OS PTY master to the SSH session
// for the lifetime of the child process:
//
//	sess.Stdin()  --> master    (user keystrokes into the child)
//	master       --> sess.Stdout (rendered frames out to the client)
//	sess.WindowChanges() --> ioctl(TIOCSWINSZ, master) (SIGWINCH propagation)
//
// Returns when ctx is done OR the master read returns EOF (= child exited
// cleanly OR was killed and the kernel released the pty). The caller is
// responsible for closing master after we return.
//
// Both copy loops swallow io.EOF and net.ErrClosed-style errors silently —
// those are the normal teardown path. Other errors are logged at debug.
func bridgePTY(ctx context.Context, master *os.File, sess SessionIO, logger *slog.Logger) error {
	var wg sync.WaitGroup
	// Use one inner context: cancelling it on the first goroutine exit makes
	// the others wake up immediately (instead of waiting for their own EOF).
	bridgeCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// stdin -> master. When the SSH connection closes, sess.Stdin() returns
	// io.EOF and the copy unblocks naturally. When the child exits and we
	// close master, the read also unblocks.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()
		_, err := io.Copy(master, sess.Stdin())
		if err != nil && !isClosedError(err) {
			logger.Debug("carbonyl bridge: stdin copy ended", "err", err)
		}
	}()

	// master -> stdout. EOF here means the child exited; that's the signal
	// for the whole bridge to wind down.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()
		_, err := io.Copy(sess.Stdout(), master)
		if err != nil && !isClosedError(err) {
			logger.Debug("carbonyl bridge: stdout copy ended", "err", err)
		}
	}()

	// Window-change forwarder. Per-event ioctl is cheap; if it fails (master
	// closed mid-call) we just exit the goroutine.
	wg.Add(1)
	go func() {
		defer wg.Done()
		winCh := sess.WindowChanges()
		for {
			select {
			case <-bridgeCtx.Done():
				return
			case <-sess.Done():
				return
			case win, ok := <-winCh:
				if !ok {
					return
				}
				if err := unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{
					Row: uint16(win.Rows),
					Col: uint16(win.Cols),
				}); err != nil {
					logger.Debug("carbonyl bridge: TIOCSWINSZ failed", "err", err)
					return
				}
			}
		}
	}()

	// Wait for either ctx cancel or both copy loops to drain. When ctx is
	// cancelled we close master to unblock any goroutine still in a syscall.
	go func() {
		<-bridgeCtx.Done()
		// Closing master races with the copy goroutines; that's fine — their
		// Read/Write returns err, isClosedError swallows it, they call Done().
		_ = master.Close()
	}()

	wg.Wait()
	return nil
}

// isClosedError returns true for the io.EOF + "use of closed network
// connection"-shaped errors that show up on every clean teardown. We don't
// want those at WARN — they're expected.
func isClosedError(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, context.Canceled) {
		return true
	}
	// os.PathError("use of closed file") on closed *os.File.
	if errors.Is(err, os.ErrClosed) {
		return true
	}
	return false
}
