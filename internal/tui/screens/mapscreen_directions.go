package screens

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/nickna/ssh.night.ms/internal/providers/geocoding"
	"github.com/nickna/ssh.night.ms/internal/providers/routing"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// mapDestSearchMsg is the directions-modal equivalent of mapSearchMsg, kept
// distinct so the search modal (which may also be open in parallel reasoning)
// doesn't intercept the result.
type mapDestSearchMsg struct {
	results []geocoding.Result
	err     error
}

// openDirections enters the directions sub-mode. Origin is always the map
// center; the modal collects a destination via either geocoder query or
// saved-location pick. Refuses to open if no routing provider is configured
// — that path shows a toast instead.
func (m *MapScreen) openDirections() tea.Cmd {
	if m.sess.Routing == nil {
		return m.setToast("routing disabled — see operator")
	}
	m.sub = subDirections
	m.destInput.SetValue("")
	m.destInput.Focus()
	m.destResults = nil
	m.destSearching = false
	m.destErr = ""
	m.destSavedPick = false
	return textinput.Blink
}

// closeDirections returns to the browse view.
func (m *MapScreen) closeDirections() {
	m.sub = subBrowse
	m.destInput.Blur()
	m.destResults = nil
	m.destSearching = false
	m.destErr = ""
	m.destSavedPick = false
}

// fireDestSearch runs the geocoder on the current destination input.
func (m *MapScreen) fireDestSearch() tea.Cmd {
	query := strings.TrimSpace(m.destInput.Value())
	if query == "" {
		m.destErr = "type a destination first."
		return nil
	}
	svc := m.sess.Geocoder
	if svc == nil {
		m.destErr = "geocoder isn't configured on this server."
		return nil
	}
	m.destSearching = true
	m.destErr = ""
	m.destResults = nil
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		results, err := svc.Search(ctx, query, 5)
		return mapDestSearchMsg{results: results, err: err}
	}
}

// handleDestSearchResult installs geocoder results into modal state.
func (m *MapScreen) handleDestSearchResult(msg mapDestSearchMsg) (tea.Model, tea.Cmd) {
	m.destSearching = false
	if msg.err != nil {
		m.destErr = msg.err.Error()
		m.destResults = nil
		return m, nil
	}
	m.destResults = msg.results
	if len(msg.results) == 0 {
		m.destErr = "no matches."
	}
	return m, nil
}

// pickDestination closes the modal and fires the routing request with
// origin = current map center and the supplied destination.
func (m *MapScreen) pickDestination(dest routing.LatLon, label string) tea.Cmd {
	origin := routing.LatLon{Lat: m.centerLat, Lon: m.centerLon}
	mode := m.mode
	m.closeDirections()
	return tea.Batch(
		m.requestRoute(origin, dest, mode),
		m.setToast("routing → "+label+"…"),
	)
}

// handleDirectionsKey owns input while the directions modal is open. Pressing
// `s` toggles between the geocoder text input and a numbered list of the
// user's saved locations. Digits pick from whichever list is currently
// visible (search results OR saved). Two-tier Esc clears results first,
// then closes the modal.
func (m *MapScreen) handleDirectionsKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Saved-location picker — when toggled on, digits select directly.
	if m.destSavedPick {
		s := k.String()
		if len(s) == 1 && s[0] >= '1' && s[0] <= '9' {
			idx := int(s[0] - '1')
			if idx < len(m.savedLocs) {
				loc := m.savedLocs[idx]
				return m, m.pickDestination(routing.LatLon{Lat: loc.Latitude, Lon: loc.Longitude}, loc.Label)
			}
		}
		switch s {
		case "esc":
			// Esc from saved-pick returns to the text input.
			m.destSavedPick = false
			m.destInput.Focus()
			return m, textinput.Blink
		case "s":
			m.destSavedPick = false
			m.destInput.Focus()
			return m, textinput.Blink
		}
		return m, nil
	}

	// Result picker — only active when geocoder results are showing.
	if len(m.destResults) > 0 {
		s := k.String()
		if len(s) == 1 && s[0] >= '1' && s[0] <= '9' {
			idx := int(s[0] - '1')
			if idx < len(m.destResults) {
				r := m.destResults[idx]
				return m, m.pickDestination(routing.LatLon{Lat: r.Latitude, Lon: r.Longitude}, r.Canonical())
			}
		}
	}
	switch k.String() {
	case "esc":
		if len(m.destResults) > 0 || m.destSearching {
			m.destResults = nil
			m.destSearching = false
			m.destErr = ""
			return m, nil
		}
		m.closeDirections()
		return m, nil
	case "enter":
		return m, m.fireDestSearch()
	case "s":
		if len(m.savedLocs) == 0 {
			m.destErr = "no saved locations — type a place name instead."
			return m, nil
		}
		m.destSavedPick = true
		m.destInput.Blur()
		m.destErr = ""
		return m, nil
	}
	var cmd tea.Cmd
	m.destInput, cmd = m.destInput.Update(k)
	return m, cmd
}

// renderDirectionsView paints the full-screen directions modal. Mirrors the
// search modal's layout but adds an "origin / destination / mode" preamble
// and an optional saved-location picker that swaps in when `s` is pressed.
func (m *MapScreen) renderDirectionsView() string {
	numStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorYellow))
	headStyle := lipgloss.NewStyle().Bold(true).Italic(true).Foreground(lipgloss.Color(theme.ColorAccent))
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorAccentDim))
	inputLabel := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorAccent))

	var b strings.Builder
	b.WriteString(mapTitle.Render("Map · directions"))
	b.WriteString("\n\n")
	b.WriteString(labelStyle.Render(fmt.Sprintf(
		"origin: %.4f, %.4f (map center) · mode: %s",
		m.centerLat, m.centerLon, m.mode.Label())))
	b.WriteString("\n\n")

	if m.destSavedPick {
		b.WriteString(headStyle.Render("saved locations — press a number to pick:"))
		b.WriteString("\n")
		for i, loc := range m.savedLocs {
			line := fmt.Sprintf("  %s  %s  (%.4f, %.4f)",
				numStyle.Render(fmt.Sprintf("%d", i+1)),
				runewidth.Truncate(loc.Label, 32, "…"),
				loc.Latitude, loc.Longitude)
			b.WriteString(line)
			b.WriteString("\n")
		}
		b.WriteString("\n")
		b.WriteString(mapHint.Render("1-N pick · s back to search · Esc back"))
		return b.String()
	}

	b.WriteString(inputLabel.Render("destination: ") + m.destInput.View())
	b.WriteString("\n\n")

	switch {
	case m.destSearching:
		b.WriteString(mapHint.Render("searching…"))
		b.WriteString("\n\n")
	case m.destErr != "":
		b.WriteString(mapErr.Render("! " + m.destErr))
		b.WriteString("\n\n")
	}

	if len(m.destResults) > 0 {
		b.WriteString(headStyle.Render("matches — press a number to pick:"))
		b.WriteString("\n")
		for i, r := range m.destResults {
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
	case len(m.destResults) > 0:
		hint = "1-N pick · s saved · Esc clear results"
	case len(m.savedLocs) > 0:
		hint = "Enter search · s pick saved · Esc cancel"
	default:
		hint = "Enter search · Esc cancel"
	}
	b.WriteString(mapHint.Render(hint))
	return b.String()
}
