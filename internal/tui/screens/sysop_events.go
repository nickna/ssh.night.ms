package screens

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nickna/ssh.night.ms/internal/data"
	"github.com/nickna/ssh.night.ms/internal/data/gen"
	"github.com/nickna/ssh.night.ms/internal/tui/components"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// Events tab — unified audit_log + security_events feed with chip-style
// filtering, keyboard navigation, and a detail modal that drills into the
// row's jsonb payload and surfaces related events.

const (
	eventsPageSize       = 100
	eventsRelatedWindow  = 5 * time.Minute
	eventsFilterDebounce = 150 * time.Millisecond
)

// sysopEventsLoadedMsg is the completion for loadEventsCmd. err is non-nil
// on failure; rows is empty on first load with no data.
type sysopEventsLoadedMsg struct {
	rows    []gen.ListUnifiedEventsRow
	count   int32
	hasMore bool
	err     error
}

// sysopEventsRelatedLoadedMsg completes the detail modal's "related events"
// query.
type sysopEventsRelatedLoadedMsg struct {
	rows []gen.ListUnifiedEventsRelatedRow
	err  error
}

// sysopEventsFilterTickMsg fires the debounce check — if the textinput has
// been quiet for eventsFilterDebounce we reload with the current filter.
type sysopEventsFilterTickMsg struct{}

// loadEventsCmd fires the appropriate query based on the current filter
// state. With no filters, uses the sqlc-generated ListUnifiedEvents; with
// filters, uses the hand-written ListUnifiedEventsFiltered which builds
// dynamic WHERE clauses.
//
// Always re-fetches CountUnifiedEvents in parallel so the footer "N total"
// stays accurate — cheap (two indexed count(*) calls).
func (m *Sysop) loadEventsCmd() tea.Cmd {
	filters := m.eventsActiveFilters()
	pool := m.sess.Pool
	queries := m.sess.Queries
	m.eventsLoading = true
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()

		var rows []gen.ListUnifiedEventsRow
		var err error
		if len(filters) == 0 {
			rows, err = queries.ListUnifiedEvents(ctx, gen.ListUnifiedEventsParams{
				RowLimit: int32(eventsPageSize),
			})
		} else {
			rows, err = data.ListUnifiedEventsFiltered(ctx, pool, filters, eventsPageSize, time.Time{})
		}
		if err != nil {
			return sysopEventsLoadedMsg{err: err}
		}
		count, _ := queries.CountUnifiedEvents(ctx)
		return sysopEventsLoadedMsg{
			rows:    rows,
			count:   count,
			hasMore: len(rows) == eventsPageSize,
		}
	}
}

// loadMoreEventsCmd extends the loaded slice by fetching another page
// before the oldest currently-loaded timestamp. Triggered when the user's
// cursor reaches the bottom of the loaded window.
func (m *Sysop) loadMoreEventsCmd() tea.Cmd {
	if len(m.events) == 0 || !m.eventsHasMore || m.eventsLoading {
		return nil
	}
	filters := m.eventsActiveFilters()
	pool := m.sess.Pool
	queries := m.sess.Queries
	before := m.events[len(m.events)-1].At.Time
	m.eventsLoading = true
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()

		var rows []gen.ListUnifiedEventsRow
		var err error
		if len(filters) == 0 {
			rows, err = queries.ListUnifiedEvents(ctx, gen.ListUnifiedEventsParams{
				Before:   pgtypeFromTime(before),
				RowLimit: int32(eventsPageSize),
			})
		} else {
			rows, err = data.ListUnifiedEventsFiltered(ctx, pool, filters, eventsPageSize, before)
		}
		// Always include the existing rows by signalling "append" via a
		// sentinel field. Simpler: piggyback on sysopEventsLoadedMsg with
		// a flag — but that adds noise. Just return a fresh page and let
		// the handler append.
		return sysopEventsLoadedMsg{
			rows:    rows,
			count:   m.eventsCount, // don't re-count on load-more
			hasMore: err == nil && len(rows) == eventsPageSize,
			err:     err,
		}
	}
}

// handleEventsLoaded routes the load completion into either a fresh-load
// replacement or a load-more append. The trick: a fresh-load fires from
// loadEventsCmd (which always rebuilds from page 1); load-more comes from
// loadMoreEventsCmd. We distinguish by whether m.events is empty OR the
// returned first-row timestamp is newer than our current head — load-more
// pages are strictly older.
func (m *Sysop) handleEventsLoaded(msg sysopEventsLoadedMsg) tea.Cmd {
	m.eventsLoading = false
	if msg.err != nil {
		m.status = "[!] events: " + msg.err.Error()
		return nil
	}
	m.eventsCount = msg.count
	m.eventsHasMore = msg.hasMore

	if len(m.events) == 0 || isFreshLoad(m.events, msg.rows) {
		m.events = msg.rows
		m.eventsCursor = clampIndex(m.eventsCursor, len(m.events))
		m.eventsScroll = 0
	} else {
		// Append, deduping by ID+source (handles the race where a new row
		// arrives between the cursor-load and the load-more).
		seen := make(map[string]bool, len(m.events))
		for _, r := range m.events {
			seen[r.Source+":"+fmt.Sprint(r.ID)] = true
		}
		for _, r := range msg.rows {
			if !seen[r.Source+":"+fmt.Sprint(r.ID)] {
				m.events = append(m.events, r)
			}
		}
	}
	return nil
}

// isFreshLoad returns true when the incoming page should REPLACE the
// existing slice rather than append. A fresh-load's first row is always at
// least as new as the existing first row; a load-more page's first row is
// strictly older.
func isFreshLoad(existing, incoming []gen.ListUnifiedEventsRow) bool {
	if len(existing) == 0 || len(incoming) == 0 {
		return true
	}
	return !incoming[0].At.Time.Before(existing[0].At.Time)
}

// eventsActiveFilters converts the carrier-typed filters on the Sysop
// struct into data.Filter for the query layer. The carrier indirection
// exists so sysop.go can hold the field without importing internal/data.
func (m *Sysop) eventsActiveFilters() []data.Filter {
	out := make([]data.Filter, 0, len(m.eventsFilters))
	for _, c := range m.eventsFilters {
		out = append(out, data.Filter{Dim: c.Dim, Text: c.Text, Time: c.Time})
	}
	return out
}

// pgtypeFromTime wraps a time.Time in a Valid pgtype.Timestamptz so it
// can be passed straight into sqlc-generated Params structs.
func pgtypeFromTime(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

// scheduleFilterTick fires a one-shot tea.Tick that wakes us up after the
// debounce window to check if the textinput has been idle long enough to
// reload.
func (m *Sysop) scheduleFilterTick() tea.Cmd {
	return tea.Tick(eventsFilterDebounce, func(time.Time) tea.Msg {
		return sysopEventsFilterTickMsg{}
	})
}

// maybeFireFilterReload is called on every sysopEventsFilterTickMsg. If the
// textinput has been idle since the last scheduled reload, parse + reload.
// Otherwise schedule another tick.
func (m *Sysop) maybeFireFilterReload() tea.Cmd {
	if m.eventsPendingFilterAt.IsZero() {
		return nil
	}
	if time.Now().Before(m.eventsPendingFilterAt) {
		return m.scheduleFilterTick()
	}
	m.eventsPendingFilterAt = time.Time{}
	raw := m.cmd.Value()
	if raw == m.eventsFiltersRaw {
		return nil
	}
	m.eventsFiltersRaw = raw
	parsed := parseFilters(raw)
	m.eventsFilters = m.eventsFilters[:0]
	for _, p := range parsed {
		m.eventsFilters = append(m.eventsFilters, sysopFilterCarrier{Dim: p.Dim, Text: p.Text, Time: p.Time})
	}
	return m.loadEventsCmd()
}

// handleEventsKey returns (handled, model, cmd). When handled is true the
// caller should return the model+cmd immediately; otherwise the key falls
// through to the textinput.
//
// List-mode (focusFilter=false): navigation keys move the cursor; Enter
// opens the detail modal; `/` jumps focus back to the filter input.
//
// Filter-mode (focusFilter=true): everything passes through to the
// textinput, EXCEPT Esc (clears focus → back to list-mode without
// clearing the filter) and the up/down keys (which always move the cursor
// regardless of where focus is — letting the user filter + browse without
// pressing Tab).
func (m *Sysop) handleEventsKey(msg tea.KeyMsg) (bool, tea.Model, tea.Cmd) {
	key := msg.String()

	// Always-applies navigation keys (work in both list and filter modes).
	switch key {
	case "up":
		m.eventsCursor = max(0, m.eventsCursor-1)
		m.eventsFocusFilter = false
		return true, m, nil
	case "down":
		if m.eventsCursor < len(m.events)-1 {
			m.eventsCursor++
		}
		m.eventsFocusFilter = false
		// Auto-fetch when the cursor reaches the last loaded row.
		if m.eventsCursor >= len(m.events)-3 && m.eventsHasMore && !m.eventsLoading {
			return true, m, m.loadMoreEventsCmd()
		}
		return true, m, nil
	case "pgup":
		m.eventsCursor = max(0, m.eventsCursor-20)
		m.eventsFocusFilter = false
		return true, m, nil
	case "pgdown":
		m.eventsCursor = min(len(m.events)-1, m.eventsCursor+20)
		m.eventsFocusFilter = false
		if m.eventsCursor >= len(m.events)-3 && m.eventsHasMore && !m.eventsLoading {
			return true, m, m.loadMoreEventsCmd()
		}
		return true, m, nil
	case "home":
		m.eventsCursor = 0
		m.eventsFocusFilter = false
		return true, m, nil
	case "end":
		m.eventsCursor = max(0, len(m.events)-1)
		m.eventsFocusFilter = false
		return true, m, nil
	}

	// List-mode-only keys.
	if !m.eventsFocusFilter {
		switch key {
		case "enter":
			if m.eventsCursor >= 0 && m.eventsCursor < len(m.events) {
				row := m.events[m.eventsCursor]
				m.eventsDetail = &row
				return true, m, m.loadRelatedEventsCmd(row)
			}
			return true, m, nil
		case "/":
			m.eventsFocusFilter = true
			return true, m, nil
		case "esc":
			// Esc when nothing's selected and filter is empty falls through
			// to page-level (back to lobby). Otherwise clear filter.
			if m.cmd.Value() != "" {
				m.cmd.SetValue("")
				m.eventsFiltersRaw = ""
				m.eventsFilters = m.eventsFilters[:0]
				m.eventsFocusFilter = false
				return true, m, m.loadEventsCmd()
			}
			return false, m, nil
		}
		// Any other printable key → grab filter focus and forward.
		if isPrintableKey(key) {
			m.eventsFocusFilter = true
			return false, m, nil
		}
		return false, m, nil
	}

	// Filter-mode-only keys.
	switch key {
	case "esc":
		m.eventsFocusFilter = false
		return true, m, nil
	}

	return false, m, nil
}

// isPrintableKey returns true for single-character keys plus a few common
// editing keys we want to forward to the textinput.
func isPrintableKey(s string) bool {
	if len(s) == 1 {
		return true
	}
	switch s {
	case "backspace", "ctrl+u", "ctrl+w", "ctrl+a", "ctrl+e", "left", "right":
		return true
	}
	return false
}

// loadRelatedEventsCmd fires the related-events query for the detail
// modal. Picks handle or ip as the "match" dimension based on what the
// row has populated.
func (m *Sysop) loadRelatedEventsCmd(row gen.ListUnifiedEventsRow) tea.Cmd {
	q := m.sess.Queries
	matchHandle := ""
	matchIP := ""
	if row.SubjectHandle != nil {
		matchHandle = *row.SubjectHandle
	} else if row.Actor != "" && row.Actor != "<system>" {
		matchHandle = row.Actor
	}
	if row.SubjectIp != nil {
		matchIP = *row.SubjectIp
	}
	m.eventsRelatedLoading = true
	m.eventsRelated = nil
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		rows, err := q.ListUnifiedEventsRelated(ctx, gen.ListUnifiedEventsRelatedParams{
			Around:        row.At,
			WindowSeconds: int32(eventsRelatedWindow.Seconds()),
			MatchHandle:   matchHandle,
			MatchIp:       matchIP,
		})
		return sysopEventsRelatedLoadedMsg{rows: rows, err: err}
	}
}

// renderEvents draws the Events tab body: filter chip count, scrollable
// row list with cursor highlight, and a footer hint.
func (m *Sysop) renderEvents(w, h int) string {
	var b strings.Builder

	// Header: filter status.
	if len(m.eventsFilters) == 0 {
		b.WriteString(sysopHeader.Render("events (unified audit + security)"))
		b.WriteString("  " + sysopHint.Render("no filters · times UTC"))
	} else {
		b.WriteString(sysopHeader.Render(fmt.Sprintf("events (%d filters active)", len(m.eventsFilters))))
		b.WriteString("  " + sysopHint.Render(m.summariseFilters()+" · times UTC"))
	}
	b.WriteString("\n")

	if m.eventsLoading && len(m.events) == 0 {
		b.WriteString(sysopHint.Render("loading…"))
		return b.String()
	}

	listH := h - 3 // header line, blank, footer
	if listH < 4 {
		listH = 4
	}

	// Visible window: keep cursor in view by shifting m.eventsScroll.
	if m.eventsCursor < m.eventsScroll {
		m.eventsScroll = m.eventsCursor
	} else if m.eventsCursor >= m.eventsScroll+listH {
		m.eventsScroll = m.eventsCursor - listH + 1
	}
	end := m.eventsScroll + listH
	if end > len(m.events) {
		end = len(m.events)
	}

	for i := m.eventsScroll; i < end; i++ {
		row := m.events[i]
		line := m.formatEventRow(row, w)
		if i == m.eventsCursor {
			line = sysopCursorRow.Render(line)
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	if len(m.events) == 0 {
		b.WriteString(sysopHint.Render("  (no events match)") + "\n")
	}

	// Footer.
	b.WriteString("\n")
	hint := "↑↓ scroll · PgUp/PgDn page · Enter detail · / filter · Esc clear"
	if m.eventsHasMore {
		hint += "  (more available)"
	}
	b.WriteString(sysopHint.Render(hint))
	if m.eventsCount > 0 {
		b.WriteString("    ")
		b.WriteString(sysopMuted.Render(fmt.Sprintf(
			"%d total · showing %d", m.eventsCount, len(m.events),
		)))
	}

	return lipgloss.NewStyle().Width(w).Render(b.String())
}

// formatEventRow renders one event into a single line. Columns:
//
//	MM-DD HH:MM:SS   SEV   source    kind                subject               preview
func (m *Sysop) formatEventRow(r gen.ListUnifiedEventsRow, w int) string {
	ts := "             "
	if r.At.Valid {
		ts = r.At.Time.UTC().Format("01-02 15:04:05")
	}

	sev := ""
	if r.Severity != nil {
		sev = *r.Severity
	}
	sevStyle, ok := sysopSevStyles[sev]
	if !ok {
		sevStyle = sysopMuted
	}
	sevCell := sevStyle.Render(padOrTrunc(strings.ToUpper(sev), 4))
	if sev == "" {
		sevCell = sysopMuted.Render("    ")
	}

	srcStyle := sysopSrcAudit
	if r.Source == "security" {
		srcStyle = sysopSrcSecurity
	}
	srcCell := srcStyle.Render(padOrTrunc(r.Source, 8))

	subject := ""
	switch {
	case r.SubjectHandle != nil && *r.SubjectHandle != "":
		subject = *r.SubjectHandle
	case r.SubjectIp != nil && *r.SubjectIp != "":
		subject = *r.SubjectIp
	case r.Source == "audit":
		subject = r.Actor
		if r.Target != "" {
			subject += " → " + r.Target
		}
	}

	preview := previewDetails(r.Details)

	line := fmt.Sprintf("%s %s %s %s  %s  %s",
		sysopMuted.Render(ts),
		sevCell,
		srcCell,
		padOrTrunc(r.Kind, 22),
		padOrTrunc(subject, 28),
		sysopMuted.Render(preview),
	)
	// lipgloss handles ANSI-aware width; rely on Width(w) wrapper outside.
	_ = w
	return line
}

// composeEventsDetailOverlay renders the detail modal on top of the base
// view via components.Overlay.
func (m *Sysop) composeEventsDetailOverlay(base string) string {
	if m.eventsDetail == nil {
		return base
	}
	modal := m.renderEventsDetailModal()
	// Center the modal in the screen.
	return components.Overlay(base, modal, m.sess.Width, m.sess.Height)
}

// renderEventsDetailModal builds the modal contents — fields + jsonb
// pretty-print + related-events list.
func (m *Sysop) renderEventsDetailModal() string {
	r := m.eventsDetail
	var b strings.Builder
	b.WriteString(sysopHeader.Render("event detail"))
	b.WriteString("\n\n")

	ts := "<no timestamp>"
	if r.At.Valid {
		ts = sysopTS(r.At.Time)
	}
	sev := "-"
	if r.Severity != nil {
		sev = *r.Severity
	}
	subjHandle := ""
	if r.SubjectHandle != nil {
		subjHandle = *r.SubjectHandle
	}
	subjIP := ""
	if r.SubjectIp != nil {
		subjIP = *r.SubjectIp
	}

	b.WriteString(fmt.Sprintf("  when:     %s\n", ts))
	b.WriteString(fmt.Sprintf("  source:   %s\n", r.Source))
	b.WriteString(fmt.Sprintf("  kind:     %s\n", r.Kind))
	b.WriteString(fmt.Sprintf("  severity: %s\n", sev))
	if r.Actor != "" {
		b.WriteString(fmt.Sprintf("  actor:    %s\n", r.Actor))
	}
	if subjHandle != "" {
		b.WriteString(fmt.Sprintf("  handle:   %s\n", subjHandle))
	}
	if subjIP != "" {
		b.WriteString(fmt.Sprintf("  ip:       %s\n", subjIP))
	}
	if r.Target != "" {
		b.WriteString(fmt.Sprintf("  target:   %s\n", r.Target))
	}

	if len(r.Details) > 0 && string(r.Details) != "null" {
		var pretty []byte
		var asMap any
		if err := json.Unmarshal(r.Details, &asMap); err == nil {
			pretty, _ = json.MarshalIndent(asMap, "    ", "  ")
		}
		if len(pretty) > 0 {
			b.WriteString("\n  details:\n    ")
			b.WriteString(strings.ReplaceAll(string(pretty), "\n", "\n    "))
			b.WriteString("\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(sysopHeader.Render(fmt.Sprintf("related events (±%s):", eventsRelatedWindow)))
	b.WriteString("\n")
	if m.eventsRelatedLoading {
		b.WriteString(sysopHint.Render("  loading…"))
	} else if len(m.eventsRelated) == 0 {
		b.WriteString(sysopHint.Render("  (none)"))
	} else {
		for _, rel := range m.eventsRelated {
			rts := "             "
			if rel.At.Valid {
				rts = sysopTSClock(rel.At.Time)
			}
			subj := ""
			if rel.SubjectHandle != nil {
				subj = *rel.SubjectHandle
			} else if rel.SubjectIp != nil {
				subj = *rel.SubjectIp
			} else if rel.Actor != "" {
				subj = rel.Actor
			}
			b.WriteString(fmt.Sprintf("  %s %s %-22s %s\n",
				sysopMuted.Render(rts),
				padOrTrunc(rel.Source, 8),
				padOrTrunc(rel.Kind, 22),
				subj,
			))
		}
	}

	b.WriteString("\n")
	b.WriteString(sysopHint.Render("[Esc] close"))

	// Box the whole thing — simple manual box around the content.
	const padW = 78
	const padH = 22
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(theme.ColorAccentDim)).
		Padding(0, 1).
		Width(padW).
		MaxHeight(padH).
		Render(b.String())
}

// summariseFilters produces a short one-line summary of active filters for
// the header.
func (m *Sysop) summariseFilters() string {
	parts := make([]string, 0, len(m.eventsFilters))
	for _, f := range m.eventsFilters {
		if !f.Time.IsZero() {
			parts = append(parts, fmt.Sprintf("%s:%s", f.Dim, f.Time.Format("15:04")))
		} else {
			parts = append(parts, fmt.Sprintf("%s:%s", f.Dim, f.Text))
		}
	}
	return strings.Join(parts, " ")
}

// previewDetails turns the jsonb blob into a one-line summary suitable for
// the events feed. Best-effort: unmarshal into a generic map, format the
// first 2 keys in sorted order.
func previewDetails(raw []byte) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	var parts []string
	for i, k := range keys {
		if i >= 2 {
			break
		}
		parts = append(parts, fmt.Sprintf("%s=%v", k, m[k]))
	}
	return strings.Join(parts, " ")
}

// padOrTrunc returns s padded with spaces to width n if shorter, or
// truncated (no ellipsis — width n is already minimal) if longer.
func padOrTrunc(s string, n int) string {
	if len(s) >= n {
		return s[:n]
	}
	return s + strings.Repeat(" ", n-len(s))
}

// Event-specific styling.
var (
	sysopSevStyles = map[string]lipgloss.Style{
		"info": lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorDim)),
		"warn": lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorYellow)),
		"crit": lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorRed)).Bold(true),
	}
	sysopSrcAudit    = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorMuted))
	sysopSrcSecurity = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorAccentDim))
	sysopCursorRow   = lipgloss.NewStyle().Reverse(true)
)
