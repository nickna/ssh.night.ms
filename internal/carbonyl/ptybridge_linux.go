//go:build linux

package carbonyl

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// escChord is the single byte that triggers the emergency exit out of rich
// mode. Ctrl+\ in raw mode is 0x1c (the ASCII FS — "file separator" — control
// code). Chosen because (a) browsers never bind it for anything, (b) it's not
// a default SSH escape, (c) it's a single byte so the intercept is trivial,
// and (d) the user can hit it without remembering a chord sequence. The byte
// is consumed by the bridge and never forwarded to Carbonyl.
const escChord byte = 0x1c

// escByte / quitKey form the discoverable two-key exit chord: press ESC, then
// 'q' within escQTimeout. The window is what separates the chord from "user
// hit ESC to close a dialog and is now typing 'q' as page input" — at human
// typing speed ESC→q as an intentional chord is well under 500ms, while
// "ESC then later q" is normally seconds apart.
//
// ESC alone can't be the exit key because Carbonyl needs every ESC byte for
// browser navigation: it's the prefix of every arrow / function / alt-key
// sequence and the close gesture for menus, fullscreen, and find-in-page.
const (
	escByte     byte          = 0x1b
	quitKey     byte          = 'q'
	escQTimeout time.Duration = 500 * time.Millisecond
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
// killChild is invoked when we detect the user pressed escChord (Ctrl+\) —
// the chord byte is consumed before reaching Carbonyl, and killChild cancels
// the parent launch context so exec.CommandContext SIGKILLs the child without
// waiting for it to notice EOF on its stdin (Chromium has many threads and
// stdin-EOF isn't a reliable shutdown signal).
//
// Both copy loops swallow io.EOF and net.ErrClosed-style errors silently —
// those are the normal teardown path. Other errors are logged at debug.
func bridgePTY(ctx context.Context, master *os.File, sess SessionIO, logger *slog.Logger, killChild func()) error {
	var wg sync.WaitGroup
	// Use one inner context: cancelling it on the first goroutine exit makes
	// the others wake up immediately (instead of waiting for their own EOF).
	bridgeCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// stdin -> master. Custom copier (not io.Copy) so we can scan each chunk
	// for the escChord byte and trigger an emergency exit before Carbonyl
	// sees it. The common case (no chord) writes the whole buffer once and
	// is functionally identical to io.Copy.
	//
	// When the SSH connection closes, sess.Stdin() returns io.EOF and the
	// loop unblocks. When the child exits and we close master, the write
	// errors and unblocks.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()
		buf := make([]byte, 4096)
		// pendingESCAt is non-zero when the previous read ended with a bare
		// ESC byte that we forwarded. If the very next read begins with 'q'
		// within escQTimeout, the user typed the ESC→Q chord across two
		// reads and we exit. After the timeout the trailing ESC is treated
		// as an ordinary keystroke again.
		var pendingESCAt time.Time
		for {
			n, err := sess.Stdin().Read(buf)
			if n > 0 {
				// Cross-read ESC→Q: previous read ended in ESC (already
				// forwarded), this one starts with 'q' inside the window.
				// Drop the 'q' and exit; the stray ESC the browser saw is
				// harmless (closes a dialog at worst).
				if !pendingESCAt.IsZero() && time.Since(pendingESCAt) <= escQTimeout && buf[0] == quitKey {
					logger.Info("carbonyl bridge: esc-q exit requested")
					if killChild != nil {
						killChild()
					}
					return
				}
				pendingESCAt = time.Time{}

				if i := bytes.IndexByte(buf[:n], escChord); i >= 0 {
					// Write everything up to the chord (so a paste containing
					// 0x1c mid-buffer still delivers the leading bytes), drop
					// the byte itself, and signal the parent to SIGKILL the
					// child. The defer cancel() winds down the other
					// goroutines; killChild() makes cmd.Wait() return promptly.
					if i > 0 {
						_, _ = master.Write(buf[:i])
					}
					logger.Info("carbonyl bridge: ctrl-\\ emergency exit requested")
					if killChild != nil {
						killChild()
					}
					return
				}

				// In-buffer ESC q (both bytes arrived in the same read —
				// typical for a fast typist or a paste).
				if i := bytes.Index(buf[:n], []byte{escByte, quitKey}); i >= 0 {
					if i > 0 {
						_, _ = master.Write(buf[:i])
					}
					logger.Info("carbonyl bridge: esc-q exit requested")
					if killChild != nil {
						killChild()
					}
					return
				}

				// Forward the buffer, then remember whether the tail was a
				// bare ESC so the next read can complete the chord.
				if _, werr := master.Write(buf[:n]); werr != nil {
					if !isClosedError(werr) {
						logger.Debug("carbonyl bridge: stdin write ended", "err", werr)
					}
					return
				}
				if buf[n-1] == escByte {
					pendingESCAt = time.Now()
				}
			}
			if err != nil {
				if !isClosedError(err) {
					logger.Debug("carbonyl bridge: stdin read ended", "err", err)
				}
				return
			}
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
