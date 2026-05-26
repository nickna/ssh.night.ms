package transport

import (
	"io"

	"github.com/charmbracelet/ssh"

	"github.com/nickna/ssh.night.ms/internal/carbonyl"
)

// sshSessionIO implements carbonyl.SessionIO over a wish ssh.Session.
//
// The wish bubbletea middleware already drains the SSH SIGWINCH channel and
// turns each event into a tea.WindowSizeMsg sent to the program, so we can't
// also drain the same channel here without one or the other missing events.
// Instead the screen forwards WindowSizeMsg into the `resizes` chan it owns,
// which we hand back here from WindowChanges(). That keeps the SIGWINCH path
// authoritative through bubbletea while the bridge still sees every resize.
type sshSessionIO struct {
	sess    ssh.Session
	resizes <-chan carbonyl.WinSize
}

// newSSHSessionIO is constructed inside the launcher closure that the
// programHandler installs onto session.Session. The resizes chan is the
// per-launch screen-owned channel.
func newSSHSessionIO(sess ssh.Session, resizes <-chan carbonyl.WinSize) *sshSessionIO {
	return &sshSessionIO{sess: sess, resizes: resizes}
}

// Stdin returns the SSH channel as an io.Reader. wish's ssh.Session embeds
// gossh.Channel which is itself an io.ReadWriteCloser.
func (s *sshSessionIO) Stdin() io.Reader { return s.sess }

// Stdout returns the SSH channel as an io.Writer.
func (s *sshSessionIO) Stdout() io.Writer { return s.sess }

// Stderr returns the SSH stderr stream. Carbonyl's actual stderr is captured
// in slog by the Runner (see drainStderr); this method exists for SessionIO
// conformance and isn't read on the hot path.
func (s *sshSessionIO) Stderr() io.Writer { return s.sess.Stderr() }

func (s *sshSessionIO) WindowChanges() <-chan carbonyl.WinSize { return s.resizes }

func (s *sshSessionIO) Done() <-chan struct{} { return s.sess.Context().Done() }
