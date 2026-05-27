package screens

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/nickna/ssh.night.ms/internal/providers/geocoding"
	"github.com/nickna/ssh.night.ms/internal/realtime"
	"github.com/nickna/ssh.night.ms/internal/tui/components"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// locationsLoadedMsg carries the result of the initial list query and any
// reload after Add/Delete. The screen swaps savedLocations on success.
type locationsLoadedMsg struct {
	locs []realtime.SavedLocation
	err  error
}

// locationMutatedMsg carries the result of an Add or Delete. The screen
// reloads the list and refreshes Session.PrimaryLocation on success so
// WeatherCoords() picks up the change without restarting the session.
type locationMutatedMsg struct {
	err error
}

// openLocations transitions to modeLocations, resets the cursor + form,
// and kicks off the initial list load.
func (m *Profile) openLocations() tea.Cmd {
	m.previousMode = m.mode
	m.mode = modeLocations
	m.locCursor = 0
	m.locAddOpen = false
	m.locErr = ""
	m.resetLocationForm()
	return m.reloadLocations()
}

// resetLocationForm builds a fresh set of textinputs for the inline add
// form. Called on entry and after a successful Add.
func (m *Profile) resetLocationForm() {
	label := textinput.New()
	label.CharLimit = 64
	label.Width = 24
	label.Placeholder = "Home, Office, …"

	lat := textinput.New()
	lat.CharLimit = 12
	lat.Width = 12
	lat.Placeholder = "40.7128"

	lon := textinput.New()
	lon.CharLimit = 12
	lon.Width = 12
	lon.Placeholder = "-74.0060"

	m.locFormLabel = label
	m.locFormLat = lat
	m.locFormLon = lon
	m.locFormFocus = 0
	m.locFormCanonical = ""
}

// reloadLocations refetches the user's full list. Used on entry and after
// any mutation so the screen reflects the authoritative state.
func (m *Profile) reloadLocations() tea.Cmd {
	svc := m.sess.Locations
	userID := m.sess.Identity.UserID
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5*time.Second)
		defer cancel()
		if svc == nil {
			return locationsLoadedMsg{err: errors.New("location service unavailable")}
		}
		locs, err := svc.List(ctx, userID)
		return locationsLoadedMsg{locs: locs, err: err}
	}
}

// handleLocationsKey routes keys for the Locations modal. The add-form
// and rename-form branches intercept when their respective flags are set
// so typing characters lands in the focused textinput rather than the
// list navigation.
func (m *Profile) handleLocationsKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.locAddOpen {
		return m.handleLocationsAddFormKey(k)
	}
	if m.locRenameOpen {
		return m.handleLocationsRenameFormKey(k)
	}
	switch k.String() {
	case "esc", "q":
		m.mode = m.previousMode
		m.locErr = ""
		return m, nil
	case "up", "k":
		if m.locCursor > 0 {
			m.locCursor--
		}
	case "down", "j":
		if m.locCursor < len(m.savedLocations)-1 {
			m.locCursor++
		}
	case "shift+up", "K":
		return m, m.swapWithPrev()
	case "shift+down", "J":
		return m, m.swapWithNext()
	case "a", "+":
		if len(m.savedLocations) >= realtime.MaxSavedLocationsPerUser {
			m.locErr = fmt.Sprintf("limit reached — %d saved locations max; remove one first.", realtime.MaxSavedLocationsPerUser)
			return m, nil
		}
		m.locAddOpen = true
		m.locErr = ""
		m.resetLocationForm()
		m.locFormLabel.Focus()
		return m, textinput.Blink
	case "r":
		if m.locCursor >= len(m.savedLocations) {
			return m, nil
		}
		target := m.savedLocations[m.locCursor]
		m.locRenameOpen = true
		m.locRenameID = target.ID
		m.locErr = ""
		m.resetLocationForm()
		m.locFormLabel.SetValue(target.Label)
		m.locFormLabel.CursorEnd()
		m.locFormLabel.Focus()
		return m, textinput.Blink
	case "d", "delete":
		return m, m.requestLocationDelete()
	}
	return m, nil
}

// handleLocationsRenameFormKey owns the inline rename form. Only one
// input is active (label); Enter submits, Esc cancels back to the list.
func (m *Profile) handleLocationsRenameFormKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.locRenameOpen = false
		m.locRenameID = 0
		m.locErr = ""
		return m, nil
	case "enter":
		return m, m.submitLocationRename()
	}
	var cmd tea.Cmd
	m.locFormLabel, cmd = m.locFormLabel.Update(k)
	return m, cmd
}

// swapWithPrev / swapWithNext move the cursor's row up / down by one
// sort_order step. Cursor follows the row so the user can chain ↑↑ to
// push something all the way to the top without re-selecting between
// presses. No-ops at the list edges. Errors land via locationMutatedMsg.
func (m *Profile) swapWithPrev() tea.Cmd {
	if m.locCursor <= 0 || m.locCursor >= len(m.savedLocations) {
		return nil
	}
	a := m.savedLocations[m.locCursor]
	b := m.savedLocations[m.locCursor-1]
	m.locCursor--
	return m.dispatchSwap(a, b)
}

func (m *Profile) swapWithNext() tea.Cmd {
	if m.locCursor < 0 || m.locCursor >= len(m.savedLocations)-1 {
		return nil
	}
	a := m.savedLocations[m.locCursor]
	b := m.savedLocations[m.locCursor+1]
	m.locCursor++
	return m.dispatchSwap(a, b)
}

// dispatchSwap fires the back-end swap and lets the existing
// locationMutatedMsg handler do the reload + primary-location refresh.
func (m *Profile) dispatchSwap(a, b realtime.SavedLocation) tea.Cmd {
	svc := m.sess.Locations
	userID := m.sess.Identity.UserID
	m.working = true
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5*time.Second)
		defer cancel()
		if svc == nil {
			return locationMutatedMsg{err: errors.New("location service unavailable")}
		}
		if err := svc.Swap(ctx, userID, a, b); err != nil {
			return locationMutatedMsg{err: err}
		}
		return locationMutatedMsg{}
	}
}

// submitLocationRename validates + dispatches the rename via the service.
// Reuses locationMutatedMsg so the list + primary-location cache refresh
// the same way Add/Delete do.
func (m *Profile) submitLocationRename() tea.Cmd {
	label := strings.TrimSpace(m.locFormLabel.Value())
	if label == "" {
		m.locErr = "label is required."
		return nil
	}
	svc := m.sess.Locations
	userID := m.sess.Identity.UserID
	id := m.locRenameID
	m.working = true
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5*time.Second)
		defer cancel()
		if svc == nil {
			return locationMutatedMsg{err: errors.New("location service unavailable")}
		}
		if err := svc.Rename(ctx, userID, id, label); err != nil {
			return locationMutatedMsg{err: err}
		}
		return locationMutatedMsg{}
	}
}

// handleLocationsAddFormKey owns the inline add-form. Tab cycles the three
// inputs; Enter submits; Esc cancels back to the list. Ctrl+F triggers a
// geocoder lookup using whatever's in the label field; digit keys 1-9
// while a search result list is visible auto-fill the form from that pick.
func (m *Profile) handleLocationsAddFormKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Search-result picker — digits 1..N while results are visible select
	// a candidate. Must run BEFORE the input-update fall-through or the
	// label field would just absorb the digit. Label gets the short Name
	// (e.g. "Paris") so the saved button label is concise; the long
	// disambiguating form lands in locFormCanonical and persists to the
	// row's canonical column for hover-style detail in the modal list.
	if len(m.locSearchResults) > 0 {
		switch s := k.String(); s {
		case "1", "2", "3", "4", "5", "6", "7", "8", "9":
			idx := int(s[0] - '1')
			if idx < len(m.locSearchResults) {
				r := m.locSearchResults[idx]
				m.locFormLabel.SetValue(r.Name)
				m.locFormLabel.CursorEnd()
				m.locFormLat.SetValue(fmt.Sprintf("%.4f", r.Latitude))
				m.locFormLon.SetValue(fmt.Sprintf("%.4f", r.Longitude))
				m.locFormCanonical = r.Canonical()
				m.locSearchResults = nil
				m.locErr = ""
				return m, nil
			}
		}
	}
	switch k.String() {
	case "esc":
		// Esc clears search results first if any are showing — otherwise
		// closes the whole add form. Two-tier behavior matches the chat
		// thread-filter / Boards composer patterns.
		if len(m.locSearchResults) > 0 || m.locSearching {
			m.locSearchResults = nil
			m.locSearching = false
			m.locErr = ""
			return m, nil
		}
		m.locAddOpen = false
		m.locErr = ""
		return m, nil
	case "tab":
		m.locFormFocus = (m.locFormFocus + 1) % 3
		m.applyLocationFormFocus()
		return m, textinput.Blink
	case "shift+tab":
		m.locFormFocus = (m.locFormFocus - 1 + 3) % 3
		m.applyLocationFormFocus()
		return m, textinput.Blink
	case "ctrl+f":
		return m, m.searchByLabel()
	case "enter":
		return m, m.submitLocationAdd()
	}
	var cmd tea.Cmd
	switch m.locFormFocus {
	case 0:
		m.locFormLabel, cmd = m.locFormLabel.Update(k)
	case 1:
		m.locFormLat, cmd = m.locFormLat.Update(k)
	case 2:
		m.locFormLon, cmd = m.locFormLon.Update(k)
	}
	return m, cmd
}

// locationSearchMsg lands when the geocoder Search completes. Non-empty
// Results populates the inline picker; an error surfaces inline so the
// user can correct the query.
type locationSearchMsg struct {
	results []geocoding.Result
	err     error
}

// searchByLabel takes whatever's in the label field, fires the geocoder,
// and lets the resulting locationSearchMsg handler populate the picker.
// Whitespace-only / missing geocoder collapses to an inline error.
func (m *Profile) searchByLabel() tea.Cmd {
	query := strings.TrimSpace(m.locFormLabel.Value())
	if query == "" {
		m.locErr = "type a place name in the label field first, then Ctrl+F."
		return nil
	}
	svc := m.sess.Geocoder
	if svc == nil {
		m.locErr = "geocoder isn't configured on this server."
		return nil
	}
	m.locSearching = true
	m.locErr = ""
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5*time.Second)
		defer cancel()
		results, err := svc.Search(ctx, query, 5)
		return locationSearchMsg{results: results, err: err}
	}
}

// applyLocationFormFocus mirrors the focus state into the textinputs so the
// cursor blinks on exactly one at a time.
func (m *Profile) applyLocationFormFocus() {
	m.locFormLabel.Blur()
	m.locFormLat.Blur()
	m.locFormLon.Blur()
	switch m.locFormFocus {
	case 0:
		m.locFormLabel.Focus()
	case 1:
		m.locFormLat.Focus()
	case 2:
		m.locFormLon.Focus()
	}
}

// submitLocationAdd parses the three inputs and calls LocationService.Add.
// Validation errors surface inline; back-end errors land via
// locationMutatedMsg below.
func (m *Profile) submitLocationAdd() tea.Cmd {
	label := strings.TrimSpace(m.locFormLabel.Value())
	latStr := strings.TrimSpace(m.locFormLat.Value())
	lonStr := strings.TrimSpace(m.locFormLon.Value())
	if label == "" || latStr == "" || lonStr == "" {
		m.locErr = "label, latitude, and longitude are all required."
		return nil
	}
	lat, err := strconv.ParseFloat(latStr, 64)
	if err != nil {
		m.locErr = "latitude must be a number (e.g. 40.7128)."
		return nil
	}
	lon, err := strconv.ParseFloat(lonStr, 64)
	if err != nil {
		m.locErr = "longitude must be a number (e.g. -74.0060)."
		return nil
	}
	svc := m.sess.Locations
	userID := m.sess.Identity.UserID
	canonical := m.locFormCanonical
	m.working = true
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5*time.Second)
		defer cancel()
		if svc == nil {
			return locationMutatedMsg{err: errors.New("location service unavailable")}
		}
		if _, err := svc.Add(ctx, userID, label, canonical, lat, lon); err != nil {
			return locationMutatedMsg{err: err}
		}
		return locationMutatedMsg{}
	}
}

// requestLocationDelete fires the same confirm-modal pattern as Keys.
// Returns nil when the cursor is past the end (empty list, etc).
func (m *Profile) requestLocationDelete() tea.Cmd {
	if m.locCursor >= len(m.savedLocations) {
		return nil
	}
	target := m.savedLocations[m.locCursor]
	m.confirm = components.NewConfirm(
		"remove saved location",
		fmt.Sprintf("remove %q (%.4f, %.4f)?", target.Label, target.Latitude, target.Longitude),
	)
	m.confirmKind = fmt.Sprintf("removeLocation:%d", target.ID)
	m.confirmReturnMode = modeLocations
	m.mode = modeConfirm
	return nil
}

// deleteLocation is the actual back-end call dispatched from the Yes branch
// of the confirm modal. Mirrors deleteKey.
func (m *Profile) deleteLocation(id int64) tea.Cmd {
	svc := m.sess.Locations
	userID := m.sess.Identity.UserID
	m.working = true
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5*time.Second)
		defer cancel()
		if svc == nil {
			return locationMutatedMsg{err: errors.New("location service unavailable")}
		}
		if err := svc.Delete(ctx, userID, id); err != nil {
			return locationMutatedMsg{err: err}
		}
		return locationMutatedMsg{}
	}
}

// renderLocationsModal draws the modal. Layout: header, blurb, optional
// add-form, list of rows, hint line.
func (m *Profile) renderLocationsModal() string {
	innerW := m.sess.Width - 12
	if innerW > 80 {
		innerW = 80
	}
	if innerW < 50 {
		innerW = 50
	}

	header := lipgloss.NewStyle().Bold(true).
		Foreground(lipgloss.Color(theme.ColorAccent)).Render("saved locations")
	blurb := lipgloss.NewStyle().Italic(true).
		Foreground(lipgloss.Color(theme.ColorDim)).Width(innerW).
		Render("Weather + Map screens use the first row by default; remove the top row to promote the next one. Latitude/longitude are WGS84 decimal degrees.")

	var sections []string
	sections = append(sections, header, blurb, "")

	if m.locErr != "" {
		errLine := lipgloss.NewStyle().Bold(true).
			Foreground(lipgloss.Color(theme.ColorRed)).Render("! " + m.locErr)
		sections = append(sections, errLine, "")
	}

	if m.locAddOpen {
		if m.locSearching {
			sections = append(sections, lipgloss.NewStyle().Italic(true).
				Foreground(lipgloss.Color(theme.ColorDim)).Render("searching…"), "")
		}
		if len(m.locSearchResults) > 0 {
			sections = append(sections, m.renderLocationSearchResults())
		}
		sections = append(sections, m.renderLocationAddForm(innerW))
	}
	if m.locRenameOpen {
		sections = append(sections, m.renderLocationRenameForm(innerW))
	}

	rows := make([]string, 0, len(m.savedLocations)+1)
	if len(m.savedLocations) == 0 {
		rows = append(rows, lipgloss.NewStyle().Italic(true).
			Foreground(lipgloss.Color(theme.ColorDim)).Render("no saved locations."))
	}
	canonicalStyle := lipgloss.NewStyle().Italic(true).
		Foreground(lipgloss.Color(theme.ColorDim))
	for i, loc := range m.savedLocations {
		marker := "  "
		if i == 0 {
			marker = "★ "
		}
		line := fmt.Sprintf("%s%s   %.4f, %.4f",
			marker, runewidth.Truncate(loc.Label, 28, "…"), loc.Latitude, loc.Longitude)
		if i == m.locCursor && !m.locAddOpen && !m.locRenameOpen {
			line = lipgloss.NewStyle().Bold(true).
				Background(lipgloss.Color(theme.ColorSurfaceAlt)).
				Foreground(lipgloss.Color(theme.ColorYellow)).Render("▸ " + line)
		} else {
			line = "  " + line
		}
		rows = append(rows, line)
		// Canonical line — italic muted, only shown when it adds info
		// beyond the label (so a manually-typed location stays compact
		// and a geocoded one carries its disambiguation visibly).
		if loc.Canonical != "" && loc.Canonical != loc.Label {
			rows = append(rows, "    "+canonicalStyle.Render(runewidth.Truncate(loc.Canonical, 64, "…")))
		}
	}
	sections = append(sections, strings.Join(rows, "\n"))

	var hint string
	switch {
	case m.locAddOpen && len(m.locSearchResults) > 0:
		hint = "1-N pick · Ctrl+F search again · Esc clear results"
	case m.locAddOpen:
		hint = "Tab cycle inputs · Ctrl+F search by name · Enter submit · Esc cancel"
	case m.locRenameOpen:
		hint = "Enter save · Esc cancel"
	default:
		hint = "↑/↓ select · Shift+↑/↓ reorder · a add · r rename · d remove · Esc back"
	}
	sections = append(sections, "", lipgloss.NewStyle().Italic(true).
		Foreground(lipgloss.Color(theme.ColorDim)).Render(hint))

	return theme.ModalFrame.Width(innerW + 6).Render(strings.Join(sections, "\n"))
}

// renderLocationSearchResults draws the numbered geocoder candidates
// above the add form. Press 1..N to pick; the digit key handler in
// handleLocationsAddFormKey populates label/lat/lon from the choice.
func (m *Profile) renderLocationSearchResults() string {
	head := lipgloss.NewStyle().Italic(true).Bold(true).
		Foreground(lipgloss.Color(theme.ColorAccent)).Render("matches — press a number to pick:")
	rows := []string{head}
	numStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorYellow))
	for i, r := range m.locSearchResults {
		rows = append(rows, fmt.Sprintf("  %s  %s  (%.4f, %.4f)",
			numStyle.Render(fmt.Sprintf("%d", i+1)),
			r.Canonical(), r.Latitude, r.Longitude))
	}
	rows = append(rows, "")
	return strings.Join(rows, "\n")
}

// renderLocationRenameForm draws the inline single-input form when
// locRenameOpen. Pre-populated with the current label by the 'r' key
// handler. Lat/lon stay untouched — rename is label-only.
func (m *Profile) renderLocationRenameForm(_ int) string {
	labelStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorAccent))
	rows := []string{
		labelStyle.Render("rename    ") + "  " + m.locFormLabel.View(),
		"",
	}
	return strings.Join(rows, "\n")
}

// renderLocationAddForm draws the inline 3-input form when locAddOpen.
func (m *Profile) renderLocationAddForm(_ int) string {
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorAccentDim))
	activeStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorAccent))
	field := func(idx int, name string) string {
		if m.locFormFocus == idx {
			return activeStyle.Render(name)
		}
		return labelStyle.Render(name)
	}
	rows := []string{
		field(0, "label    ") + "  " + m.locFormLabel.View(),
		field(1, "latitude ") + "  " + m.locFormLat.View(),
		field(2, "longitude") + "  " + m.locFormLon.View(),
		"",
	}
	return strings.Join(rows, "\n")
}
