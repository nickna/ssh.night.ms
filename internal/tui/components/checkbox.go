// Package components — checkbox.go is a stateless boolean toggle widget. The
// parent owns key routing; this struct just renders `[x] label` (or `[ ]`)
// and exposes Toggle(). Focused checkboxes render their label in the focus
// color so the user can see which one Tab landed on.
package components

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// Checkbox renders `[x] label` / `[ ] label` with optional focus highlight.
type Checkbox struct {
	Label   string
	Checked bool
	Focus   bool
}

// Toggle flips the checked state. The parent calls this on Space/Enter.
func (c *Checkbox) Toggle() { c.Checked = !c.Checked }

// View returns the rendered widget on a single line.
func (c *Checkbox) View() string {
	box := "[ ]"
	if c.Checked {
		box = "[x]"
	}
	label := c.Label
	if c.Focus {
		boxStyled := lipgloss.NewStyle().
			Foreground(lipgloss.Color(theme.ColorAccent)).
			Bold(true).
			Render(box)
		labelStyled := lipgloss.NewStyle().
			Foreground(lipgloss.Color(theme.ColorAccent)).
			Render(label)
		return boxStyled + " " + labelStyled
	}
	boxStyled := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.ColorAccentDim)).
		Render(box)
	labelStyled := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.ColorText)).
		Render(label)
	return boxStyled + " " + labelStyled
}
