package screens

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nickna/ssh.night.ms/internal/tui/components"
	"github.com/nickna/ssh.night.ms/internal/tui/nav"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

func (m *Profile) handleSettingsTabKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		return m, nav.Navigate(nav.DestLobby)
	case "ctrl+t":
		m.mode = modeTabProfile
		m.focusIndex = 0
		m.applyFocus()
		return m, nil
	case "ctrl+s":
		return m, m.submitProfile()
	case "tab":
		m.focusIndex = (m.focusIndex + 1) % settingsTabStops
		m.applyFocus()
	case "shift+tab":
		m.focusIndex = (m.focusIndex - 1 + settingsTabStops) % settingsTabStops
		m.applyFocus()
	case " ", "space":
		switch m.focusIndex {
		case 0:
			m.suppressKeys.Toggle()
		case 1:
			return m, m.handleRequireSshToggle()
		}
	case "enter":
		switch m.focusIndex {
		case 0:
			m.suppressKeys.Toggle()
		case 1:
			return m, m.handleRequireSshToggle()
		case 2:
			return m, m.submitProfile()
		}
	}
	return m, nil
}

// handleRequireSshToggle is a wrapper that adds the lockout guard when the
// user tries to flip RequireSshKey from off → on. Flipping off is always
// safe; flipping on with 0 keys refuses with a notice, with 1 key pops the
// confirmation modal.
func (m *Profile) handleRequireSshToggle() tea.Cmd {
	if m.requireSsh.Checked {
		// Currently on → toggling off, no guard needed.
		m.requireSsh.Toggle()
		return nil
	}
	if len(m.keys) == 0 {
		m.notice = "add an SSH key first (on the web) before enabling passwordless login."
		m.noticeKind = "err"
		return nil
	}
	if len(m.keys) == 1 {
		// Pop confirm modal.
		m.confirm = components.NewConfirm(
			"single point of failure",
			"only one SSH key is registered. If you lose this key, only a sysop can restore access. Enable anyway?",
		)
		m.confirmKind = "requireSsh"
		m.previousMode = modeTabSettings
		m.mode = modeConfirm
		return nil
	}
	// Multiple keys — flip without prompt.
	m.requireSsh.Toggle()
	return nil
}

func (m *Profile) viewSettingsTab() string {
	header := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.ColorAccentDim)).Italic(true).
		Render("toggles that affect login behaviour and prompts.")

	suppressHint := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.ColorDim)).Italic(true).Padding(0, 0, 0, 4).
		Render("when on, you won't be prompted to add unknown SSH keys you log in with.")
	requireHint := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.ColorDim)).Italic(true).Padding(0, 0, 0, 4).
		Render("forces SSH-key-only login; password auth is refused. If you lose every key,\nonly a sysop can restore access.")

	saveBtn := m.styleButton("save settings", m.focusIndex == 2)

	body := lipgloss.JoinVertical(lipgloss.Left,
		header,
		"",
		"  "+m.suppressKeys.View(),
		suppressHint,
		"",
		"  "+m.requireSsh.View(),
		requireHint,
		"",
		"  "+saveBtn,
	)
	return body
}
