package screens

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nickna/ssh.night.ms/internal/auth"
	"github.com/nickna/ssh.night.ms/internal/tui/components"
	"github.com/nickna/ssh.night.ms/internal/tui/nav"
	"github.com/nickna/ssh.night.ms/internal/tui/session"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// RegisterCompletedMsg is emitted on a successful CreateAccount. The
// SignupRoot consumes it to swap into the lobby; downstream screens never
// see it.
type RegisterCompletedMsg struct {
	Known auth.Known
}

// Register is the TOFU signup form. Mirrors src/Night.Ms.SshServer/Tui/
// Screens/RegisterScreen.cs: prefilled handle from the SSH username, password
// + confirm, optional adopt-key checkbox when the client offered a key.
type Register struct {
	sess    *session.Session
	signup  auth.SignupRequired
	regDeps auth.RegisterDeps

	handle  textinput.Model
	pw      textinput.Model
	confirm textinput.Model

	adoptKey      bool
	hasOfferedKey bool

	focus    int // 0=handle, 1=pw, 2=confirm, 3=adopt (if hasOfferedKey), 4=submit, 5=cancel
	maxFocus int
	status   string
	working  bool
}

// NewRegister builds the form. The handle and password fields are pre-
// populated from whatever the user typed at the SSH prompt — saves a
// duplicate keystroke for the common case.
func NewRegister(sess *session.Session, signup auth.SignupRequired, regDeps auth.RegisterDeps) tea.Model {
	hasKey := signup.OfferedFingerprint != "" && len(signup.OfferedBlob) > 0

	h := textinput.New()
	h.Placeholder = "handle"
	h.CharLimit = 32
	h.SetValue(signup.Handle)
	h.Focus()

	p := textinput.New()
	p.Placeholder = "password"
	p.CharLimit = 256
	p.EchoMode = textinput.EchoPassword
	p.SetValue(signup.OfferedPassword)

	c := textinput.New()
	c.Placeholder = "confirm password"
	c.CharLimit = 256
	c.EchoMode = textinput.EchoPassword
	c.SetValue(signup.OfferedPassword)

	max := 5 // handle, pw, confirm, submit, cancel
	if hasKey {
		max = 6 // adds adopt checkbox at index 3
	}

	return &Register{
		sess:          sess,
		signup:        signup,
		regDeps:       regDeps,
		handle:        h,
		pw:            p,
		confirm:       c,
		adoptKey:      hasKey,
		hasOfferedKey: hasKey,
		focus:         0,
		maxFocus:      max - 1,
	}
}

func (m *Register) Init() tea.Cmd { return textinput.Blink }

// submitMsg fires when an Argon2id hash + INSERT round-trip finishes.
type submitMsg struct {
	known auth.Known
	err   error
}

func (m *Register) submitCmd() tea.Cmd {
	if m.working {
		return nil
	}
	handle := strings.TrimSpace(m.handle.Value())
	pw := m.pw.Value()
	confirm := m.confirm.Value()
	if pw != confirm {
		m.status = "[!] Passwords don't match."
		return nil
	}
	m.working = true
	m.status = "working…"
	in := auth.RegisterInput{
		Handle:             handle,
		Password:           pw,
		AdoptKey:           m.adoptKey && m.hasOfferedKey,
		OfferedFingerprint: m.signup.OfferedFingerprint,
		OfferedAlgorithm:   m.signup.OfferedAlgorithm,
		OfferedBlob:        m.signup.OfferedBlob,
	}
	deps := m.regDeps
	return func() tea.Msg {
		// Argon2id takes ~100ms; give a generous timeout so a slow disk write
		// for the user row doesn't fail the obvious success path.
		ctx, cancel := m.sess.CtxWithTimeout(15*time.Second)
		defer cancel()
		known, err := auth.CreateAccount(ctx, deps, in)
		return submitMsg{known: known, err: err}
	}
}

func (m *Register) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case submitMsg:
		m.working = false
		if msg.err != nil {
			m.status = registrationErrorMessage(msg.err, m.regDeps.MinPasswordLength)
			return m, nil
		}
		return m, func() tea.Msg { return RegisterCompletedMsg{Known: msg.known} }

	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			return m, nav.Navigate(nav.DestLogout)
		case "tab", "down":
			m.advanceFocus(+1)
			return m, nil
		case "shift+tab", "up":
			m.advanceFocus(-1)
			return m, nil
		case " ":
			// Toggle the adopt-key checkbox when it owns focus.
			if m.hasOfferedKey && m.focus == 3 {
				m.adoptKey = !m.adoptKey
				return m, nil
			}
		case "enter":
			// Enter submits from any field except cancel.
			cancelFocus := m.maxFocus
			submitFocus := m.maxFocus - 1
			if m.focus == cancelFocus {
				return m, nav.Navigate(nav.DestLogout)
			}
			if m.focus == submitFocus || m.focus < submitFocus {
				return m, m.submitCmd()
			}
		}
	}

	// Forward keystrokes to the focused text input.
	var cmd tea.Cmd
	switch m.focus {
	case 0:
		m.handle, cmd = m.handle.Update(msg)
	case 1:
		m.pw, cmd = m.pw.Update(msg)
	case 2:
		m.confirm, cmd = m.confirm.Update(msg)
	}
	return m, cmd
}

func (m *Register) advanceFocus(delta int) {
	m.focus = (m.focus + delta + m.maxFocus + 1) % (m.maxFocus + 1)
	m.handle.Blur()
	m.pw.Blur()
	m.confirm.Blur()
	switch m.focus {
	case 0:
		m.handle.Focus()
	case 1:
		m.pw.Focus()
	case 2:
		m.confirm.Focus()
	}
}

func registrationErrorMessage(err error, minLen int) string {
	var rerr *auth.RegistrationErr
	if !errors.As(err, &rerr) {
		return "[!] " + err.Error()
	}
	switch rerr.Kind {
	case auth.RegErrHandleInvalid:
		return "[!] Handle must be 3-32 chars: letters, digits, underscore, dash."
	case auth.RegErrPasswordTooShort:
		if minLen <= 0 {
			minLen = 8
		}
		return fmt.Sprintf("[!] Password must be at least %d characters.", minLen)
	case auth.RegErrHandleTaken:
		return "[!] That handle is already taken. Try another."
	case auth.RegErrKeyAlreadyUsed:
		return "[!] That SSH key is already registered to another account. Uncheck 'Adopt the SSH key' to sign up password-only."
	}
	return "[!] Registration failed: " + rerr.Error()
}

var (
	regTitle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorAccent))
	regHint     = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorMuted)).Italic(true)
	regLabel    = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorAccentDim))
	regError    = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorRed))
	regBtn      = lipgloss.NewStyle().Padding(0, 2).Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color(theme.ColorAccentDim))
	regBtnFocus = lipgloss.NewStyle().Padding(0, 2).Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color(theme.ColorAccent)).Bold(true)
)

func (m *Register) View() string {
	if m.sess.Width == 0 || m.sess.Height == 0 {
		return "initializing..."
	}
	var b strings.Builder
	// Banner first — matches the .NET RegisterScreen which paints the same
	// ArtProvider header above the form. Helps brand-new users feel they're
	// in the right place before they pick a handle.
	if m.sess.LoginBanner != nil {
		bn := m.sess.LoginBanner.Banner().Render(components.RenderCellGrid)
		if bn != "" {
			b.WriteString(bn)
			b.WriteString("\n\n")
		}
	}
	b.WriteString(regTitle.Render("ssh.night.ms — create your account"))
	b.WriteString("\n\n")
	if m.signup.OfferedPassword != "" {
		b.WriteString(regHint.Render("  Welcome — we pre-filled your handle and password from the SSH prompt.\n  Review them, pick adopt-key if you'd like passwordless logins, and Register."))
	} else {
		b.WriteString(regHint.Render("  Welcome to ssh.night.ms. Looks like you're new here.\n  Pick a handle and password; if you SSHed in with a key, adopt it for passwordless logins."))
	}
	b.WriteString("\n\n")

	b.WriteString("  " + regLabel.Render("Handle (3-32 chars: letters, digits, _ or -):") + "\n")
	b.WriteString("    " + m.handle.View() + "\n\n")

	minLen := m.regDeps.MinPasswordLength
	if minLen <= 0 {
		minLen = 8
	}
	b.WriteString("  " + regLabel.Render(fmt.Sprintf("Password (min %d chars):", minLen)) + "\n")
	b.WriteString("    " + m.pw.View() + "\n\n")

	b.WriteString("  " + regLabel.Render("Confirm password:") + "\n")
	b.WriteString("    " + m.confirm.View() + "\n\n")

	if m.hasOfferedKey {
		check := "[ ]"
		if m.adoptKey {
			check = "[x]"
		}
		row := fmt.Sprintf("%s Adopt the SSH key I connected with (recommended)", check)
		if m.focus == 3 {
			b.WriteString("  " + regBtnFocus.Render(row) + "\n")
		} else {
			b.WriteString("  " + regBtn.Render(row) + "\n")
		}
		b.WriteString("    " + regHint.Render(fmt.Sprintf("key  %s  %s", m.signup.OfferedAlgorithm, m.signup.OfferedFingerprint)) + "\n\n")
	}

	submit := "Register"
	cancel := "Disconnect"
	submitFocus := m.maxFocus - 1
	if m.focus == submitFocus {
		b.WriteString("  " + regBtnFocus.Render(submit) + "  ")
	} else {
		b.WriteString("  " + regBtn.Render(submit) + "  ")
	}
	if m.focus == m.maxFocus {
		b.WriteString(regBtnFocus.Render(cancel) + "\n\n")
	} else {
		b.WriteString(regBtn.Render(cancel) + "\n\n")
	}

	if m.status != "" {
		if strings.HasPrefix(m.status, "[!]") {
			b.WriteString("  " + regError.Render(m.status) + "\n")
		} else {
			b.WriteString("  " + regHint.Render(m.status) + "\n")
		}
	}
	b.WriteString("\n  " + regHint.Render("Tab/Shift+Tab to move · Enter to submit · Esc to disconnect"))
	return b.String()
}
