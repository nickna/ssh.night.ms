package screens

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nickna/ssh.night.ms/internal/carbonyl"
	"github.com/nickna/ssh.night.ms/internal/tui/nav"
	"github.com/nickna/ssh.night.ms/internal/tui/session"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// Web is the Carbonyl-backed full-browser screen. Distinct from Browser
// (reader-mode) — picked from the lobby's "Web" carousel item, which only
// appears when the session can host Carbonyl (SSH + binary present + kill
// switch on). The screen is intentionally tiny: prompt for a URL, hand off
// to Carbonyl, surface the exit toast, repeat.
type Web struct {
	sess  *session.Session
	input textinput.Model

	// resize chan owned per-launch; nil when not running. WindowSizeMsg
	// forwards into it while richModeActive.
	richModeActive bool
	richResize     chan carbonyl.WinSize

	// status shown above the URL row — usually an error from the last
	// launch (toast was overwhelming for the all-empty screen, so this is
	// a persistent line until the user types again).
	status string
}

// webExitedMsg lands when the Carbonyl child exits. Mirrors carbonylExitedMsg
// in browser.go but kept separate to avoid the two screens sharing message
// types — keeps the screens independently editable.
type webExitedMsg struct {
	url string
	err error
}

func NewWeb(sess *session.Session) tea.Model {
	in := textinput.New()
	in.Placeholder = "https://example.com"
	in.CharLimit = 2048
	in.Focus()
	return &Web{sess: sess, input: in}
}

func (m *Web) Init() tea.Cmd { return textinput.Blink }

func (m *Web) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case webExitedMsg:
		m.richModeActive = false
		if m.richResize != nil {
			close(m.richResize)
			m.richResize = nil
		}
		m.input.Focus()
		if msg.err != nil {
			text := msg.err.Error()
			if i := strings.IndexByte(text, '\n'); i >= 0 {
				text = text[:i]
			}
			m.status = "! " + text
		} else {
			m.status = "exited " + msg.url
		}
		return m, textinput.Blink

	case tea.WindowSizeMsg:
		if m.richModeActive && m.richResize != nil {
			select {
			case m.richResize <- carbonyl.WinSize{Rows: msg.Height, Cols: msg.Width}:
			default:
			}
		}
		return m, nil

	case tea.KeyMsg:
		if m.richModeActive {
			// Drop key events while Carbonyl owns the terminal — the
			// bubbletea read loop is released during launch, so we shouldn't
			// see any. Defensive only.
			return m, nil
		}
		switch msg.String() {
		case "esc":
			return m, nav.Navigate(nav.DestLobby)
		case "enter":
			return m, m.launch()
		}
	}

	if !m.richModeActive {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *Web) launch() tea.Cmd {
	raw := strings.TrimSpace(m.input.Value())
	if raw == "" {
		m.status = "type a URL first"
		return nil
	}
	// Bare host shortcut: "example.com" → "https://example.com". URLs the
	// user types from memory rarely start with the scheme.
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	if err := carbonyl.ValidateURL(raw); err != nil {
		m.status = "! " + err.Error()
		return nil
	}
	// These four conditions are also gated at the lobby (the Web item only
	// appears when they're true), but a stale session could land here with
	// them flipped — surface the same explanation rather than crashing.
	if !m.sess.IsSSH {
		m.status = "rich mode requires SSH (web terminal can't host it)"
		return nil
	}
	if m.sess.Settings == nil || !m.sess.Settings.Get().CarbonylEnabled {
		m.status = "rich mode is disabled (set carbonyl_enabled=true in the sysop tab)"
		return nil
	}
	if m.sess.LaunchCarbonyl == nil {
		m.status = "rich mode unavailable: carbonyl binary missing on the server"
		return nil
	}
	if m.sess.TeaProgram == nil {
		m.status = "internal: tea program reference missing"
		return nil
	}

	m.richResize = make(chan carbonyl.WinSize, 4)
	m.richModeActive = true
	m.status = ""

	url := raw
	prog := m.sess.TeaProgram
	launch := m.sess.LaunchCarbonyl
	ctx := m.sess.Ctx()
	cols := m.sess.Width
	rows := m.sess.Height
	uid := m.sess.Identity.UserID
	handle := m.sess.Identity.Handle
	resizeCh := m.richResize

	return func() tea.Msg {
		if err := prog.ReleaseTerminal(); err != nil {
			return webExitedMsg{url: url, err: fmt.Errorf("release terminal: %w", err)}
		}
		req := carbonyl.LaunchRequest{
			URL:         url,
			UserID:      uid,
			Handle:      handle,
			InitialCols: cols,
			InitialRows: rows,
		}
		launchErr := launch(ctx, req, resizeCh)
		restoreErr := prog.RestoreTerminal()
		// Carbonyl leaves alt-screen + cursor-hide + mouse-tracking SGR on
		// exit; wipe the terminal state so the resumed bubbletea repaint
		// lands cleanly.
		if prog != nil {
			prog.Printf("\x1b[?1049l\x1b[?25h\x1b[?1000l\x1b[?1006l")
		}
		if launchErr != nil {
			return webExitedMsg{url: url, err: launchErr}
		}
		if restoreErr != nil {
			return webExitedMsg{url: url, err: fmt.Errorf("restore terminal: %w", restoreErr)}
		}
		return webExitedMsg{url: url}
	}
}

var (
	webTitle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorAccent))
	webHint   = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorMuted)).Italic(true)
	webPrompt = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorCyan))
	webErr    = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorRed))
	webNote   = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorYellow))
)

func (m *Web) View() string {
	if m.sess.Width == 0 || m.sess.Height == 0 {
		return "initializing..."
	}
	if m.richModeActive {
		// Bubbletea redraws over this whenever it can, but during the
		// ReleaseTerminal window the screen is fully owned by Carbonyl
		// — this string is only seen for the brief moment between
		// "Enter pressed" and "ReleaseTerminal returns".
		return webHint.Render("launching full browser…")
	}

	var b strings.Builder
	b.WriteString(webTitle.Render("Web"))
	b.WriteString("  ")
	b.WriteString(webHint.Render("full Chromium-based browser"))
	b.WriteString("\n\n")

	if m.status != "" {
		style := webNote
		if strings.HasPrefix(m.status, "!") {
			style = webErr
		}
		b.WriteString(style.Render(m.status))
		b.WriteString("\n\n")
	}

	b.WriteString(webPrompt.Render("URL"))
	b.WriteString("  ")
	b.WriteString(m.input.View())
	b.WriteString("\n\n")
	b.WriteString(webHint.Render("Enter to load  ·  Esc lobby"))
	b.WriteString("\n\n")
	// Pre-launch reassurance — visible every time the user lands here, so the
	// exit key is known before they ever enter the full-screen browser.
	b.WriteString(webNote.Render("While in the browser:"))
	b.WriteString("\n  ")
	b.WriteString(webHint.Render("Ctrl+C   exits Carbonyl, returns to this screen (SSH stays connected)"))
	b.WriteString("\n  ")
	b.WriteString(webHint.Render("Ctrl+\\   emergency exit — intercepted before Carbonyl sees it"))
	return b.String()
}
