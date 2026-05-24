// signup_root.go owns the per-session model for clients that authenticated
// as SignupRequired — the SSH handshake accepted the connection but the
// handle isn't in the users table yet. The model renders the register
// screen first; on a successful CreateAccount it mutates session.Identity
// into the new Known principal and swaps to the normal lobby root.
package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/nickna/ssh.night.ms/internal/auth"
	"github.com/nickna/ssh.night.ms/internal/realtime"
	"github.com/nickna/ssh.night.ms/internal/tui/nav"
	"github.com/nickna/ssh.night.ms/internal/tui/screens"
	"github.com/nickna/ssh.night.ms/internal/tui/session"
)

// SignupRoot wraps the register screen and the post-registration handoff to
// the lobby. Init/Update/View delegate to whichever sub-model is active.
type SignupRoot struct {
	sess     *session.Session
	signup   auth.SignupRequired
	regDeps  auth.RegisterDeps
	presence *realtime.PresenceService
	screen   tea.Model
}

// NewSignupRoot wires the register screen. presence may be nil; if set, the
// heartbeat starts after registration completes — same lifetime hook the
// transport uses for already-Known sessions.
func NewSignupRoot(
	sess *session.Session,
	signup auth.SignupRequired,
	regDeps auth.RegisterDeps,
	presence *realtime.PresenceService,
) tea.Model {
	return &SignupRoot{
		sess:     sess,
		signup:   signup,
		regDeps:  regDeps,
		presence: presence,
		screen:   screens.NewRegister(sess, signup, regDeps),
	}
}

func (m *SignupRoot) Init() tea.Cmd {
	if m.screen == nil {
		return nil
	}
	return m.screen.Init()
}

func (m *SignupRoot) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.sess.Width = msg.Width
		m.sess.Height = msg.Height
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
	case screens.RegisterCompletedMsg:
		// Account live in the DB. Adopt the new identity, start the presence
		// heartbeat (bound to the SSH context so it dies on disconnect), and
		// continue as if we'd authenticated normally.
		m.sess.Identity = msg.Known
		if m.presence != nil && m.sess.SSHContext != nil {
			go m.presence.RunHeartbeat(m.sess.SSHContext, msg.Known.Handle, msg.Known.UserID)
		}
		root := NewRoot(m.sess).(*Root)
		return root, root.Init()
	case nav.NavigateMsg:
		// The register screen only navigates to DestLogout (cancel/Esc).
		if msg.Target == nav.DestLogout {
			return m, tea.Quit
		}
	}
	if m.screen == nil {
		return m, nil
	}
	next, cmd := m.screen.Update(msg)
	m.screen = next
	return m, cmd
}

func (m *SignupRoot) View() string {
	if m.screen == nil {
		return ""
	}
	// Reuse Root's status-bar renderer so the bottom strip is consistent
	// before + after registration. The signup screen's body gets clipped
	// to PTY-height-1 to make room.
	width := m.sess.Width
	if width <= 0 {
		width = 80
	}
	height := m.sess.Height
	if height <= 0 {
		height = 24
	}
	body := clipLines(m.screen.View(), height-1)
	status := (&Root{sess: m.sess}).renderStatusBar(width)
	return body + "\n" + status
}
