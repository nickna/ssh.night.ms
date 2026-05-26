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
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

// Launch is split per-OS: the Linux implementation in runner_linux.go does
// the actual PTY + fork; runner_other.go provides a stub for other OSes
// (rich mode is Linux-only because (a) we ship a Linux Chromium binary and
// (b) the syscall surface — setsid/setctty/TIOCSWINSZ — is Unix-specific).
// The cross-platform consumers in cmd/nightms and internal/transport still
// compile, they just get a LaunchError back if anyone hits the path.

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
