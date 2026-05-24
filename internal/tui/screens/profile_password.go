package screens

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nickna/ssh.night.ms/internal/realtime"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// openPassword switches the profile screen into the password-change modal.
// Builds fresh textinput models so a previously-cancelled modal can't leak
// echoed characters into the new attempt.
func (m *Profile) openPassword() tea.Cmd {
	m.previousMode = m.mode
	m.mode = modePassword
	m.pwErr = ""
	m.pwFocusIndex = 0
	m.pwCurrent = textinput.New()
	m.pwCurrent.EchoMode = textinput.EchoPassword
	m.pwCurrent.EchoCharacter = '•'
	m.pwCurrent.Width = 32
	m.pwCurrent.CharLimit = 128
	m.pwNew = textinput.New()
	m.pwNew.EchoMode = textinput.EchoPassword
	m.pwNew.EchoCharacter = '•'
	m.pwNew.Width = 32
	m.pwNew.CharLimit = 128
	m.pwConfirm = textinput.New()
	m.pwConfirm.EchoMode = textinput.EchoPassword
	m.pwConfirm.EchoCharacter = '•'
	m.pwConfirm.Width = 32
	m.pwConfirm.CharLimit = 128
	m.applyPasswordFocus()
	return textinput.Blink
}

func (m *Profile) applyPasswordFocus() {
	m.pwCurrent.Blur()
	m.pwNew.Blur()
	m.pwConfirm.Blur()
	hasPw := m.snap != nil && m.snap.HasPassword
	switch m.pwFocusIndex {
	case 0:
		if hasPw {
			m.pwCurrent.Focus()
		} else {
			m.pwNew.Focus()
		}
	case 1:
		if hasPw {
			m.pwNew.Focus()
		} else {
			m.pwConfirm.Focus()
		}
	case 2:
		if hasPw {
			m.pwConfirm.Focus()
		}
		// Save button — no widget focus.
	}
}

func (m *Profile) handlePasswordKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	hasPw := m.snap != nil && m.snap.HasPassword
	stops := 4
	if !hasPw {
		stops = 3 // skip current-password field
	}
	switch k.String() {
	case "esc":
		m.mode = m.previousMode
		m.pwErr = ""
		return m, nil
	case "ctrl+s":
		return m, m.submitPassword()
	case "tab":
		m.pwFocusIndex = (m.pwFocusIndex + 1) % stops
		m.applyPasswordFocus()
		return m, textinput.Blink
	case "shift+tab":
		m.pwFocusIndex = (m.pwFocusIndex - 1 + stops) % stops
		m.applyPasswordFocus()
		return m, textinput.Blink
	case "enter":
		// Last focus stop is the Save button.
		if m.pwFocusIndex == stops-1 {
			return m, m.submitPassword()
		}
		m.pwFocusIndex = (m.pwFocusIndex + 1) % stops
		m.applyPasswordFocus()
		return m, textinput.Blink
	}
	var cmd tea.Cmd
	switch m.pwFocusIndex {
	case 0:
		if hasPw {
			m.pwCurrent, cmd = m.pwCurrent.Update(k)
		} else {
			m.pwNew, cmd = m.pwNew.Update(k)
		}
	case 1:
		if hasPw {
			m.pwNew, cmd = m.pwNew.Update(k)
		} else {
			m.pwConfirm, cmd = m.pwConfirm.Update(k)
		}
	case 2:
		if hasPw {
			m.pwConfirm, cmd = m.pwConfirm.Update(k)
		}
	}
	return m, cmd
}

func (m *Profile) submitPassword() tea.Cmd {
	if m.snap == nil || m.working {
		return nil
	}
	hasPw := m.snap.HasPassword
	newPw := m.pwNew.Value()
	confirm := m.pwConfirm.Value()
	current := m.pwCurrent.Value()

	if len(newPw) < realtime.MinPasswordLength {
		m.pwErr = fmt.Sprintf("new password must be at least %d characters.", realtime.MinPasswordLength)
		return nil
	}
	if newPw != confirm {
		m.pwErr = "new password and confirmation do not match."
		return nil
	}

	hasher := m.sess.Hasher
	userID := m.sess.Identity.UserID
	svc := m.sess.Profile

	// Snapshot of existing hash for verify; avoid sending the textinput
	// values into the goroutine where they could outlive the modal.
	storedHash := []byte(nil)
	storedAlgo := ""
	if hasPw {
		row, err := m.sess.Queries.GetUserByID(m.sess.Ctx(), userID)
		if err == nil {
			storedHash = row.PasswordHash
			if row.PasswordAlgo != nil {
				storedAlgo = *row.PasswordAlgo
			}
		} else {
			m.pwErr = "could not verify current password: " + err.Error()
			return nil
		}
	}

	m.working = true
	m.pwErr = ""
	return func() tea.Msg {
		if hasPw {
			res := hasher.Verify(current, storedHash, storedAlgo)
			if !res.OK {
				return passwordChangedMsg{err: fmt.Errorf("current password is incorrect")}
			}
		}
		hash, algo, err := hasher.Hash(newPw)
		if err != nil {
			return passwordChangedMsg{err: err}
		}
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		return passwordChangedMsg{err: svc.ChangePassword(ctx, userID, hash, algo)}
	}
}

// renderPasswordModal builds the modal body for change/set-password.
func (m *Profile) renderPasswordModal() string {
	innerW := 40
	header := lipgloss.NewStyle().Bold(true).
		Foreground(lipgloss.Color(theme.ColorAccent)).Render(passwordModalTitle(m.snap))
	hint := lipgloss.NewStyle().Italic(true).
		Foreground(lipgloss.Color(theme.ColorDim)).
		Render("Tab move · Ctrl+S save · Esc cancel")
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorAccentDim))

	var lines []string
	lines = append(lines, header, hint, "")
	if m.snap.HasPassword {
		lines = append(lines, labelStyle.Render("current password"))
		lines = append(lines, m.pwCurrent.View(), "")
	}
	lines = append(lines, labelStyle.Render(fmt.Sprintf("new password (min %d chars)", realtime.MinPasswordLength)))
	lines = append(lines, m.pwNew.View(), "")
	lines = append(lines, labelStyle.Render("confirm new password"))
	lines = append(lines, m.pwConfirm.View(), "")
	if m.pwErr != "" {
		lines = append(lines, lipgloss.NewStyle().
			Foreground(lipgloss.Color(theme.ColorRed)).Render("! "+m.pwErr), "")
	}
	saveFocused := m.pwFocusIndex == passwordSaveIndex(m.snap)
	lines = append(lines, m.styleButton("save", saveFocused))
	return theme.ModalFrame.Width(innerW + 6).Render(strings.Join(lines, "\n"))
}

func passwordModalTitle(snap *realtime.ProfileSnapshot) string {
	if snap != nil && snap.HasPassword {
		return "change password"
	}
	return "set password"
}

func passwordSaveIndex(snap *realtime.ProfileSnapshot) int {
	if snap != nil && snap.HasPassword {
		return 3
	}
	return 2
}
