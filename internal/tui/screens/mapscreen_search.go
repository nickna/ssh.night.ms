package screens

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nickna/ssh.night.ms/internal/providers/geocoding"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// mapSearchMsg lands when Geocoder.Search returns. Non-empty results populate
// the numbered picker; an error surfaces inline so the user can re-type the
// query without leaving the modal.
type mapSearchMsg struct {
	results []geocoding.Result
	err     error
}

// openSearch enters the geocoder sub-mode: clears any prior input/results
// and focuses the textinput so the user can start typing immediately.
func (m *MapScreen) openSearch() tea.Cmd {
	m.sub = subSearch
	m.searchInput.SetValue("")
	m.searchInput.Focus()
	m.searchResults = nil
	m.searching = false
	m.searchErr = ""
	return textinput.Blink
}

// closeSearch returns to the browse view, dropping any pending results and
// blurring the input so its cursor stops blinking.
func (m *MapScreen) closeSearch() {
	m.sub = subBrowse
	m.searchInput.Blur()
	m.searchResults = nil
	m.searching = false
	m.searchErr = ""
}

// fireSearch validates the input and dispatches Geocoder.Search. Empty query
// or missing provider collapses to an inline error rather than firing a
// no-op request.
func (m *MapScreen) fireSearch() tea.Cmd {
	query := strings.TrimSpace(m.searchInput.Value())
	if query == "" {
		m.searchErr = "type a place name first."
		return nil
	}
	svc := m.sess.Geocoder
	if svc == nil {
		m.searchErr = "geocoder isn't configured on this server."
		return nil
	}
	m.searching = true
	m.searchErr = ""
	m.searchResults = nil
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		results, err := svc.Search(ctx, query, 5)
		return mapSearchMsg{results: results, err: err}
	}
}

// handleSearchResult routes the geocoder response into modal state.
func (m *MapScreen) handleSearchResult(msg mapSearchMsg) (tea.Model, tea.Cmd) {
	m.searching = false
	if msg.err != nil {
		m.searchErr = msg.err.Error()
		m.searchResults = nil
		return m, nil
	}
	m.searchResults = msg.results
	if len(msg.results) == 0 {
		m.searchErr = "no matches."
	}
	return m, nil
}

// handleSearchKey owns input while the search modal is open. Digit picker
// runs before the textinput fall-through so 1-N selects a result instead of
// landing in the query field. Two-tier Esc clears results first, then exits.
func (m *MapScreen) handleSearchKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Result picker — only active when results are showing. Numbers in a
	// fresh query string (e.g., "1600 Pennsylvania Ave") still flow through
	// to the textinput because no results are visible yet.
	if len(m.searchResults) > 0 {
		s := k.String()
		if len(s) == 1 && s[0] >= '1' && s[0] <= '9' {
			idx := int(s[0] - '1')
			if idx < len(m.searchResults) {
				r := m.searchResults[idx]
				label := r.Canonical()
				m.closeSearch()
				return m, tea.Batch(m.jumpTo(r.Latitude, r.Longitude), m.setToast("→ "+label))
			}
		}
	}
	switch k.String() {
	case "esc":
		if len(m.searchResults) > 0 || m.searching {
			m.searchResults = nil
			m.searching = false
			m.searchErr = ""
			return m, nil
		}
		m.closeSearch()
		return m, nil
	case "enter":
		return m, m.fireSearch()
	}
	var cmd tea.Cmd
	m.searchInput, cmd = m.searchInput.Update(k)
	return m, cmd
}

// renderSearchView paints the full-screen search modal in place of the map
// while subSearch is active. The map disappears until the user picks a
// result or cancels — simpler than fighting layout for a side panel.
func (m *MapScreen) renderSearchView() string {
	numStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorYellow))
	headStyle := lipgloss.NewStyle().Bold(true).Italic(true).Foreground(lipgloss.Color(theme.ColorAccent))
	inputLabel := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorAccent))

	var b strings.Builder
	b.WriteString(mapTitle.Render("Map · search"))
	b.WriteString("\n\n")
	b.WriteString(inputLabel.Render("search: ") + m.searchInput.View())
	b.WriteString("\n\n")

	switch {
	case m.searching:
		b.WriteString(mapHint.Render("searching…"))
		b.WriteString("\n\n")
	case m.searchErr != "":
		b.WriteString(mapErr.Render("! " + m.searchErr))
		b.WriteString("\n\n")
	}

	if len(m.searchResults) > 0 {
		b.WriteString(headStyle.Render("matches — press a number to pick:"))
		b.WriteString("\n")
		for i, r := range m.searchResults {
			line := fmt.Sprintf("  %s  %s  (%.4f, %.4f)",
				numStyle.Render(fmt.Sprintf("%d", i+1)),
				r.Canonical(), r.Latitude, r.Longitude)
			b.WriteString(line)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	var hint string
	switch {
	case len(m.searchResults) > 0:
		hint = "1-N pick · Esc clear results"
	default:
		hint = "Enter search · Esc cancel"
	}
	b.WriteString(mapHint.Render(hint))
	return b.String()
}
