// Package components — searchablelist.go is a typeahead-filtered single-
// column list used by the Profile screen's timezone picker. Bubbles' list
// model is overkill (delegates, paginator) and doesn't do inline
// type-to-filter cleanly, so this component owns a small fixed contract.
package components

import (
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// SearchableList renders a vertical list with a typeahead filter.
type SearchableList struct {
	Items   []string // full source — never mutated after construction
	Filter  string   // current filter text (lowercased on every Update)
	Cursor  int      // index into the *filtered* slice (0-based)
	Focus   bool
	Width   int   // render width in cells
	visible []int // indices into Items that survive the filter
}

// NewSearchableList builds a list with the given items. selected is the value
// the cursor should start on (matched against Items); empty string → cursor 0.
func NewSearchableList(items []string, selected string) *SearchableList {
	s := &SearchableList{Items: items}
	s.recompute()
	if selected != "" {
		for i, v := range s.visible {
			if items[v] == selected {
				s.Cursor = i
				break
			}
		}
	}
	return s
}

// Selected returns the highlighted item, or "" when the filter has no
// matches.
func (s *SearchableList) Selected() string {
	if len(s.visible) == 0 {
		return ""
	}
	if s.Cursor < 0 || s.Cursor >= len(s.visible) {
		return ""
	}
	return s.Items[s.visible[s.Cursor]]
}

// SetSelected forces the cursor onto the row matching value, if present.
// A no-op when value isn't in the current filter view.
func (s *SearchableList) SetSelected(value string) {
	for i, v := range s.visible {
		if s.Items[v] == value {
			s.Cursor = i
			return
		}
	}
}

// recompute rebuilds the visible-index slice. Called on every filter change
// so the cursor stays referenced against a fresh denominator.
func (s *SearchableList) recompute() {
	needle := strings.ToLower(s.Filter)
	s.visible = s.visible[:0]
	if needle == "" {
		for i := range s.Items {
			s.visible = append(s.visible, i)
		}
	} else {
		for i, item := range s.Items {
			if strings.Contains(strings.ToLower(item), needle) {
				s.visible = append(s.visible, i)
			}
		}
	}
	if s.Cursor >= len(s.visible) {
		s.Cursor = 0
	}
}

// Update consumes key events. Returns the consumed bool: when true, the
// parent should NOT forward the event to other focused widgets. (A
// non-alphanumeric key like Tab/Esc/Ctrl-S is left for the parent.)
func (s *SearchableList) Update(msg tea.KeyMsg) (consumed bool) {
	if !s.Focus {
		return false
	}
	switch msg.String() {
	case "up", "ctrl+p":
		if s.Cursor > 0 {
			s.Cursor--
		}
		return true
	case "down", "ctrl+n":
		if s.Cursor < len(s.visible)-1 {
			s.Cursor++
		}
		return true
	case "pgup":
		s.Cursor -= 5
		if s.Cursor < 0 {
			s.Cursor = 0
		}
		return true
	case "pgdown":
		s.Cursor += 5
		if s.Cursor >= len(s.visible) {
			s.Cursor = len(s.visible) - 1
		}
		return true
	case "home":
		s.Cursor = 0
		return true
	case "end":
		s.Cursor = len(s.visible) - 1
		if s.Cursor < 0 {
			s.Cursor = 0
		}
		return true
	case "backspace":
		if len(s.Filter) > 0 {
			s.Filter = s.Filter[:len(s.Filter)-1]
			s.recompute()
		}
		return true
	}
	// Alphanumeric runes (and a few separators) extend the filter.
	if len(msg.Runes) == 1 {
		r := msg.Runes[0]
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '/' || r == '_' || r == '-' || r == ' ' {
			s.Filter += string(r)
			s.recompute()
			return true
		}
	}
	return false
}

// View renders the list collapsed to height visible rows. Filter prompt
// shows the live filter text; rows below paint as many of the filtered
// items as fit, scrolled to keep the cursor in view.
func (s *SearchableList) View(height int) string {
	if height < 3 {
		height = 3
	}
	width := s.Width
	if width < 20 {
		width = 30
	}

	// Filter prompt line. When focused we hint the user can type.
	prompt := "filter: " + s.Filter
	if s.Focus {
		prompt += lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorAccent)).Render("▎")
	}
	promptStyled := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.ColorAccentDim)).
		Render(prompt)

	// Compute visible window.
	rows := height - 1
	if rows < 1 {
		rows = 1
	}
	scroll := 0
	if s.Cursor >= rows {
		scroll = s.Cursor - rows + 1
	}
	end := scroll + rows
	if end > len(s.visible) {
		end = len(s.visible)
	}

	var lines []string
	lines = append(lines, promptStyled)
	if len(s.visible) == 0 {
		lines = append(lines, lipgloss.NewStyle().
			Italic(true).
			Foreground(lipgloss.Color(theme.ColorDim)).
			Render("  no matches"))
	}
	for i := scroll; i < end; i++ {
		item := s.Items[s.visible[i]]
		display := runewidth.Truncate(item, width-2, "…")
		if i == s.Cursor {
			active := lipgloss.NewStyle().
				Bold(true).
				Background(lipgloss.Color(theme.ColorSurfaceAlt)).
				Foreground(lipgloss.Color(theme.ColorYellow))
			lines = append(lines, active.Render("▸ "+display))
		} else {
			row := lipgloss.NewStyle().
				Foreground(lipgloss.Color(theme.ColorText)).
				Render("  " + display)
			lines = append(lines, row)
		}
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}
