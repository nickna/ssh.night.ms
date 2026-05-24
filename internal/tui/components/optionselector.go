// Package components — optionselector.go renders a horizontal radio control
// like `( Celsius ) [ Fahrenheit ] ( Both )`. Brackets mark the active
// option; parens decorate the rest. The parent routes Left/Right keys via
// Prev() / Next() and reads Index to persist the choice.
package components

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// OptionSelector is a stateless-by-parent horizontal radio.
type OptionSelector struct {
	Options []string
	Index   int
	Focus   bool
}

// Prev moves the selection left (no wrap so the user can feel the boundary).
func (o *OptionSelector) Prev() {
	if o.Index > 0 {
		o.Index--
	}
}

// Next moves the selection right (no wrap, same reason as Prev).
func (o *OptionSelector) Next() {
	if o.Index < len(o.Options)-1 {
		o.Index++
	}
}

// Selected returns the label of the active option, or "" if empty.
func (o *OptionSelector) Selected() string {
	if o.Index < 0 || o.Index >= len(o.Options) {
		return ""
	}
	return o.Options[o.Index]
}

// View renders all options inline.
func (o *OptionSelector) View() string {
	if len(o.Options) == 0 {
		return ""
	}
	activeColor := theme.ColorAccent
	if !o.Focus {
		activeColor = theme.ColorYellow
	}
	activeStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(activeColor))
	inactiveStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorAccentDim))
	var parts []string
	for i, opt := range o.Options {
		if i == o.Index {
			parts = append(parts, activeStyle.Render("[ "+opt+" ]"))
		} else {
			parts = append(parts, inactiveStyle.Render("( "+opt+" )"))
		}
	}
	return strings.Join(parts, " ")
}
