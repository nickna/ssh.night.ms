//go:build linux

package carbonyl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// Launch acquires a concurrency slot, allocates a PTY, forks Carbonyl with
// stdio on the slave + stderr captured separately, runs the bridge between
// master and req.SessionRef, and waits for the child. Returns when the child
// exits or the supplied ctx fires (which kills the child).
//
// Blocks for the duration of the rich-mode session. The screen-side caller
// runs this from inside its tea.Cmd so the bubbletea program is paused for
// the duration (see browser screen launchCarbonylCmd).
//
// Linux-only: uses syscall.SysProcAttr fields (Setsid/Setctty/Ctty) and
// unix.IoctlSetWinsize that don't exist on Windows. The cross-platform
// stub in runner_other.go returns a clean LaunchError on other OSes.
func (r *Runner) Launch(ctx context.Context, req LaunchRequest) error {
	if req.SessionRef == nil {
		return &LaunchError{Reason: RejectInternal, Err: errors.New("nil SessionRef")}
	}
	if err := ValidateURL(req.URL); err != nil {
		return &LaunchError{Reason: RejectBadURL, Err: err}
	}

	release, reason, ok := r.tokens.Acquire(req.RemoteIP, req.UserID)
	if !ok {
		return &LaunchError{Reason: reason}
	}
	defer release()

	profile, err := userDataDir(r.dataRoot, req.UserID)
	if err != nil {
		return &LaunchError{Reason: RejectInternal, Err: err}
	}

	cols := req.InitialCols
	rows := req.InitialRows
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}

	master, slave, err := pty.Open()
	if err != nil {
		return &LaunchError{Reason: RejectInternal, Err: fmt.Errorf("pty open: %w", err)}
	}
	// Set initial winsize before fork so the child's first stty/ioctl sees
	// the right value.
	_ = unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{
		Row: uint16(rows),
		Col: uint16(cols),
	})

	// Wrap the caller's ctx so the bridge can SIGKILL the child on the
	// Ctrl+\ emergency-exit chord without affecting the outer SSH-session
	// ctx (which would tear down more than we want — the SSH session
	// itself).
	launchCtx, cancelLaunch := context.WithCancel(ctx)
	defer cancelLaunch()

	args := buildArgs(req, profile, cols, rows)
	cmd := exec.CommandContext(launchCtx, r.binPath, args...)
	cmd.Stdin = slave
	cmd.Stdout = slave
	// Capture stderr separately so Carbonyl's GPU/sandbox warnings go to
	// slog, not the SSH terminal (where they'd corrupt the rendered frame).
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = master.Close()
		_ = slave.Close()
		return &LaunchError{Reason: RejectInternal, Err: fmt.Errorf("stderr pipe: %w", err)}
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
		// Ctty is an index into the cmd's ExtraFiles+stdio fd list; 0/1/2 are
		// stdin/stdout/stderr (= the slave fd inside the child). Picking 0
		// works because Stdin == slave above.
		Ctty: 0,
	}

	// Set TERM so Carbonyl/Chromium select sane defaults. We don't propagate
	// the user's $TERM because the host PTY may not match the SSH-negotiated
	// one (creack/pty doesn't surface it). xterm-256color is what most
	// modern terminals emulate and what Carbonyl is tuned for.
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"COLUMNS="+strconv.Itoa(cols),
		"LINES="+strconv.Itoa(rows),
	)

	if err := cmd.Start(); err != nil {
		_ = master.Close()
		_ = slave.Close()
		return &LaunchError{Reason: RejectInternal, Err: fmt.Errorf("start: %w", err)}
	}
	// Slave fd is duped into the child; the parent doesn't need it. Close
	// here so the child's exit causes EOF on the master.
	_ = slave.Close()

	r.logger.Info("carbonyl: launched",
		"handle", req.Handle, "url", req.URL, "pid", cmd.Process.Pid,
		"cols", cols, "rows", rows)

	// Drain stderr into slog in the background. Bounded by carbonyl's own
	// output rate; bufio.Scanner caps lines at 64 KiB which is way more than
	// any sane log line.
	var stderrTail stderrBuffer
	var wgStderr sync.WaitGroup
	wgStderr.Add(1)
	go func() {
		defer wgStderr.Done()
		drainStderr(stderr, r.logger.With("handle", req.Handle, "url", req.URL), &stderrTail)
	}()

	bridgeErr := bridgePTY(launchCtx, master, req.SessionRef, r.logger, cancelLaunch)

	// Whatever bridgePTY returns, the child should be done shortly after —
	// either because it exited (and that's why bridgePTY returned) or because
	// we want it dead (ctx cancelled, SSH dropped). CommandContext already
	// handles ctx-driven kill; we just wait.
	waitErr := cmd.Wait()
	wgStderr.Wait()

	r.logger.Info("carbonyl: exited",
		"handle", req.Handle, "pid", cmd.Process.Pid,
		"exit_err", waitErr, "bridge_err", bridgeErr)

	if waitErr != nil {
		// Surface the captured stderr tail in the error so the toast can show
		// the actual reason instead of just "exit status 1".
		tail := stderrTail.String()
		if tail != "" {
			return &LaunchError{Reason: RejectInternal, Err: fmt.Errorf("%w\n%s", waitErr, tail)}
		}
		return &LaunchError{Reason: RejectInternal, Err: waitErr}
	}
	return nil
}
