// Package carbonyl spawns and supervises a per-session Carbonyl child
// process (a Chromium-based terminal browser) attached to a local OS PTY
// whose master end is bridged to the user's SSH channel.
//
// Carbonyl needs a real TTY (it calls isatty + tcgetattr), so the SSH
// channel can't be its stdio directly — we allocate a PTY pair via
// creack/pty, give the slave to the child, and run a bridge that pumps
// bytes between the master and the SessionIO supplied by the caller.
//
// Concurrency caps + URL policy + per-user persistent profile dir are all
// applied here so the screen layer stays a thin "press R" controller.
package carbonyl

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// Config configures the process-singleton Runner. BinPath must point to the
// extracted Carbonyl `carbonyl` binary (formerly `headless_shell`) inside
// /opt/carbonyl. DataRoot is the parent dir under which per-user profile
// subdirs are created on demand.
type Config struct {
	BinPath  string
	DataRoot string
	Logger   *slog.Logger
	Limits   Limits
}

// ErrBinaryMissing is returned from New when BinPath doesn't exist or isn't
// executable. Callers should treat it as soft — the BBS keeps booting, rich
// mode just stays disabled.
var ErrBinaryMissing = errors.New("carbonyl binary not found")

// Runner is the process-singleton that all rich-mode launches go through.
// Safe for concurrent use; Launch acquires its own limits + spawns its own
// pty/child/bridge with no shared mutable state across launches.
type Runner struct {
	binPath  string
	dataRoot string
	logger   *slog.Logger
	tokens   *tokens
}

// New constructs the Runner. Returns (nil, ErrBinaryMissing) when the binary
// is absent — the composition root in main.go uses this as the disable
// signal, logging a warn line and continuing with carbonyl set to nil on the
// session bag.
func New(cfg Config) (*Runner, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	st, err := os.Stat(cfg.BinPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrBinaryMissing, cfg.BinPath, err)
	}
	if st.IsDir() || st.Mode().Perm()&0o111 == 0 {
		return nil, fmt.Errorf("%w: %s is not executable", ErrBinaryMissing, cfg.BinPath)
	}
	if cfg.DataRoot == "" {
		return nil, errors.New("carbonyl: DataRoot must be set")
	}
	if err := os.MkdirAll(cfg.DataRoot, 0o700); err != nil {
		return nil, fmt.Errorf("carbonyl: create DataRoot: %w", err)
	}
	return &Runner{
		binPath:  cfg.BinPath,
		dataRoot: cfg.DataRoot,
		logger:   cfg.Logger,
		tokens:   newTokens(cfg.Limits),
	}, nil
}

// UpdateLimits swaps the active concurrency caps. Wired to the settings cache
// OnChange hook in main.go so a sysop tuning carbonyl_max_global etc. takes
// effect on the next Launch without a restart.
func (r *Runner) UpdateLimits(next Limits) {
	r.tokens.updateLimits(next)
}

// LaunchError is returned to the caller. Reason identifies the gate that
// rejected the launch (or empty if the child started but exited with an
// error). Err holds the underlying error when relevant.
type LaunchError struct {
	Reason RejectReason
	Err    error
}

func (e *LaunchError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("carbonyl: %s: %v", e.Reason, e.Err)
	}
	return fmt.Sprintf("carbonyl: %s", e.Reason)
}

func (e *LaunchError) Unwrap() error { return e.Err }

// Launch acquires a concurrency slot, allocates a PTY, forks Carbonyl with
// stdio on the slave + stderr captured separately, runs the bridge between
// master and req.SessionRef, and waits for the child. Returns when the child
// exits or the supplied ctx fires (which kills the child).
//
// Blocks for the duration of the rich-mode session. The screen-side caller
// runs this from inside its tea.Cmd so the bubbletea program is paused for
// the duration (see browser screen launchCarbonylCmd).
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

	args := buildArgs(req, profile, cols, rows)
	cmd := exec.CommandContext(ctx, r.binPath, args...)
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

	bridgeErr := bridgePTY(ctx, master, req.SessionRef, r.logger)

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

// Stats returns the current concurrency snapshot for sysop debugging.
func (r *Runner) Stats() (global int, perIP map[string]int, perHandle map[int64]int) {
	return r.tokens.snapshot()
}

// userDataDir resolves <root>/<userID>/ and creates it with 0700 if missing.
// The dir is the Chromium --user-data-dir; persistent across launches so
// cookies and logins survive the user disconnecting and reconnecting.
func userDataDir(root string, userID int64) (string, error) {
	if userID <= 0 {
		return "", errors.New("invalid userID")
	}
	dir := filepath.Join(root, strconv.FormatInt(userID, 10))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// stderrBuffer keeps the last few KiB of carbonyl's stderr so a non-zero exit
// can surface a meaningful error to the caller. Bounded to avoid retaining
// megabytes of GPU warnings.
type stderrBuffer struct {
	mu  sync.Mutex
	buf []byte
}

const stderrTailMax = 4 * 1024

func (b *stderrBuffer) append(line string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, line...)
	b.buf = append(b.buf, '\n')
	if len(b.buf) > stderrTailMax {
		b.buf = b.buf[len(b.buf)-stderrTailMax:]
	}
}

func (b *stderrBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

func drainStderr(r io.Reader, logger *slog.Logger, tail *stderrBuffer) {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 8*1024), 64*1024)
	for s.Scan() {
		line := s.Text()
		tail.append(line)
		logger.Warn("carbonyl stderr", "line", line)
	}
}
