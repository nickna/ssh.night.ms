// Package components — confirm.go is a Yes/No prompt rendered as a small
// bordered modal box. The parent composes it with Overlay() onto a dimmed
// underlying view. Callers read Choice (0 = No / Cancel, 1 = Yes / Confirm)
// after the user submits.
package components

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// Confirm represents the modal's internal state.
type Confirm struct {
	Title     string
	Message   string
	YesLabel  string // default "Yes"
	NoLabel   string // default "No"
	Choice    int    // 0 = No, 1 = Yes
	Submitted bool   // set when user pressed Enter
	Cancelled bool   // set when user pressed Esc
}

// NewConfirm builds a confirm prompt with default labels.
func NewConfirm(title, message string) *Confirm {
	return &Confirm{
		Title:    title,
		Message:  message,
		YesLabel: "Yes",
		NoLabel:  "No",
		Choice:   0,
	}
}

// Update handles key events. Returns the (possibly mutated) Confirm and a
// nil cmd. The caller checks Submitted/Cancelled after each call.
func (c *Confirm) Update(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "left", "h":
		c.Choice = 0
	case "right", "l":
		c.Choice = 1
	case "tab":
		c.Choice = (c.Choice + 1) % 2
	case "y", "Y":
		c.Choice = 1
		c.Submitted = true
	case "n", "N":
		c.Choice = 0
		c.Submitted = true
	case "enter":
		c.Submitted = true
	case "esc":
		c.Cancelled = true
	}
	return nil
}

// View renders the modal box. Width is the inner content width; the caller
// chooses based on terminal size.
func (c *Confirm) View(width int) string {
	if width < 30 {
		width = 30
	}
	header := lipgloss.NewStyle().Bold(true).
		Foreground(lipgloss.Color(theme.ColorAccent)).Render(c.Title)
	body := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.ColorText)).
		Width(width).Render(c.Message)

	noStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorAccentDim))
	yesStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorAccentDim))
	activeStyle := lipgloss.NewStyle().Bold(true).
		Background(lipgloss.Color(theme.ColorSurfaceAlt)).
		Foreground(lipgloss.Color(theme.ColorYellow))
	noBtn := "[ " + c.NoLabel + " ]"
	yesBtn := "[ " + c.YesLabel + " ]"
	if c.Choice == 0 {
		noBtn = activeStyle.Render(noBtn)
		yesBtn = yesStyle.Render(yesBtn)
	} else {
		noBtn = noStyle.Render(noBtn)
		yesBtn = activeStyle.Render(yesBtn)
	}
	buttons := noBtn + "   " + yesBtn
	buttonWidth := lipgloss.Width(buttons)
	pad := width - buttonWidth
	if pad < 0 {
		pad = 0
	}
	buttonsRow := strings.Repeat(" ", pad/2) + buttons

	hint := lipgloss.NewStyle().Italic(true).
		Foreground(lipgloss.Color(theme.ColorDim)).
		Render("←/→ choose · Enter confirm · Esc cancel")

	inner := strings.Join([]string{header, "", body, "", buttonsRow, "", hint}, "\n")
	return theme.ModalFrame.Width(width + 4).Render(inner)
}
